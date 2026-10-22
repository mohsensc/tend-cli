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
