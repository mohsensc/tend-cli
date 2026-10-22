## @fixture/pay 1.4.0 → 2.0.0

**Why**: deprecation · 1.x is deprecated, use 2.x: charges.* was replaced by paymentIntents.*
**Level**: major (1 → 2)

### What changed upstream

- **@fixture/pay 1.4.0 is deprecated** · [source](https://www.npmjs.com/package/@fixture/pay)
  ```
  1.x is deprecated, use 2.x: charges.* was replaced by paymentIntents.*
  ```

### Touched in this repo

- `src/billing.js:12` `return pay.charges.create({ amount, currency: 'usd', customer })` → `return pay.paymentIntents.create({ amount, currency: 'usd', customer })`
- `src/billing.js:20` `return pay.charges.retrieve(id)` → `return pay.paymentIntents.retrieve(id)`
- `package-lock.json`, `package.json`

Part of this change was written by a model. The vetting section below covers it.

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
- leftovers: no remaining references to `charges.`
- lockfile: only @fixture/pay changed
- debug: nothing added
- comments: nothing added
- dead: nothing left unreferenced
- question: nothing to note
- not applicable: fields

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="npm/@fixture/pay@2.0.0" base="BASE" rev="1" -->
