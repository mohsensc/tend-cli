# Anatomy of a pull request

Every tend PR has the same sections in the same order. A section that has
nothing to say is left out. Below is an example body, as tend renders it for a major
bump whose migration a model helped write. The golden file behind it is
`internal/deliver/testdata/breaking_bump_model.golden.md`.

````markdown
## stripe 14.2.0 → 16.0.0

**Why**: breaking · major release · released 2026-09-01
**Level**: major (14 → 16); release notes below mention breaking changes in 1 of 2 releases

### What changed upstream

- **16.0.0** · [full notes](https://github.com/stripe/stripe-node/releases/tag/v16.0.0)
  ```
  ## Breaking changes
  * `charges.*` was removed, use `paymentIntents.*`
  * Node 16 is no longer supported
  ## Changes
  * New `paymentIntents.confirm` helper
  * Fix retry on 409
  …
  ```
- **15.0.0** · [full notes](https://github.com/stripe/stripe-node/releases/tag/v15.0.0)
  ```
  Pin the API version to 2024-06-20.
  ```

### Touched in this repo

- `src/billing.js:42` `const c = await stripe.charges.create({ amount: order.total })` → `const c = await stripe.paymentIntents.create({ amount: order.total })`
- `src/billing.js:88` `return stripe.charges.retrieve(id)` → `return stripe.paymentIntents.retrieve(id)`
- `package.json`, `package-lock.json`

Part of this change was written by a model. The vetting section below covers it.

### Proof

| step | base (main@abc1234) | patched |
|---|---|---|
| `npm ci` | ok · 12s | ok · 13s |
| `npm test` | ok · 30s | ok · 31s |

<details><summary>npm test (patched), end of its output</summary>

```
TAP version 13
ok 1 - payment intents
1..1
# pass 1
```
</details>

### Vetting

- scope: every changed file is explained by this change
- symbols: every changed call resolves in the new version
- question: 1 flagged
  - `src/billing.js:42` the old call passed `capture: false`; the new one needs `capture_method: 'manual'` (flagged)

### Notes

- Auto-merge was not enabled: Pull request Auto merge is not allowed for this repository

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="npm/stripe@16.0.0" base="abc1234def" rev="2" -->
````

## Reading it

**Title.** `Bump stripe to 16.0.0`. The shapes are fixed: `Bump X to V`, `Bump X to V (fixes GHSA-...)`,
`Replace X with Y`, `Migrate off deprecated X 14.x`, `Migrate off deprecated GET /path`,
`Handle removed field F on GET /path`, `Bump node base image to 24-alpine`,
`Bump actions/checkout to v7`, `Move nodejs to 24.22.0`, `Fix deprecation warnings from X`,
`Pin X to V` and `Decide: migrate off X 14.x`, and for the first PR on a repo
`Tidy up: ...` or `Set up tend: nothing to tidy up`. `deliver.title_prefix` goes in front.

**Heading and Why.** The package and the move, then the reason in one line:
the kind of change, the advisory and its severity, the fixed version, the
sunset date, when upstream published it. Nothing here is inferred. If tend does
not know a value, the value is not shown.

**Level.** For a bump: major, minor or patch, with the versions. When the
release notes below say "breaking" in some of the releases, it says how many.

**What changed upstream.** Release notes, advisory text, spec diff lines or an
end-of-life notice, as upstream wrote them. At most six lines of each, in a code
block so a stray `@name` or `#123` in someone else's notes pings no one, then a
link to the rest. When an advisory, a deprecation or an end of life is the
reason for the PR, at most four releases are quoted, those whose notes say
"breaking" first, and the rest are counted.

**Touched in this repo.** Every call site tend found, with the line before and
after when a rename was applied, then the other files in the patch (manifest,
lockfile, config). When the requirement in the manifest already allowed the new
version, a line says so, since only the lockfile moved. A model-written change
is called out here, with what the model did: how many steps, which files it
edited, and how the project's commands went each time it ran them.

**Proof.** The project's own install, build and test lines, run on the base
commit and on the patched tree. A step that did not run says `skipped` or
`not run`. The table is followed by the end of the output of the step worth
reading: the one that failed, else the last that ran. If the project has no
build or test command, the section says so instead of showing an empty table.
Two lines under the table matter:

- "Tests already fail on `main@abc1234`, so this PR is a draft." The baseline was red before tend touched anything. The PR still opens, as a draft, with both columns visible.
- "The baseline could not be run." There is nothing to compare the patched column with.

**Vetting.** One line per check, saying what it verified: `scope: 3 files,
all explained by this change`, `symbols: 2/2 changed calls resolve in
@fixture/pay@2.0.0 typings`, `leftovers: no remaining references to charges.`,
`lockfile: only @fixture/pay changed`. Scope runs first, so no later finding
points at a file it reverted. A check whose question does not arise says
`not applicable`; one that could not run says why. Findings are listed under
their check and carry `fixed`, `dropped` or `flagged`. `fixed` and `dropped`
were applied to the patch before it was pushed and the proof was run again.
`flagged` is left for you. A flagged finding from the symbol, field, leftover or
lockfile checks makes the PR a draft.

**Notes.** Anything else worth knowing: a lockfile that could not be regenerated
because the native tool is not installed, or auto-merge that GitHub refused.

**Footer and marker.** The last line is a comment GitHub does not show:
`<!-- tend id="..." base="..." rev="N" -->`. It holds the change id, the base
commit the patch was built on and a revision that grows by one each time tend
updates the PR. A PR that went ahead of tend's first PR also carries
`tidy="due"`, so a lost state still owes the tidy-up. tend reads it back on the
next run to know which change this PR carries. Do not edit it.

## When the PR is a decision

A deprecation with no single replacement, or a change that needs code edits no
codemod covers when there is no model to make them, opens a PR that changes
nothing. Its body lists where the code uses the thing, the output of the plain
bump tend tried when one was worth trying, and the options. An option tend
cannot prove without help is marked `not mechanical`.

````markdown
## moment: choose a replacement

**Why**: deprecation · Moment is in maintenance mode

### What changed upstream

- **moment is in maintenance mode** · [source](https://momentjs.com/docs/#/-project-status/)
  ```
  Moment is a legacy project, now in maintenance mode.
  ```

### Touched in this repo

Nothing was changed in the code.

Where the code uses it:

- `src/dates.js:2` `const moment = require('moment')`
- `src/report.js:5` `moment().format('YYYY-MM-DD')`

### Options

1. **Migrate to `dayjs`** (not mechanical) — Smallest API difference, 2 KB, immutable. `tend apply 1`
2. **Migrate to `luxon`** (not mechanical) — Better time zone support, larger API change. `tend apply 2`
3. **Keep moment** (not mechanical) — No change; it keeps working. `tend apply 3`

Nothing was applied. Comment on this PR with the number you want.

<sub>tend · comment `tend apply N`, `tend skip` or `tend retry`</sub>
<!-- tend id="npm/moment@" base="abc1234" rev="1" -->
````

Comment on the PR to choose. The newest matching comment from someone with the
`OWNER`, `MEMBER` or `COLLABORATOR` role wins. Comments from bots (logins
ending in `[bot]`) are never commands, whatever their role.

| comment | effect |
|---|---|
| `tend apply N` | the next run applies option N, proves it and updates the PR |
| `tend skip` | tend closes the PR and does not propose the same target again |
| `tend retry` | tend throws away its worktree and redoes the change, once |

When one option is mechanical (a rename, a path swap), tend applies it, proves
it, and lists the others below. If the proof fails, the PR becomes a decision
PR. A deprecation notice that names a rename (`charges.* was replaced by
paymentIntents.*`) gives an option to apply it; staying on the version in use
is an option too, and applying it writes the `ignore` entry into `tend.yml` in
the same PR, so the decision is reviewed and recorded like any other change.

## The first pull request

The first run on a repo opens a tidy-up before any other change; only a
security fix goes ahead of it. Its body starts with a short welcome instead
of a heading: what tend does, what this PR does, what tend does from now on,
and how to steer it. The usual sections follow. The golden file behind this
example is `internal/deliver/testdata/tidy.golden.md`.

````markdown
Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. This first pull request tidies up: it removes 2 dependencies nothing in the project uses and moves the code off 1 deprecated library. What it found and left alone is listed below, for you to decide. From now on it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

### What changed

Removed, since nothing in the project uses them:

- `@acme/colors` 1.2.0 in `package.json`
- `left-pad` 1.3.0 in `package.json`

Moved off a deprecated library:

- `stripe` 14.2.0 → 16.0.0: 14.x is deprecated: charges.* was replaced by paymentIntents.*
  - `src/billing.js:42` `const c = await stripe.charges.create({ amount: order.total })` → `const c = await stripe.paymentIntents.create({ amount: order.total })`
  - `src/billing.js:88` `return stripe.charges.retrieve(id)` → `return stripe.paymentIntents.retrieve(id)`

Possibly unused, kept: these look unused, but tend cannot prove it, so they are yours to decide.

- `eslint-plugin-acme` 2.0.0 in `package.json`: a plugin eslint loads by a name its config gives
- `chalk` 5.3.0 in `package.json`: with it removed, `npm test` fails (exit 1)

Deprecated, not moved: each gets a pull request of its own after this one.

- `moment` 2.30.1: no model is configured to move the 3 call sites. `tend apply N` on its pull request picks one of:
  1. Migrate to `dayjs`
  2. Pin to 2.30.1 and ignore

Files: `package-lock.json`, `package.json`, `src/billing.js`.

Part of this change was written by a model, in 4 steps: 1 read, 2 edits, 1 run; the commands passed the one time they ran. The vetting section below covers it.

### Proof

| step | base (main@abc1234) | patched |
|---|---|---|
| `npm ci` | ok · 12s | ok · 13s |
| `npm test` | ok · 30s | ok · 31s |

<details><summary>npm test (patched), end of its output</summary>

```
TAP version 13
ok 1 - payment intents
1..1
# pass 1
```
</details>

### Vetting

- scope: 3 files, all explained by this change
- symbols: 2/2 changed calls resolve
- lockfile: only the bumped packages and their dependencies changed
- debug: nothing added
- comments: nothing added

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="tidy-up" base="abc1234def" rev="1" -->
````

**Title.** `Tidy up: remove 4 unused dependencies, move off 1 deprecated
library`, naming only what the PR does. A repo with nothing to tidy gets
`Set up tend: nothing to tidy up`, and a PR that adds a `tend.yml` with every
key commented out, so it changes nothing. A repo that has a `tend.yml`
already, committed or only written to the checkout tend runs in, as
`tend init --now` does, gets no such PR.

**What changed.** Four lists, each left out when it is empty:

- Removed: the dependencies nothing in the project uses, with the manifest
  that declared each. Each goes out through the native tool, so the lockfile
  follows.
- Moved off: each deprecated library the code calls, moved as its own PR
  would move it (the bump, then codemods, then the model), with every call
  site before and after.
- Possibly unused, kept: what looks unused but tend cannot prove is, and
  why; also a removal whose proof failed, with the step that failed.
- Deprecated, not moved: a library whose move could not be made or proven
  here, with its options. Each gets a decision PR of its own after this one.

**Proof and vetting.** Every removal and move is proven by one run on the
patched tree. When it fails, tend leaves parts out until the rest passes
([how it works](how-it-works.md#the-first-run)), and what it left out is
listed with why. Vetting reads the patch like any other.

### How tend decides that nothing uses a dependency

Only dependencies an npm, Go or pip manifest declares directly are looked
at; other ecosystems, Maven and Gradle plugins among them, are left alone. A
dependency is removed only when all of these hold:

- No code imports it: `import`, `require`, a dynamic `import()` or an export
  from it, under its own name or an `npm:` alias, a Go import (a blank `_`
  import in `tools.go` counts), or a Python import of a module it installs.
- Nothing else names it. tend reads every file in the repo that is not
  documentation or a lockfile for the package's name, and for the names of
  the commands it installs: a `package.json` script, a Makefile, a CI
  workflow, a shell script, `tox.ini`, `noxfile.py`,
  `.pre-commit-config.yaml`, a `tool` line or `go:generate` line, a config
  file, an entry point in `pyproject.toml`, or a string in the code such as
  `require.resolve('x')` or `jest.mock('x')`. The line that declares a
  dependency does not count. Of a Markdown page, only its JavaScript import
  lines outside code blocks count, which Docusaurus and VitePress compile.
  Yarn's `.yarn` directory and its Plug'n'Play maps (`.pnp.cjs`) do not
  count.
- No config file is named for it: `.prettierrc` keeps prettier,
  `jest.config.js` jest, `tox.ini` tox, `.golangci.yml` golangci-lint.
- It is not in `peerDependencies` or `optionalDependencies`, and no
  installed package needs it as a peer.
- It is not an `@types` package for a package in use, and the project is
  not written in TypeScript, which reads every `@types` package and needs
  `typescript` and `tslib`. An `@types` package also stays when the project
  declares `typescript`, since `tsc --checkJs` reads it without a
  `tsconfig.json`. Packages compilers write imports of (`core-js`,
  `regenerator-runtime`, `@swc/helpers`) stay too, and so does one a
  framework loads when it is installed (`sharp` with Next.js).
- For pip: it is not a build tool, a server such as gunicorn, or a database
  driver or library a framework loads by name, and tend knows the modules it
  installs, from an installed virtualenv in the repo or a built-in table.
  Notebooks count by the code in their cells, however large their outputs.
- Nothing else holds it. For Go, no other module in `go mod graph` requires
  it, since its line may hold it above the version they ask for. For pip, it
  is not in a constraints file (one named for constraints, or read with
  `-c`), and no other dependency requires it, directly or through others, as
  the virtualenv's metadata says; a frozen requirements file pins those.
- No `ignore` entry in `tend.yml` names it, whatever versions the entry
  covers.

When a dependency looks unused but one of these cannot be settled, it is
listed as possibly unused and kept: a plugin a tool loads by a short name in
its config (`eslint-plugin-*`, `eslint-import-resolver-*`,
`eslint-formatter-*`, `@babel/*`, `*-loader`, `prettier-plugin-*`, and
`@prettier/plugin-*`, which Prettier 2 loads by itself, pytest and flake8
plugins, and any pip package whose `entry_points.txt` joins
a plugin group), a Python package whose modules tend cannot name, one a
library next to it imports when the code asks for what it backs
(`python-multipart` with FastAPI, `email-validator` with pydantic, `openpyxl`
and `pyarrow` with pandas, `greenlet` with SQLAlchemy), one a
`pyproject.toml` extra offers to the package's users, a lockfile that does
not record peers (yarn 1), or a command whose name the lockfile does not give
(pnpm). So is an npm package a loader takes by its prefix (`gulp-*` with
`gulp-load-plugins`, `grunt-*` with `load-grunt-tasks`), one whose name the
code builds from a prefix of it (``import(`@fontsource/${font}`)``), one in
the scope of a tool the project declares (`@vitest/coverage-v8` with vitest,
which loads it for `--coverage`), and an `expo-*`, `react-native-*`,
`@react-native*` or `@expo/*` package in a React Native or Expo app, which
links native modules the code never imports. An npm removal needs the tool
that writes its lockfile; without it the removal is left out and kept, since
a `package.json` its lockfile no longer matches fails `npm ci` and every
frozen install. A pip package's commands come from its `entry_points.txt`, so
`awscli` stays while a Makefile runs `aws`.

A Go module comes out with `go mod edit -droprequire` and `go mod tidy`. When
tidy brings it back as a direct requirement, code tend did not read imports
it (a generated file over 2 MB, say), and the removal fails rather than move
the module to its latest version; it is listed as kept, with why. When tidy
brings it back as indirect at a lower version, the version the line held is
put back.

### After the first pull request

It is the one open PR until it is merged or closed. A re-run refreshes it
when the base moves, and `tend skip` and `tend retry` work as on any PR.
Closing it unmerged is final: tend does not open it again, and goes on with
its usual work, the deprecated libraries it held back included. A security
fix that arrives while it is open closes it and takes its place, and it
comes back once the fix merges; a comment left on it after tend closed it
does not change that. The fix's marker says the first PR is still owed, so
a state lost while the fix is open still brings it back. `onboarding: quiet` in `tend.yml` skips the first PR,
and `off` also skips the look for unused dependencies. Set either while the
first PR is open, or let the base reach a point with nothing left to tidy,
and the next run closes it with a comment saying why. `tend check` shows
whether the first PR is due but, since it reads the checkout with any local
edits, does not decide it.

## Re-runs

There is never more than one open tend PR. A re-run that finds the same change
leaves the PR alone when the files are the same, and otherwise force-pushes the
branch (with a lease, so a commit someone else added is never overwritten) and
edits the body. Whose commits are on the branch is read from GitHub, not from
tend's state: a PR on a `tend/` branch with tend's marker in its body, whose
branch is one commit on the base the marker names, is tend's to update. A
commit pushed on top of it, or a merge of the base branch, is someone else's;
tend says so once on the PR and leaves it until it is merged or closed. A
security fix that appears while another PR is open closes that
PR with a comment and takes its place; the closed change returns to the queue
after the fix merges.
