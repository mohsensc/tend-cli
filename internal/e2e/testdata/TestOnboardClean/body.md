Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. Its first look found nothing to tidy up, so this pull request only adds a `tend.yml` with every key commented out. From now on it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

### What changed

Adds `tend.yml` with every key commented out, so it changes nothing until you uncomment a line.

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
<!-- tend id="tidy-up" base="BASE" rev="1" -->
