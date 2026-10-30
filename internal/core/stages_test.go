package core

import "testing"

func TestProven(t *testing.T) {
	run := func(pass bool, steps ...Step) *Run { return &Run{Pass: pass, Steps: steps} }
	install := Step{Cmd: "yarn install", Phase: PhaseInstall}
	failed := func(s Step, tail string) Step { s.Exit, s.Tail = 1, tail; return s }
	skipped := func(s Step) Step { s.Skipped = "previous step failed"; return s }
	test := Step{Cmd: "yarn test", Phase: PhaseTest}
	tests := []struct {
		name  string
		proof Proof
		want  bool
	}{
		{"passes", Proof{Patched: run(true, install, test)}, true},
		{"base fails before its tests", Proof{
			Base:    run(false, failed(install, "sh: yarn: command not found"), skipped(test)),
			Patched: run(false, failed(install, "sh: yarn: command not found"), skipped(test)),
		}, false},
		{"base fails at its tests", Proof{
			Base:    run(false, install, failed(test, "1 failing")),
			Patched: run(false, install, failed(test, "1 failing")),
		}, true},
		{"patch fails earlier", Proof{
			Base:    run(false, install, failed(test, "1 failing")),
			Patched: run(false, failed(install, "boom"), skipped(test)),
		}, false},
		{"patch cannot resolve", Proof{
			Base:    run(false, install, failed(test, "ImportError")),
			Patched: run(false, install, failed(test, "ERROR: ResolutionImpossible")),
		}, false},
		{"recorded before phases", Proof{
			Base:    run(false, Step{Cmd: "make test", Exit: 2}),
			Patched: run(false, Step{Cmd: "make test", Exit: 2}),
		}, true},
	}
	for _, tt := range tests {
		if got := tt.proof.Proven(); got != tt.want {
			t.Errorf("%s: Proven() = %v, want %v", tt.name, got, tt.want)
		}
	}
}
