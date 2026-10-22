## actions/checkout v2 → v7

**Why**: deprecation · runs on node12, removed from GitHub runners
**Level**: major (2 → 7); release notes below mention breaking changes in 2 of 8 releases

### What changed upstream

- **actions/checkout v2 is deprecated** · [source](https://github.com/actions/checkout)
  ```
  runs on node12, removed from GitHub runners
  ```
- **GitHub Actions retired the set-output and save-state commands** · [source](https://github.blog/changelog/2022-10-11-github-actions-deprecating-save-state-and-set-output-commands/)
- **v7.0.0** · [full notes](https://github.com/actions/checkout/releases/tag/v7.0.0)
  ```
  ## What's Changed
  * block checking out fork pr for pull_request_target and workflow_run by @aiqiaoy in https://github.com/actions/checkout/pull/2454
  * Bump actions/publish-immutable-action from 0.0.3 to 0.0.4 in the minor-actions-dependencies group across 1 directory by @dependabot[bot] in https://github.com/actions/checkout/pull/2458
  * Bump flatted from 3.3.1 to 3.4.2 by @dependabot[bot] in https://github.com/actions/checkout/pull/2460
  * Bump js-yaml from 4.1.0 to 4.2.0 by @dependabot[bot] in https://github.com/actions/checkout/pull/2461
  * Bump @actions/core and @actions/tool-cache and Remove uuid by @dependabot[bot] in https://github.com/actions/checkout/pull/2459
  …
  ```
- **v6.1.0** · [full notes](https://github.com/actions/checkout/releases/tag/v6.1.0)
  ```
  ## What's Changed
  * **[BREAKING]** backport `allow-unsafe-pr-checkout` to v6 by @aiqiaoy in https://github.com/actions/checkout/pull/2500
  * backport fixes to releases-v6 by @aiqiaoy in https://github.com/actions/checkout/pull/2527
  https://github.blog/changelog/2026-06-18-safer-pull_request_target-defaults-for-github-actions-checkout/ for more details about this breaking change
  **Full Changelog**: https://github.com/actions/checkout/compare/v6.0.3...v6.1.0
  ```
- **v6.0.3** · [full notes](https://github.com/actions/checkout/releases/tag/v6.0.3)
  ```
  ## What's Changed
  * Update changelog by @ericsciple in https://github.com/actions/checkout/pull/2357
  * fix: expand merge commit SHA regex and add SHA-256 test cases by @yaananth in https://github.com/actions/checkout/pull/2414
  * Fix checkout init for SHA-256 repositories by @yaananth in https://github.com/actions/checkout/pull/2439
  * Update changelog for v6.0.3 by @yaananth in https://github.com/actions/checkout/pull/2446
  ## New Contributors
  …
  ```
- **v5.1.0** · [full notes](https://github.com/actions/checkout/releases/tag/v5.1.0)
  ```
  ## What's Changed
  * **[BREAKING]** backport `allow-unsafe-pr-checkout` to v5 by @aiqiaoy in https://github.com/actions/checkout/pull/2501
  * backport fixes to releases-v5 by @aiqiaoy in https://github.com/actions/checkout/pull/2523
  https://github.blog/changelog/2026-06-18-safer-pull_request_target-defaults-for-github-actions-checkout/ for more details about this breaking change
  **Full Changelog**: https://github.com/actions/checkout/compare/v5.0.1...v5.1.0
  ```
- 4 more releases, not quoted

### Touched in this repo

- `.github/workflows/ci.yml:12` `- uses: actions/checkout@v2` → `- uses: actions/checkout@v7`
- `.github/workflows/ci.yml:14` `run: echo "::set-output name=sha::$(git rev-parse HEAD)"` → `run: echo "sha=$(git rev-parse HEAD)" >> "$GITHUB_OUTPUT"`

### Proof

No build or test command was found for this project, so nothing was run. Set `verify.test` in tend.yml to give the proof something to run.

### Vetting

- scope: 1 file, explained by this change
- question: not run: no model configured
- not applicable: symbols, fields, leftovers, lockfile, debug, comments, dead

### Options

1. **Write step outputs and state to environment files** — The same values go to $GITHUB_OUTPUT and $GITHUB_STATE, which every current runner supports. Applied in this PR.

<sub>tend · comment `tend apply N`, `tend skip` or `tend retry`</sub>
<!-- tend id="actions/actions/checkout@v7" base="BASE" rev="1" -->
