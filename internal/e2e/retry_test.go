package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

// `tend retry` is honored once: a retry that fails again is not redone on
// every run after it, the patch and the proof and every model call with it.
func TestRetryRunsOnce(t *testing.T) {
	need(t, "go")
	w := setup(t, "go-vuln")
	w.publish("v1.3.0")
	w.commit("Take minor updates", map[string]string{"tend.yml": `updates: {minor: true}
ignore:
  - name: example.com/fixture/crypto
    versions: ["<1.3.0"]
`})
	const id = "go/example.com/fixture/crypto@v1.3.0"
	branch := "tend/go-crypto-v1.3.0"
	w.git(w.dir, "push", "--quiet", "origin", "main:refs/heads/"+branch)
	body := "<!-- tend id=\"" + id + "\" base=\"0\" rev=\"1\" -->"
	if _, err := w.forge.CreatePR(t.Context(), core.PRInput{Title: "Bump crypto", Branch: branch, Base: "main", Body: body}); err != nil {
		t.Fatal(err)
	}
	if res := w.tend("run").want(t, 4); !strings.HasPrefix(res.stdout, "tend: could not prove "+id) {
		t.Fatalf("first run: %q", res.stdout)
	}

	// The first run adopted #1 from the forge at the wall clock's time; the
	// command comes after that.
	w.forge.Now = func() time.Time { return time.Now().Add(time.Hour) }
	w.say(1, "maintainer", "OWNER", "tend retry")
	res := w.tend("run").want(t, 4)
	if !strings.Contains(res.stderr, "working on") {
		t.Fatalf("the retry was not honored: %q\n%s", res.stdout, res.stderr)
	}

	res = w.tend("run")
	if strings.Contains(res.stderr, "working on") {
		t.Errorf("the retry was honored again: %q\n%s", res.stdout, res.stderr)
	}
	if res.code != 0 {
		t.Errorf("exit %d: %q\n%s", res.code, res.stdout, res.stderr)
	}
}
