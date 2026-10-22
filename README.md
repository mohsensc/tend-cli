# tend

A plugin that keeps your project's dependencies and APIs current through
GitHub Actions, one tested PR at a time.

tend watches the packages your project depends on and the external APIs it
calls. When something changes in a way that matters to your code, it fixes
the code, runs your own build and tests on the fix, and opens one pull request
with the evidence. When an update touches nothing, it stays quiet.

## Add it to your project

1. Add [.github/workflows/tend.yml](docs/github-actions.md#the-workflow) to
   your repository, or run `tend init --ci` to write it.
2. Under Settings > Actions > General, turn on "Allow GitHub Actions to create
   and approve pull requests".
3. Optional: to let tend migrate code no codemod covers, add a model to
   `tend.yml` and its key as the `TEND_MODEL_KEY` secret.

```yaml
# tend.yml
model: groq/openai/gpt-oss-120b
```

Merge the workflow and tend runs once right away. It catches the project
up in at most four pull requests, one after another, each proven by your
own build and tests:

1. Fixes: security fixes, changes to the APIs your code calls, moves off
   deprecated libraries, and the removal of dependencies nothing uses. When
   your tests are too thin to prove the rest, this one first adds tests that
   pin down how the code behaves today.
2. Runtimes and build tooling: runtime pins past their end of life, base
   images, Actions, compilers and bundlers.
3. Major upgrades, a major at a time when the jump fails.
4. Migrations and replacements: wide migrations, and deprecated libraries
   with no single replacement, where tend picks one itself and proves it.

After that, tend runs on a schedule and opens one PR at a time, only for
changes that touch your code. What it could not do on its own it lists in
one tracking issue.

## What a pull request looks like

Each PR says what changed upstream, which lines of your code it touched, what
ran on the base commit and on the fix, and what tend checked in its own patch:

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

[docs/pr-anatomy.md](docs/pr-anatomy.md) walks through a whole PR, including
the first one.

## What it watches

- Packages: npm, pnpm and yarn, Go modules, pip, poetry and uv, Maven,
  Gradle, Cargo, RubyGems, Composer and NuGet.
- Docker base images, GitHub Actions, Terraform providers and modules, and
  Helm charts.
- Runtime pins (`.nvmrc`, `.python-version`, `.tool-versions`, `go.mod`) as
  release lines reach end of life.
- External APIs, called through an SDK or by raw HTTP, matched against the
  provider's OpenAPI document or changelog.
- Deprecation warnings your runtime prints while your tests run.
- Anything else, through a `deps` entry in `tend.yml`.

## How it decides

- Security fixes, deprecations, and breaking changes to something your code
  uses get a PR. Plain version bumps do not, unless you turn them on.
- There is never more than one tend PR open, and it holds the whole change:
  manifest, lockfile, every call site, and the tests that had to move.
- The catch-up comes first and carries what it can of the rest. After it,
  security comes first, then API changes that break your code, then
  deprecations, then breaking releases.
- When there is more than one way off something, tend takes the first its
  rule allows that passes the proof, and says why. Comment `tend apply 2` to
  have another, `tend skip` to say no, or `tend retry` to try again. Only
  when none passes does the PR change nothing and list the options.
- Releases younger than three days wait, except security fixes, deprecations
  and API changes.

## Configuration

No config is needed. A typical `tend.yml` is a few lines:

```yaml
schedule: 7d
cooldown: 7d
ignore:
  - {name: "@types/*"}
deliver: {automerge: security}
```

[docs/config.md](docs/config.md) describes every key. `deliver: {mode: commit}`
commits to the branch instead of opening PRs, and keeps a repo current with
no one watching: it pushes only what passed its proof, waits for your CI on
each commit, and reverts its own commit when CI fails on it.

## Running it locally

```sh
go install github.com/mohsensc/tend-cli/cmd/tend@latest
cd your-project
tend check            # what would change, most important first
tend run --dry-run    # work the top change and print its PR
tend run              # open the PR; needs GITHUB_TOKEN or `gh auth login`
```

Without Go, download a binary from
[Releases](https://github.com/mohsensc/tend-cli/releases).

## Docs

- [GitHub Actions](docs/github-actions.md): the workflow, tokens and inputs
- [How it works](docs/how-it-works.md): the proof, vetting, models, and what leaves your machine
- [Configuration](docs/config.md)
- [Self-update](docs/self-update.md): releases and signatures
- [Limitations](docs/limitations.md)
- [Contributing](docs/CONTRIBUTING.md)

## License

MIT. See [LICENSE](LICENSE).
