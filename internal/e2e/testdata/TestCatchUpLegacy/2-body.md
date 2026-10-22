This pull request carries on tend's catch-up with runtimes and build tooling: 1 runtime minimum and 1 end-of-life runtime. 2 more stages follow, listed under Next stages.

### Runtimes and tooling

- `nodejs` 4 → 22 in `package.json`: nodejs 4 end of life 2018-04-30
- `nodejs` 4.9.1 → 24.22.0 in `.nvmrc`: nodejs 4 end of life 2018-04-30

### Next stages

Each comes as a pull request of its own once this one is merged, proven the same way.

3. Major upgrades
   - `@fixture/web` 1.0.0 → 3.0.0 in `package.json`: a new major release.
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
- lockfile: only nodejs changed
- not applicable: symbols, fields, leftovers, debug, comments, dead, question

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="catch-up-2" base="BASE" rev="1" stage="2" -->
