## fixture GET /v1/orders

**Why**: breaking · response field removed (total_cents)

### What changed upstream

- **fixture spec 2.0.0** · [source](https://api.fixture.dev/openapi.json)
  ```
  GET /v1/orders
  response property total_cents removed
  response property total added: {amount, currency}
  ```

### Touched in this repo

- `app/orders.py:17` `return order["total_cents"]` → `return order["total"]["amount"]`

### Proof

| step | base (main@c0ffee1) | patched |
|---|---|---|
| `python3 -m unittest` | ok · 1s | ok · 1s |

<details><summary>python3 -m unittest (patched), end of its output</summary>

```
Ran 3 tests

OK
```
</details>

### Vetting

- fields: every field the patch reads is in the spec
- leftovers: no references to the old names remain

### Options

1. **Read `total.amount`** — total.amount carries the same value in minor units. Applied in this PR.

<sub>tend · comment `tend apply N`, `tend skip` or `tend retry`</sub>
<!-- tend id="api/fixture/GET /v1/orders#total_cents" base="c0ffee1" rev="1" -->
