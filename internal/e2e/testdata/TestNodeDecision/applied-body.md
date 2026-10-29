## @fixture/pay 1.4.0 → 2.0.0

**Why**: deprecation · 1.x is deprecated, use 2.x: charges.\* was replaced by paymentIntents.\*
**Level**: major (1 → 2)

### What changed upstream

- **@&#8203;fixture/pay 1.4.0 is deprecated** · [source](https://www.npmjs.com/package/@fixture/pay)
  ```
  1.x is deprecated, use 2.x: charges.* was replaced by paymentIntents.*
  ```

### Touched in this repo

- `src/billing.js:12` `return pay.charges.create({ amount, currency: 'usd', customer })`
- `src/billing.js:20` `return pay.charges.retrieve(id)`
- `tend.yml`

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

- scope: 1 file, explained by this change
- symbols: 2/2 calls resolve in @fixture/pay@1.4.0 typings
- leftovers: not run: the patch stays on the version in use
- question: not run: no model configured
- not applicable: fields, lockfile, debug, comments, dead

### Options

1. **Apply the rename hinted by the deprecation notice (charges.* -> paymentIntents.*)** (not mechanical) — Move to @fixture/pay 2.0.0 and rename the calls the notice names. The notice is prose, not a migration guide, so the proof decides whether the rename is all it takes. `tend apply 1`
2. **Pin to 1.4.0 and ignore** — Stay on 1.4.0 and have tend stop proposing 2.0.0. Applied in this PR.

   This goes into tend.yml:

   ```yaml
   ignore:
     - name: "@fixture/pay"
       versions: [">=2.0.0"]
       reason: staying on 1.4.0
   ```

<sub>tend · comment `tend apply N`, `tend skip` or `tend retry`</sub>
<!-- tend id="npm/@fixture/pay@2.0.0" base="BASE" rev="2" -->
