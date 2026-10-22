Thanks for adding tend: it looks after the packages this repository depends on and the APIs its code calls. Its first look found nothing to tidy up, so this pull request only adds a `tend.yml` with every key commented out. From now on it opens one pull request at a time, only for changes that touch your code, and each one shows the build and test runs that prove it. Steer it with `tend.yml`, or comment `tend skip`, `tend retry` or `tend apply N` on its pull requests.

### What changed

Adds `tend.yml` with every key commented out, so it changes nothing until you uncomment a line.

### Proof

| step | base (main@abc1234) | patched |
|---|---|---|
| `go test ./...` | ok · 4s | ok · 4s |

<details><summary>go test ./... (patched), end of its output</summary>

```
ok  example.com/app
```
</details>

### Vetting

- scope: 1 file, explained by this change

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="tidy-up" base="abc1234" rev="1" -->
