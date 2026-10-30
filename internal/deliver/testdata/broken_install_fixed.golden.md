## requests 2.19.0 → 2.32.3

**Why**: update · minor release
**Level**: minor (2.19.0 → 2.32.3)

### Touched in this repo

- `requirements.txt`

### Proof

| step | base (main@feaaca8) | patched |
|---|---|---|
| `python3 -m venv .venv && .venv/bin/pip install -r requirements.txt` | failed (exit 1) · 4s | ok · 6s |
| `.venv/bin/python -m unittest` | skipped (after failure) | ok · 1s |

`python3 -m venv .venv && .venv/bin/pip install -r requirements.txt` fails on `main@feaaca8` and passes with this change, so this PR is a draft until a person confirms it.

<details><summary>python3 -m venv .venv &amp;&amp; .venv/bin/pip install -r requirements.txt (base), end of its output</summary>

```
ERROR: Cannot install urllib3==1.24 and requests==2.19.0 because these package versions have conflicting dependencies.
```
</details>

### Vetting

- scope: every changed file is explained by this change

Verdict: draft.

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="pip/requests@2.32.3" base="feaaca8" rev="1" -->
