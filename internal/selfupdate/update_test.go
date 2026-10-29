package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mohsensc/tend-cli/internal/fetch"
)

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipfile(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	return buf.Bytes()
}

// release is a fake release server. Fields can be bent to make a bad release.
type release struct {
	tag    string
	assets map[string][]byte
	sums   string
	sig    string
	priv   ed25519.PrivateKey
	noSig  bool
}

func newRelease(t *testing.T, tag string, assets map[string][]byte) *release {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := PublicKey
	PublicKey = pub
	t.Cleanup(func() { PublicKey = old })
	r := &release{tag: tag, assets: assets, priv: priv}
	r.resign()
	return r
}

func (r *release) resign() {
	var b strings.Builder
	for name, body := range r.assets {
		sum := sha256.Sum256(body)
		fmt.Fprintf(&b, "%x  %s\n", sum, name)
	}
	r.sums = b.String()
	r.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(r.priv, []byte(r.sums)))
}

func reply(w http.ResponseWriter, s string) {
	_, _ = w.Write([]byte(s))
}

func (r *release) routes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"checksums.txt", func(w http.ResponseWriter, _ *http.Request) { reply(w, r.sums) })
	mux.HandleFunc(prefix+"checksums.txt.sig", func(w http.ResponseWriter, _ *http.Request) {
		if r.noSig {
			http.NotFound(w, nil)
			return
		}
		reply(w, r.sig+"\n")
	})
	for name, body := range r.assets {
		mux.HandleFunc(prefix+name, func(w http.ResponseWriter, _ *http.Request) { reply(w, string(body)) })
	}
}

func (r *release) serve(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	r.routes(mux, "/dl/"+r.tag+"/")
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q, "html_url": "https://example.test/%s"}`, r.tag, r.tag)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oldL, oldA := latestURL, assetsURL
	latestURL, assetsURL = srv.URL+"/latest", srv.URL+"/dl"
	t.Cleanup(func() { latestURL, assetsURL = oldL, oldA })
}

func exe(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tend")
	if err := os.WriteFile(p, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApply(t *testing.T) {
	linux := tarball(t, map[string]string{"tend": "new binary", "README.md": "docs"})
	win := zipfile(t, map[string]string{"tend.exe": "new windows binary", "LICENSE": "mit"})
	assets := map[string][]byte{
		"tend_1.2.0_linux_amd64.tar.gz":  linux,
		"tend_1.2.0_windows_amd64.zip":   win,
		"tend_1.2.0_darwin_arm64.tar.gz": tarball(t, map[string]string{"README.md": "no binary here"}),
	}
	f := fetch.New("", "test")
	info := &ReleaseInfo{Tag: "v1.2.0", Version: "1.2.0", Newer: true}

	tests := []struct {
		name    string
		goos    string
		goarch  string
		bend    func(*release)
		want    string // new contents of the executable
		wantErr string
		oldFile bool
	}{
		{name: "linux", goos: "linux", goarch: "amd64", want: "new binary"},
		{name: "windows keeps the old binary aside", goos: "windows", goarch: "amd64", want: "new windows binary", oldFile: true},
		{name: "signature from another key", goos: "linux", goarch: "amd64", wantErr: "does not match the key",
			bend: func(r *release) {
				_, other, _ := ed25519.GenerateKey(rand.Reader)
				r.priv = other
				r.resign()
			}},
		{name: "checksums edited after signing", goos: "linux", goarch: "amd64", wantErr: "does not match the key",
			bend: func(r *release) { r.sums += "0000  evil.tar.gz\n" }},
		{name: "signature missing", goos: "linux", goarch: "amd64", wantErr: "without a signature is refused",
			bend: func(r *release) { r.noSig = true }},
		{name: "signature is not base64", goos: "linux", goarch: "amd64", wantErr: "not base64",
			bend: func(r *release) { r.sig = "!!!not base64!!!" }},
		{name: "archive does not match its checksum", goos: "linux", goarch: "amd64", wantErr: "sha256 of tend_1.2.0_linux_amd64.tar.gz",
			bend: func(r *release) {
				r.assets = map[string][]byte{"tend_1.2.0_linux_amd64.tar.gz": tarball(t, map[string]string{"tend": "tampered"})}
				good := r.sums
				r.resign()
				r.sums, r.sig = good, base64.StdEncoding.EncodeToString(ed25519.Sign(r.priv, []byte(good)))
			}},
		{name: "no entry for this platform", goos: "linux", goarch: "riscv64", wantErr: "no entry for tend_1.2.0_linux_riscv64.tar.gz"},
		{name: "archive without a binary", goos: "darwin", goarch: "arm64", wantErr: "no tend binary"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRelease(t, "v1.2.0", map[string][]byte{})
			for k, v := range assets {
				r.assets[k] = v
			}
			r.resign()
			if tc.bend != nil {
				tc.bend(r)
			}
			r.serve(t)
			path := exe(t)

			err := apply(context.Background(), f, info, path, tc.goos, tc.goarch)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				if got := read(t, path); got != "old binary" {
					t.Errorf("a refused update changed the binary to %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := read(t, path); got != tc.want {
				t.Errorf("binary = %q, want %q", got, tc.want)
			}
			fi, _ := os.Stat(path)
			if fi.Mode().Perm() != 0o755 {
				t.Errorf("mode = %v", fi.Mode())
			}
			_, oldErr := os.Stat(path + ".old")
			if (oldErr == nil) != tc.oldFile {
				t.Errorf(".old present = %v, want %v", oldErr == nil, tc.oldFile)
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".tend-update-") {
					t.Errorf("temp file left behind: %s", e.Name())
				}
			}
		})
	}
}

func TestApplyNeedsARelease(t *testing.T) {
	err := apply(context.Background(), fetch.New("", "test"), nil, exe(t), "linux", "amd64")
	if err == nil {
		t.Fatal("applied nothing without complaint")
	}
}

func TestLatest(t *testing.T) {
	newRelease(t, "v1.4.0", nil).serve(t)
	f := fetch.New("", "test")
	tests := []struct {
		current string
		tag     string
		version string
		newer   bool
	}{
		{"1.3.9", "v1.4.0", "1.4.0", true},
		{"v1.3.9", "v1.4.0", "1.4.0", true},
		{"1.4.0", "v1.4.0", "1.4.0", false},
		{"1.10.0", "v1.4.0", "1.4.0", false},
		{"", "dev", "dev", false},
		{"dev", "dev", "dev", false},
		{"v1.3.9-4-gabc1234-dirty", "dev", "dev", false},
		{"v1.4.0-rc.1", "v1.4.0", "1.4.0", true},
		{"v1.4.0-beta2", "v1.4.0", "1.4.0", true},
		{"v1.5.0-rc.1", "v1.4.0", "1.4.0", false},
		{"v1.2.4-0.20261029120000-abcdef123456", "dev", "dev", false},
		{"v1.3.0+dirty", "dev", "dev", false},
		{"v1.3.0-rc.1+dirty", "dev", "dev", false},
		{"v1.3.0-rc.1-2-gabc1234", "dev", "dev", false},
	}
	for _, tc := range tests {
		t.Run(tc.current, func(t *testing.T) {
			got, err := Latest(context.Background(), f, tc.current)
			if err != nil {
				t.Fatal(err)
			}
			if got.Tag != tc.tag || got.Version != tc.version || got.Newer != tc.newer {
				t.Errorf("Latest(%q) = %+v", tc.current, got)
			}
		})
	}
}

func TestLatestDevMakesNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		reply(w, `{"tag_name": "v9.0.0"}`)
	}))
	defer srv.Close()
	old := latestURL
	latestURL = srv.URL
	defer func() { latestURL = old }()
	for _, v := range []string{"", "dev", "v1.2.4-0.20261029120000-abcdef123456", "v1.3.0+dirty"} {
		if _, err := Latest(context.Background(), fetch.New("", "test"), v); err != nil {
			t.Fatal(err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("development builds made %d requests", n)
	}
	if _, err := Latest(context.Background(), fetch.New("", "test"), "1.0.0"); err != nil || hits.Load() != 1 {
		t.Fatalf("a release build made %d requests: %v", hits.Load(), err)
	}
}

func TestLatestErrors(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"not found": func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
		"not json":  func(w http.ResponseWriter, _ *http.Request) { reply(w, "<html>") },
		"odd tag":   func(w http.ResponseWriter, _ *http.Request) { reply(w, `{"tag_name": "nightly"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			old := latestURL
			latestURL = srv.URL
			defer func() { latestURL = old }()
			if _, err := Latest(context.Background(), fetch.New("", "test"), "1.0.0"); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestChecksum(t *testing.T) {
	sums := []byte("AA11  tend_1.0.0_linux_amd64.tar.gz\nbb22 *tend_1.0.0_windows_amd64.zip\n\nmalformed line\n")
	tests := []struct {
		name string
		want string
		err  bool
	}{
		{"tend_1.0.0_linux_amd64.tar.gz", "aa11", false},
		{"tend_1.0.0_windows_amd64.zip", "bb22", false},
		{"tend_1.0.0_darwin_arm64.tar.gz", "", true},
		{"malformed", "", true},
	}
	for _, tc := range tests {
		got, err := checksum(sums, tc.name)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("checksum(%s) = %q, %v", tc.name, got, err)
		}
	}
}

func TestPublicKeyIsValid(t *testing.T) {
	if len(PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("key is %d bytes", len(PublicKey))
	}
	if hex.EncodeToString(PublicKey) == strings.Repeat("0", 64) {
		t.Fatal("the key is still the placeholder")
	}
}

func TestKeyMatchesActionPEM(t *testing.T) {
	pem, err := os.ReadFile("../../action/pubkey.pem")
	if err != nil {
		t.Skip(err)
	}
	// The PKIX encoding of an ed25519 key is a fixed 12-byte prefix plus the raw key.
	prefix, _ := hex.DecodeString("302a300506032b6570032100")
	body := strings.NewReplacer("-----BEGIN PUBLIC KEY-----", "", "-----END PUBLIC KEY-----", "", "\n", "").Replace(string(pem))
	der, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(der, append(prefix, PublicKey...)) {
		t.Error("action/pubkey.pem and key.go hold different keys")
	}
}

func TestReadLimited(t *testing.T) {
	if b, err := readLimited(bytes.NewReader(make([]byte, 64)), 64); err != nil || len(b) != 64 {
		t.Errorf("a binary at the limit: %d bytes, %v", len(b), err)
	}
	if _, err := readLimited(bytes.NewReader(make([]byte, 65)), 64); err == nil || !strings.Contains(err.Error(), "larger than the limit") {
		t.Errorf("a binary over the limit: %v", err)
	}
}

func TestReplaceInAReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs a directory this user cannot write")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	path := filepath.Join(dir, "tend")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	for name, err := range map[string]error{
		"replace":  replace(path, []byte("new"), false),
		"writable": writable(path),
		"apply":    apply(context.Background(), fetch.New("", "test"), &ReleaseInfo{Tag: "v1.2.0", Version: "1.2.0"}, path, "linux", "amd64"),
	} {
		if err == nil || !strings.Contains(err.Error(), "cannot write to "+dir) || !strings.Contains(err.Error(), "sudo tend update") || !errors.Is(err, fs.ErrPermission) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := read(t, path); got != "old binary" {
		t.Errorf("binary = %q", got)
	}
}

func TestApplyThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	r := newRelease(t, "v1.2.0", map[string][]byte{
		"tend_1.2.0_linux_amd64.tar.gz": tarball(t, map[string]string{"tend": "new binary"}),
	})
	r.serve(t)
	dir := t.TempDir()
	real := filepath.Join(dir, "opt", "tend")
	link := filepath.Join(dir, "bin", "tend")
	for _, d := range []string{filepath.Dir(real), filepath.Dir(link)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(real, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(context.Background(), fetch.New("", "test"), &ReleaseInfo{Tag: "v1.2.0", Version: "1.2.0"}, exe, "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, real); got != "new binary" {
		t.Errorf("real file = %q", got)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced: %v %v", fi, err)
	}
	if got := read(t, link); got != "new binary" {
		t.Errorf("through the symlink = %q", got)
	}
}

const childEnv = "TEND_SELFUPDATE_CHILD"

func TestSelfUpdateChild(t *testing.T) {
	switch os.Getenv(childEnv) {
	case "apply":
		latestURL, assetsURL = os.Getenv("TEND_TEST_LATEST"), os.Getenv("TEND_TEST_ASSETS")
		PublicKey, _ = hex.DecodeString(os.Getenv("TEND_TEST_KEY"))
		if err := Apply(context.Background(), fetch.New("", "test"), &ReleaseInfo{Tag: "v1.2.0", Version: "1.2.0"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Exit(0)
	case "cleanup":
		Cleanup()
		os.Exit(0)
	}
}

func runChild(t *testing.T, path, mode string, env ...string) {
	t.Helper()
	cmd := exec.Command(path, "-test.run=^TestSelfUpdateChild$")
	cmd.Env = append(append(os.Environ(), childEnv+"="+mode), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", mode, err, out)
	}
}

func TestApplyReplacesTheRunningBinary(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("tend_1.2.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := tarball(t, map[string]string{"tend": string(bin)})
	exeName := "tend"
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(name, ".tar.gz") + ".zip"
		archive = zipfile(t, map[string]string{"tend.exe": string(bin)})
		exeName = "tend.exe"
	}
	r := newRelease(t, "v1.2.0", map[string][]byte{name: archive})
	r.serve(t)

	dir := t.TempDir()
	real := filepath.Join(dir, "opt", exeName)
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	start := real
	if runtime.GOOS != "windows" {
		start = filepath.Join(dir, "bin", exeName)
		if err := os.MkdirAll(filepath.Dir(start), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, start); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}

	runChild(t, start, "apply", "TEND_TEST_LATEST="+latestURL, "TEND_TEST_ASSETS="+assetsURL, "TEND_TEST_KEY="+hex.EncodeToString(PublicKey))

	after, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("the binary was not replaced")
	}
	if got, _ := os.ReadFile(real); !bytes.Equal(got, bin) {
		t.Fatal("the new binary differs from the release")
	}
	if start != real {
		if fi, err := os.Lstat(start); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the symlink the binary ran from was replaced: %v", err)
		}
	}
	_, oldErr := os.Stat(real + ".old")
	if (oldErr == nil) != (runtime.GOOS == "windows") {
		t.Fatalf(".old present = %v on %s", oldErr == nil, runtime.GOOS)
	}

	runChild(t, start, "cleanup")
	if _, err := os.Stat(real + ".old"); !os.IsNotExist(err) {
		t.Errorf("Cleanup left %s.old: %v", exeName, err)
	}
}
