package e2e

import (
	"archive/tar"
	"bytes"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Registry serves the packages under a testdata/npmreg tree the way
// registry.npmjs.org does: packuments at /<name>, tarballs at
// /<name>/-/<base>-<version>.tgz. Vars fills the placeholders a fixture
// project carries: {{registry}} and {{integrity <name>@<version>}}.
type Registry struct {
	*httptest.Server
	root string
	pkgs map[string]map[string][]byte // name -> version -> tarball
}

func NewRegistry(root string) (*Registry, error) {
	r := &Registry{root: root, pkgs: map[string]map[string][]byte{}}
	plain, err := filepath.Glob(filepath.Join(root, "*", "meta.json"))
	if err != nil {
		return nil, err
	}
	scoped, err := filepath.Glob(filepath.Join(root, "@*", "*", "meta.json"))
	if err != nil {
		return nil, err
	}
	metas := append(plain, scoped...)
	for _, m := range metas {
		dir := filepath.Dir(m)
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return nil, err
		}
		name := filepath.ToSlash(rel)
		r.pkgs[name] = map[string][]byte{}
		vs, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			if !v.IsDir() {
				continue
			}
			if r.pkgs[name][v.Name()], err = pack(filepath.Join(dir, v.Name())); err != nil {
				return nil, err
			}
		}
	}
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	return r, nil
}

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	p, _ := url.PathUnescape(strings.TrimPrefix(req.URL.EscapedPath(), "/"))
	if name, file, ok := strings.Cut(p, "/-/"); ok {
		for v, tgz := range r.pkgs[name] {
			if file == path.Base(name)+"-"+v+".tgz" {
				_, _ = w.Write(tgz)
				return
			}
		}
		http.NotFound(w, req)
		return
	}
	if _, ok := r.pkgs[p]; !ok {
		http.NotFound(w, req)
		return
	}
	doc, err := r.packument(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

func (r *Registry) packument(name string) (map[string]any, error) {
	dir := filepath.Join(r.root, filepath.FromSlash(name))
	var meta map[string]any
	if err := readJSON(filepath.Join(dir, "meta.json"), &meta); err != nil {
		return nil, err
	}
	deprecated, _ := meta["deprecated"].(map[string]any)
	delete(meta, "deprecated")
	all := map[string]any{}
	for v, tgz := range r.pkgs[name] {
		var man map[string]any
		if err := readJSON(filepath.Join(dir, v, "package.json"), &man); err != nil {
			return nil, err
		}
		sha := sha1.Sum(tgz)
		man["dist"] = map[string]any{
			"tarball":   r.URL + "/" + name + "/-/" + path.Base(name) + "-" + v + ".tgz",
			"shasum":    hex.EncodeToString(sha[:]),
			"integrity": integrity(tgz),
		}
		if msg, ok := deprecated[v]; ok {
			man["deprecated"] = msg
		}
		all[v] = man
	}
	meta["versions"] = all
	return meta, nil
}

// Vars are the placeholders fixture files carry, filled for this registry.
func (r *Registry) Vars() map[string]string {
	res := map[string]string{"{{registry}}": r.URL + "/"}
	for name, versions := range r.pkgs {
		for v, tgz := range versions {
			res["{{integrity "+name+"@"+v+"}}"] = integrity(tgz)
		}
	}
	return res
}

func integrity(tgz []byte) string {
	sum := sha512.Sum512(tgz)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

// pack makes a tarball of dir that is the same bytes every time, so the
// integrity a lockfile records holds.
func pack(dir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		hdr := &tar.Header{Name: "package/" + e.Name(), Mode: 0o644, Size: int64(len(b)), ModTime: time.Unix(499162500, 0), Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return stored(buf.Bytes()), nil
}

// stored wraps b in gzip without compressing it. compress/gzip frames its
// output differently from one Go release to the next, even at NoCompression;
// stored deflate blocks have one encoding.
func stored(b []byte) []byte {
	res := []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0xff}
	sum, size := crc32.ChecksumIEEE(b), uint32(len(b))
	for {
		n := min(len(b), 0xffff)
		final := byte(0)
		if n == len(b) {
			final = 1
		}
		res = append(res, final)
		res = binary.LittleEndian.AppendUint16(res, uint16(n))
		res = binary.LittleEndian.AppendUint16(res, ^uint16(n))
		res = append(res, b[:n]...)
		if final == 1 {
			break
		}
		b = b[n:]
	}
	res = binary.LittleEndian.AppendUint32(res, sum)
	return binary.LittleEndian.AppendUint32(res, size)
}

// CopyProject copies a fixture project to dst, filling in vars.
func CopyProject(src, dst string, vars map[string]string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(b)
		for k, v := range vars {
			s = strings.ReplaceAll(s, k, v)
		}
		return os.WriteFile(out, []byte(s), 0o644)
	})
}

func readJSON(p string, v any) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
