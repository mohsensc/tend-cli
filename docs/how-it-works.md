# How tend works

One run inventories the project, asks upstream what changed, keeps what
matters, picks the most important change, fixes it in a worktree, proves the
fix, vets the patch and delivers it. This page covers the parts that decide
whether you can trust the result.

## What it watches

Each name below is what config, state and branch names call the ecosystem.

| name | what tend reads |
|---|---|
| `npm` | `package.json` with `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock` |
| `go` | `go.mod` and `go.sum` |
| `pip` | requirements files, `pyproject.toml` with `poetry.lock` or `uv.lock`, `Pipfile.lock` |
| `maven`, `gradle` | `pom.xml`; Gradle build files, version catalogs and lockfiles |
| `cargo`, `rubygems`, `composer`, `nuget` | `Cargo.toml`, `Gemfile`, `composer.json`, project files, each with its lockfile |
| `docker` | base images in Dockerfiles and compose files; an image pinned by digest keeps a pin, to the new tag's digest |
| `actions` | the Actions your workflows use |
| `toolchain` | runtime pins (`.nvmrc`, `.python-version`, `.tool-versions`, `rust-toolchain`, `go.mod`'s `toolchain` line), which move on when their release line reaches end of life, and the oldest runtime the project supports (`engines.node`, `requires-python`, `go.mod`'s `go` line, a pom's Java release), which moves up once it falls out of support |
| `git` | tags pinned in `.pre-commit-config.yaml`, in git dependencies of `package.json`, pip requirements, `Cargo.toml` and `Gemfile`, and in GitHub release downloads in scripts, Dockerfiles, Makefiles and workflows |
| `terraform`, `helm` | providers and modules; chart dependencies, named for repository and chart (`charts.bitnami.com/bitnami/postgresql`) |
| `custom` | the `deps` entries in `tend.yml` |

External APIs are found two ways: an SDK package in your manifests (Stripe,
GitHub, OpenAI, Twilio, SendGrid, Plaid, Cloudflare and a few more are built
in), and URL literals in code that name a host. Both are matched against the
provider's OpenAPI document, so tend can tell when an endpoint you call is
deprecated, when a field you read is gone, and when a required parameter
appeared. For a host outside the catalog it looks the spec up in the APIs.guru
directory and at the usual paths on the host itself. An API with a changelog
but no spec is followed through the changelog: an entry that names a path
your code calls or a field it reads, and says it is going away, is a
deprecation of it.

While your tests run on the base commit, Node, Python and Ruby are asked to
print their deprecation warnings, and staticcheck reports deprecated Go calls
where it is installed; the deprecation lines of javac, PHP and the .NET
compiler count when your build prints them. A warning that points at your code
becomes a change to the call it names. A warning about the runtime's own
library goes to the runtime the project declares.

## The fix

For the change it picked, tend:

1. finds the call sites in your code,
2. makes the fix: a version bump through the native tool, lockfile included,
   locked at exactly the release tend chose, and nothing else moved (a Go
   bump keeps the requires `go mod tidy` would drop as unused); codemods for
   renames; and, with a model configured, a migration for the rest,
3. runs your install, build and tests on the base commit and on the patch,
4. vets the patch,
5. opens or updates the one pull request.

## The first run

The first run on a repo where tend has never opened a PR carries one
tidy-up before anything else; only a security fix goes ahead of it. It has
two kinds of part:

- each direct dependency nothing in the project uses, removed through the
  native tool so the lockfile follows (npm, pnpm and yarn; Go modules; pip,
  Poetry and uv); a deprecated one is removed rather than moved off;
- each deprecated library the code calls, moved off the way its own PR would
  move it: the bump, then codemods, then the model. A move goes in only when
  it is made in full; one that needs code edits nobody can make here waits
  for a decision PR of its own.

All the parts are built on the base commit together and proven by one run.
When that run fails, tend splits the parts in halves and keeps what passes
of each, the second half on top of what the first kept, until every part
that fails is left out with the step that failed. The model is not asked
again: each move's edits are kept from the first time it was made and
applied again next to the other parts. What passes is vetted like any patch.
A repo with nothing to tidy gets a PR that adds a commented `tend.yml`.
[docs/pr-anatomy.md](pr-anatomy.md#the-first-pull-request) shows the PR and
how tend decides that nothing uses a dependency.

## The proof

The fix is built in a git worktree of the base commit under tend's state
directory, not in your checkout. Your working tree and your index are not
touched.

The project's commands run with a filtered environment: variables whose names
look like secrets (anything containing token, secret, password, api key,
private or credential, and anything ending in `_KEY`, `_TOKEN`, `_SECRET`,
`_PAT` or `_CREDENTIALS`), `GITHUB_TOKEN` and every `TEND_*` variable are
removed, and `CI=1` is set. `verify.env` names the variables your tests
genuinely need, and those are let through whatever their names look like. tend runs git with hooks and file
system monitors switched off, without tokens in its environment and without
the caller's `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` or any other
variable that points git at another repository, and it
reaches GitHub only from your main repository, only while that repository's
git settings are what they were before the commands ran. Nothing a command
leaves behind runs later with tend's token, or sends the token somewhere
else.

The base commit is run once per commit and remembered, so the PR can show both
columns. A base that already fails runs a second time before it is believed.
A patch counts as proven when its run passes, or when it stops at the very step
where the base stops; that PR opens as a draft and says so.

The commands run on the host by default. A freshly bumped dependency is code
that executes during the proof. On Linux, tend makes its own process
unreadable to the commands it starts, so they cannot read its token from
`/proc`; on macOS and Windows, a process running as the same user can still
read tend's environment. `verify.container` is the real boundary: set it to an
image and each command runs through `docker run` with only the worktree
mounted. If docker is missing the proof fails and says why; it does not fall
back to the host.

## Vetting

Every patch is questioned before it leaves the machine, and only what was
verified is reported. Does the diff touch only files this change explains?
Files it does not explain are reverted first, so no later finding points at
them. Does every changed call exist in the new version? Does every field the
patch reads exist in the spec? Is the old symbol still used anywhere? Did the
lockfile move more than the bump needs? Was debug output or a comment added?
A Python debug print that was its block's only statement becomes `pass`. Is
anything now unreferenced? Each finding is fixed, dropped, or left flagged in the
PR, and a flag makes the PR a draft. A patch that vetting changed is proven
again.

## Model providers

A model is optional. Without one, tend does version bumps, lockfile updates,
renames and path swaps that a codemod can express, and opens a decision PR for
the rest, saying once that it is running without one.

With one, the call sites no codemod covers are the model's work. A model with
tool calling works the change the way a person would: it reads and searches
the worktree, edits the files the change explains and their tests, and runs
your install, build and test commands, by name and never as a shell line it
wrote. It is done when every command passes on the tree it leaves, not when it
says so. A failed run goes back to it, and running out of `model.turns` or
`model.tokens` turns the PR into a decision that says what was tried. Every
tool call is logged in the state directory, and the PR says how many steps it
took.

It starts from the evidence for the change (release notes, advisory text, spec
diff lines, the deprecation notice), the known ways to migrate, and each call
site with 25 lines of context. Whatever it reads after that comes from the
worktree, one file or one search at a time; files named like credentials
(`.env`, `*.pem`, `*.key`, `.npmrc`, `.pypirc`, `.netrc` and the like) are
refused. Release notes and changelogs are written by whoever publishes the
package, so code a model wrote never auto-merges, whatever `deliver.automerge`
says.

Providers: `anthropic`, `openai` (or any server that speaks its chat
completions API, through `model.base_url`), the presets `groq` and `ollama`,
and `command`, which runs a shell line of your choosing with the prompt on
stdin and the reply on stdout, so a model on your own machine works. `command`
has no tool calling: it is asked for a unified diff, which is applied, built
and tested, with the test output going back for up to `model.attempts` tries.
It runs without tokens and keys in its environment. One line is enough:

```yaml
model: groq/openai/gpt-oss-120b
```

The API key is read from the variable named by `model.key_env`, else
`TEND_MODEL_KEY` (the Action's `model-key` input), else the provider's usual
one: `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` or `GROQ_API_KEY`. A key is only
sent over https, or over http to this machine. A request is tried up to four
times on a rate limit, a server error, or a reply the provider rejects as a
malformed tool call, waiting as long as the provider asks, up to a minute.
`make demo` runs tend, live, against a small project whose payments SDK is
deprecated, with a local package registry and a Groq model doing the
migration; it takes the Groq key from `TEND_MODEL_KEY` or `GROQ_API_KEY`.

During vetting a model never edits. It is shown the hunks it wrote, the new
file around them, what upstream said about the change, the new declarations
of the calls it wrote when the typings have them, and what the other checks
and the proof already established. It is asked for problems of three fixed
kinds (an argument the new call does not accept, a response that can now be
empty, state the old call carried and the new one drops), and to answer
`none` or one `file:line: problem` line per problem. That is one call per file
with up to 8 hunks each, at most 8 calls and 4 at a time; the check says when
it stopped short. Only answers that name a file and a line it was shown and
state a problem are reported, as flagged findings, with the model's sentence
as the reason. An answer that hedges ("may", "might"), says a line is fine or
unchanged, or repeats the code on its line is dropped.

## State

tend remembers things in `$TEND_HOME`, by default `~/.cache/tend` on Linux,
`~/Library/Caches/tend` on macOS and `%LocalAppData%\tend` on Windows: a cache
of HTTP responses, pruned after a week; one `state.json` per repository (what
was seen, what you rejected, which PR is open, whether the first PR is behind
it); spec snapshots; the logs of the
last run of each change; and the worktree while a run is in progress. Nothing
is written inside your repository.

The state is a cache. Deleting it costs a few extra requests: open PRs, and
PRs you closed, are read back from GitHub. The Action keeps it between runs
with `actions/cache`.

## What leaves the machine

Requests to public registries and advisory databases (package names and
versions), `endoflife.date`, the providers' spec and changelog URLs, the
APIs.guru directory and, for a host outside the catalog, `GET` requests for
its spec at the usual paths; the GitHub API and `git push` of the branch; and,
if you configured a model, the model requests described above. Also sent: the
failing output of your commands when a run fails, to the model and in the PR
body, with the values of `verify.env` variables masked. Never sent: tokens,
your environment, or files named like credentials. Probing endpoints for
`Deprecation` headers is off by default.
