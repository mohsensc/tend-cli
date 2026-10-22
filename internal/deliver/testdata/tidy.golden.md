Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. This first pull request starts catching the repository up: a test baseline, 3 security fixes, 1 API change, 1 deprecated library and 2 unused dependencies. The rest comes in 3 more pull requests, one stage at a time, listed under Next stages and each proven the same way. Once the catch-up is done it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

Part of this pull request was written by a model, in 4 steps: 1 read, 2 edits, 1 run; the commands passed the one time they ran. The vetting section below covers it.

### Baseline

This project's tests are too thin to prove the catch-up by: there is no test command. So this pull request first adds tests that pin down how the code behaves on main@abc1234, and every proof from here on runs them:

- `test/tend/characterization.js`: tests a model wrote of the current behavior
- `test/tend/smoke.js`: loads each of the 4 modules that load on the base commit, and exits
- `node --test test/tend/characterization.js test/tend/smoke.js` in `verify.test` in `tend.yml`, after the project's own test lines

They pass on main@abc1234: the base column of the proof below is that commit with these tests added.

### Security

npm:

- `lodash` 4.17.20 → 4.17.21: GHSA-35jh-r3h4-6jhm (high)
- `tar` 7.5.20 → 7.5.22 (transitive): GHSA-8qq5-rm4j-mr97 (moderate); 7.5.21 fixes it, and the lockfile took the newest release its ranges allow

go:

- `golang.org/x/net` v0.36.0 → v0.38.0: GO-2025-3595 (moderate)

### Stripe API

- `GET /v1/charges`: response field deprecated (source)

### Deprecated

- `stripe` 14.2.0 → 16.0.0: 14.x is deprecated: charges.* was replaced by paymentIntents.*
  - `src/billing.js:42` `const c = await stripe.charges.create({ amount: order.total })` → `const c = await stripe.paymentIntents.create({ amount: order.total })`
  - `src/billing.js:88` `return stripe.charges.retrieve(id)` → `return stripe.paymentIntents.retrieve(id)`

### Unused

Removed, since nothing in the project uses them:

- `@acme/colors` 1.2.0 in `package.json`
- `left-pad` 1.3.0 in `package.json`

Possibly unused, kept: these look unused, but tend cannot prove it, so they are yours to decide.

- `eslint-plugin-acme` 2.0.0 in `package.json`: a plugin eslint loads by a name its config gives
- `chalk` 5.3.0 in `package.json`: with it removed, `npm test` fails (exit 1)

### Next stages

Each comes as a pull request of its own once this one is merged, proven the same way.

2. Runtimes and build tooling
   - `nodejs` 18.20.4 → 22.20.0 in `.nvmrc`: Node.js 18 reached end of life on 2025-04-30.
   - `axios` 0.27.2 → 1.12.0: with it in the batch, `npm test` fails (exit 1).
3. Major upgrades
   - `vitest` 1.6.0 → 3.2.4: a new major release.
4. Migrations and replacements
   - `moment` 2.30.1: deprecated, with no single replacement. tend takes the first of `dayjs` and `luxon` its proof passes, in that order.

### Not done

These could not be made or proven here. Each comes back as a pull request of its own after the catch-up, and tend's tracking issue keeps the list.

- `uuid` 3.4.0 → 9.0.1: making it changes no files.

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

- scope: 6 files, all explained by this change
- symbols: 2/2 changed calls resolve
- lockfile: only the bumped packages and their dependencies changed
- debug: nothing added
- comments: nothing added

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="tidy-up" base="abc1234def" rev="1" stage="1" -->
