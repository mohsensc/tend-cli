package source

import (
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

// The recorded files under testdata/recorded answer exactly these requests.
// Change an inventory here and the recordings have to be redone.

const recorded = "testdata/recorded"

var recordedNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func dep(eco, name, version string) core.Dep {
	return core.Dep{
		Ecosystem: eco,
		Name:      name,
		Version:   version,
		Direct:    true,
		Manifest:  core.Manifest{Ecosystem: eco, Path: "manifest"},
	}
}

func osvInventory() *core.Inventory {
	return &core.Inventory{Deps: []core.Dep{
		dep(core.Npm, "minimist", "1.2.0"),
		dep(core.Go, "github.com/dgrijalva/jwt-go", "v3.2.0+incompatible"),
		dep(core.Npm, "left-pad", "1.3.0"),
		dep(core.Docker, "library/node", "16-alpine"),
	}}
}

func eolInventory() *core.Inventory {
	return &core.Inventory{Deps: []core.Dep{
		dep(core.Docker, "library/node", "16-alpine"),
		dep(core.Docker, "library/postgres", "11"),
		dep(core.Docker, "library/debian", "bullseye-slim"),
		dep(core.Docker, "library/python", "3.14-slim"),
		dep(core.Npm, "left-pad", "1.3.0"),
		dep(core.Toolchain, "nodejs", "16.20.2"),
		dep(core.Toolchain, "nodejs", "20"),
	}}
}

// archivedRepos are the source repos the recorded GitHub replies cover.
var archivedRepos = []string{
	"https://github.com/dgrijalva/jwt-go",
	"git+https://github.com/lodash/lodash.git",
	"https://gitlab.com/left/pad",
}

var (
	releasesMeta  = &core.Meta{Name: "p-limit", Repo: "https://github.com/sindresorhus/p-limit"}
	changelogMeta = &core.Meta{Name: "request", Repo: "https://github.com/request/request"}
	releasesFrom  = "5.0.0"
	releasesTo    = "6.2.0"
	changelogFrom = "2.85.0"
	changelogTo   = "2.88.0"
	replay        = func() core.Fetcher { return fetch.Replay(recorded) }
)
