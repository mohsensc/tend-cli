package selfupdate

import (
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const repoPath = "/mohsensc/tend-cli"

// installFixture serves a few signed releases the way GitHub lays them out and
// holds the public key install.sh must check them against.
type installFixture struct {
	url      string
	action   string // GITHUB_ACTION_PATH with action/pubkey.pem in it
	releases []*release
	priv     ed25519.PrivateKey
	tags     [][2]string // name and commit, in the order /tags lists them

	mu     sync.Mutex
	apiHit []string // Authorization headers seen on API paths
	dlHit  []string // Authorization headers seen on downloads
}

func fakeBinary(version string) []byte {
	return []byte("#!/bin/sh\necho " + version + "\n")
}

func commitOf(name string) string {
	sum := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%x", sum)[:40]
}

func (fx *installFixture) add(t *testing.T, tag string) *release {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	bin := string(fakeBinary(version))
	r := &release{tag: tag, priv: fx.priv, assets: map[string][]byte{
		fmt.Sprintf("tend_%s_linux_amd64.tar.gz", version):  tarball(t, map[string]string{"tend": bin}),
		fmt.Sprintf("tend_%s_darwin_arm64.tar.gz", version): tarball(t, map[string]string{"tend": bin}),
		fmt.Sprintf("tend_%s_windows_amd64.zip", version):   zipfile(t, map[string]string{"tend.exe": bin}),
	}}
	r.resign()
	fx.releases = append(fx.releases, r)
	fx.tag(tag, commitOf(tag))
	return r
}

func (fx *installFixture) tag(name, commit string) {
	fx.tags = append(fx.tags, [2]string{name, commit})
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	fx := &installFixture{priv: priv, action: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(fx.action, "action"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.action, "action", "pubkey.pem"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return fx
}

// start serves the releases added so far; the first one added is the newest.
func (fx *installFixture) start(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	for _, r := range fx.releases {
		r.routes(mux, repoPath+"/releases/download/"+r.tag+"/")
	}
	mux.HandleFunc("/repos"+repoPath+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "{\n  \"tag_name\": %q,\n  \"prerelease\": false\n}\n", fx.releases[0].tag)
	})
	mux.HandleFunc("/repos"+repoPath+"/releases", func(w http.ResponseWriter, _ *http.Request) {
		var items []string
		for _, r := range fx.releases {
			items = append(items, fmt.Sprintf("{\"tag_name\": %q, \"draft\": false}", r.tag))
		}
		fmt.Fprintf(w, "[%s]", strings.Join(items, ", "))
	})
	mux.HandleFunc("/repos"+repoPath+"/tags", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "100" {
			http.Error(w, "want per_page=100", http.StatusBadRequest)
			return
		}
		page, _ := strconv.Atoi(cmp.Or(r.URL.Query().Get("page"), "1"))
		var items []string
		for i, tg := range fx.tags {
			if i/100 == page-1 {
				items = append(items, fmt.Sprintf("{\"name\":%q,\"zipball_url\":\"z\",\"commit\":{\"sha\":%q,\"url\":\"u\"},\"node_id\":\"n\"}", tg[0], tg[1]))
			}
		}
		fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.mu.Lock()
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			fx.apiHit = append(fx.apiHit, r.Header.Get("Authorization"))
		} else {
			fx.dlHit = append(fx.dlHit, r.Header.Get("Authorization"))
		}
		fx.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	fx.url = srv.URL
}

func runInstall(t *testing.T, fx *installFixture, env []string, expect, match string, args ...string) {
	t.Helper()
	script, err := filepath.Abs("../../action/install_test.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script, expect, match}, args...)...)
	cmd.Env = append(os.Environ(),
		"TEND_API="+fx.url, "TEND_DOWNLOAD="+fx.url, "GITHUB_ACTION_PATH="+fx.action,
		"RUNNER_OS=Linux", "RUNNER_ARCH=X64", "GITHUB_ACTION_REF=", "GITHUB_TOKEN=")
	cmd.Env = append(cmd.Env, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

func needTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture binaries are shell scripts; the action job runs install.sh on Windows")
	}
	for _, tool := range []string{"bash", "openssl", "curl", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not found")
		}
	}
	out, _ := exec.Command("openssl", "version").Output()
	if !strings.HasPrefix(string(out), "OpenSSL 3") && !strings.HasPrefix(string(out), "OpenSSL 1.1.1") {
		t.Skip("openssl is " + strings.TrimSpace(string(out)) + ", ed25519 needs OpenSSL 1.1.1 or newer")
	}
}

func TestInstallScript(t *testing.T) {
	needTools(t)

	t.Run("explicit version", func(t *testing.T) {
		fx := newInstallFixture(t)
		fx.add(t, "v2.0.0")
		fx.add(t, "v1.2.0")
		fx.start(t)
		runInstall(t, fx, nil, "ok", "1.2.0", "1.2.0")
		runInstall(t, fx, nil, "ok", "1.2.0", "v1.2.0")
	})

	tests := []struct {
		name   string
		ref    string
		args   []string
		expect string
		match  string
	}{
		{"full tag in the ref", "v1.2.0", nil, "ok", "1.2.0"},
		{"major tag picks the newest in that major", "v1", nil, "ok", "1.10.0"},
		{"branch ref gets the latest release", "main", nil, "ok", "2.0.0"},
		{"empty ref gets the latest release", "", nil, "ok", "2.0.0"},
		{"input beats the ref", "v1", []string{"1.2.0"}, "ok", "1.2.0"},
		{"major with no release", "v3", nil, "fail", "no release of mohsensc/tend-cli matches"},
		{"minor ref stays on its minor", "v1.2", nil, "ok", "1.2.0"},
		{"minor with no release", "v1.3", nil, "fail", "no release of mohsensc/tend-cli matches"},
		{"commit of a v1 release while v2 exists", commitOf("v1.2.0"), nil, "ok", "1.2.0"},
		{"commit the major tag also points at", commitOf("v1.10.0"), nil, "ok", "1.10.0"},
		{"commit in upper case", strings.ToUpper(commitOf("v1.9.5")), nil, "ok", "1.9.5"},
		{"commit that is no release", commitOf("main"), nil, "fail", "is not a release"},
		{"commit tagged only with a major", commitOf("v0"), nil, "fail", "is not a release"},
		{"input beats a commit", commitOf("v1.2.0"), []string{"1.9.5"}, "ok", "1.9.5"},
		{"release-like branch", "v2-dev", nil, "fail", "not a release tag"},
		{"four-part ref", "v1.2.0.1", nil, "fail", "not a release tag"},
		{"unknown version", "", []string{"9.9.9"}, "fail", "could not download"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newInstallFixture(t)
			fx.add(t, "v2.0.0")
			fx.add(t, "v1.10.0")
			fx.add(t, "v1.2.0")
			fx.add(t, "v1.9.5")
			fx.tag("v1", commitOf("v1.10.0"))
			fx.tag("v0", commitOf("v0"))
			fx.start(t)
			runInstall(t, fx, []string{"GITHUB_ACTION_REF=" + tc.ref}, tc.expect, tc.match, tc.args...)
		})
	}

	bad := []struct {
		name  string
		bend  func(*testing.T, *installFixture, *release)
		match string
	}{
		{"signature from another key", func(t *testing.T, fx *installFixture, r *release) {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			r.priv = other
			r.resign()
		}, "does not verify"},
		{"checksums edited after signing", func(t *testing.T, fx *installFixture, r *release) {
			r.sums += "00  extra\n"
		}, "does not verify"},
		{"signature missing", func(t *testing.T, fx *installFixture, r *release) { r.noSig = true }, "could not download"},
		{"signature is garbage", func(t *testing.T, fx *installFixture, r *release) { r.sig = "***" }, "does not verify"},
		{"archive replaced", func(t *testing.T, fx *installFixture, r *release) {
			for name := range r.assets {
				r.assets[name] = tarball(t, map[string]string{"tend": "#!/bin/sh\necho evil\n"})
			}
		}, "sha256 mismatch for tend_1.2.0_linux_amd64.tar.gz"},
		{"no checksum for this platform", func(t *testing.T, fx *installFixture, r *release) {
			r.assets = map[string][]byte{"tend_1.2.0_linux_arm64.tar.gz": nil}
			r.resign()
		}, "no entry for tend_1.2.0_linux_amd64.tar.gz"},
		{"archive without the binary", func(t *testing.T, fx *installFixture, r *release) {
			body := tarball(t, map[string]string{"README": "x"})
			sum := sha256.Sum256(body)
			r.assets = map[string][]byte{"tend_1.2.0_linux_amd64.tar.gz": body}
			r.sums = fmt.Sprintf("%x  tend_1.2.0_linux_amd64.tar.gz\n", sum)
			r.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(r.priv, []byte(r.sums)))
		}, "does not contain tend"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			fx := newInstallFixture(t)
			r := fx.add(t, "v1.2.0")
			tc.bend(t, fx, r)
			fx.start(t)
			runInstall(t, fx, nil, "fail", tc.match, "1.2.0")
		})
	}

	t.Run("commit found on a later page of tags", func(t *testing.T) {
		fx := newInstallFixture(t)
		fx.add(t, "v2.0.0")
		for i := range 150 {
			fx.tag(fmt.Sprintf("v0.0.%d", i), commitOf(fmt.Sprint(i)))
		}
		fx.add(t, "v1.2.0")
		fx.start(t)
		runInstall(t, fx, []string{"GITHUB_ACTION_REF=" + commitOf("v1.2.0")}, "ok", "1.2.0")
	})

	t.Run("macOS on arm64", func(t *testing.T) {
		fx := newInstallFixture(t)
		fx.add(t, "v1.2.0")
		fx.start(t)
		runInstall(t, fx, []string{"RUNNER_OS=macOS", "RUNNER_ARCH=ARM64"}, "ok", "1.2.0", "1.2.0")
	})

	t.Run("Windows on x64", func(t *testing.T) {
		if _, err := exec.LookPath("unzip"); err != nil {
			t.Skip("unzip not found")
		}
		fx := newInstallFixture(t)
		fx.add(t, "v1.2.0")
		fx.start(t)
		runInstall(t, fx, []string{"RUNNER_OS=Windows", "RUNNER_ARCH=X64"}, "ok", "1.2.0", "1.2.0")
	})

	t.Run("the newest of a minor without a signature", func(t *testing.T) {
		fx := newInstallFixture(t)
		fx.add(t, "v1.3.0")
		fx.add(t, "v1.2.5").noSig = true
		fx.add(t, "v1.2.4")
		fx.start(t)
		runInstall(t, fx, []string{"GITHUB_ACTION_REF=v1.2"}, "ok", "1.2.4")
	})

	t.Run("a commit pin does not fall back", func(t *testing.T) {
		fx := newInstallFixture(t)
		fx.add(t, "v1.2.5").noSig = true
		fx.add(t, "v1.2.4")
		fx.start(t)
		runInstall(t, fx, []string{"GITHUB_ACTION_REF=" + commitOf("v1.2.5")}, "fail", "could not download")
	})

	t.Run("the newest of a major without a signature", func(t *testing.T) {
		fx := newInstallFixture(t)
		fx.add(t, "v1.10.0").noSig = true
		fx.add(t, "v1.9.5")
		fx.start(t)
		runInstall(t, fx, []string{"GITHUB_ACTION_REF=v1"}, "ok", "1.9.5")
	})

	t.Run("token goes to the API and not to downloads", func(t *testing.T) {
		fx := newInstallFixture(t)
		fx.add(t, "v1.2.0")
		fx.start(t)
		runInstall(t, fx, []string{"GITHUB_TOKEN=s3cret", "GITHUB_ACTION_REF=v1"}, "ok", "1.2.0")
		fx.mu.Lock()
		defer fx.mu.Unlock()
		if len(fx.apiHit) == 0 || fx.apiHit[0] != "Bearer s3cret" {
			t.Errorf("API calls carried %q", fx.apiHit)
		}
		for _, h := range fx.dlHit {
			if h != "" {
				t.Errorf("a download carried the token: %q", h)
			}
		}
	})
}
