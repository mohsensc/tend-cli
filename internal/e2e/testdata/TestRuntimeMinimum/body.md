## go 1.24 → 1.26

**Why**: deprecation · go 1.24 end of life 2026-02-10

### What changed upstream

- **go 1.24 end of life 2026-02-10** · [source](https://endoflife.date/go)
  ```
  go 1.24 reached end of life on 2026-02-10. The oldest go release still maintained is 1.26.
  ```

### Touched in this repo

- `go.mod:3` `go 1.24` → `go 1.26`

`go.mod` names the oldest go this project supports, not one it runs on. Raising it to 1.26 drops releases past their end of life: anyone still on one has to upgrade to use what comes next.

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

- scope: 1 file, explained by this change
- question: not run: no model configured
- not applicable: symbols, fields, leftovers, lockfile, debug, comments, dead

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="toolchain/go@1.26" base="BASE" rev="1" -->
