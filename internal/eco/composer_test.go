package eco

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestComposerDetect(t *testing.T) {
	root := copyTestdata(t, "composer/project")
	got := manifestsIn(t, composer{}, root)
	want := []core.Manifest{
		{Ecosystem: "composer", Path: "composer.json", Lock: "composer.lock", Tool: "composer"},
		{Ecosystem: "composer", Path: "sub/composer.json", Tool: "composer"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Detect = %+v\nwant %+v", got, want)
	}
}

func TestComposerDeps(t *testing.T) {
	root := copyTestdata(t, "composer/project")
	for _, tt := range []struct{ name, manifest string }{
		{"project", "composer.json"},
		{"inline", "sub/composer.json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "composer/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, composer{}, root, tt.manifest)))
		})
	}
}

func TestComposerDepsSkipPlatform(t *testing.T) {
	root := copyTestdata(t, "composer/project")
	for _, d := range depsIn(t, composer{}, root, "composer.json") {
		if !strings.Contains(d.Name, "/") || d.Name != strings.ToLower(d.Name) {
			t.Errorf("dep %q is not a lowercase vendor/package", d.Name)
		}
	}
}

func TestJSONKeyLine(t *testing.T) {
	src := strings.Split(`{
  "require": {
    "a/b": "^1.0",
    "c\/d": "^2.0"
  },
  "require-dev": {
    "a/b": "^3.0", "e/f": "1.0"
  },
  "extra": {"a/b": "nope"}
}`, "\n")
	tests := []struct {
		section, key string
		want         int
	}{
		{"require", "a/b", 3},
		{"require", "c/d", 4},
		{"require-dev", "a/b", 7},
		{"require-dev", "e/f", 7},
		{"require", "e/f", 0},
		{"require", "x/y", 0},
		{"extra", "a/b", 9},
		{"conflict", "a/b", 0},
	}
	for _, tt := range tests {
		if got := jsonKeyLine(src, tt.section, tt.key); got != tt.want {
			t.Errorf("jsonKeyLine(%s, %s) = %d, want %d", tt.section, tt.key, got, tt.want)
		}
	}
}

func TestComposerBump(t *testing.T) {
	bumpGolden(t, composer{}, "composer/project", []bumpCase{
		{"caret", "composer.json", "monolog/monolog", true, "4.0.0", ""},
		{"tilde-mixed-case-key", "composer.json", "symfony/console", true, "v7.0.0", ""},
		{"alternatives", "composer.json", "guzzlehttp/guzzle", true, "9.1.0", ""},
		{"exact", "composer.json", "vlucas/phpdotenv", true, "5.6.1", ""},
		{"wildcard", "composer.json", "acme/internal", true, "2.0.0", ""},
		{"floor", "composer.json", "laravel/framework", true, "11.0.0", ""},
		{"dev", "composer.json", "phpunit/phpunit", true, "11.0.0", ""},
		{"tilde-patch", "composer.json", "mockery/mockery", true, "1.7.0", ""},
		{"inside-range", "composer.json", "monolog/monolog", true, "3.9.0", ""},
		{"transitive", "composer.json", "psr/log", false, "3.0.1", ""},
		{"swap", "composer.json", "monolog/monolog", true, "2.0.0", "acme/logger"},
		{"inline-object", "sub/composer.json", "monolog/monolog", true, "3.5.0", ""},
		{"no-lock", "sub/composer.json", "monolog/monolog", true, "2.10.0", ""},
	})
}

func TestComposerBumpErrors(t *testing.T) {
	hideTools(t)
	root := copyTestdata(t, "composer/project")
	ctx := context.Background()
	d := depIn(t, composer{}, root, "composer.json", "monolog/monolog", true)
	stale := d
	stale.Req = "^9.9"
	if _, err := (composer{}).Bump(ctx, root, stale, "4.0.0", ""); err == nil {
		t.Error("bumped a requirement that is not in the file")
	}
	psr := depIn(t, composer{}, root, "composer.json", "psr/log", false)
	if _, err := (composer{}).Bump(ctx, root, psr, "3.0.1", "other/log"); err == nil {
		t.Error("swapped a transitive package")
	}
	write(t, root, map[string]string{"composer.json": `{"require": {"a/b": "^1.0@dev"}}`})
	odd := core.Dep{Name: "a/b", Req: "^1.0@dev", Line: 1, Direct: true, Manifest: core.Manifest{Path: "composer.json"}}
	if _, err := (composer{}).Bump(ctx, root, odd, "2.0.0", ""); err == nil {
		t.Error("rewrote a constraint with a stability flag")
	}
}

func TestComposerSwapSameConstraint(t *testing.T) {
	hideTools(t)
	root := copyTestdata(t, "composer/project")
	d := depIn(t, composer{}, root, "composer.json", "monolog/monolog", true)
	res, err := (composer{}).Bump(context.Background(), root, d, "3.5.0", "acme/logger")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Files, []string{"composer.json"}) {
		t.Errorf("files = %q, want the renamed manifest", res.Files)
	}
	b, err := os.ReadFile(filepath.Join(root, "composer.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"acme/logger": "^3.5"`) {
		t.Errorf("composer.json = %s", b)
	}
}

func TestComposerVendorInSubdir(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"app/composer.json":                          `{"require": {"zzz/subdir-vendor": "^1.0"}}`,
		"app/composer.lock":                          `{"packages": [{"name": "zzz/subdir-vendor", "version": "1.0.0"}]}`,
		"app/vendor/zzz/subdir-vendor/composer.json": `{"autoload": {"psr-4": {"Zzz\\Sub\\": "src/"}}}`,
	})
	depsIn(t, composer{}, root, "app/composer.json")
	if got := (composer{}).Imports(core.Dep{Name: "zzz/subdir-vendor"}); !reflect.DeepEqual(got, []string{`Zzz\Sub`}) {
		t.Errorf("Imports = %q, want the namespace from app/vendor", got)
	}
}

func TestComposerBumpCommands(t *testing.T) {
	ctx := context.Background()
	root := copyTestdata(t, "composer/project")
	logged := lockingTool(t, "composer", root, "composer.lock", strings.NewReplacer(
		`"name": "monolog/monolog",
            "version": "3.5.0"`, `"name": "monolog/monolog",
            "version": "4.0.0"`,
		`"name": "psr/log",
            "version": "3.0.0"`, `"name": "psr/log",
            "version": "3.0.1"`,
		`"name": "symfony/console",
            "version": "v6.4.3"`, `"name": "acme/console",
            "version": "v7.0.0"`,
	).Replace)
	monolog := depIn(t, composer{}, root, "composer.json", "monolog/monolog", true)
	psr := depIn(t, composer{}, root, "composer.json", "psr/log", false)
	console := depIn(t, composer{}, root, "composer.json", "symfony/console", true)
	res, err := (composer{}).Bump(ctx, root, monolog, "4.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"composer.json", "composer.lock"}) {
		t.Errorf("bump = %+v", res)
	}
	if _, err := (composer{}).Bump(ctx, root, psr, "3.0.1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := (composer{}).Bump(ctx, root, console, "7.0.0", "acme/console"); err != nil {
		t.Fatal(err)
	}
	flags := " --with-all-dependencies --no-install --no-scripts --no-interaction"
	want := []string{
		". update monolog/monolog:4.0.0" + flags,
		". update psr/log:3.0.1" + flags,
		". update acme/console:7.0.0 symfony/console" + flags,
	}
	if got := logged(root); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q\nwant %q", got, want)
	}
}

func TestUnminify(t *testing.T) {
	var doc struct {
		Packages map[string][]map[string]any `json:"packages"`
	}
	if err := json.Unmarshal([]byte(`{"packages": {"a/b": [
		{"name": "a/b", "version": "2.0.0", "abandoned": true, "require": {"php": ">=8"}, "keywords": ["x"]},
		{"version": "1.9.0", "abandoned": "__unset", "keywords": "__unset"},
		{"version": "1.8.0", "require": {"php": ">=7"}}
	]}}`), &doc); err != nil {
		t.Fatal(err)
	}
	got := unminify(doc.Packages["a/b"])
	want := []map[string]any{
		{"name": "a/b", "version": "2.0.0", "abandoned": true, "require": map[string]any{"php": ">=8"}, "keywords": []any{"x"}},
		{"name": "a/b", "version": "1.9.0", "require": map[string]any{"php": ">=8"}},
		{"name": "a/b", "version": "1.8.0", "require": map[string]any{"php": ">=7"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unminify = %v\nwant %v", got, want)
	}
	if doc.Packages["a/b"][1]["abandoned"] != "__unset" {
		t.Error("unminify changed its input")
	}
}

func TestComposerMeta(t *testing.T) {
	ctx := context.Background()
	t.Run("recorded", func(t *testing.T) {
		m, err := (composer{}).Meta(ctx, fetch.Replay(recordedDir("composer")), "monolog/monolog")
		if err != nil {
			t.Fatal(err)
		}
		if m.Repo != "https://github.com/Seldaek/monolog" || len(m.Releases) < 3 || m.Deprecated != "" {
			t.Fatalf("meta = %+v", m)
		}
		for i, r := range m.Releases {
			if r.Time.IsZero() || strings.HasPrefix(r.Version, "dev-") || (i > 0 && r.Version == m.Releases[i-1].Version) {
				t.Errorf("release %d = %+v", i, r)
			}
		}
	})
	t.Run("recorded abandoned with replacement", func(t *testing.T) {
		m, err := (composer{}).Meta(ctx, fetch.Replay(recordedDir("composer")), "swiftmailer/swiftmailer")
		if err != nil {
			t.Fatal(err)
		}
		if m.Replacement != "symfony/mailer" || !strings.Contains(m.Deprecated, "abandoned") || !strings.Contains(m.Deprecated, "symfony/mailer") {
			t.Fatalf("meta = %+v", m)
		}
	})
	t.Run("minified with unset and dev versions", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://repo.packagist.org/p2/acme/lib.json", file: "composer/minified.json"})
		m, err := (composer{}).Meta(ctx, f, "acme/lib")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range m.Releases {
			got = append(got, fmt.Sprintf("%s pre=%v at=%s", r.Version, r.Pre, r.Time.Format("2006-01-02")))
		}
		want := []string{"1.9.0 pre=false at=2023-01-05", "v2.0.0-beta1 pre=true at=2024-02-01", "2.0.0 pre=false at=2024-03-01"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("releases = %q\nwant %q", got, want)
		}
		if m.Deprecated != "package is abandoned" || m.Replacement != "" {
			t.Errorf("deprecated %q replacement %q", m.Deprecated, m.Replacement)
		}
		if m.Repo != "https://github.com/acme/lib" {
			t.Errorf("repo = %q", m.Repo)
		}
	})
	t.Run("abandoned removed in the newest release", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://repo.packagist.org/p2/acme/revived.json", body: `{"minified": "composer/2.0", "packages": {"acme/revived": [
			{"name": "acme/revived", "version": "2.0.0", "version_normalized": "2.0.0.0", "time": "2024-03-01T00:00:00+00:00"},
			{"version": "1.0.0", "version_normalized": "1.0.0.0", "time": "2020-03-01T00:00:00+00:00", "abandoned": "acme/other"}]}}`})
		m, err := (composer{}).Meta(ctx, f, "acme/revived")
		if err != nil {
			t.Fatal(err)
		}
		if m.Deprecated != "" || m.Replacement != "" || len(m.Releases) != 2 {
			t.Errorf("meta = %+v", m)
		}
	})
	t.Run("not minified", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://repo.packagist.org/p2/acme/plain.json", body: `{"packages": {"acme/plain": [
			{"name": "acme/plain", "version": "1.1.0", "version_normalized": "1.1.0.0", "time": "2024-03-01T00:00:00+00:00", "abandoned": true},
			{"name": "acme/plain", "version": "1.0.0", "version_normalized": "1.0.0.0", "time": "2024-01-01T00:00:00+00:00"}]}}`})
		m, err := (composer{}).Meta(ctx, f, "acme/plain")
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Releases) != 2 || m.Deprecated == "" {
			t.Errorf("meta = %+v", m)
		}
	})
	t.Run("only dev versions", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://repo.packagist.org/p2/acme/dev.json", body: `{"packages": {"acme/dev": [{"name": "acme/dev", "version": "dev-main", "version_normalized": "dev-main"}]}}`})
		m, err := (composer{}).Meta(ctx, f, "acme/dev")
		if err != nil || len(m.Releases) != 0 {
			t.Errorf("meta = %+v, %v", m, err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		f := replayCanned(t, canned{url: "https://repo.packagist.org/p2/acme/nope.json", status: 404, body: "{}"})
		if _, err := (composer{}).Meta(ctx, f, "acme/nope"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestComposerMetaRecord(t *testing.T) {
	wantRecord(t)
	f := liveClient(t, "composer")
	for _, name := range []string{"monolog/monolog", "swiftmailer/swiftmailer"} {
		if _, err := (composer{}).Meta(context.Background(), f, name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestComposerImports(t *testing.T) {
	root := copyTestdata(t, "composer/project")
	depsIn(t, composer{}, root, "composer.json")
	tests := map[string][]string{
		"monolog/monolog":    {"Monolog"},
		"acme/internal":      {"Acme\\Internal", "Acme\\Internal\\Contracts"},
		"laravel/framework":  {"Illuminate", "Illuminate\\Support"},
		"zzz/unseen-package": {"Zzz\\UnseenPackage"},
		"zzz/under_score.js": {"Zzz\\UnderScoreJs"},
	}
	for name, want := range tests {
		got := (composer{}).Imports(core.Dep{Name: name})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Imports(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestComposerCommands(t *testing.T) {
	root := copyTestdata(t, "composer/project")
	write(t, root, map[string]string{"api/phpunit.xml": "<phpunit/>", "api/composer.json": "{}"})
	tests := []struct {
		path string
		want core.Commands
	}{
		{"composer.json", core.Commands{Install: []string{"composer install --no-interaction --no-progress"}, Test: []string{"vendor/bin/phpunit"}}},
		{"api/composer.json", core.Commands{Install: []string{"cd api && composer install --no-interaction --no-progress"}, Test: []string{"cd api && vendor/bin/phpunit"}}},
		{"sub/composer.json", core.Commands{Install: []string{"cd sub && composer install --no-interaction --no-progress"}}},
	}
	for _, tt := range tests {
		got := (composer{}).Commands(root, core.Manifest{Path: tt.path})
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Commands(%s) = %+v, want %+v", tt.path, got, tt.want)
		}
	}
}
