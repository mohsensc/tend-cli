## @fixture/pay 1.4.0 → 2.0.0

**Why**: deprecation · 1.x is deprecated, use 2.x: charges.\* was replaced by paymentIntents.\*
**Level**: major (1 → 2)

### What changed upstream

- **@&#8203;fixture/pay 1.4.0 is deprecated** · [source](https://www.npmjs.com/package/@fixture/pay)
  ```
  1.x is deprecated, use 2.x: charges.* was replaced by paymentIntents.*
  ```

### Touched in this repo

Nothing was changed in the code. The bump alone breaks this project's tests; pick a way forward below.

Where the code uses it:

- `src/billing.js:12` `return pay.charges.create({ amount, currency: 'usd', customer })`
- `src/billing.js:20` `return pay.charges.retrieve(id)`

### Proof

| step | base (main@BASE) | patched |
|---|---|---|
| `npm ci` | ok · 0s | ok · 0s |
| `npm test` | ok · 0s | failed (exit 1) · 0s |

The patched column is what tend tried before deciding to ask; its failing output is below.

<details><summary>npm test (patched), end of its output</summary>

```
(output)
```
</details>

### Options

1. **Apply the rename hinted by the deprecation notice (charges.* -> paymentIntents.*)** (not mechanical) — Move to @fixture/pay 2.0.0 and rename the calls the notice names. The notice is prose, not a migration guide, so the proof decides whether the rename is all it takes. `tend apply 1`
2. **Pin to 1.4.0 and ignore** — Stay on 1.4.0 and have tend stop proposing 2.0.0. `tend apply 2`

   This goes into tend.yml:

   ```yaml
   ignore:
     - name: "@fixture/pay"
       versions: [">=2.0.0"]
       reason: staying on 1.4.0
   ```

Nothing was applied. Comment on this PR with the number you want.

<sub>tend · comment `tend apply N`, `tend skip` or `tend retry`</sub>
<!-- tend id="npm/@fixture/pay@2.0.0" base="BASE" rev="1" -->
