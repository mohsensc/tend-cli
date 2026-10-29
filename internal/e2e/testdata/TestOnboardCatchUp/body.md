Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. This first pull request catches up in one go: 2 security fixes, 1 deprecated library and 3 unused dependencies. From now on it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

Part of this pull request was written by a model. The vetting section below covers it.

### Security

npm:

- `@fixture/archive` 1.0.0 → 1.1.1 (transitive): GHSA-2c8f-9v3w-4hq7 (high); 1.1.0 fixes it, and the lockfile took the newest release its ranges allow
- `@fixture/parser` 1.0.0 → 1.0.1: GHSA-3m7x-8p2r-6fjw (moderate)

### Deprecated

- `@fixture/pay` 1.4.0 → 2.0.0: 1.x is deprecated, use 2.x: charges.\* was replaced by paymentIntents.\*
  - `src/billing.js:12` `return pay.charges.create({ amount, currency: 'usd', customer })` → `return pay.paymentIntents.create({ amount, currency: 'usd', customer })`
  - `src/billing.js:20` `return pay.charges.retrieve(id)` → `return pay.paymentIntents.retrieve(id)`

### Unused

Removed, since nothing in the project uses them:

- `@fixture/colors` 1.0.0 in `package.json`
- `@fixture/strings` 1.0.0 in `package.json`
- `@fixture/old-test-helper` 1.0.0 in `package.json`

### Proof

| step | base (main@BASE) | patched |
|---|---|---|
| `npm ci` | ok · 0s | ok · 0s |
| `npm test` | ok · 0s | ok · 0s |

<details><summary>npm test (patched), end of its output</summary>

```
(output)
```
</details>

### Vetting

- scope: 3 files, all explained by this change
- symbols: 3/3 calls resolve in @fixture/parser@1.0.1 typings, in @fixture/pay@2.0.0 typings
- lockfile: only @fixture/colors, @fixture/strings, @fixture/old-test-helper, @fixture/archive, @fixture/parser, @fixture/pay changed
- debug: nothing added
- comments: nothing added
- dead: nothing left unreferenced
- question: nothing to note
- not applicable: fields, leftovers

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="tidy-up" base="BASE" rev="1" stage="1" -->
