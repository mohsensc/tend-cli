## request → undici 6.19.8

**Why**: deprecation · request has been deprecated, see https://github.com/request/request/issues/3142

### What changed upstream

- **request is deprecated (Feb 2020)** · [source](https://github.com/request/request/issues/3142)

### Touched in this repo

- `lib/http.js:3` `const request = require('request')` → `const { request } = require('undici')`
- `package.json`, `package-lock.json`

### Proof

| step | base (main@abc1234) | patched |
|---|---|---|
| `npm test` | ok · 4s | ok · 5s |

<details><summary>npm test (patched), end of its output</summary>

```
# pass 4
```
</details>

### Vetting

- symbols: every changed call resolves in the new version
- leftovers: no references to the old names remain

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="npm/request->undici@6.19.8" base="abc1234" rev="1" -->
