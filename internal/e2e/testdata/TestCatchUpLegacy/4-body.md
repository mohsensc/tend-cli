This pull request carries on tend's catch-up with migrations and replacements: 1 replaced library. It is the last stage; after it, tend opens one pull request at a time, only for changes that touch your code.

Part of this pull request was written by a model. The vetting section below covers it.

### Deprecated

- `request` 2.88.2 → `undici` 7.16.0, picked from 4 options: the catalog's recommended replacement
  - `src/client.js:6` `request.get('https://prices.example.test/v1/' + sku, function (err, res, body) {` → `undici.request('https://prices.example.test/v1/' + sku).then(function (res) { …`

### Proof

| step | base (main@BASE) | patched |
|---|---|---|
| `npm ci` | ok · 0s | ok · 0s |
| `node --test test/tend/characterization.js test/tend/smoke.js` | ok · 0s | ok · 0s |

<details><summary>node --test test/tend/characterization.js test/tend/smoke.js (patched), end of its output</summary>

```
(output)
```
</details>

### Vetting

- scope: 3 files, all explained by this change
- symbols: 1/1 changed calls resolve in undici@7.16.0 typings
- lockfile: only request, undici changed
- debug: nothing added
- comments: nothing added
- dead: nothing left unreferenced
- question: nothing to note
- not applicable: fields, leftovers

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="catch-up-4" base="BASE" rev="1" stage="4" -->
