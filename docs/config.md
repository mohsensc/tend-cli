# Configuration

tend works with no configuration. Everything below goes in one optional file,
`tend.yml` at the repo root. tend looks for `tend.yml`, then `.github/tend.yml`.
`--config <path>` (or `TEND_CONFIG`) names a file explicitly. A relative
`--config` is read from the directory tend runs in, after `-C`; a relative
`TEND_CONFIG` from the repository's top level.

Every key is optional. An empty file is the same as no file. Unknown keys are
errors, so a typo cannot quietly turn something off. `tend init` writes a file
with every key at its default, commented out.

Durations use Go syntax (`20m`, `1h30m`) plus whole days (`3d`).

- [Top level](#top-level)
- [updates](#updates)
- [ignore](#ignore)
- [groups](#groups)
- [apis](#apis)
- [deps](#deps)
- [model](#model)
- [verify](#verify)
- [deliver](#deliver)
- [Environment](#environment)

## Top level

### base

String, default `""`. The branch PRs are opened against and worktrees are cut
from: a branch name, such as `develop`, not `origin/develop`. Empty means the
repository's default branch.

```yaml
base: develop
```

### cooldown

Duration from `0d` to `90d`, default `3d`. Releases younger than this are
skipped, which keeps the first days of a bad or hijacked release out of your
tree. Security fixes, deprecations and API changes do not wait: each is about
something wrong with what you use now, not about a new release being out.

```yaml
cooldown: 7d
```

### schedule

Duration, default `1d`. How often `tend watch` runs when `--every` is not
given, and the cron `tend init --ci` writes into the workflow. One of `1h`,
`2h`, `3h`, `4h`, `6h`, `8h`, `12h`, `1d` or `7d`, the intervals a cron line
keeps exactly. A daily run starts at 06:17 UTC and a weekly one on Monday at
that time. Changing it later does not touch a workflow already written; run
`tend init --ci --force` again, or edit the cron line.

```yaml
schedule: 6h
```

### onboarding

`pr`, `quiet` or `off`, default `pr`. What tend's first run on a repo does.

- `pr`: tend catches the repo up in a few PRs, a stage each: fixes, then
  runtimes and build tooling, then major upgrades, then migrations and
  replacements. The first opens with a short welcome, removes the
  dependencies nothing in the project uses, adds a test baseline when the
  project's tests are too thin to prove the rest, and lists the stages to
  come. A repo with nothing to catch up on gets a `tend.yml` with every key
  commented out, so the PR is not empty.
- `quiet`: no catch-up. tend starts its usual work and says once, in its
  output, which dependencies nothing uses.
- `off`: no catch-up, and no look for unused dependencies.

The first run is the first on a repo where tend has never opened a PR, open,
closed or merged; the forge says so even when tend's state is lost. A
catch-up PR is the one open PR like any other, and nothing goes ahead of it:
a security fix goes in it, and a tend PR left open from before is closed and
folded in. Once it merges, the next stage opens. Closing one unmerged is
final: tend ends the catch-up and goes on with its usual work. Setting
`quiet` or `off` while one is open closes it on the next run. In commit mode
each stage is a commit.
[docs/how-it-works.md](how-it-works.md#the-catch-up) says what goes in each
stage, and [docs/pr-anatomy.md](pr-anatomy.md#the-first-pull-request) shows
the PRs and how tend decides what nothing uses.

```yaml
onboarding: quiet
```

### ecosystems

List of names, default `[]`, which means every ecosystem. When set, tend reads
the manifests of these ecosystems only. The names are the ones listed under
[ignore](#ignore); `custom` keeps the `deps` entries. API calls are not an
ecosystem and are always followed; an `ignore` entry with `ecosystem: api`
leaves one alone.

```yaml
ecosystems: [npm, docker, actions]
```

## updates

Which changes get a PR. A change that matches none of these is recorded in
local state and shown by `tend check --all`, but gets no PR.

| key | type | default | meaning |
|---|---|---|---|
| `security` | bool | `true` | an advisory affects the version in use |
| `deprecations` | bool | `true` | a package, version, symbol, endpoint, field, image or Action is deprecated, sunset, end of life or archived |
| `major` | bool | `false` | a major bump of a package the code does not use |
| `minor` | bool | `false` | a minor bump. Off because a bump that touches nothing is noise |
| `patch` | bool | `false` | a patch bump, same reasoning |

A breaking change always gets a PR: a major bump of a package the code uses,
a removed endpoint or field, or a minor or patch bump whose release notes
break or remove something the code calls. A base image or an Action counts as
used by the line that names it. A bump that carries a security fix is a
security change. The last three keys only decide what happens to plain bumps.

```yaml
updates:
  minor: true
  patch: true
```

## ignore

List, default `[]`. Changes matching an entry are recorded as ignored and
never get a PR.

| key | type | meaning |
|---|---|---|
| `name` | string, required | glob on the canonical name: `lodash`, `@types/*`, `actions/*` |
| `ecosystem` | string | limit to one ecosystem, or `api` to match an API provider by name, or a host the code calls |
| `versions` | list of strings | ranges: `>=`, `>`, `<=`, `<`, `==`, `^x`, `~x`. A comma means and. Without it every version matches |
| `reason` | string | shown in `tend check --all` |

```yaml
ignore:
  - name: lodash
    ecosystem: npm
    versions: [">=5"]
    reason: pinned on purpose
  - name: stripe
    ecosystem: api
    reason: migration scheduled for Q1
```

An entry named `"*"` with an ecosystem and no versions turns that ecosystem
off: tend does not even read its manifests. To name the few to keep instead,
use [ecosystems](#ecosystems). The names are `npm go pip maven
gradle cargo rubygems composer nuget docker actions toolchain git terraform
helm custom`; [how it works](how-it-works.md#what-it-watches) says what each
covers.

```yaml
ignore:
  - name: "*"
    ecosystem: gradle
```

## groups

List, default `[]`. Packages whose names match go into one PR. Packages from
the same source repository that release together are grouped without any
configuration. So is an npm package that only resolves at the version of the
one being bumped: one whose release in use pins it as a peer at exactly that
version, or one of its scope family at the same version (`vitest` and
`@vitest/coverage-v8`). It moves to the same target in the same PR, in one
lock pass, when it has that release.

| key | type | meaning |
|---|---|---|
| `name` | string, required | the group name, used in the PR title and branch |
| `match` | list of globs, required | canonical package names |

```yaml
groups:
  - name: aws-sdk
    match: ["@aws-sdk/*", "aws-sdk-*"]
```

## apis

List, default `[]`. External APIs to watch in addition to the built-in
catalog (see [adding a provider](adding-a-provider.md) for the catalog). An
entry with the name of a catalog provider replaces that provider's hosts and
spec.

A host your code calls that neither covers is looked up on its own: in the
[APIs.guru](https://apis.guru) directory, then at the usual spec paths
(`/openapi.json`, `/swagger.json`, `/v3/api-docs` and a few more) on the host
and on its `docs.` and `developer.` siblings. Nothing is sent but a `GET`, a
host that does not answer is not asked again, and what was found is trusted
for a week. A host with no spec anywhere is listed
under `untracked` in `tend check` with the entry to add here. An `ignore`
entry with `ecosystem: api` and the host as its name keeps a host out of all
of this.

| key | type | meaning |
|---|---|---|
| `name` | string, required | provider name, also what `ignore` matches with `ecosystem: api` |
| `hosts` | list of strings, required | hosts the code calls |
| `spec` | string | URL of an OpenAPI 3 document (JSON or YAML), or a path relative to the repo root |
| `changelog` | string | URL of an RSS or Atom feed or an HTML page, for an API without a spec. An entry that names an endpoint your code calls, or a field it reads, together with words like deprecated, removed or sunset becomes a deprecation of it. With a [model](#model), entries that say so in prose are read by the model, once each |
| `probe` | bool, default `false` | send `OPTIONS` and `HEAD` to the endpoints your code uses and read `Deprecation` and `Sunset` headers. Unauthenticated, never off-host. A host that does not answer is not asked about its other endpoints |

One of `spec` and `changelog` is required.

```yaml
apis:
  - name: billing
    hosts: [api.billing.internal]
    spec: https://billing.internal/openapi.json
    probe: true
```

## deps

List, default `[]`. A version written somewhere no ecosystem reads, such as an
install script, followed through the registry or the repository it comes from.

| key | type | meaning |
|---|---|---|
| `name` | string, required | what to call it in titles and branches |
| `file` | glob, required | the files to look in; with a `/` it matches the whole path, without one the file name |
| `match` | string, required | a regexp with a group named `v` around the version |
| `source` | string, required | `github:<owner>/<repo>` for its tags and releases, `npm:<package>`, `pypi:<package>` or `<ecosystem>:<name>` for that registry's package, or `url:<template>` for a page that lists versions, with `{name}` filled in |

A source that is a registry package brings that package's yanks, deprecations
and security advisories with it.

```yaml
deps:
  - name: protoc
    file: scripts/install.sh
    match: 'PROTOC_VERSION=(?P<v>[0-9.]+)'
    source: github:protocolbuffers/protobuf
  - name: esbuild
    file: Makefile
    match: 'esbuild@(?P<v>[0-9.]+)'
    source: npm:esbuild
```

## model

Optional. Without a model, tend applies version bumps and codemods, and opens
a decision PR when a change needs real code edits. With one, it also writes the
migration for call sites no codemod covers, and the tests a catch-up's
baseline needs. [how-it-works.md](how-it-works.md#what-leaves-the-machine) says what is sent.

The whole entry can be one line naming the provider and the model it serves,
or `none` for no model:

```yaml
model: groq/openai/gpt-oss-120b
```

| key | type | default | meaning |
|---|---|---|---|
| `provider` | `none`, `anthropic`, `openai`, `groq`, `ollama`, `command` | `none` | `groq` and `ollama` are `openai` at their own address |
| `model` | string | `""` | the provider's model id, also accepted as `name`. Required except for `command` |
| `base_url` | string | `""` | for `openai`: any server that speaks the chat completions API. Empty means the provider's own. A key goes only over https, or over http to this machine |
| `key_env` | string | `""` | name of the env var holding the API key. Empty tries `TEND_MODEL_KEY`, then the provider's usual variable (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GROQ_API_KEY`). `ollama` needs none, and sends `TEND_MODEL_KEY` when it is set. `GITHUB_TOKEN`, `GH_TOKEN` and tend's own `TEND_*` variables are refused |
| `command` | string | `""` | for `provider: command`: a shell line that reads the prompt on stdin and writes the reply on stdout. It runs without tokens and keys in its environment; name the one it needs in `key_env` |
| `attempts` | int | `2` | tries at a diff that applies and passes the project's commands, for the `command` provider |
| `turns` | int | `30` | tool calls a model with tool calling may make on one change, once more for every 10 call sites the change moves, up to five times |
| `tokens` | int | `400000` | tokens it may spend on one change, prompts and replies together, scaled as `turns` is. `0` means no limit |

A provider with tool calling (all but `command`) works the change itself: it
reads and searches the worktree, edits the files the change explains and their
tests, and runs the project's own install, build and test commands. It is done
when every command passes, not when it says so. Running out of `turns` or
`tokens` turns the PR into a decision with what was tried. A catch-up of a
large or old project is a lot of migration; a stronger model than the
example's gets further with it.

The `command` provider writes one diff instead; a reply that holds no diff, or
one that does not apply, is sent back with the reason, and one that applies
but fails the project's commands is taken back and sent back with their
output, up to `attempts` times. The last try's diff stays for the proof to
decide.

```yaml
model:
  provider: command
  command: ollama run qwen2.5-coder
  attempts: 3
```

## verify

How the proof runs. By default tend detects install, build and test commands
per ecosystem (`npm ci`, `go build ./...`, `go test ./...`, `pytest`, and so
on). Each line runs with `sh -c` in the worktree, in order install, build,
test, and stops at the first failure.

| key | type | default | meaning |
|---|---|---|---|
| `install` | list of strings | `[]` | replaces the detected install lines |
| `build` | list of strings | `[]` | replaces the detected build lines |
| `test` | list of strings | `[]` | replaces the detected test lines |
| `timeout` | duration | `20m` | for the whole run |
| `container` | string | `""` | image name. When set, each command runs through `docker run` with the worktree mounted. A missing docker is an error, never a silent fallback to the host |
| `env` | list of strings | `[]` | names of env vars to pass through, for what the project's tests genuinely need. Anything that looks like a secret is removed otherwise: names containing token, secret, password, api key, private or credential, and names ending in `_KEY` or `_PAT`. `GITHUB_TOKEN`, `GH_TOKEN` and `TEND_*` are tend's own and are refused here. The same rule, with the same names kept, holds for the lock and bump tools tend runs itself, so a private registry's token listed here reaches them too |

```yaml
verify:
  install: [npm ci]
  test: [npm test, npm run lint]
  timeout: 30m
  env: [NPM_CONFIG_REGISTRY]
```

## deliver

How the result leaves your machine.

| key | type | default | meaning |
|---|---|---|---|
| `mode` | `pr`, `commit` | `pr` | `commit` pushes straight to `branch`, only what passed its proof, and reverts it when the repository's CI fails on it. Opt in only; it skips review |
| `branch` | string | `""` | commit mode: the branch to push to, a branch name. Empty means `base` |
| `labels` | list of strings | `[tend]` | added to every PR. Missing labels are created with color `0e8a16` |
| `draft` | bool | `false` | open every PR as a draft. A PR is a draft anyway when vetting flags something or the tests already fail on the base |
| `automerge` | `none`, `patch`, `minor`, `security` | `none` | turn on GitHub auto-merge for covered changes, see below |
| `merge_method` | `merge`, `squash`, `rebase` | `squash` | used for auto-merge |
| `reviewers` | list of strings | `[]` | logins, or `org/team` for a team. A login GitHub refuses (the PR author, say) is reported on stderr and skipped |
| `title_prefix` | string | `""` | prepended verbatim to titles and commit subjects |

`automerge` names a ceiling, and applies only when the proof passed on the
patched tree, vetting found nothing to flag, and the PR is not a draft:

- `security` covers every security fix that is a patch-level or minor-level bump
- `patch` covers patch-level bumps
- `minor` covers patch-level and minor-level bumps

Only mechanical changes, version bumps and codemods, can merge on their own.
Major bumps, security fixes among them, API changes, decision PRs, changes
without a proof and changes a model wrote any part of are never merged
automatically. If the repository does not allow auto-merge, tend adds a note
to the PR body and carries on.

```yaml
deliver:
  labels: [dependencies, tend]
  reviewers: [alice, acme/platform]
  automerge: patch
  title_prefix: "chore(deps): "
```

Commit mode, pushing to a long-lived branch:

```yaml
deliver:
  mode: commit
  branch: deps
```

In commit mode the push must fast-forward. If the branch moved, nothing is
pushed, since the tree on top of the new tip was never proven, and the next run
does the change again from there. A change with no build or test command to
prove it, or one that ends as a decision, is never pushed. The next run waits
for the repository's CI on the commit, and reverts the commit when CI fails
on it and did not fail on the one before; the tracking issue lists what was
reverted and why. CI runs on tend's commits only when the token can start
workflows, which the default token cannot.
[How it works](how-it-works.md#commit-mode) has the details.

## Environment

Nothing but these.

| variable | use |
|---|---|
| `GITHUB_TOKEN`, `GH_TOKEN` | token for the GitHub API and for pushing. Without either, tend runs `gh auth token` if `gh` is installed |
| `GITHUB_REPOSITORY` | `owner/name`, used when the repo has no origin remote |
| `TEND_CONFIG` | path to the config file. A relative one is read from the repository's top level; `--config` is read from the directory tend runs in, after `-C`, as `git -C` does |
| `TEND_HOME` | state directory. Default is `tend` under the user cache dir. A relative path, here or in `--home`, is read from the directory tend runs in, after `-C` |
| `TEND_LOG` | `debug` logs HTTP requests with tokens removed |
| `TEND_MODEL_KEY` | API key for the model provider, after `model.key_env` |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GROQ_API_KEY` | the providers' usual key variables, read when neither of the above is set |
| `TEND_DEFAULT_TOKEN` | set to `true` by the Action when it runs with the workflow's own token, so tend can say that `deliver.automerge` will not merge anything |
| `TEND_GIT_USER`, `TEND_GIT_EMAIL` | commit identity when the repo's git config has none. Both are needed |
| `TEND_RECORD` | `1` records upstream responses in tests. Contributors only |
