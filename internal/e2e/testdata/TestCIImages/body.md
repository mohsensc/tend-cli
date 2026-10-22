## node 16-alpine → 24-alpine

**Why**: deprecation · nodejs 16 end of life 2023-09-11

### What changed upstream

- **nodejs 16 end of life 2023-09-11** · [source](https://endoflife.date/nodejs)
  ```
  nodejs 16 reached end of life on 2023-09-11.
  ```

### Touched in this repo

- `Dockerfile:1` `FROM node:16-alpine AS build` → `FROM node:24-alpine AS build`
- `Dockerfile:7` `FROM node:16-alpine` → `FROM node:24-alpine`

### Proof

No build or test command was found for this project, so nothing was run. Set `verify.test` in tend.yml to give the proof something to run.

### Vetting

- scope: 1 file, explained by this change
- question: not run: no model configured
- not applicable: symbols, fields, leftovers, lockfile, debug, comments, dead

<sub>tend · comment `tend skip` or `tend retry`</sub>
<!-- tend id="docker/library/node@24-alpine" base="BASE" rev="1" -->
