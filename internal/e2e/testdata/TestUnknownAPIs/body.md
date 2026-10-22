## api.example-unknown.com GET /v2/items

**Why**: deprecation · operation deprecated · sunset 2027-06-30

### What changed upstream

- **api.example-unknown.com spec 2.0.0** · [source](https://api.apis.guru/v2/specs/example-unknown.com/2.0.0/openapi.json)
  ```
  List items
  Use GET /v3/items, which pages with a cursor; /v2/items stops answering after its sunset.
  ```

### Touched in this repo

Nothing was changed in the code. No model is configured to move the call site, and there is nothing to bump.

Where the code uses it:

- `src/inventory.js:4` `GET https://api.example-unknown.com/v2/items`

### Proof

No build or test command was found for this project, so nothing was run. Set `verify.test` in tend.yml to give the proof something to run.

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="api/api.example-unknown.com/GET /v2/items" base="BASE" rev="1" -->
