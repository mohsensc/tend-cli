# Adding an ecosystem

An ecosystem is one file in `internal/eco`: it finds manifests, reads
dependencies, asks a registry what exists, and edits a requirement. Everything
else (noise policy, ranking, call-site search, proof, vetting, delivery) works
on the types it returns and needs no change.

Take `internal/eco/cargo.go` as the model to copy, and read the `Ecosystem`
interface in [internal/core/core.go](../internal/core/core.go).

## Steps

1. Write `internal/eco/<name>.go` with a type that implements the interface.
   `Name` returns a short lowercase name; it appears in config, state and
   branch names and never changes.
2. Add it to `All()` in `internal/eco/eco.go`. The order there is the order
   tend reports in.

That is all tend needs. Optionally, a case in `ReadLock` in
`internal/eco/lock.go`, reading the lockfile with the parser `Deps` already
uses, lets vetting tell unrelated churn from the bump's own transitive
changes; without one, vetting says it cannot read the lockfile. Code in a
language tend does not scan yet needs a language file in `internal/scan`,
which is separate work; the adapter works without it, with no call sites
reported.

## The contract

- `Detect` finds manifests anywhere in the tree, so monorepos work. Results are sorted by path. `walk` in `eco.go` lists the files git knows of.
- `Deps` returns direct dependencies first, in manifest order, with `Line` set. Transitive dependencies from the lockfile follow, sorted by name, with `Direct` false. `Version` is the resolved version when a lockfile says so, else what the manifest pins. Names are canonical: compare them with `==`.
- `Meta` reads the public registry through the `Fetcher` it is given, never `net/http` directly, so tests can replay recorded answers. Releases are ascending. Mark yanked, retracted and unlisted versions `Yanked`. Put a per-version deprecation message in `Release.Deprecated` and a whole-package one in `Meta.Deprecated`. A 404 is an error naming the package.
- `Bump` edits exactly the requirement, keeping its style (`^`, `~`, `>=`, exact pins, quotes, spacing), and has the native tool lock exactly `to`, not the newest release the range allows. When the target is already inside the written range, only the lockfile changes. If the tool is not installed, return `Locked: false` and a `Note` saying so; do not fail. A swap (`toName != ""`) removes the old requirement and adds the new one in the same place (dev or prod).
- `Commands` returns conventional install, build and test lines, and nothing when nothing sensible exists; the proof then says there was nothing to run.
- `Imports` is pure: the names source code uses to import the dependency. Return nothing for what code does not import, such as a base image; the line that pins it then counts as its use.
- `Traits` are the facts other stages go by: the ecosystem's name in OSV, if OSV covers it; where to read about a package; whether a tag keeps its shape across a bump; whether the native tool can move a transitive dependency by itself.
- No shared state, with one exception: what `Meta` learned that `Bump` needs later in the same run, such as the commit behind each tag (`actionsShas` in `actions.go`). `Bump` fails with a clear error when it is not there.

## Tests

Table-driven and offline, in `internal/eco/<name>_test.go`, with fixtures
under `internal/eco/testdata/<name>/`. Cover `Detect` over a tree with nested,
ignored and vendored manifests; `Deps` with a golden JSON per manifest and
lockfile pair, with and without a lock; `Meta` against recorded responses for
a normal package, a deprecated or yanked one and a 404 (record with
`TEND_RECORD=1 go test ./internal/eco -run <Name>Record`, which is skipped
otherwise and never runs in CI); `Bump` with a golden diff for each
requirement style, with `have` replaced so the tool is missing, a bump inside
the written range, and one run of the real tool that skips when it is not
installed; and `Imports`.

Run `go test ./internal/eco`, `gofmt -l .` and `golangci-lint run`. No test
may reach the network.
