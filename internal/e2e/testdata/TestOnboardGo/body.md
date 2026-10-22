Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. This first pull request catches up in one go: 1 security fix and 1 unused dependency. From now on it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

### Security

go:

- `example.com/fixture/crypto` v1.0.0 → v1.0.1: GO-2026-0001 (high)

### Unused

Removed, since nothing in the project uses it:

- `example.com/fixture/logs` v1.0.0 in `go.mod`

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
<!-- tend id="tidy-up" base="BASE" rev="2" stage="1" -->
