## fixturelib 1.0.0: deprecated calls

**Why**: deprecation · fixturelib warns that something the code calls is deprecated

### What changed upstream

- **fixturelib warns that something the code calls is deprecated**
  ```
  fetch_all() is deprecated and goes away in fixturelib 2.0; use iter_rows()
  ```

### Touched in this repo

- `app/report.py:7` `rows = fixturelib.fetch_all()` → `rows = fixturelib.iter_rows()`

Part of this change was written by a model, in 4 steps: 1 read, 1 edit, 1 run; the commands passed both times they ran. The vetting section below covers it.

### Proof

| step | base (main@BASE) | patched |
|---|---|---|
| `python3 -m venv .venv && .venv/bin/pip install --quiet --no-index --find-links wheels -r requirements.txt` | ok · 0s | ok · 0s |
| `.venv/bin/python -m unittest` | ok · 0s | ok · 0s |

<details><summary>.venv/bin/python -m unittest (patched), end of its output</summary>

```
(output)
```
</details>

### Vetting

- scope: 1 file, explained by this change
- symbols: 1/1 changed calls resolve in fixturelib as installed
- leftovers: the patched run prints none of the warnings
- debug: nothing added
- comments: nothing added
- dead: nothing left unreferenced
- question: nothing to note
- not applicable: fields, lockfile

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="pip/fixturelib@1.0.0" base="BASE" rev="1" -->
