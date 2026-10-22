# Adding an API provider

A provider is one entry in `internal/api/providers.yml`. With an entry, tend
knows which hosts and SDK packages belong to the API, where its OpenAPI
document lives and how an SDK call maps to an HTTP operation. Nothing else
needs code.

Users can add providers for their own services without touching tend, in the
`apis` list of `tend.yml` (see [config.md](config.md#apis)). This page is for
adding one to the built-in catalog.

## An entry

```yaml
- name: stripe
  hosts: [api.stripe.com]
  spec: https://raw.githubusercontent.com/stripe/openapi/master/openapi/spec3.json
  sdks: [{eco: npm, name: stripe}, {eco: pip, name: stripe}, {eco: go, name: github.com/stripe/stripe-go}]
  base: /v1
  verbs: {create: POST, retrieve: "GET/{id}", update: "POST/{id}", del: "DELETE/{id}", list: GET, cancel: "POST/{id}/cancel"}
```

| field | required | meaning |
|---|---|---|
| `name` | yes | unique; also what `ignore:` matches with `ecosystem: api` |
| `hosts` | yes | hostnames code uses to call the API. A URL literal on one of them counts as a use |
| `spec` | this or `changelog` | HTTPS URL of an OpenAPI 3 document, JSON or YAML |
| `changelog` | this or `spec` | HTTPS URL of an RSS or Atom feed or an HTML page, for an API with no spec. A sentence in it that names a path the code calls and says the path is deprecated, removed or sunset becomes a deprecation of that path |
| `sdks` | no | SDK packages, as `{eco, name}` with the canonical package name of that ecosystem. A dependency on one makes the provider "in use" |
| `base` | no | path prefix SDK calls go under, such as `/v1` |
| `verbs` | no | SDK method name to `METHOD` or `METHOD/suffix`, added to the defaults |
| `sunset` | no | dotted key on an operation that holds its removal date, such as `x-github.removalDate` |

The default verbs are `create` (POST), `retrieve` (GET `/{id}`), `list` (GET),
`update` (PATCH `/{id}`), `del` (DELETE `/{id}`), and the actions `cancel`,
`capture`, `confirm` and `send` (POST `/{id}/<action>`). Aliases map onto them:
`get`, `fetch` and `find` are `retrieve`; `all` is `list`; `modify` is `update`;
`delete`, `remove` and `destroy` are `del`. A provider's own `verbs` win.

## How a call becomes an operation

For an SDK call such as `stripe.paymentIntents.create(...)`, tend drops the
root binding, takes the last segment as the verb, turns the middle segments
from camelCase to snake_case and joins them under `base`:
`POST /v1/payment_intents`. `openai.chat.completions.create` becomes
`POST /v1/chat/completions`. Go SDK packages use the package name as the
resource.

A guess only counts if the operation exists in the spec, so a wrong mapping
produces nothing rather than noise. If an SDK names things in a way the rule
cannot reach, add the verb to `verbs`, or fix the host and path in the spec.

## Migrations

When a field or operation is removed or deprecated and the way off it is known,
add it to `internal/api/migrations.yml`:

```yaml
- provider: stripe
  method: POST
  path: /v1/charges
  options:
    - title: Move to PaymentIntents
      detail: >-
        Stripe's recommended replacement. The result can come back as
        requires_action, which the calling code has to handle.
      safe: false
      codemod:
        renames: {charges.create: paymentIntents.create}
        paths: {/v1/charges: /v1/payment_intents}
        fields: {source: payment_method}
```

`safe: true` marks an option tend may apply and prove without a model. Leave it
`false` unless the rename is the whole migration. The `detail` is shown to the
maintainer in the PR, so say what changes beyond the rename.

## Before sending it

Fetch the spec URL and confirm it returns an OpenAPI 3 document; an entry
whose spec does not resolve does not go in. An API that publishes no spec gets
a `changelog` instead, if it keeps one. Keep `providers.yml` in its order,
most used first, and record a trimmed copy of the spec under
`internal/api/testdata/recorded/` with only the paths the tests use.

The catalog test in `internal/api` already checks that the entry parses and
has a name, hosts and an HTTPS spec. Add a row to the chain table for each
verb you added (SDK chain in, `METHOD /path` out) and, if you wrote a
migration, a conformance case where the removed operation or field produces a
change carrying your options. Then run `go test ./internal/api`.
