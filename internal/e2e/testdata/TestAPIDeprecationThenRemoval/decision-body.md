## fixture GET /v1/orders/{id}/legacy

**Why**: deprecation · operation deprecated · sunset 2027-03-01

### What changed upstream

- **fixture spec 2025-01-01** · [source](https://api.fixture.dev/openapi.json)
  ```
  Fetch an order in the legacy shape
  Use GET /v1/orders/{id} instead; it returns the total as an amount and a currency.
  ```

### Touched in this repo

Nothing was changed in the code. No model is configured to move the call site, and there is nothing to bump.

Where the code uses it:

- `app/orders.py:25` `with urllib.request.urlopen(BASE + "/v1/orders/%s/legacy" % order_id) as res:`

### Proof

| step | base (main@BASE) | patched |
|---|---|---|
| `python3 -m unittest` | ok · 0s | not run |

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="api/fixture/GET /v1/orders/{id}/legacy" base="BASE" rev="1" -->
