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

func TestMavenDetect(t *testing.T) {
	root := copyTestdata(t, "maven")
	var got []string
	for _, m := range manifestsIn(t, maven{}, root) {
		got = append(got, m.Path+" "+m.Tool+" "+m.Lock)
	}
	want := []string{"boot/pom.xml mvn ", "multi/api/pom.xml mvn ", "multi/core/pom.xml mvn ", "multi/pom.xml mvn "}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Detect = %q", got)
	}
}

func TestMavenDeps(t *testing.T) {
	root := copyTestdata(t, "maven")
	for _, tt := range []struct{ name, manifest string }{
		{"parent", "multi/pom.xml"},
		{"api", "multi/api/pom.xml"},
		{"core", "multi/core/pom.xml"},
		{"external-parent", "boot/pom.xml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goldenCheck(t, "maven/deps/"+tt.name+".golden.json", jsonOf(t, depsIn(t, maven{}, root, tt.manifest)))
		})
	}
}

func TestMavenMeta(t *testing.T) {
	f := fetch.Replay(recordedDir("maven"))
	for _, name := range []string{"com.google.code.gson:gson", "mysql:mysql-connector-java"} {
		t.Run(name, func(t *testing.T) {
			meta, err := maven{}.Meta(context.Background(), f, name)
			if err != nil {
				t.Fatal(err)
			}
			goldenCheck(t, "maven/meta/"+strings.ReplaceAll(name, ":", "_")+".golden.json", jsonOf(t, meta))
		})
	}
	t.Run("not found", func(t *testing.T) {
		if _, err := (maven{}).Meta(context.Background(), f, "com.example.tend:nope"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("not a coordinate", func(t *testing.T) {
		if _, err := (maven{}).Meta(context.Background(), f, "gson"); err == nil {
			t.Error("no error for a name without a groupId")
		}
	})
}

func TestMavenMetaQualifiers(t *testing.T) {
	base := mavenCentral + "/org/example/lib"
	f := replayCanned(t,
		canned{url: base + "/maven-metadata.xml", body: `<metadata><versioning><release>1.1.0</release><versions>
			<version>1.0.0-SNAPSHOT</version><version>1.0.0</version><version>1.0.1.Final</version>
			<version>1.1.0-M1</version><version>1.1.0.RC1</version><version>1.1.0</version>
			<version>2.0.0.Alpha1</version><version>2.0.0-SNAPSHOT</version></versions></versioning></metadata>`},
		canned{url: mavenSearch + "?q=g:%22org.example%22+AND+a:%22lib%22&core=gav&rows=400&wt=json", body: `{"response":{"docs":[{"v":"1.1.0","timestamp":1700000000000}]}}`},
		canned{url: base + "/1.1.0/lib-1.1.0.pom", body: `<project><scm><connection>scm:git:git@github.com:example/lib.git</connection></scm></project>`},
	)
	meta, err := maven{}.Meta(context.Background(), f, "org.example:lib")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range meta.Releases {
		s := r.Version
		if r.Pre {
			s += " pre"
		}
		if !r.Time.IsZero() {
			s += " " + r.Time.Format("2006-01-02")
		}
		got = append(got, s)
	}
	want := []string{"1.0.0", "1.0.1.Final", "1.1.0-M1 pre", "1.1.0.RC1 pre", "1.1.0 2023-11-14", "2.0.0.Alpha1 pre"}
	if !reflect.DeepEqual(got, want) || meta.Latest != "1.1.0" || meta.Repo != "https://github.com/example/lib" {
		t.Errorf("releases = %q, latest %s, repo %s", got, meta.Latest, meta.Repo)
	}
}

func TestMavenMetaRecord(t *testing.T) {
	wantRecord(t)
	f := liveClient(t, "maven")
	for _, name := range []string{"com.google.code.gson:gson", "mysql:mysql-connector-java", "com.example.tend:nope"} {
		if _, err := (maven{}).Meta(context.Background(), f, name); err != nil {
			t.Log(err)
		}
	}
}

func TestMvnCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0.0", -1},
		{"1.0.0.Final", "1.0.0", 1},
		{"1.0.0.RELEASE", "1.0.0.Final", 1},
		{"1.0.0-M1", "1.0.0", -1},
		{"1.0.0-alpha-1", "1.0.0-beta-1", -1},
		{"1.0.0.Beta2", "1.0.0.CR1", -1},
		{"1.0.0-RC1", "1.0.0-RC2", -1},
		{"2.0.0-M3", "1.9.9", 1},
		{"33.0.0-android", "33.0.0-jre", -1},
		{"9.4.51.v20230217", "9.4.52.v20230823", -1},
		{"1.10.0", "1.9.0", 1},
	}
	for _, tt := range tests {
		if got := mvnCompare(tt.a, tt.b); got != tt.want {
			t.Errorf("mvnCompare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestMavenBump(t *testing.T) {
	bumpGolden(t, maven{}, "maven/multi", []bumpCase{
		{"own-property", "api/pom.xml", "com.squareup.okhttp3:okhttp", true, "4.12.0", ""},
		{"parent-property", "api/pom.xml", "com.fasterxml.jackson.core:jackson-databind", true, "2.17.0", ""},
		{"literal", "api/pom.xml", "org.apache.commons:commons-lang3", true, "3.14.0", ""},
		{"multi-line", "api/pom.xml", "mysql:mysql-connector-java", true, "8.0.34", ""},
		{"relocation", "api/pom.xml", "mysql:mysql-connector-java", true, "8.4.0", "com.mysql:mysql-connector-j"},
		{"managed-bom", "pom.xml", "software.amazon.awssdk:bom", false, "2.25.0", ""},
	})
	bumpGolden(t, maven{}, "maven/boot", []bumpCase{
		{"external-parent", "pom.xml", "org.springframework.boot:spring-boot-starter-parent", true, "3.3.4", ""},
	})
}

func TestMavenBumpRefused(t *testing.T) {
	ctx := context.Background()
	root := copyTestdata(t, "maven")
	tests := []struct {
		name, manifest, dep, toName, why string
	}{
		{"version built from two parts", "multi/api/pom.xml", "com.google.guava:guava", "", "is not a plain value"},
		{"version from a parent outside the repo", "boot/pom.xml", "org.springframework.boot:spring-boot-starter-web", "",
			"comes from the dependencyManagement of parent org.springframework.boot:spring-boot-starter-parent:3.1.0, outside this repository"},
		{"swap the parent", "boot/pom.xml", "org.springframework.boot:spring-boot-starter-parent", "io.quarkus:quarkus-bom", "replacing"},
		{"swap a version the parent manages", "multi/api/pom.xml", "com.fasterxml.jackson.core:jackson-databind", "tools.jackson.core:jackson-databind", "is managed in"},
		{"swap a version this pom manages", "own/pom.xml", "com.google.code.gson:gson", "com.squareup.moshi:moshi", "is managed in"},
	}
	write(t, root, map[string]string{"own/pom.xml": `<project>
  <groupId>x</groupId>
  <artifactId>y</artifactId>
  <version>1</version>
  <dependencyManagement>
    <dependencies>
      <dependency>
        <groupId>com.google.code.gson</groupId>
        <artifactId>gson</artifactId>
        <version>2.10.1</version>
      </dependency>
    </dependencies>
  </dependencyManagement>
  <dependencies>
    <dependency>
      <groupId>com.google.code.gson</groupId>
      <artifactId>gson</artifactId>
    </dependency>
  </dependencies>
</project>
`})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := depIn(t, maven{}, root, tt.manifest, tt.dep, true)
			before := treeSnapshot(t, root)
			_, err := (maven{}).Bump(ctx, root, d, "99.0.0", tt.toName)
			if !errors.Is(err, core.ErrNotSupported) || !strings.Contains(err.Error(), tt.why) {
				t.Errorf("err = %v, want it to say %q", err, tt.why)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, root)) {
				t.Error("a refused bump changed files")
			}
		})
	}
}

func TestMavenBumpCRLF(t *testing.T) {
	root := t.TempDir()
	src := "<project>\r\n  <groupId>x</groupId>\r\n  <artifactId>y</artifactId>\r\n  <version>1</version>\r\n  <dependencies>\r\n    <dependency>\r\n      <groupId>com.google.code.gson</groupId>\r\n      <artifactId>gson</artifactId>\r\n      <version>2.10.1</version>\r\n    </dependency>\r\n  </dependencies>\r\n</project>\r\n"
	write(t, root, map[string]string{"pom.xml": src})
	d := depIn(t, maven{}, root, "pom.xml", "com.google.code.gson:gson", true)
	if d.Line != 9 || d.Version != "2.10.1" {
		t.Fatalf("dep = %+v", d)
	}
	if _, err := (maven{}).Bump(context.Background(), root, d, "2.11.0", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "pom.xml"))
	if want := strings.Replace(src, "2.10.1", "2.11.0", 1); string(got) != want {
		t.Errorf("got %q", got)
	}
}

func TestMavenCommands(t *testing.T) {
	root := copyTestdata(t, "maven")
	tests := []struct{ manifest, dir, mvn string }{
		{"multi/api/pom.xml", "cd multi && ", "mvn"},
		{"multi/pom.xml", "cd multi && ", "mvn"},
		{"boot/pom.xml", "cd boot && ", "./mvnw"},
	}
	for _, tt := range tests {
		got := maven{}.Commands(root, core.Manifest{Path: tt.manifest})
		want := core.Commands{
			Build: []string{tt.dir + tt.mvn + " -q -B -DskipTests package"},
			Test:  []string{tt.dir + tt.mvn + " -q -B verify"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Commands(%s) = %+v", tt.manifest, got)
		}
	}
}

func TestMavenImports(t *testing.T) {
	tests := []struct{ name, want string }{
		{"org.apache.commons:commons-lang3", "org.apache.commons.lang3"},
		{"com.google.guava:guava", "com.google.common"},
		{"org.projectlombok:lombok", "lombok"},
		{"com.fasterxml.jackson.core:jackson-databind", "com.fasterxml.jackson.databind"},
		{"org.springframework.boot:spring-boot-starter-web", "org.springframework.boot"},
		{"io.github.resilience4j:resilience4j-core", "io.github.resilience4j"},
		{"com.stripe:stripe-java", "com.stripe"},
	}
	for _, tt := range tests {
		if got := (maven{}).Imports(core.Dep{Name: tt.name}); !reflect.DeepEqual(got, []string{tt.want}) {
			t.Errorf("Imports(%s) = %v", tt.name, got)
		}
	}
}

// TestMavenBumpReal checks that a bumped multi-module build still validates.
// The validate phase binds no plugins, so it runs offline.
func TestMavenBumpReal(t *testing.T) {
	if !have("mvn") {
		t.Skip("mvn not found")
	}
	root := copyTestdata(t, "maven/multi")
	d := depIn(t, maven{}, root, "api/pom.xml", "com.fasterxml.jackson.core:jackson-databind", true)
	if _, err := (maven{}).Bump(context.Background(), root, d, "2.17.0", ""); err != nil {
		t.Fatal(err)
	}
	if out, err := run(context.Background(), root, nil, "mvn -q -B -o validate"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if d := depIn(t, maven{}, root, "api/pom.xml", d.Name, true); d.Version != "2.17.0" {
		t.Errorf("after bump: %+v", d)
	}
}
