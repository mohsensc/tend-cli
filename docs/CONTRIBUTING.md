# Contributing

```sh
make test      # unit tests
make e2e       # end-to-end tests over fixture projects
make lint      # golangci-lint and gofmt
make record    # re-record upstream responses used by tests (needs network)
make demo      # tend live on a sample project; needs TEND_MODEL_KEY or GROQ_API_KEY
```

Tests never touch the network: upstream data is recorded under `testdata/`
and replayed. A test that needs a tool that is not installed skips and names
it.

## Adding an ecosystem

An ecosystem is one file in `internal/eco` that implements the `Ecosystem`
interface from `internal/core/core.go`: find manifests, read dependencies, ask
the registry what exists, edit a requirement. Add it to `All()` in
`internal/eco/eco.go` and the rest of tend (noise policy, ranking, call-site
search, proof, vetting, delivery) picks it up unchanged.
[adding-an-ecosystem.md](adding-an-ecosystem.md) has the contract and the
tests it needs, and [adding-a-provider.md](adding-a-provider.md) does the same
for API providers.

## Pull requests

One change per PR, in as few commits as it takes, usually one. Say what was
wrong and how you checked the fix. CI runs `make lint` and the full test suite
with `-race`.

## Commands

```
tend check   [--json] [--all] [--fail-on security|breaking|deprecation|any] [--reset]
tend run     [--dry-run] [--json] [--keep]
tend watch   [--every 6h]
tend init    [--ci] [--force] [--now]
tend update  [--check]
tend version [--check]

global: -C <dir>  --config <path>  --home <dir>  -v  -q
```

Exit codes: 0 done or nothing to do, 1 error, 2 usage, 3 `check --fail-on`
matched, 4 `run` could not prove the fix (in a GitHub Action, only with
`fail-on-unproven: true` once the change is in the tracking issue; otherwise
a warning), 5 `run` proved a change but had no
GitHub token to open its PR with, so it is only on a local branch, 130
interrupted.
