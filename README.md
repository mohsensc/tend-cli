# tend

Dependabot for breaking API changes, with proof.

tend watches the packages your project depends on and the external APIs it
calls. When something changes in a way that matters to your code, it fixes it,
runs your own build and tests on the fix, checks its own patch, and opens one
pull request with the evidence in the body. When an update touches nothing, it
stays quiet.

```
$ tend check
tend: 4 queued, 9 skipped, no open PR

  1  security     golang.org/x/mod   v0.33.0 → v0.40.0      GO-2026-6179 · 43 sites
  2  breaking     billing API        GET /v1/orders         field total_cents removed · 2 sites
  3  deprecation  node (Dockerfile)  16-alpine → 24-alpine  EOL 2023-09-11 · 2 sites
  4  deprecation  @acme/pay          1.4.0 → 2.0.0          2 sites

skipped: 8 noise, 1 cooldown   (tend check --all)

$ tend run
tend: opened #12 Bump golang.org/x/mod to v0.40.0 (fixes GO-2026-6179 +1 more) https://github.com/acme/shop/pull/12
```

The pull request says what changed upstream, which lines of your code it
touched, what ran on the base commit and on the patched tree, and what the
self-review checked. An excerpt:

```markdown
### Proof

| step | base (main@abc1234) | patched |
|---|---|---|
| `npm ci` | ok · 12s | ok · 13s |
| `npm test` | ok · 30s | ok · 31s |

### Vetting

- scope: every changed file is explained by this change
- symbols: every changed call resolves in the new version
```

[docs/pr-anatomy.md](docs/pr-anatomy.md) walks through a whole PR body.

## Quick start

```sh
go install github.com/mohsensc/tend-cli/cmd/tend@latest
cd your-project
tend check            # the queue, most important first; changes nothing
tend run --dry-run    # work the top change and print its PR instead of sending it
tend run              # open the PR; needs GITHUB_TOKEN or `gh auth login`
```

The first PR on a repo is a tidy-up with a short welcome: it removes the
dependencies nothing in the project uses and moves the code off deprecated
libraries, proven by your own build and tests like every tend PR, and lists
what only looks unused for you to decide. A repo with nothing to tidy gets a
PR that adds a commented `tend.yml`. After that, tend opens one PR at a time,
only for changes that touch your code. `onboarding: quiet` in `tend.yml`
skips the first PR; [docs/pr-anatomy.md](docs/pr-anatomy.md#the-first-pull-request)
shows one. `tend init` writes `tend.yml` and offers to run tend right away
(`--now` runs it without asking).

Without Go, download the archive for your platform from
[Releases](https://github.com/mohsensc/tend-cli/releases) and check it
against `checksums.txt`; [docs/self-update.md](docs/self-update.md) shows how
to check the signature too.

To run it daily on GitHub, `tend init --ci` writes this workflow (the cron
follows `schedule` in `tend.yml`). It also runs once when the workflow file
lands on your default branch, so adding tend starts the first run at once:

```yaml
name: tend
on:
  push:
    branches: ["main"]
    paths: [.github/workflows/tend.yml]
  schedule: [{cron: "17 6 * * *"}]
  workflow_dispatch:
permissions: {contents: write, pull-requests: write}
concurrency: {group: tend, cancel-in-progress: false}
jobs:
  tend:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with: {persist-credentials: false}
      - uses: mohsensc/tend-cli@v1
        with: {model-key: "${{ secrets.TEND_MODEL_KEY }}"}
```

The `model-key` line is only needed if you configure a model. Turn on "Allow
GitHub Actions to create and approve pull requests" under Settings > Actions >
General, or GitHub refuses the PR. The default `GITHUB_TOKEN` has two more
limits: PRs it opens do not start your other workflows, so your CI and
auto-merge never run on them, and it cannot push changes to
`.github/workflows`, so Action bumps fail. Pass a fine-grained token with the
workflows permission, or a GitHub App token from
`actions/create-github-app-token`, as `token:` to lift both. The other inputs
are `command` (`run` or `check`), `args`, `config` and `version`.

## What it watches

- Packages in every ecosystem it finds: npm, pnpm and yarn, Go modules, pip,
  poetry and uv, Maven, Gradle, Cargo, RubyGems, Composer and NuGet.
- Docker base images, GitHub Actions, Terraform providers and modules, and
  Helm charts.
- Runtime pins (`.nvmrc`, `.python-version`, `.tool-versions`, `go.mod`) and
  the oldest runtime the project says it supports, as release lines reach end
  of life.
- External APIs, called through an SDK or by raw HTTP, matched against the
  provider's OpenAPI document or changelog.
- The deprecation warnings your runtime prints while your tests run.
- Anything else, through a `deps` entry in `tend.yml`.

What it finds and cannot follow is listed under `untracked` by `tend check`,
with the `tend.yml` lines that would fix it; [limitations](docs/limitations.md)
has the exact rule for API hosts.

## How it decides

- A security fix, a deprecation and a breaking change to something your code
  uses get a pull request. Breaking means a removed endpoint, field or
  parameter, or a release whose number or notes say it breaks a symbol you
  call.
- A bump that touches none of that is recorded in local state and shown by
  `tend check --all`. `updates: {major: true, minor: true, patch: true}` turns
  on plain bump PRs.
- Releases younger than `cooldown` (3 days) wait. Security fixes,
  deprecations and API changes do not.
- Anything an `ignore` entry matches, and any change whose PR you closed, is
  left alone until a newer target exists. A PR a bot closed does not count:
  only a person closing it, or `tend skip`, says no.
- tend never has more than one PR open, and that PR holds the whole change:
  manifest, lockfile, every call site, config and the tests that had to move.
  Re-running updates it. The next change waits until it is merged or closed.

The queue runs security first, then API changes that break your code, then
deprecations, then breaking releases, then the rest. A security fix that shows
up while another PR is open takes its place, and the other change comes back
after it merges.

When there is no single replacement for something deprecated, tend opens a PR
that changes nothing and lists the options with their trade-offs; staying put
is always one. Comment `tend apply 2` and the next run applies option 2,
proves it and updates the PR. `tend skip` closes the PR and leaves that
release alone, and `tend retry` does the change again, once.

## Configuration

No config is needed. `tend init` writes a `tend.yml` with every key at its
default and commented out, and [docs/config.md](docs/config.md) describes each
one. A typical file is a few lines:

```yaml
cooldown: 7d
ignore:
  - {name: "@types/*"}
model: groq/openai/gpt-oss-120b
deliver: {automerge: security}
```

| key | default | what it sets |
|---|---|---|
| `base` | the repo's default branch | the branch PRs target |
| `cooldown` | `3d` | how old a release must be before tend takes it |
| `schedule` | `1d` | how often `watch` runs, and the cron `init --ci` writes |
| `onboarding` | `pr` | the first run's tidy-up PR: `pr`, `quiet` (no PR) or `off` |
| `ecosystems` | all | the only ecosystems tend reads |
| `updates` | security and deprecations on, plain bumps off | which changes get a PR |
| `ignore` | nothing | packages, versions or APIs to leave alone |
| `groups` | same source repo | packages that move together in one PR |
| `apis` | the built-in catalog | more API providers: hosts, spec, changelog |
| `deps` | nothing | versions written where no ecosystem looks |
| `model` | `none` | the provider for migrations no codemod covers |
| `verify` | detected per ecosystem | install, build and test commands, timeout, container, env |
| `deliver` | `mode: pr` | PR or commit, labels, draft, automerge, reviewers |

## Proof and vetting

The fix is made in a git worktree of the base commit under tend's state
directory; your checkout and index are never touched. tend runs the project's
own install, build and test commands on the base commit and on the patched
tree, with tokens and secrets removed from their environment, and the PR shows
both. A patch is proven when its run passes, or when it stops at the very step
where the base already stops; that PR opens as a draft and says why. Set
`verify.container` to an image and every command runs through `docker run`
with only the worktree mounted.

Before anything is pushed, the patch goes through vetting, a skeptical pass
that asks short questions and reports only what it checked. Does every changed
call exist in the new version? Does every field the patch reads exist in the
spec? Is the old symbol still used anywhere? Did the lockfile move more than
the bump needs? Was debug output or a comment added? Is anything now
unreferenced? Does the diff touch only files this change explains? Each
finding is fixed, dropped, or flagged in the PR, and a flag makes the PR a
draft. A patch that vetting changed is proven again.

## Self-update

Every release carries `checksums.txt` and an ed25519 signature over it, and
the public key is compiled into the binary. `tend update` checks the
signature, then the archive's checksum, then replaces itself; any mismatch
changes nothing. `run` and `watch` print one line when a newer release exists
and never update on their own. The Action verifies the same way before it puts
the binary on the path. `@v1` moves to the newest `v1.x.y`; pin `@v1.2.3` to
stay put. tend runs daily on its own repository.

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
matched, 4 `run` could not prove the fix.

## More

- [docs/how-it-works.md](docs/how-it-works.md): the proof, vetting, model
  providers, and what tend keeps and sends
- [docs/self-update.md](docs/self-update.md): releases, signatures and `tend update`
- [docs/limitations.md](docs/limitations.md): what tend does not do

## Contributing

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

An ecosystem is one file in `internal/eco` that implements the `Ecosystem`
interface from `internal/core/core.go`: find manifests, read dependencies, ask
the registry what exists, edit a requirement. Add it to `All()` in
`internal/eco/eco.go` and the rest of tend (noise policy, ranking, call-site
search, proof, vetting, delivery) picks it up unchanged.
[docs/adding-an-ecosystem.md](docs/adding-an-ecosystem.md) has the contract
and the tests it needs, and [docs/adding-a-provider.md](docs/adding-a-provider.md)
does the same for API providers.

## License

MIT. See [LICENSE](LICENSE).
