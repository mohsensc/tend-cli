Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. This first pull request catches up in one go: 1 deprecated library and 3 unused dependencies. From now on it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

Part of this pull request was written by a model. The vetting section below covers it.

### Deprecated

- `@fixture/pay` 1.4.0 → 2.0.0: 1.x is deprecated, use 2.x: charges.* was replaced by paymentIntents.*
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
- symbols: 2/2 changed calls resolve in @fixture/pay@2.0.0 typings
- lockfile: only @fixture/colors, @fixture/strings, @fixture/old-test-helper, @fixture/pay changed
- debug: nothing added
- comments: nothing added
- dead: nothing left unreferenced
- question: nothing to note
- not applicable: fields, leftovers

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="tidy-up" base="BASE" rev="1" stage="1" -->
