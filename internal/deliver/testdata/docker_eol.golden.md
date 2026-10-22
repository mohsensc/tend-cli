## node 16-alpine → 22-alpine

**Why**: deprecation · node 16 reached end of life on 2023-09-11

### What changed upstream

- **node 16 reached end of life on 2023-09-11** · [source](https://endoflife.date/nodejs)

### Touched in this repo

- `Dockerfile:1` `FROM node:16-alpine` → `FROM node:22-alpine`

### Proof

No build or test command was found for this project, so nothing was run. Set `verify.test` in tend.yml to give the proof something to run.

### Vetting

- scope: every changed file is explained by this change

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="docker/library/node@22-alpine" base="d00dfeed" rev="1" -->
