## fixture GET /v1/orders

**Why**: breaking · response field removed (total\_cents) · published 2026-10-21

### What changed upstream

- **fixture spec 2026-10-01** · [source](https://api.fixture.dev/openapi.json)
  ```
  List orders
  GET /v1/orders: response field removed total_cents
  ```

### Touched in this repo

- `app/orders.py:9` `with urllib.request.urlopen(BASE + "/v1/orders") as res:`
- `app/orders.py:17` `cents += order["total_cents"]` → `cents += order["total"]["amount"]`

Part of this change was written by a model. The vetting section below covers it.

### Proof

| step | base (main@BASE) | patched |
|---|---|---|
| `python3 -m unittest` | ok · 0s | ok · 0s |

<details><summary>python3 -m unittest (patched), end of its output</summary>

```
(output)
```
</details>

### Vetting

- scope: 1 file, explained by this change
- fields: total.amount exists in the response schema; total_cents no longer referenced
- leftovers: no remaining references to `total_cents`
- debug: nothing added
- comments: nothing added
- dead: nothing left unreferenced
- question: nothing to note
- not applicable: symbols, lockfile

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="api/fixture/GET /v1/orders#total_cents" base="BASE" rev="1" -->
