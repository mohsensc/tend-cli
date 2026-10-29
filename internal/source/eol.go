package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
	"github.com/mohsensc/tend-cli/internal/ver"
)

const eolURL = "https://endoflife.date/api/v1/products/"

// eolProducts maps a Docker image name, without registry or "library/", to its endoflife.date product.
var eolProducts = map[string]string{
	"node":            "nodejs",
	"python":          "python",
	"golang":          "go",
	"ruby":            "ruby",
	"php":             "php",
	"postgres":        "postgresql",
	"mysql":           "mysql",
	"mariadb":         "mariadb",
	"redis":           "redis",
	"mongo":           "mongodb",
	"nginx":           "nginx",
	"alpine":          "alpine-linux",
	"debian":          "debian",
	"ubuntu":          "ubuntu",
	"rockylinux":      "rocky-linux",
	"amazonlinux":     "amazon-linux",
	"eclipse-temurin": "eclipse-temurin",
	"openjdk":         "eclipse-temurin",
}

const dotnetImages = "mcr.microsoft.com/dotnet/"

// EOL flags base images whose release cycle has reached end of life.
type EOL struct {
	Fetch core.Fetcher
	Now   func() time.Time // defaults to time.Now
}

var _ core.Source = (*EOL)(nil)

func (s *EOL) Name() string { return "eol" }

type isoDate string

func (d *isoDate) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) != nil {
		s = ""
	}
	*d = isoDate(s)
	return nil
}

func (d isoDate) time() time.Time {
	t, _ := time.Parse(time.DateOnly, string(d))
	return t
}

type eolRelease struct {
	Name         string  `json:"name"`
	Codename     string  `json:"codename"`
	ReleaseDate  isoDate `json:"releaseDate"`
	EolFrom      isoDate `json:"eolFrom"`
	IsLts        bool    `json:"isLts"`
	IsEol        bool    `json:"isEol"`
	IsMaintained *bool   `json:"isMaintained"`
	Latest       struct {
		Name string `json:"name"`
	} `json:"latest"`
}

func (r eolRelease) dead() bool { return r.IsEol || r.IsMaintained != nil && !*r.IsMaintained }

func (r eolRelease) alive() bool { return !r.dead() }

// word is the codename tags use: "Resolute Raccoon" is "resolute".
func (r eolRelease) word() string {
	w, _, _ := strings.Cut(strings.ToLower(r.Codename), " ")
	return w
}

func (s *EOL) Changes(ctx context.Context, inv *core.Inventory) ([]core.Change, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	var products []string
	for _, d := range inv.Deps {
		if p := product(d); p != "" && !slices.Contains(products, p) {
			products = append(products, p)
		}
	}
	got := make([][]eolRelease, len(products))
	errs := make([]error, len(products))
	Parallel(len(products), func(i int) {
		got[i], errs[i] = s.releases(ctx, products[i])
	})
	rels := map[string][]eolRelease{}
	for i, p := range products {
		if errs[i] != nil {
			return nil, errs[i]
		}
		rels[p] = got[i]
	}
	var res []core.Change
	for _, d := range inv.Deps {
		p := product(d)
		if p == "" {
			continue
		}
		if c, ok := eolChange(d, p, rels[p], now()); ok {
			c.Sites = []core.Site{lineSite(inv.Root, d)}
			res = append(res, c)
		}
	}
	return res, nil
}

// releases returns nil for a product endoflife.date does not know.
func (s *EOL) releases(ctx context.Context, product string) ([]eolRelease, error) {
	resp, err := s.Fetch.Get(ctx, eolURL+product, fetch.TTL(day))
	if err != nil {
		return nil, err
	}
	switch resp.Status {
	case 200:
	case 404:
		return nil, nil
	default:
		return nil, fmt.Errorf("eol: %s: status %d", product, resp.Status)
	}
	var out struct {
		Result struct {
			Releases []eolRelease `json:"releases"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("eol: %s: %w", product, err)
	}
	return out.Result.Releases, nil
}

// product is the endoflife.date product a dep runs on: a base image's, or a
// runtime's, which the toolchain ecosystem names after its product. Java's
// releases are read from Temurin's, the build most projects run.
func product(d core.Dep) string {
	if d.Ecosystem == core.Toolchain {
		if d.Name == "java" {
			return "eclipse-temurin"
		}
		return d.Name
	}
	if d.Ecosystem != core.Docker {
		return ""
	}
	name := d.Name
	for _, host := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		name = strings.TrimPrefix(name, host)
	}
	if strings.HasPrefix(name, dotnetImages) {
		return "dotnet"
	}
	return eolProducts[strings.TrimPrefix(name, "library/")]
}

func eolChange(d core.Dep, product string, rels []eolRelease, now time.Time) (core.Change, bool) {
	head, suffix := splitTag(d.Version)
	cur, extra, ok := findCycle(rels, head)
	if !ok || cur.alive() {
		return core.Change{}, false
	}
	title := product + " " + cur.Name + " end of life"
	text := product + " " + cur.Name + " is no longer maintained."
	if cur.EolFrom != "" {
		title += " " + string(cur.EolFrom)
		text = product + " " + cur.Name + " reached end of life on " + string(cur.EolFrom) + "."
	}
	c := core.Change{
		Kind:     core.Deprecation,
		Deps:     []core.Dep{d},
		Sunset:   cur.EolFrom.time(),
		Evidence: []core.Evidence{{Source: "eol", Title: title, Text: text, URL: "https://endoflife.date/" + product}},
	}
	if d.Min {
		// A minimum moves only as far as it must: to the oldest release
		// still maintained, written with as many parts as before.
		if to, passed, ok := floor(rels, cur, now); ok {
			c.To = pad(to.Name, strings.Count(d.Version, ".")+1)
			if passed != nil {
				c.Evidence[0].Text += fmt.Sprintf(" %s %s reaches its own end of life on %s, so the oldest release to move to is %s.", product, passed.Name, passed.EolFrom, to.Name)
			} else {
				c.Evidence[0].Text += " The oldest " + product + " release still maintained is " + to.Name + "."
			}
		}
		c.ID = core.ChangeID(&c)
		return c, true
	}
	if to, ok := successor(rels, now); ok {
		c.To = retag(to, head, extra) + snapshotRe.ReplaceAllString(suffix, "")
		// A runtime pinned to a patch release moves to the newest one, not to
		// the bare cycle: go.mod's toolchain line wants go1.25.3, not go1.25.
		if d.Ecosystem == core.Toolchain && len(extra) > 0 && to.Latest.Name != "" {
			c.To = to.Latest.Name
		}
	}
	c.ID = core.ChangeID(&c)
	return c, true
}

// snapshotRe matches the date Ubuntu and Debian images append to a codename:
// the successor was never built on that day.
var snapshotRe = regexp.MustCompile(`-\d{8}$`)

// splitTag cuts "3.9-slim" into "3.9" and "-slim", and "bookworm-slim" into "bookworm" and "-slim".
func splitTag(tag string) (head, suffix string) {
	n := 0
	for n < len(tag) && (isDigit(tag[n]) || tag[n] == '.') {
		n++
	}
	if n == 0 {
		for n < len(tag) && tag[n] >= 'a' && tag[n] <= 'z' {
			n++
		}
	}
	head = strings.TrimRight(tag[:n], ".")
	return head, tag[len(head):]
}

// findCycle matches the longest dotted prefix of head that names a release
// exactly, so 3.9.18 is python 3.9 and 8.0 is dotnet 8. extra is what was left over.
func findCycle(rels []eolRelease, head string) (eolRelease, []string, bool) {
	if head == "" {
		return eolRelease{}, nil, false
	}
	if !isDigit(head[0]) {
		for _, r := range rels {
			if r.word() == head {
				return r, nil, true
			}
		}
		return eolRelease{}, nil, false
	}
	parts := strings.Split(head, ".")
	for n := len(parts); n > 0; n-- {
		name := strings.Join(parts[:n], ".")
		for _, r := range rels {
			if r.Name == name {
				return r, parts[n:], true
			}
		}
	}
	return eolRelease{}, nil, false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// successor is the newest maintained release; when the product has LTS
// releases, the newest maintained LTS.
func successor(rels []eolRelease, now time.Time) (eolRelease, bool) {
	var live []eolRelease
	for _, r := range rels {
		if t := r.ReleaseDate.time(); r.alive() && !t.After(now) {
			live = append(live, r)
		}
	}
	slices.SortFunc(live, func(a, b eolRelease) int { return -cmpName(a.Name, b.Name) })
	for _, r := range live {
		if r.IsLts {
			return r, true
		}
	}
	if len(live) == 0 {
		return eolRelease{}, false
	}
	return live[0], true
}

// soon is how close to its own end of life a release may be and still be
// the one a minimum moves to.
const soon = 30 * 24 * time.Hour

// floor is the oldest maintained release newer than cur, passing over one
// that is about to reach its own end of life when a later one is there;
// passed is the first one it passed over.
func floor(rels []eolRelease, cur eolRelease, now time.Time) (to eolRelease, passed *eolRelease, ok bool) {
	var live []eolRelease
	for _, r := range rels {
		if t := r.ReleaseDate.time(); r.alive() && !t.After(now) && cmpName(r.Name, cur.Name) > 0 {
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return eolRelease{}, nil, false
	}
	slices.SortFunc(live, func(a, b eolRelease) int { return cmpName(a.Name, b.Name) })
	for i, r := range live {
		if e := r.EolFrom.time(); e.IsZero() || e.After(now.Add(soon)) {
			if i > 0 {
				passed = &live[0]
			}
			return r, passed, true
		}
	}
	return live[0], nil, true
}

// pad writes a release name with n parts: 22 as 22.0.0 for a requirement
// that was written 16.14.0.
func pad(name string, n int) string {
	if k := strings.Count(name, ".") + 1; k < n {
		name += strings.Repeat(".0", n-k)
	}
	return name
}

func cmpName(a, b string) int {
	switch {
	case a == b:
		return 0
	case ver.Less(a, b):
		return -1
	}
	return 1
}

// retag writes the target release the way the old tag was written: by
// codename when it was one, with ".0" padding when it carried only zeros past the cycle.
func retag(to eolRelease, head string, extra []string) string {
	if head != "" && !isDigit(head[0]) && to.word() != "" {
		return to.word()
	}
	res := to.Name
	for _, x := range extra {
		if x != "0" {
			return res
		}
	}
	return res + strings.Repeat(".0", len(extra))
}

// lineSite points at the manifest line that names the dependency.
func lineSite(root string, d core.Dep) core.Site {
	site := core.Site{File: d.Manifest.Path, Line: d.Line}
	if d.Line < 1 {
		return site
	}
	b, err := os.ReadFile(filepath.Join(root, d.Manifest.Path))
	if err != nil {
		return site
	}
	lines := strings.Split(string(b), "\n")
	if d.Line <= len(lines) {
		site.Text = core.Clip(strings.TrimSpace(lines[d.Line-1]), 200)
	}
	return site
}
