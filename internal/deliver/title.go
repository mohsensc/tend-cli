package deliver

import (
	"fmt"
	"strings"

	"github.com/mohsensc/tend-cli/internal/api"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

const branchMax = 100

// Title is the PR title and commit subject, with prefix prepended verbatim. It
// names what the PR carries: a decision asks, a pin pins, anything else moves.
func Title(in BodyInput, prefix string) string {
	ch := shown(in).Change
	if ch.Kind == core.First && ch.Batch != nil {
		return prefix + firstTitle(ch.Batch)
	}
	if p := in.Patch; p != nil && p.Decision && len(ch.Options) > 0 {
		return prefix + decide(ch)
	}
	if o := picked(in); o != nil && pins(ch, o) {
		return prefix + "Pin " + depName(ch.Deps[0]) + " to " + o.To
	}
	return prefix + title(ch)
}

// Message is the commit message: the title, then the lines the patch applied.
// A decision commits nothing, so it says only what is being asked.
func Message(in BodyInput, prefix string) string {
	res := Title(in, prefix)
	if p := in.Patch; p != nil && !p.Decision && len(p.Applied) > 0 {
		res += "\n\n" + strings.Join(p.Applied, "\n")
	}
	return res
}

func decide(ch *core.Change) string {
	if a := ch.API; a != nil {
		return "Decide: migrate off " + strings.TrimSpace(strings.ToUpper(a.Method)+" "+a.Path)
	}
	d := ch.Deps[0]
	if v, ok := ver.Parse(d.Version); ok && len(v.Nums) > 0 && ch.To != "" {
		return fmt.Sprintf("Decide: migrate off %s %d.x", depName(d), v.Major())
	}
	return "Decide: migrate off " + depName(d)
}

// picked is the option the PR carries, if the maintainer picked one or tend
// took one by its own rule.
func picked(in BodyInput) *core.Option {
	n := in.Option
	if c := in.Change.Choice; n == 0 && c != nil {
		n = c.N
	}
	if n > 0 && n <= len(in.Change.Options) {
		return &in.Change.Options[n-1]
	}
	return nil
}

// pins reports whether o keeps the version in use rather than moving off it.
func pins(ch *core.Change, o *core.Option) bool {
	return len(ch.Deps) > 0 && (o.Stay || o.ToName == "" && o.Codemod == nil && o.To != "" && o.To == ch.Deps[0].Version)
}

func title(ch *core.Change) string {
	if ch.API != nil {
		return apiTitle(ch.API)
	}
	if len(ch.Deps) == 0 {
		return "Update " + ch.ID
	}
	d := ch.Deps[0]
	name := depName(d)
	switch {
	case ch.ToName == "" && ch.To != "" && ch.To == d.Version:
		return "Fix deprecation warnings from " + name
	case ch.ToName != "":
		return "Replace " + name + " with " + ch.ToName
	case ch.To == "":
		return "Decide: migrate off " + name
	case len(core.Names(ch)) > 1 && ch.Kind == core.Security:
		return "Bump " + groupName(ch) + " packages to " + ch.To + fixes(ch)
	case len(core.Names(ch)) > 1:
		return "Bump " + groupName(ch) + " packages to " + ch.To
	case ch.Kind == core.Security:
		return "Bump " + name + " to " + ch.To + fixes(ch)
	case d.Ecosystem == core.Docker:
		return "Bump " + name + " base image to " + ch.To
	case d.Ecosystem == core.Toolchain && d.Min:
		return "Require " + name + " " + ch.To + " or later"
	case d.Ecosystem == core.Toolchain:
		return "Move " + name + " to " + ch.To
	case d.Ecosystem == core.Actions:
		return "Bump " + name + " to " + ch.To
	case ch.Kind == core.Deprecation:
		if v, ok := ver.Parse(d.Version); ok && len(v.Nums) > 0 {
			return fmt.Sprintf("Migrate off deprecated %s %d.x", name, v.Major())
		}
		return "Migrate off deprecated " + name
	}
	return "Bump " + name + " to " + ch.To
}

func apiTitle(a *core.APIChange) string {
	op := strings.TrimSpace(strings.ToUpper(a.Method) + " " + a.Path)
	noun := "field"
	if strings.Contains(a.What, "parameter") {
		noun = "parameter"
	}
	subject := op
	switch {
	case a.Field != "" && op == "":
		subject = noun + " " + a.Field + " of the " + api.Title(a.Provider) + " API"
	case a.Field != "":
		subject = noun + " " + a.Field + " on " + op
	}
	switch {
	case strings.Contains(a.What, "deprecated"):
		return "Migrate off deprecated " + subject
	case strings.HasPrefix(a.What, "required parameter added"):
		if a.Field == "" {
			return "Handle new required parameter on " + op
		}
		return "Handle new required " + subject
	}
	return "Handle removed " + subject
}

func fixes(ch *core.Change) string {
	ids := advisories(ch)
	if len(ids) == 0 {
		return ""
	}
	res := " (fixes " + ids[0]
	if len(ids) > 1 {
		res += fmt.Sprintf(" +%d more", len(ids)-1)
	}
	return res + ")"
}

func advisories(ch *core.Change) []string {
	var res []string
	seen := map[string]bool{}
	for _, e := range ch.Evidence {
		if e.Source != "osv" {
			continue
		}
		id, _, _ := strings.Cut(strings.TrimSpace(e.Title), " ")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		res = append(res, id)
	}
	return res
}

func depName(d core.Dep) string {
	return strings.TrimPrefix(d.Name, "library/")
}

func groupName(ch *core.Change) string {
	id := core.ChangeID(ch)
	_, rest, ok := strings.Cut(id, "/group:")
	if !ok {
		return ch.Deps[0].Name
	}
	if i := strings.LastIndexByte(rest, '@'); i > 0 {
		rest = rest[:i]
	}
	return rest
}

// Branch is tend/<eco>-<name>-<to>, tend/<eco>-group-<group>-<to>,
// tend/api-<provider>-<op>, FirstBranch for tend's first PR, or
// StageBranch for a later catch-up PR.
func Branch(ch *core.Change) string {
	if ch.Kind == core.First {
		if k := core.StageOf(ch.ID); k > 0 {
			return StageBranch(k)
		}
		return FirstBranch
	}
	if a := ch.API; a != nil {
		op := strings.TrimSpace(strings.ToUpper(a.Method) + " " + a.Path)
		if a.Field != "" {
			op += " " + a.Field
		}
		return fit("tend/api-"+core.Slug(a.Provider)+"-", core.Slug(op), "")
	}
	if len(ch.Deps) == 0 {
		return fit("tend/", core.Slug(ch.ID), "")
	}
	eco := ch.Deps[0].Ecosystem
	to := ""
	if ch.To != "" {
		to = "-" + core.Slug(ch.To)
	}
	switch {
	case len(core.Names(ch)) > 1:
		return fit("tend/"+eco+"-group-", core.Slug(groupName(ch)), to)
	case ch.ToName != "":
		return fit("tend/"+eco+"-", core.Slug(ch.Deps[0].Name)+"-to-"+core.Slug(ch.ToName), to)
	case ch.To == "":
		return fit("tend/"+eco+"-", core.Slug(ch.Deps[0].Name), "-decide")
	}
	return fit("tend/"+eco+"-", core.Slug(ch.Deps[0].Name), to)
}

func fit(head, mid, tail string) string {
	room := branchMax - len(head) - len(tail)
	if len(mid) > room {
		mid = strings.TrimRight(mid[:max(room, 1)], "-.")
	}
	return head + mid + tail
}
