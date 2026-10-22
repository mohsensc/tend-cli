package selfupdate

import (
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

	mu     sync.Mutex
	apiHit []string // Authorization headers seen on API paths
	dlHit  []string // Authorization headers seen on downloads
}

func fakeBinary(version string) []byte {
	return []byte("#!/bin/sh\necho " + version + "\n")
}

func (fx *installFixture) add(t *testing.T, tag string) *release {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	name := fmt.Sprintf("tend_%s_linux_amd64.tar.gz", version)
	r := &release{tag: tag, priv: fx.priv, assets: map[string][]byte{name: tarball(t, map[string]string{"tend": string(fakeBinary(version))})}}
	r.resign()
	fx.releases = append(fx.releases, r)
	return r
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
		{"unknown version", "", []string{"9.9.9"}, "fail", "could not download"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newInstallFixture(t)
			fx.add(t, "v2.0.0")
			fx.add(t, "v1.10.0")
			fx.add(t, "v1.2.0")
			fx.add(t, "v1.9.5")
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
