This pull request carries on tend's catch-up with migrations and replacements: 1 replaced library and 1 major upgrade. It is the last stage; after it, tend opens one pull request at a time, only for changes that touch your code.

Part of this pull request was written by a model. The vetting section below covers it.

### Deprecated

- `request` 2.88.2 → `axios` 1.12.2, picked from 2 options: the project already depends on axios

### Upgrades

- `express` 3.21.2 → 5.1.0, by way of 4.21.2, each proven: a new major release

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

- scope: 4 files, all explained by this change

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="catch-up-4" base="abc1234def" rev="1" stage="4" -->
