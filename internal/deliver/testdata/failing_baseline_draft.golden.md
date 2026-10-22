## left-pad 1.1.0 → 1.3.0

**Why**: update · minor release
**Level**: minor (1.1.0 → 1.3.0)

### Touched in this repo

- `src/pad.js:1` `const pad = require('left-pad')`
- `package.json`, `package-lock.json`

### Proof

| step | base (main@abc1234) | patched |
|---|---|---|
| `npm ci` | ok · 9s | ok · 9s |
| `npm test` | failed (exit 1) · 2s | failed (exit 1) · 1m15s |
| `npm run build` | not run | skipped (after failure) |

Tests already fail on `main@abc1234`, so this PR is a draft.

<details><summary>npm test (patched), end of its output</summary>

```
not ok 1 - flaky clock
# fail 1
```
</details>

### Vetting

- scope: every changed file is explained by this change
- lockfile: 1 flagged
  - unrelated lockfile churn: ms, debug (flagged)

Verdict: draft.

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="npm/left-pad@1.3.0" base="abc1234" rev="1" -->
