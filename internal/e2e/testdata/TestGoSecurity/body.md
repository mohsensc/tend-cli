## example.com/fixture/crypto v1.0.0 → v1.0.1

**Why**: security · GO-2026-0001 (high) · fixed in v1.0.1 · advisory published 2026-10-19
**Level**: patch (v1.0.0 → v1.0.1)

### What changed upstream

- **GO-2026-0001 Sign leaks key material** · [source](https://osv.dev/vulnerability/GO-2026-0001)
  ```
  Sign passes the key to hash.Sum as the slice to append to, so every signature starts with the key itself, hex encoded. Anyone who sees a signature can sign as the owner of the key. Sign returns only the MAC from v1.0.1 on.
  ```

### Touched in this repo

- `main.go:9` `func sign(msg, key string) string { return crypto.Sign([]byte(msg), key) }`
- `main.go:15` `return crypto.Verify([]byte(msg), key, sig)`
- `go.mod`, `go.sum`

### Proof

| step | base (main@BASE) | patched |
|---|---|---|
| `go build ./...` | ok · 0s | ok · 0s |
| `go vet ./... && go test ./...` | ok · 0s | ok · 0s |

<details><summary>go vet ./... &amp;&amp; go test ./... (patched), end of its output</summary>

```
(output)
```
</details>

### Vetting

- scope: 2 files, all explained by this change
- symbols: 2/2 calls resolve (go doc)
- question: not run: no model configured
- not applicable: fields, leftovers, lockfile, debug, comments, dead

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="go/example.com/fixture/crypto@v1.0.1" base="BASE" rev="1" -->
