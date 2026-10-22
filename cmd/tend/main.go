package main

import (
	"os"
	"runtime/debug"

	"github.com/mohsensc/tend-cli/internal/cli"
	"github.com/mohsensc/tend-cli/internal/selfupdate"
)

// Set by the release build with -ldflags "-X main.version=...".
var version, commit, date string

func main() {
	harden()
	selfupdate.Cleanup()
	e := cli.DefaultEnv()
	e.Version, e.Commit, e.Date = version, commit, date
	if version == "" {
		e.Version, e.Commit, e.Date = built()
	}
	os.Exit(cli.Main(os.Args[1:], e))
}

// built reads what the go command stamped into the binary, so a
// `go install ...@v1.2.3` reports v1.2.3.
func built() (string, string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev", "", ""
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		v = "dev"
	}
	var rev, at string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value[:min(7, len(s.Value))]
		case "vcs.time":
			at = s.Value
		}
	}
	return v, rev, at
}
