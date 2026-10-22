package deliver

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/config"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAnatomyShowsTheGoldenBodies(t *testing.T) {
	doc := repoFile(t, "docs/pr-anatomy.md")
	for _, name := range []string{"breaking_bump_model", "decision", "tidy"} {
		body := strings.TrimRight(repoFile(t, "internal/deliver/testdata/"+name+".golden.md"), "\n")
		if !strings.Contains(doc, body) {
			t.Errorf("docs/pr-anatomy.md does not contain testdata/%s.golden.md; regenerate the example", name)
		}
	}
}

func TestConfigDocNamesEveryKey(t *testing.T) {
	doc := repoFile(t, "docs/config.md")
	open := regexp.MustCompile(`(?m)^# ((?:  )?[a-z_]+:.*)$`).ReplaceAll(config.Sample, []byte("$1"))
	var keys map[string]any
	if err := yaml.Unmarshal(open, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) < 10 {
		t.Fatalf("read %d keys from the sample", len(keys))
	}
	for top, v := range keys {
		if !strings.Contains(doc, "## "+top) && !strings.Contains(doc, "### "+top) {
			t.Errorf("docs/config.md has no section for %q", top)
		}
		sub, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for k := range sub {
			if !strings.Contains(doc, "`"+k+"`") {
				t.Errorf("docs/config.md does not describe %s.%s", top, k)
			}
		}
	}
}

func TestWordingRules(t *testing.T) {
	banned := regexp.MustCompile(`(?i)\b(robust|seamless(ly)?|comprehensive(ly)?|powerful|blazing)\b`)
	files := []string{"README.md", "docs/config.md", "docs/pr-anatomy.md", "docs/how-it-works.md", "docs/self-update.md", "docs/limitations.md", "docs/adding-an-ecosystem.md", "docs/adding-a-provider.md", "action/install.sh"}
	for _, f := range files {
		text := repoFile(t, f)
		if m := banned.FindString(text); m != "" {
			t.Errorf("%s uses %q", f, m)
		}
		for _, r := range text {
			if unicode.Is(unicode.So, r) && r != '→' && r != '·' && r != '…' {
				t.Errorf("%s contains the symbol %q", f, r)
				break
			}
		}
	}
}
