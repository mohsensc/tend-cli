package eco

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

// pyImplicit are distributions a project needs with no import or name of
// theirs in it: what builds and serves it, and what frameworks load by
// themselves.
var pyImplicit = map[string]string{
	"setuptools": "a build tool", "setuptools-scm": "a build tool", "wheel": "a build tool",
	"pip": "a build tool", "build": "a build tool", "twine": "a build tool", "cython": "a build tool",
	"hatchling": "a build tool", "flit-core": "a build tool", "poetry-core": "a build tool",
	"pdm-backend": "a build tool", "maturin": "a build tool", "scikit-build-core": "a build tool",
	"gunicorn": "a server, started from outside the code", "uvicorn": "a server, started from outside the code",
	"psycopg2": "a database driver frameworks load by name", "psycopg2-binary": "a database driver frameworks load by name",
	"psycopg": "a database driver frameworks load by name", "psycopg-binary": "a database driver frameworks load by name",
	"mysqlclient": "a database driver frameworks load by name", "pymysql": "a database driver frameworks load by name",
	"oracledb": "a database driver frameworks load by name", "cx-oracle": "a database driver frameworks load by name",
	"pyodbc":  "a database driver frameworks load by name",
	"asyncpg": "a database driver frameworks load by name", "aiosqlite": "a database driver frameworks load by name",
	"aiomysql": "a database driver frameworks load by name", "asyncmy": "a database driver frameworks load by name",
	"pg8000": "a database driver frameworks load by name", "mysql-connector-python": "a database driver frameworks load by name",
	"pillow": "Django's ImageField loads it by itself", "python-dotenv": "Flask reads .env files with it when it is installed",
	"tzdata": "zoneinfo falls back to it where the system has no time zone data",
}

// pyLoaded are distributions a library imports by itself when the code asks
// for something it backs: a form, an Excel file, an async engine. They count
// only next to one of the libraries that load them.
var pyLoaded = map[string]struct {
	by   []string
	what string
}{
	"python-multipart": {[]string{"fastapi", "starlette"}, "FastAPI and Starlette parse forms with it"},
	"email-validator":  {[]string{"pydantic", "fastapi"}, "pydantic checks EmailStr with it"},
	"httpx":            {[]string{"fastapi", "starlette"}, "Starlette's TestClient runs on it"},
	"itsdangerous":     {[]string{"fastapi", "starlette"}, "Starlette signs sessions with it"},
	"jinja2":           {[]string{"fastapi", "starlette"}, "Starlette renders templates with it"},
	"orjson":           {[]string{"fastapi"}, "FastAPI's ORJSONResponse encodes with it"},
	"ujson":            {[]string{"fastapi"}, "FastAPI's UJSONResponse encodes with it"},
	"uvloop":           {[]string{"uvicorn"}, "uvicorn runs on it when it is installed"},
	"httptools":        {[]string{"uvicorn"}, "uvicorn parses HTTP with it when it is installed"},
	"websockets":       {[]string{"uvicorn"}, "uvicorn serves WebSockets with it"},
	"watchfiles":       {[]string{"uvicorn"}, "uvicorn reloads with it when it is installed"},
	"greenlet":         {[]string{"sqlalchemy"}, "SQLAlchemy's asyncio support runs on it"},
	"openpyxl":         {[]string{"pandas"}, "pandas reads and writes Excel files with it"},
	"xlrd":             {[]string{"pandas"}, "pandas reads Excel files with it"},
	"xlsxwriter":       {[]string{"pandas"}, "pandas writes Excel files with it"},
	"odfpy":            {[]string{"pandas"}, "pandas reads OpenDocument files with it"},
	"pyxlsb":           {[]string{"pandas"}, "pandas reads Excel binary files with it"},
	"python-calamine":  {[]string{"pandas"}, "pandas reads Excel files with it"},
	"pyarrow":          {[]string{"pandas", "polars", "datasets"}, "pandas reads and writes Parquet and Feather with it"},
	"fastparquet":      {[]string{"pandas"}, "pandas reads and writes Parquet with it"},
	"tables":           {[]string{"pandas"}, "pandas reads and writes HDF5 with it"},
	"tabulate":         {[]string{"pandas"}, "pandas writes Markdown tables with it"},
	"matplotlib":       {[]string{"pandas"}, "pandas plots with it"},
	"lxml":             {[]string{"pandas", "beautifulsoup4"}, "pandas and BeautifulSoup parse HTML and XML with it"},
	"html5lib":         {[]string{"pandas", "beautifulsoup4"}, "pandas and BeautifulSoup parse HTML with it"},
	"numexpr":          {[]string{"pandas"}, "pandas computes with it when it is installed"},
	"bottleneck":       {[]string{"pandas"}, "pandas computes with it when it is installed"},
	"s3fs":             {[]string{"pandas", "fsspec", "dask"}, "fsspec opens s3:// paths with it"},
	"gcsfs":            {[]string{"pandas", "fsspec", "dask"}, "fsspec opens gs:// paths with it"},
	"adlfs":            {[]string{"pandas", "fsspec", "dask"}, "fsspec opens Azure paths with it"},
	"cryptography":     {[]string{"pyjwt", "python-jose", "requests", "urllib3"}, "PyJWT signs RS and ES tokens with it, and TLS libraries load it"},
	"pysocks":          {[]string{"requests", "urllib3", "httpx"}, "requests and urllib3 reach SOCKS proxies with it"},
	"brotli":           {[]string{"requests", "urllib3", "httpx", "aiohttp"}, "HTTP clients decode Brotli with it when it is installed"},
	"brotlicffi":       {[]string{"requests", "urllib3", "httpx", "aiohttp"}, "HTTP clients decode Brotli with it when it is installed"},
	"zstandard":        {[]string{"urllib3", "httpx"}, "HTTP clients decode zstd with it when it is installed"},
	"h2":               {[]string{"httpx"}, "httpx speaks HTTP/2 with it"},
	"hiredis":          {[]string{"redis"}, "redis-py parses replies with it when it is installed"},
	"redis":            {[]string{"celery", "django", "rq", "kombu"}, "Celery and Django caches load it from a redis:// URL"},
	"bcrypt":           {[]string{"django", "passlib"}, "Django's and passlib's password hashers load it by name"},
	"argon2-cffi":      {[]string{"django", "passlib"}, "Django's and passlib's password hashers load it by name"},
}

// pyPlugins are the name shapes of distributions another tool loads by
// itself: by name from its config, or as whatever is installed.
var pyPlugins = []struct{ prefix, tool string }{
	{"pytest-", "pytest"}, {"flake8-", "flake8"}, {"pylint-", "pylint"}, {"mypy-", "mypy"},
	{"types-", "mypy"}, {"sphinx", "Sphinx"}, {"mkdocs-", "MkDocs"}, {"django-", "Django"},
}

func (py python) needs(u *usage, d core.Dep) (need, doubt string) {
	if path.Base(d.Manifest.Path) == "Pipfile" {
		return "Pipfile is read only to tend", ""
	}
	if u.pyConstraint(d.Manifest.Path) {
		return "a constraints file, which pins versions without installing anything", ""
	}
	n := PyNorm(d.Name)
	if why, ok := pyImplicit[n]; ok {
		return why, ""
	}
	if by := u.pyRequiredBy(n); by != "" {
		return "pinned for " + by + ", which requires it", ""
	}
	if why := pyExtra(u.root, d.Manifest, n); why != "" {
		return "", why
	}
	mods := py.Imports(d)
	// [tool.black] in pyproject.toml configures black.
	words := append([]string{d.Name, n, strings.ReplaceAll(n, "-", "_"), strings.ReplaceAll(n, "-", "."), "tool." + n}, mods...)
	if at := u.mention(words...); at != "" {
		return "named in " + at, ""
	}
	if at := u.configured(append([]string{n}, mods...)...); at != "" {
		return "configured in " + at, ""
	}
	pts := pyPointsOf(n)
	if at := u.mention(pts.scripts...); at != "" {
		return "its command is run in " + at, ""
	}
	if l, ok := pyLoaded[n]; ok && slices.ContainsFunc(l.by, func(by string) bool { return u.has(core.Pip, by) }) {
		return "", l.what
	}
	if len(pts.groups) > 0 {
		return "", "a plugin: its entry points in " + strings.Join(pts.groups, ", ") + " are loaded without an import"
	}
	if strings.HasSuffix(n, "-stubs") {
		return "", "stubs mypy reads by itself"
	}
	for _, p := range pyPlugins {
		if strings.HasPrefix(n, p.prefix) {
			return "", "a plugin " + p.tool + " loads by itself"
		}
	}
	if !pyKnown(n) {
		return "", fmt.Sprintf("nothing imports %s, but tend cannot tell which modules %s installs", mods[0], d.Name)
	}
	return "", ""
}

// pyExtra says which extra of a pyproject.toml offers n to the package's
// users, who install it by that name even when the package's own code never
// imports it; "" when none does.
func pyExtra(root string, m core.Manifest, n string) string {
	if path.Base(m.Path) != "pyproject.toml" {
		return ""
	}
	entries, err := pyEntries(root, m)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		switch {
		case e.extra == "" || PyNorm(e.name) != n:
		case e.extra == "*":
			return "an extra offers it to the package's users (optional = true)"
		default:
			return fmt.Sprintf("the extra %q offers it to the package's users", e.extra)
		}
	}
	return ""
}

// pyConstraint reports a requirements file that is named for constraints,
// or that another one reads with -c.
func (u *usage) pyConstraint(rel string) bool {
	if strings.Contains(strings.ToLower(path.Base(rel)), "constraint") {
		return true
	}
	if u.pyCons == nil {
		u.pyCons = map[string]bool{}
		files := slices.DeleteFunc(slices.Clone(u.files), func(f string) bool { return !pyReqFile(f) })
		for _, d := range u.deps {
			if ext := path.Ext(d.Manifest.Path); d.Ecosystem == core.Pip && (ext == ".txt" || ext == ".in") {
				files = append(files, d.Manifest.Path)
			}
		}
		slices.Sort(files)
		for _, f := range slices.Compact(files) {
			_, cons := pyIncludes(u.root, f)
			for _, c := range cons {
				u.pyCons[c] = true
			}
		}
	}
	return u.pyCons[rel]
}

// pyRequiredBy names another dependency of the project that, as an
// installed virtualenv says, requires n directly or through others; "" when
// none does. n's line then pins what that one installs anyway, and dropping
// it would let the version move.
func (u *usage) pyRequiredBy(n string) string {
	for _, d := range u.deps {
		if d.Ecosystem != core.Pip || d.Name == n {
			continue
		}
		seen := map[string]bool{d.Name: true}
		for queue := []string{d.Name}; len(queue) > 0; queue = queue[1:] {
			reqs, _ := pyDistReqs.Load(queue[0])
			names, _ := reqs.([]string)
			if slices.Contains(names, n) {
				return d.Name
			}
			for _, r := range names {
				if !seen[r] {
					seen[r] = true
					queue = append(queue, r)
				}
			}
		}
	}
	return ""
}

func pyPointsOf(n string) pyEntryPoints {
	if v, ok := pyPoints.Load(n); ok {
		return v.(pyEntryPoints)
	}
	return pyEntryPoints{}
}

// pyKnown reports whether tend knows the modules a distribution installs,
// from an installed virtualenv or its table, rather than guessing them.
func pyKnown(n string) bool {
	if _, ok := pyTopLevel.Load(n); ok {
		return true
	}
	_, ok := pyModules[n]
	return ok
}

// Remove deletes the requirement lines, or the entries of a pyproject.toml
// array, and has the lock tool lock again.
func (python) Remove(ctx context.Context, root string, deps []core.Dep) (*core.Bump, error) {
	m := deps[0].Manifest
	if path.Base(m.Path) == "Pipfile" {
		return nil, fmt.Errorf("%s is read only: %w", m.Path, core.ErrNotSupported)
	}
	entries, err := pyEntries(root, m)
	if err != nil {
		return nil, err
	}
	p := filepath.Join(root, filepath.FromSlash(m.Path))
	src, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	out, err := pyDrop(string(src), entries, deps, path.Ext(m.Path) == ".toml")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Path, err)
	}
	snap := snapFiles(root, m.Path, m.Lock)
	if err := writeKeepMode(p, []byte(out)); err != nil {
		return nil, err
	}
	if m.Lock == "" {
		return &core.Bump{Files: snap.changed(root), Note: "no lockfile"}, nil
	}
	tool := pyLockTool(root, m)
	if !have(tool) {
		return &core.Bump{Files: snap.changed(root), Note: tool + " not found"}, nil
	}
	if err := runIn(ctx, root, path.Dir(m.Path), nil, pyRelock(ctx, root, m)); err != nil {
		return nil, errors.Join(err, snap.restore(root))
	}
	return &core.Bump{Files: snap.changed(root), Locked: true}, nil
}

// pyDrop takes the requirements on deps out of src: whole lines, with their
// continuation lines, in a requirements file or a Poetry table, and the
// quoted entry with its comma in a TOML array, the line too when nothing else
// is on it.
func pyDrop(src string, entries []pyEntry, deps []core.Dep, toml bool) (string, error) {
	names := map[string]bool{}
	for _, d := range deps {
		names[PyNorm(d.Name)] = true
	}
	lines := strings.SplitAfter(src, "\n")
	gone := map[int]bool{}
	found := map[string]bool{}
	for _, e := range entries {
		n := PyNorm(e.name)
		if !names[n] {
			continue
		}
		if e.line < 1 || e.line > len(lines) {
			return "", fmt.Errorf("cannot find %s as written", e.name)
		}
		found[n] = true
		i := e.line - 1
		if !toml || e.poetry {
			gone[i] = true
			for !toml && strings.HasSuffix(strings.TrimRight(lines[i], "\r\n"), `\`) && i+1 < len(lines) {
				i++
				gone[i] = true
			}
			continue
		}
		rest, ok := pyCut(lines[i], e.raw)
		if !ok {
			return "", fmt.Errorf("cannot find %s as written", e.name)
		}
		lines[i] = rest
		gone[i] = strings.TrimSpace(rest) == ""
	}
	for n := range names {
		if !found[n] {
			return "", fmt.Errorf("no requirement for %s", n)
		}
	}
	var b strings.Builder
	for i, l := range lines {
		if !gone[i] {
			b.WriteString(l)
		}
	}
	return b.String(), nil
}

// pyCut takes the quoted requirement lit out of a line of a TOML array, with
// the comma that separates it from its neighbor.
func pyCut(line, lit string) (string, bool) {
	i := strings.Index(line, lit)
	if lit == "" || i < 0 {
		return "", false
	}
	before, after := line[:i], line[i+len(lit):]
	if t := strings.TrimLeft(after, " \t"); strings.HasPrefix(t, ",") {
		after = strings.TrimLeft(t[1:], " \t")
	} else if t := strings.TrimRight(before, " \t"); strings.HasSuffix(t, ",") {
		before = t[:len(t)-1]
	}
	return before + after, true
}

// pyRelock is the command that locks m again once requirements have left it.
func pyRelock(ctx context.Context, root string, m core.Manifest) string {
	switch path.Base(m.Lock) {
	case "uv.lock":
		return "uv lock"
	case "poetry.lock":
		return poetryLock(ctx, root, path.Dir(m.Path))
	}
	in, out := path.Base(m.Path), memberDir(path.Dir(m.Path), m.Lock)
	if pyCompiledBy(root, m.Lock) == "uv" {
		return "uv pip compile --quiet " + shArg(in) + " -o " + shArg(out)
	}
	return "pip-compile --quiet " + shArg(in) + " -o " + shArg(out)
}

// maxNotebook bounds the notebooks mention reads. Their outputs, plots
// above all, make them far larger than the code in them.
const maxNotebook = 64 << 20

// notebookCode is the code in a Jupyter notebook, its outputs left out, or
// nil when it cannot be read.
func notebookCode(p string) []byte {
	if filepath.Ext(p) != ".ipynb" {
		return nil
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxNotebook {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var nb struct {
		Cells []struct {
			Type   string          `json:"cell_type"`
			Source json.RawMessage `json:"source"`
		} `json:"cells"`
	}
	if json.Unmarshal(b, &nb) != nil {
		return nil
	}
	var res []byte
	for _, c := range nb.Cells {
		if c.Type != "code" {
			continue
		}
		var lines []string
		var one string
		if json.Unmarshal(c.Source, &lines) != nil && json.Unmarshal(c.Source, &one) == nil {
			lines = []string{one}
		}
		for _, l := range lines {
			res = append(res, l...)
		}
		res = append(res, '\n')
	}
	return res
}
