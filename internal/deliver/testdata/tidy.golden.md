Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. This first pull request tidies up: it removes 2 dependencies nothing in the project uses and moves the code off 1 deprecated library. What it found and left alone is listed below, for you to decide. From now on it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

### What changed

Removed, since nothing in the project uses them:

- `@acme/colors` 1.2.0 in `package.json`
- `left-pad` 1.3.0 in `package.json`

Moved off a deprecated library:

- `stripe` 14.2.0 → 16.0.0: 14.x is deprecated: charges.* was replaced by paymentIntents.*
  - `src/billing.js:42` `const c = await stripe.charges.create({ amount: order.total })` → `const c = await stripe.paymentIntents.create({ amount: order.total })`
  - `src/billing.js:88` `return stripe.charges.retrieve(id)` → `return stripe.paymentIntents.retrieve(id)`

Possibly unused, kept: these look unused, but tend cannot prove it, so they are yours to decide.

- `eslint-plugin-acme` 2.0.0 in `package.json`: a plugin eslint loads by a name its config gives
- `chalk` 5.3.0 in `package.json`: with it removed, `npm test` fails (exit 1)

Deprecated, not moved: each gets a pull request of its own after this one.

- `moment` 2.30.1: no model is configured to move the 3 call sites. `tend apply N` on its pull request picks one of:
  1. Migrate to `dayjs`
  2. Pin to 2.30.1 and ignore

Files: `package-lock.json`, `package.json`, `src/billing.js`.

Part of this change was written by a model, in 4 steps: 1 read, 2 edits, 1 run; the commands passed the one time they ran. The vetting section below covers it.

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

- scope: 3 files, all explained by this change
- symbols: 2/2 changed calls resolve
- lockfile: only the bumped packages and their dependencies changed
- debug: nothing added
- comments: nothing added

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="tidy-up" base="abc1234def" rev="1" -->
