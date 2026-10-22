## actions/checkout v2 → v4

**Why**: deprecation · actions/checkout v2 runs on node12

### What changed upstream

- **actions/checkout v2 runs on node12** · [source](https://github.com/actions/checkout/releases/tag/v4.0.0)

### Touched in this repo

- `.github/workflows/ci.yml:9` `- uses: actions/checkout@v2` → `- uses: actions/checkout@v4`
- `.github/workflows/release.yml:12` `- uses: actions/checkout@v2` → `- uses: actions/checkout@v4`

### Proof

No build or test command was found for this project, so nothing was run. Set `verify.test` in tend.yml to give the proof something to run.

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="actions/actions/checkout@v4" base="d00dfeed" rev="1" -->
