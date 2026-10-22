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
        with: {persist-credentials: false}
      - uses: mohsensc/tend-cli@v1
        with: {model-key: "${{ secrets.TEND_MODEL_KEY }}"}
```

The `model-key` line is only needed if `tend.yml` names a model. `issues:
write` lets tend keep its one tracking issue; `checks: read` and
`statuses: read` let commit mode read your CI on the commits it pushed. On a private
repository every run uses Actions minutes. To run weekly, set `schedule: 7d`
before `tend init --ci`, or change the cron to `"17 6 * * 1"`.

## Tokens

Turn on "Allow GitHub Actions to create and approve pull requests" under
Settings > Actions > General, or GitHub refuses the PR.

The default `GITHUB_TOKEN` has two more limits. PRs it opens do not start
your other workflows, so your CI and auto-merge never run on them. It also
cannot push changes to `.github/workflows`, so Action bumps fail. Pass a
fine-grained token with the workflows permission, or a GitHub App token from
`actions/create-github-app-token`, as `token:` to lift both. In commit mode
this matters more: a push with the default token starts no CI, so tend has no
CI result to revert by.

## Inputs

| input | default | what it sets |
|---|---|---|
| `token` | `github.token` | the token tend pushes and opens PRs with |
| `command` | `run` | `run` or `check` |
| `args` | none | extra CLI arguments |
| `config` | `tend.yml` | path to the config |
| `version` | newest release of the action's major | the tend version to install |
| `model-key` | none | the key for the model `tend.yml` names |

`@v1` moves to the newest `v1.x.y`, or to the one before it while the
newest release's files are still being uploaded, so a scheduled run never
fails on a release in progress; pin `@v1.2.3` to stay put. The action
checks the release signature before it puts the binary on the path.
