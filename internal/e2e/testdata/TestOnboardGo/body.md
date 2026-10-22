Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. This first pull request tidies up: it removes 1 dependency nothing in the project uses. From now on it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

### What changed

Removed, since nothing in the project uses it:

- `example.com/fixture/logs` v1.0.0 in `go.mod`

Files: `go.mod`, `go.sum`.

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
- question: not run: no model configured
- not applicable: symbols, fields, leftovers, lockfile, debug, comments, dead

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="tidy-up" base="BASE" rev="1" -->
