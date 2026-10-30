# Running on GitHub Actions

## The workflow

`tend init --ci` writes this file to `.github/workflows/tend.yml`. The cron
follows `schedule` in `tend.yml`, daily by default. The workflow also runs
once when the file lands on your default branch, so adding tend starts the
first run at once.

```yaml
name: tend
on:
  push:
    branches: ["main"]
    paths: [.github/workflows/tend.yml]
  schedule: [{cron: "17 6 * * *"}]
  workflow_dispatch:
permissions: {contents: write, pull-requests: write, issues: write, checks: read, statuses: read}
concurrency: {group: tend, cancel-in-progress: false}
jobs:
  tend:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with: {persist-credentials: false, fetch-depth: 0}
      - uses: mohsensc/tend-cli@v1
        with: {model-key: "${{ secrets.TEND_MODEL_KEY }}"}
```

The `model-key` line is only needed if `tend.yml` names a model. `issues:
write` lets tend keep its one tracking issue; `checks: read` and
`statuses: read` let commit mode read your CI on the commits it pushed, and
`fetch-depth: 0` gives it the history to find them; a shallow checkout is
deepened when needed, which costs a fetch. On a private
repository every run uses Actions minutes. To run weekly, set `schedule: 7d`
before `tend init --ci`, or change the cron to `"17 6 * * 1"`. tend keeps its
state in the Actions cache, and GitHub evicts a cache nobody used for 7 days,
so a weekly run can start from scratch and forget an owed first PR; the daily
default, or a second run mid-week, avoids that. Worktrees are never cached.

## Tokens

Turn on "Allow GitHub Actions to create and approve pull requests" under
Settings > Actions > General, or GitHub refuses the PR.

The default `GITHUB_TOKEN` has one more limit: PRs it opens do not start
your other workflows, so your CI and auto-merge never run on them. Pass a
fine-grained token, or a GitHub App token from
`actions/create-github-app-token`, as `token:` to lift it. Such a token needs
the Workflows (read and write) permission too, or GitHub refuses any Action
bump that edits `.github/workflows`, and tend reports the refusal rather than
retrying. In commit mode this matters more: a push with the default token
starts no CI, so tend has no CI result to revert by.

## Inputs

| input | default | what it sets |
|---|---|---|
| `token` | `github.token` | the token tend pushes and opens PRs with |
| `command` | `run` | `run` or `check` |
| `args` | none | extra CLI arguments |
| `config` | `tend.yml`, then `.github/tend.yml` | path to the config |
| `version` | the release the action ref names | the tend version to install |
| `model-key` | none | the key for the model `tend.yml` names |
| `fail-on-unproven` | `false` | fail the job when tend cannot prove a change; by default it warns, lists the change in its tracking issue and tries it again when the base moves |

`@v1` installs the newest `v1.x.y` release, or the one before it if the
newest has no signed checksums; `@v1.2` stays inside `v1.2.x`. Pin `@v1.2.3`,
or the commit that release is tagged at (`@<sha> # v1.2.3`), to stay put: a
commit pin installs the release tagged at that commit and fails if there is
none. A branch ref installs the latest release. The action checks the release
signature before it puts the binary on the path.

Before tend runs, the action removes the token `actions/checkout` left in the
repository's config and the credentials file it wrote under `RUNNER_TEMP`, and
it starts tend with `exec`, so no shell holding the token stays behind as
tend's parent. A process running as the runner's user can still reach tokens
in other ways, through `sudo` on hosted runners for one; set
`verify.container` to keep the project's commands away from them.
