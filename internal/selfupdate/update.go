// Package selfupdate replaces the running binary with a signed, checksummed release.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

const binMax = 256 << 20

// Variables so tests can point them at a local server.
var (
	latestURL = "https://api.github.com/repos/mohsensc/tend-cli/releases/latest"
	assetsURL = "https://github.com/mohsensc/tend-cli/releases/download"
)

type ReleaseInfo struct {
	Tag     string
	Version string
	Newer   bool // newer than the running binary
	URL     string
}

var releaseRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-(alpha|beta|rc)\.?\d+)?$`)

// Latest asks GitHub for the latest release and compares it with current. A
// build that is not a release or a prerelease the release workflow publishes
// ("", "dev", a git describe string, a pseudo-version, a +dirty build) never
// has a newer version and costs no request.
func Latest(ctx context.Context, f core.Fetcher, current string) (*ReleaseInfo, error) {
	if !releaseRe.MatchString(current) {
		return &ReleaseInfo{Tag: "dev", Version: "dev"}, nil
	}
	res, err := f.Get(ctx, latestURL, func(o *core.FetchOpts) { o.NoCache = true })
	if err != nil {
		return nil, fmt.Errorf("selfupdate: %w", err)
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("selfupdate: latest release: status %d", res.Status)
	}
	var rel struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if err := json.Unmarshal(res.Body, &rel); err != nil {
		return nil, fmt.Errorf("selfupdate: latest release: %w", err)
	}
	next, ok := ver.Parse(rel.Tag)
	if !ok {
		return nil, fmt.Errorf("selfupdate: latest release has an odd tag %q", rel.Tag)
	}
	curr, _ := ver.Parse(current)
	return &ReleaseInfo{
		Tag:     rel.Tag,
		Version: strings.TrimPrefix(rel.Tag, "v"),
		Newer:   ver.Compare(next, curr) > 0,
		URL:     rel.URL,
	}, nil
}

// Apply downloads r, verifies the signature and checksum, and swaps the binary.
func Apply(ctx context.Context, f core.Fetcher, r *ReleaseInfo) error {
	exe, err := executable()
	if err != nil {
		return fmt.Errorf("selfupdate: %w", err)
	}
	return apply(ctx, f, r, exe, runtime.GOOS, runtime.GOARCH)
}

// Cleanup removes the old binary a Windows update leaves behind; call it at start.
func Cleanup() {
	if runtime.GOOS != "windows" {
		return
	}
	if exe, err := executable(); err == nil {
		os.Remove(exe + ".old")
	}
}

// executable is the running binary with symlinks resolved: Apply moves the
// real file aside, so Cleanup has to look beside the real file too.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func apply(ctx context.Context, f core.Fetcher, r *ReleaseInfo, exe, goos, goarch string) error {
	if r == nil || r.Tag == "" || r.Version == "" {
		return errors.New("selfupdate: no release to apply")
	}
	if err := writable(exe); err != nil {
		return err
	}
	base := assetsURL + "/" + r.Tag + "/"
	sums, err := download(ctx, f, base+"checksums.txt", 0)
	if err != nil {
		return err
	}
	sig, err := download(ctx, f, base+"checksums.txt.sig", 0)
	if err != nil {
		return fmt.Errorf("%w (a release without a signature is refused)", err)
	}
	if err := verifySig(PublicKey, sums, sig); err != nil {
		return err
	}
	name := fmt.Sprintf("tend_%s_%s_%s.tar.gz", r.Version, goos, goarch)
	if goos == "windows" {
		name = strings.TrimSuffix(name, ".tar.gz") + ".zip"
	}
	want, err := checksum(sums, name)
	if err != nil {
		return err
	}
	archive, err := download(ctx, f, base+name, archiveTimeout)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("selfupdate: sha256 of %s is %x, checksums.txt says %s", name, sum, want)
	}
	bin, err := extract(archive, goos)
	if err != nil {
		return fmt.Errorf("selfupdate: %s: %w", name, err)
	}
	return replace(exe, bin, goos == "windows")
}

// archiveTimeout is how long the release archive, a few megabytes, may take
// to download on a slow link.
const archiveTimeout = 10 * time.Minute

func download(ctx context.Context, f core.Fetcher, url string, timeout time.Duration) ([]byte, error) {
	res, err := f.Get(ctx, url, func(o *core.FetchOpts) { o.NoCache, o.Timeout = true, timeout })
	if err != nil {
		return nil, fmt.Errorf("selfupdate: %w", err)
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("selfupdate: GET %s: status %d", url, res.Status)
	}
	return res.Body, nil
}

func verifySig(key ed25519.PublicKey, data, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return fmt.Errorf("selfupdate: checksums.txt.sig is not base64: %w", err)
	}
	if !ed25519.Verify(key, data, raw) {
		return errors.New("selfupdate: signature of checksums.txt does not match the key built into this binary")
	}
	return nil
}

func checksum(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("selfupdate: checksums.txt has no entry for %s", name)
}

func extract(archive []byte, goos string) ([]byte, error) {
	if goos == "windows" {
		return fromZip(archive)
	}
	return fromTar(archive)
}

func fromTar(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("no tend binary in the archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == "tend" {
			return readLimited(tr, binMax)
		}
	}
}

func fromZip(archive []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if path.Base(f.Name) != "tend.exe" || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return readLimited(rc, binMax)
	}
	return nil, errors.New("no tend.exe in the archive")
}

func readLimited(r io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err == nil && len(b) > limit {
		err = errors.New("binary is larger than the limit")
	}
	return b, err
}

// writable fails early, before any download, when exe cannot be replaced.
func writable(exe string) error {
	f, err := os.CreateTemp(filepath.Dir(exe), ".tend-update-*")
	if err != nil {
		return denied(exe, err)
	}
	f.Close()
	return os.Remove(f.Name())
}

func denied(exe string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("selfupdate: cannot write to %s (%w); rerun as a user who can, e.g. sudo tend update, or reinstall tend into a directory you own", filepath.Dir(exe), err)
	}
	return fmt.Errorf("selfupdate: %w", err)
}

// replace swaps exe for bin. The new file is written beside exe so the final
// rename stays on one filesystem, and synced so a crash cannot leave a
// truncated binary behind. Windows cannot overwrite a running image but can
// rename it, so the old one moves to exe.old first.
func replace(exe string, bin []byte, windows bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".tend-update-*")
	if err != nil {
		return denied(exe, err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(bin)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		return fmt.Errorf("selfupdate: %w", err)
	}
	if !windows {
		return rename(exe, tmp.Name(), exe)
	}
	old := exe + ".old"
	os.Remove(old)
	if err := rename(exe, exe, old); err != nil {
		return err
	}
	if err := rename(exe, tmp.Name(), exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	return nil
}

func rename(exe, from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return denied(exe, err)
	}
	return nil
}
