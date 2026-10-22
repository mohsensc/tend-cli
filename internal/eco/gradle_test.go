package eco

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

func TestGradleDetect(t *testing.T) {
	root := copyTestdata(t, "gradle")
	write(t, root, map[string]string{"kts/build/generated/build.gradle": "", "docs/libs.versions.toml": ""})
	got := manifestsIn(t, gradle{}, root)
	want := []core.Manifest{
		{Ecosystem: "gradle", Path: "groovy/build.gradle", Tool: "gradle"},
		{Ecosystem: "gradle", Path: "kts/app/build.gradle.kts", Lock: "kts/app/gradle.lockfile", Tool: "gradle"},
		{Ecosystem: "gradle", Path: "kts/gradle/libs.versions.toml", Tool: "gradle"},
		{Ecosystem: "gradle", Path: "kts/lib/build.gradle.kts", Tool: "gradle"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Detect = %+v\nwant %+v", got, want)
	}
}

func TestGradleDeps(t *testing.T) {
	root := copyTestdata(t, "gradle")
	for _, tt := range []struct{ name, manifest string }{
		{"kts-app", "kts/app/build.gradle.kts"},
		{"kts-lib", "kts/lib/build.gradle.kts"},
		{"catalog", "kts/gradle/libs.versions.toml"},
		{"groovy", "groovy/build.gradle"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "gradle/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, gradle{}, root, tt.manifest)))
		})
	}
}

func TestGradleClean(t *testing.T) {
	src := "a // b\n\"x // y\" /* c\nd */ 'q\\'' // \"z\"\n"
	want := "a     \n\"x // y\"     \n     'q\\''       \n"
	if got := string(gradleClean([]byte(src))); got != want {
		t.Errorf("gradleClean = %q\nwant %q", got, want)
	}
	plugins := "plugins {\n  id(\"x\") version \"1\"\n  nested { }\n}\ndependencies { }\n"
	got := string(gradleNoPlugins([]byte(plugins)))
	if strings.Contains(got, "version") || !strings.Contains(got, "dependencies { }") || strings.Count(got, "\n") != 5 {
		t.Errorf("gradleNoPlugins = %q", got)
	}
}

func TestGradleMeta(t *testing.T) {
	f := fetch.Replay(recordedDir("gradle"))
	meta, err := gradle{}.Meta(context.Background(), f, "androidx.annotation:annotation")
	if err != nil {
		t.Fatal(err)
	}
	goldenCheck(t, "gradle/meta/androidx.annotation_annotation.golden.json", jsonOf(t, meta))
	if _, err := (gradle{}).Meta(context.Background(), f, "com.example.tend:nope"); err == nil || !strings.Contains(err.Error(), "dl.google.com") {
		t.Errorf("err = %v, want both repositories named", err)
	}
}

func TestGradleMetaRecord(t *testing.T) {
	wantRecord(t)
	f := liveClient(t, "gradle")
	for _, name := range []string{"androidx.annotation:annotation", "com.example.tend:nope"} {
		if _, err := (gradle{}).Meta(context.Background(), f, name); err != nil {
			t.Log(err)
		}
	}
}

func TestGradleBump(t *testing.T) {
	bumpGolden(t, gradle{}, "gradle/kts", []bumpCase{
		{"catalog-version-ref", "gradle/libs.versions.toml", "com.squareup.okhttp3:logging-interceptor", true, "4.12.0", ""},
		{"catalog-rich-version", "gradle/libs.versions.toml", "org.junit.jupiter:junit-jupiter", true, "5.11.0", ""},
		{"catalog-notation", "gradle/libs.versions.toml", "com.google.code.gson:gson", true, "2.11.0", ""},
		{"catalog-inline", "gradle/libs.versions.toml", "com.fasterxml.jackson:jackson-bom", true, "2.17.0", ""},
		{"kts-literal", "app/build.gradle.kts", "com.google.guava:guava", true, "33.0.0-jre", ""},
		{"kts-platform", "app/build.gradle.kts", "software.amazon.awssdk:bom", true, "2.25.0", ""},
		{"kts-val", "lib/build.gradle.kts", "io.ktor:ktor-client-cio", true, "2.3.12", ""},
	})
	bumpGolden(t, gradle{}, "gradle/groovy", []bumpCase{
		{"groovy-map", "build.gradle", "org.apache.commons:commons-text", true, "1.12.0", ""},
		{"groovy-properties", "build.gradle", "junit:junit", true, "4.13.3", ""},
		{"groovy-ext", "build.gradle", "org.jetbrains.kotlin:kotlin-gradle-plugin", true, "1.9.24", ""},
		{"groovy-swap", "build.gradle", "com.squareup.retrofit2:retrofit", true, "3.0.0", "com.squareup.retrofit3:retrofit"},
		{"groovy-trailing-comment", "build.gradle", "org.xerial:sqlite-jdbc", true, "3.46.1.0", ""},
	})
}

func TestGradleBumpRefused(t *testing.T) {
	hideTools(t)
	ctx := context.Background()
	root := copyTestdata(t, "gradle/kts")
	tests := []struct{ name, manifest, dep, toName string }{
		{"no version in the catalog", "gradle/libs.versions.toml", "org.jetbrains.kotlinx:kotlinx-coroutines-core", ""},
		{"swap a catalog entry", "gradle/libs.versions.toml", "com.squareup.okhttp3:okhttp", "com.squareup.okhttp3:okhttp-bom"},
		{"swap a variable version", "lib/build.gradle.kts", "io.ktor:ktor-client-core", "io.ktor:ktor-client"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := depIn(t, gradle{}, root, tt.manifest, tt.dep, true)
			before := treeSnapshot(t, root)
			if _, err := (gradle{}).Bump(ctx, root, d, "9.9.9", tt.toName); !errors.Is(err, core.ErrNotSupported) {
				t.Errorf("err = %v", err)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Error("a refused bump changed files")
			}
		})
	}
}

func TestGradleBumpLocks(t *testing.T) {
	root := copyTestdata(t, "gradle/kts")
	log := filepath.Join(t.TempDir(), "log")
	write(t, root, map[string]string{"lib/gradle.lockfile": "empty=\n"})
	if err := os.WriteFile(filepath.Join(root, "gradlew"), []byte("#!/bin/sh\necho \"$*\" >> "+log+"\necho changed >> app/gradle.lockfile\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := depIn(t, gradle{}, root, "gradle/libs.versions.toml", "com.squareup.okhttp3:okhttp", true)
	res, err := gradle{}.Bump(context.Background(), root, d, "4.12.0", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app/gradle.lockfile", "gradle/libs.versions.toml"}
	if !res.Locked || !reflect.DeepEqual(res.Files, want) {
		t.Errorf("bump = %+v", res)
	}
	if b, _ := os.ReadFile(log); string(b) != ":app:dependencies :lib:dependencies --write-locks\n" {
		t.Errorf("gradlew ran with %q", b)
	}
	if got := gradleLockTasks(".", []string{"gradle.lockfile", "buildscript-gradle.lockfile", "a/b/gradle.lockfile", "c/gradle/dependency-locks/compileClasspath.lockfile"}); got != "dependencies :a:b:dependencies :c:dependencies" {
		t.Errorf("gradleLockTasks = %q", got)
	}
}

// Without gradle a lockfile cannot move: an edit still lands with a note,
// and a bump that edits nothing reports the missing tool.
func TestGradleBumpNoTool(t *testing.T) {
	hideTools(t)
	ctx := context.Background()
	root := copyTestdata(t, "gradle/kts")
	d := depIn(t, gradle{}, root, "app/build.gradle.kts", "com.google.guava:guava", true)
	before := treeSnapshot(t, root)
	if _, err := (gradle{}).Bump(ctx, root, d, d.Version, ""); !errors.Is(err, core.ToolMissing{Tool: "gradle"}) {
		t.Errorf("lock only: err = %v", err)
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
		t.Error("a bump with nothing to edit changed files")
	}
	res, err := gradle{}.Bump(ctx, root, d, "33.0.0-jre", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Locked || res.Note != "gradle not found" || !reflect.DeepEqual(res.Files, []string{"app/build.gradle.kts"}) {
		t.Errorf("edit: bump = %+v", res)
	}
}

func TestGradleBumpCRLF(t *testing.T) {
	hideTools(t)
	root := t.TempDir()
	src := "[versions]\r\nokhttp = \"4.11.0\"\r\n\r\n[libraries]\r\nokhttp = { module = \"com.squareup.okhttp3:okhttp\", version.ref = \"okhttp\" }\r\n"
	write(t, root, map[string]string{"gradle/libs.versions.toml": src})
	d := depIn(t, gradle{}, root, "gradle/libs.versions.toml", "com.squareup.okhttp3:okhttp", true)
	if d.Line != 2 || d.Version != "4.11.0" {
		t.Fatalf("dep = %+v", d)
	}
	if _, err := (gradle{}).Bump(context.Background(), root, d, "4.12.0", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "gradle/libs.versions.toml"))
	if want := strings.Replace(src, "4.11.0", "4.12.0", 1); string(got) != want {
		t.Errorf("got %q", got)
	}
}

func TestGradleCommands(t *testing.T) {
	root := copyTestdata(t, "gradle")
	tests := []struct{ manifest, want string }{
		{"kts/app/build.gradle.kts", "cd kts && gradle"},
		{"kts/gradle/libs.versions.toml", "cd kts && gradle"},
		{"groovy/build.gradle", "cd groovy && ./gradlew"},
	}
	for _, tt := range tests {
		got := gradle{}.Commands(root, core.Manifest{Path: tt.manifest})
		want := core.Commands{Build: []string{tt.want + " build -x test"}, Test: []string{tt.want + " test"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Commands(%s) = %+v", tt.manifest, got)
		}
	}
	if got := (gradle{}).Imports(core.Dep{Name: "com.google.guava:guava"}); !reflect.DeepEqual(got, []string{"com.google.common"}) {
		t.Errorf("Imports = %v", got)
	}
}

// TestGradleBumpReal regenerates a lockfile against a file repository, so
// gradle resolves without the network.
func TestGradleBumpReal(t *testing.T) {
	if !have("gradle") {
		t.Skip("gradle not found")
	}
	ctx := context.Background()
	root := t.TempDir()
	files := map[string]string{
		"settings.gradle": "rootProject.name = 'app'\n",
		"build.gradle": "plugins { id 'java' }\n\nrepositories { maven { url = uri('repo') } }\n\n" +
			"dependencies {\n    implementation 'com.example:fixture:1.0.0'\n}\n\ndependencyLocking { lockAllConfigurations() }\n",
	}
	for _, v := range []string{"1.0.0", "2.0.0"} {
		dir := "repo/com/example/fixture/" + v + "/"
		files[dir+"fixture-"+v+".pom"] = "<project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId><artifactId>fixture</artifactId><version>" + v + "</version></project>\n"
		files[dir+"fixture-"+v+".jar"] = "PK\x05\x06" + strings.Repeat("\x00", 18)
	}
	write(t, root, files)
	if out, err := run(ctx, root, nil, "gradle -q dependencies --write-locks --offline"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	d := depIn(t, gradle{}, root, "build.gradle", "com.example:fixture", true)
	res, err := gradle{}.Bump(ctx, root, d, "2.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locked || !reflect.DeepEqual(res.Files, []string{"build.gradle", "gradle.lockfile"}) {
		t.Errorf("bump = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "gradle.lockfile")); !strings.Contains(string(b), "com.example:fixture:2.0.0=") {
		t.Errorf("gradle.lockfile:\n%s", b)
	}
}
