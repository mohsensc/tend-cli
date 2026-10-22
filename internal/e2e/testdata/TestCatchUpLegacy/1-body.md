Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. This first pull request starts catching the repository up: a test baseline, 1 security fix and 1 unused dependency. The rest comes in 3 more pull requests, one stage at a time, listed under Next stages and each proven the same way. Once the catch-up is done it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

Part of this pull request was written by a model. The vetting section below covers it.

### Baseline

This project's tests are too thin to prove the catch-up by: there is no test command. So this pull request first adds tests that pin down how the code behaves on main@BASE, and every proof from here on runs them:

- `test/tend/characterization.js`: tests a model wrote of the current behavior
- `test/tend/smoke.js`: loads each of the 3 modules that load on the base commit, and exits
- `node --test test/tend/characterization.js test/tend/smoke.js` in `verify.test` in `tend.yml`, after the project's own test lines

They pass on main@BASE: the base column of the proof below is that commit with these tests added.

### Security

npm:

- `@fixture/parser` 1.0.0 → 1.0.1: GHSA-3m7x-8p2r-6fjw (moderate)

### Unused

Removed, since nothing in the project uses it:

- `@fixture/colors` 1.0.0 in `package.json`

### Next stages

Each comes as a pull request of its own once this one is merged, proven the same way.

2. Runtimes and build tooling
   - `nodejs` 4 → 22 in `package.json`: the oldest nodejs the project supports, past its end of life.
   - `nodejs` 4.9.1 → 24.22.0 in `.nvmrc`: nodejs 4 end of life 2018-04-30.
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

- scope: 5 files, all explained by this change
- symbols: 1/1 calls resolve in @fixture/parser@1.0.1 typings
- lockfile: only @fixture/colors, @fixture/parser changed
- debug: nothing added
- comments: nothing added
- dead: not run: nothing was removed
- question: nothing to note
- not applicable: fields, leftovers

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="tidy-up" base="BASE" rev="1" stage="1" -->
