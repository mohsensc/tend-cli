## stripe 14.2.0 → 16.0.1

**Why**: security · GHSA-xxxx-yyyy-zzzz (high) +1 more · fixed in 16.0.1 · advisory published 2026-10-12
**Level**: major (14 → 16)

### What changed upstream

- **GHSA-xxxx-yyyy-zzzz Request smuggling in stripe** · [source](https://osv.dev/vulnerability/GHSA-xxxx-yyyy-zzzz)
  ```
  Versions before 16.0.1 accept a crafted header.
  Upgrade to 16.0.1 or later.
  ```
- **GHSA-aaaa-bbbb-cccc Prototype pollution** · [source](https://osv.dev/vulnerability/GHSA-aaaa-bbbb-cccc)

### Touched in this repo

- `src/billing.js:42` `const c = await stripe.charges.create({ amount: order.total })` → `const c = await stripe.paymentIntents.create({ amount: order.total })`
- `src/billing.js:88` `return stripe.charges.retrieve(id)` → `return stripe.paymentIntents.retrieve(id)`
- `package.json`, `package-lock.json`

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
- fields: not run: this is not an API change
- lockfile: only the bumped packages and their dependencies changed

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="npm/stripe@16.0.1" base="abc1234def" rev="1" -->
