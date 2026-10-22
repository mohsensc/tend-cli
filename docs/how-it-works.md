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
   renames; and, with a model configured, a migration for the rest. For a
   security fix the release tend chose is the least one that fixes it; a
   dependency only the lockfile names may land on a later release of the
   same major, which the PR names,
3. runs your install, build and tests on the base commit and on the patch,
   and when the patch fails, tries what it may do by itself: a major at a
   time, then each option its rule allows
   ([choosing on its own](#choosing-on-its-own)),
4. vets the patch,
5. opens or updates the one pull request, or in commit mode pushes the
   commit.

## The catch-up

A repo tend has never opened a PR on is caught up first, in a few PRs, one
stage each, before tend opens PRs one change at a time. Every change in the
queue belongs to one stage, by the first of these rules that matches:

| stage | what goes there |
|---|---|
| 4. migrations and replacements | a change that moves more than 10 call sites; a deprecated package with no single replacement |
| 1. fixes | a security fix; a change to an API the code calls |
| 2. runtimes and build tooling | runtime pins and the oldest runtime the project supports, base images, Actions, and dev dependencies nothing outside the tests calls (compilers, bundlers, test runners) |
| 3. major upgrades | a release that is only new, a major above all |
| 1. fixes | any other move off something deprecated: a deprecated version or package with one target |

The stages run in that order. Fixes come first because they fix what is
wrong now, and the first PR carries the test baseline the rest is proven
against. Runtimes and build tools come before majors, since new majors need
new runtimes; the majors are then proven on the runtime they will ship on.
Migrations come last, written against current libraries.

Each catch-up PR carries its stage and whatever is left of the stages
before it: a change that failed in an earlier stage, or a new advisory, is
tried again there, on the newer base. Stages with nothing in them are
skipped, so a repo is current after at most four PRs. The first one also
removes the direct dependencies nothing uses (npm, pnpm and yarn; Go
modules; pip, Poetry and uv), through the native tool so the lockfile
follows; a deprecated or vulnerable one is removed rather than fixed. It
lists the stages to come under Next stages; once it merges, the next stage
opens, and so on. A repo with nothing to catch up on gets a PR that adds a
commented `tend.yml`.

Each change that needs more than its bump is made once on its own, so a
model is asked only once; then every change and removal is built on the
base commit together and proven by one run. When that run fails, tend
splits the parts in halves and keeps what passes of each, the second half on
top of what the first kept, until every part that fails is left out with
the step that failed. A change whose own making fails is left out at once,
without a run. A catch-up PR runs the project's commands at least 8 times
and twice per part when it has more; what is still unsettled then waits for
the next stage, and the log names every part left out. A part left out of
the last stage is listed under Not done and in the tracking issue, and comes
back as a PR of its own after the catch-up. What passes is vetted like any
patch.

Closing a catch-up PR unmerged ends the catch-up: tend goes on one change at
a time, what that PR carried included. An open tend PR from before the
first PR, as an earlier tend left on a repo where a security fix went ahead
of its first PR, is closed with a line saying the first PR folds it in. A
change that joins the queue while a catch-up PR is open, a new advisory say,
rebuilds that PR rather than going ahead of it.
[docs/pr-anatomy.md](pr-anatomy.md#the-first-pull-request) shows the PRs and
how tend decides that nothing uses a dependency.

### Choosing on its own

Where there is more than one way off something, tend takes one itself and
proves it: only options that move are tried, a replacement only when it has
a release that is neither yanked nor deprecated, one the project already
depends on first, then the catalog's order, recommended first and then by
how widely each is used. The first of at most three that passes its proof
is taken, and the PR says which rule picked it; `tend apply N` on the PR
takes another. Staying put is the outcome only when none passes.

### Climbing majors

A major two or more behind that fails its proof as one jump is climbed a
major at a time: tend bumps to the newest release of the next major, has
the model migrate the code with the release notes for that range only,
proves it, and goes on from there. Each step is proven. The climb stops at
the first step that fails, and the PR ships the highest step that passed,
saying which failed; the rest comes in the next stage or a later PR.
Runtimes and base images are not climbed: the lines in between are past
their end of life too.

### A baseline for weak tests

The first catch-up PR looks at whether the project's tests can prove the
catch-up at all. They are weak when there is no test command, or when a
file the catch-up will change, a call site of any queued change outside the
tests, is reached by no test: no JavaScript or TypeScript test names a
relative path that resolves to it, no Python test imports its module, no Go
test sits in its package, and elsewhere no test file carries its name.

When they are weak, and the ecosystem can run generated tests (npm, pip
and Go), the PR adds a baseline before anything else:

1. A smoke test. tend lists the project's modules (code outside tests,
   vendored and generated code and tool config, at most 50), loads each in a
   child process on the base commit, and writes a test from a fixed template
   that loads again exactly those that loaded; a module that starts a server
   is loaded and the process exits. Go's build already loads every package.
2. A typecheck, `tsc --noEmit`, for a TypeScript project without a build
   script, when it passes on the base.
3. With a model, tests of how the files the catch-up changes behave now,
   written to one file (`test/tend/characterization.js`,
   `tests/tend/test_characterization.py`, `tend_characterization_test.go`).
   They must pass on the base commit; a failure goes back to the model while
   its budget lasts, and tests that never pass are left out.
4. The line that runs them, added to `verify.test` in `tend.yml` after the
   project's own test lines, so every later proof runs them too.

The baseline is part of every set the batch proves and is never split out,
and the base column of the proof is the base commit with the baseline
added. The PR's Baseline section says why the tests were weak and what was
added.

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
renames and path swaps that a codemod can express, takes such an option
itself when it passes the proof, and opens a decision PR for the rest, saying
once that it is running without one.

With one, the call sites no codemod covers are the model's work. A model with
tool calling works the change the way a person would: it reads and searches
the worktree, edits the files the change explains and their tests, and runs
your install, build and test commands, by name and never as a shell line it
wrote. It is done when every command passes on the tree it leaves, not when it
says so. A failed run goes back to it, and running out of `model.turns` or
`model.tokens` turns the PR into a decision that says what was tried. Every
tool call is logged in the state directory, and the PR says how many steps it
took.

The budget grows with the work: a change gets `model.turns` and
`model.tokens` once, and once more for every 10 call sites it moves, up to
five times; each step of a climb gets its own; writing a baseline's tests
gets one more share for every five files it covers, up to five. A catch-up
of a large, old project asks a lot of a model, and a stronger one than the
demo's `groq/openai/gpt-oss-120b` gets further on it.

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

## Commit mode

With `deliver: {mode: commit}` tend pushes to the branch instead of opening
PRs, and keeps a repo current with no one watching:

- Only what passed its proof is pushed. A change with no build or test
  command to prove it, or one that ends as a decision, is not, and the
  tracking issue says why. The catch-up runs the same way, a commit per
  stage.
- The next run reads the repository's own CI on the commit tend pushed,
  its check runs and commit statuses. While CI runs, tend waits and does
  nothing else, for up to six hours; no answer by then counts as no CI.
- When CI fails on tend's commit and did not fail on the commit before it,
  tend reverts its commit on the branch's tip, with a message that names the
  commit, the checks that failed and the change, and pushes the revert. A
  revert onto commits someone pushed since is proven first; one that puts
  back the tree tend started from is not. The change is not tried again at
  that release; a newer one starts over. A reverted catch-up commit ends the
  catch-up, and its changes come back one at a time, so CI can tell which one
  it was.
- Pushes made with the workflow's default token start no other workflows,
  so CI never runs on them and tend sees no CI. Pass a fine-grained or App
  token for CI to check tend's commits.

## The tracking issue

tend keeps one issue on the repo, found by a marker in its body, that lists
what it could not do on its own, each with why: changes it could not make or
prove on the current base, commits it reverted after CI failed, deprecated
packages it knows no replacement for, and parts the last catch-up stage left
out. It opens the issue when there is something to list, edits it when the
list changes, closes it when the list empties and opens it again when it
fills. A run with nothing new does not touch it. Both modes keep it; the
workflow `tend init --ci` writes asks for `issues: write`.

## State

tend remembers things in `$TEND_HOME`, by default `~/.cache/tend` on Linux,
`~/Library/Caches/tend` on macOS and `%LocalAppData%\tend` on Windows: a cache
of HTTP responses, pruned after a week; one `state.json` per repository (what
was seen, what you rejected, which PR is open, how far the catch-up got, the
tracking issue); spec snapshots; the logs of the
last run of each change; and the worktree while a run is in progress. Nothing
is written inside your repository.

The state is a cache. Deleting it costs a few extra requests: open PRs, and
PRs you closed, are read back from GitHub. The Action keeps it between runs
with `actions/cache`.

## What leaves the machine

Requests to public registries and advisory databases (package names and
versions), `endoflife.date`, the providers' spec and changelog URLs, the
APIs.guru directory and, for a host outside the catalog, `GET` requests for
its spec at the usual paths; the GitHub API, including the CI results of the
commits tend pushed and its tracking issue, and `git push` of the branch; and,
if you configured a model, the model requests described above. Also sent: the
failing output of your commands when a run fails, to the model and in the PR
body, with the values of `verify.env` variables masked. Never sent: tokens,
your environment, or files named like credentials. Probing endpoints for
`Deprecation` headers is off by default.
