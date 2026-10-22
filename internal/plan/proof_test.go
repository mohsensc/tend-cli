package plan

import (
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func run(exits ...int) *core.Run {
	cmds := []string{"npm ci", "npm run build", "npm test"}
	res := &core.Run{Pass: true}
	for i, e := range exits {
		if !res.Pass {
			res.Steps = append(res.Steps, core.Step{Cmd: cmds[i], Skipped: "an earlier step failed"})
			continue
		}
		res.Steps = append(res.Steps, core.Step{Cmd: cmds[i], Exit: e})
		res.Pass = e == 0
	}
	return res
}

func TestProven(t *testing.T) {
	tests := []struct {
		name          string
		base, patched *core.Run
		want          bool
	}{
		{"patched passes", run(0, 0, 0), run(0, 0, 0), true},
		{"patched passes on a failing base", run(1, 0, 0), run(0, 0, 0), true},
		{"patched fails on a passing base", run(0, 0, 0), run(0, 0, 1), false},
		{"both fail at the same step", run(0, 0, 1), run(0, 0, 1), true},
		{"base fails earlier, patched fails its tests", run(1, 0, 0), run(0, 0, 1), false},
		{"patched fails earlier than the base", run(0, 0, 1), run(1, 0, 0), false},
		{"no baseline", nil, run(0, 0, 1), false},
		{"nothing ran on the patch", run(0, 0, 1), nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Proven(&core.Proof{Base: tt.base, Patched: tt.patched}); got != tt.want {
				t.Errorf("Proven = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecides(t *testing.T) {
	site := []core.Site{{File: "a.js", Line: 1}}
	tests := []struct {
		name string
		ch   core.Change
		want bool
	}{
		{"security", core.Change{Kind: core.Security, Level: "major"}, true},
		{"options", core.Change{Kind: core.Update, Options: []core.Option{{Title: "x"}}}, true},
		{"api change", core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "p"}}, true},
		{"deprecation without sites", core.Change{Kind: core.Deprecation}, false},
		{"deprecation with sites", core.Change{Kind: core.Deprecation, Sites: site}, true},
		{"minor that breaks", core.Change{Kind: core.Breaking, Level: "minor", Sites: site}, true},
		{"plain major", core.Change{Kind: core.Breaking, Level: "major", Sites: site}, false},
		{"update", core.Change{Kind: core.Update, Level: "minor", Sites: site}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decides(&tt.ch); got != tt.want {
				t.Errorf("Decides = %v, want %v", got, tt.want)
			}
		})
	}
}
