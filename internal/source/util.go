package source

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/fetch"
	"github.com/mohsensc/tend-cli/internal/ver"
)

const (
	day     = 24 * time.Hour
	workers = 8
)

var repoRe = regexp.MustCompile(`^(?:git\+)?(?:https?://|ssh://git@|git@)(?i:github\.com)[/:]([^/\s]+)/([^/\s#?]+?)(?:\.git)?(?:[/#?].*)?$`)

// ParseRepo splits the many spellings of a GitHub repo URL into owner and name.
func ParseRepo(url string) (owner, name string, ok bool) {
	m := repoRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ghOpts are the headers the GitHub API wants. The fetch client adds the
// token for api.github.com itself.
func ghOpts(extra ...core.FetchOpt) []core.FetchOpt {
	return append([]core.FetchOpt{
		fetch.Header("Accept", "application/vnd.github+json"),
		fetch.Header("X-GitHub-Api-Version", "2022-11-28"),
	}, extra...)
}

// Parallel runs fn for 0..n-1 on a small pool. fn writes into its own slot.
func Parallel(n int, fn func(i int)) {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(n, workers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				fn(i)
			}
		}()
	}
	for i := range n {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

func sameName(ecosystem, a, b string) bool {
	if ecosystem == core.Pip {
		a, b = eco.PyNorm(a), eco.PyNorm(b)
	}
	return strings.EqualFold(a, b)
}

var sha1Re = regexp.MustCompile(`^[0-9a-f]{40}$`)

// plainVersion is the version advisory databases expect for d: no leading v,
// no Go "+incompatible". Actions refs count only when they name a full
// release; a moving tag like v4 says nothing about what runs.
func plainVersion(d core.Dep) (string, bool) {
	v := strings.TrimSpace(d.Version)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	v = strings.TrimSuffix(v, "+incompatible")
	if v == "" || sha1Re.MatchString(v) {
		return "", false
	}
	p, ok := ver.Parse(v)
	if !ok {
		return "", false
	}
	if d.Ecosystem == core.Actions && len(p.Nums) < 3 {
		return "", false
	}
	return v, true
}

// asWritten gives to the same "v" prefix style as the version it replaces.
func asWritten(cur, to string) string {
	if to == "" || !strings.HasPrefix(cur, "v") || strings.HasPrefix(to, "v") {
		return to
	}
	return "v" + to
}
