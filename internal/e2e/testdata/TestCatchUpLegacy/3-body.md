This pull request carries on tend's catch-up with major upgrades: 1 major upgrade. 1 more stage follows, listed under Next stages.

Part of this pull request was written by a model. The vetting section below covers it.

### Upgrades

- `@fixture/web` 1.0.0 → 3.0.0, by way of 2.0.0, each proven: a new major release
  - `src/app.js:8` `app.configure(function () {` → `app.set('name', 'legacy-shop')`
  - `src/app.js:23` `return web.createServer(app)` → `return app.createServer()`

### Next stages

Each comes as a pull request of its own once this one is merged, proven the same way.

4. Migrations and replacements
   - `request` 2.88.2 in `package.json`: deprecated, with no single replacement. tend takes the first of `undici`, `node-fetch` and `axios` its proof passes, in that order.

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
- lockfile: only @fixture/web changed
- debug: nothing added
- comments: nothing added
- dead: nothing left unreferenced
- question: nothing to note
- not applicable: symbols, fields, leftovers

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="catch-up-3" base="BASE" rev="1" stage="3" -->
