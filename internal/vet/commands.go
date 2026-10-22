package vet

import (
	"context"
	"slices"

	"github.com/mohsensc/tend-cli/internal/core"
)

// commands: does the patch change what the project's install, build or test
// runs? The proof ran the lines the base has, so a patch that rewrites a
// test script, say to one that always passes, proved nothing about the lines
// it leaves behind.
func commands(ctx context.Context, p *pass) ([]core.Finding, string) {
	in := p.in
	if in.Detect == nil || in.Patch.Decision {
		return nil, na
	}
	now := in.Detect()
	var res []core.Finding
	for _, x := range []struct {
		what      string
		was, then []string
	}{
		{"install", in.Commands.Install, now.Install},
		{"build", in.Commands.Build, now.Build},
		{"test", in.Commands.Test, now.Test},
	} {
		if !slices.Equal(x.was, x.then) {
			res = append(res, finding("commands", "", 0, "flagged", "the patch changes the %s lines from %q to %q; the proof ran the first", x.what, x.was, x.then))
		}
	}
	return res, ""
}
