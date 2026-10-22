## moment: choose a replacement

**Why**: deprecation · Moment is in maintenance mode

### What changed upstream

- **moment is in maintenance mode** · [source](https://momentjs.com/docs/#/-project-status/)
  ```
  Moment is a legacy project, now in maintenance mode.
  ```

### Touched in this repo

Nothing was changed in the code.

Where the code uses it:

- `src/dates.js:2` `const moment = require('moment')`
- `src/report.js:5` `moment().format('YYYY-MM-DD')`

### Options

1. **Migrate to `dayjs`** (not mechanical) — Smallest API difference, 2 KB, immutable. `tend apply 1`
2. **Migrate to `luxon`** (not mechanical) — Better time zone support, larger API change. `tend apply 2`
3. **Keep moment** (not mechanical) — No change; it keeps working. `tend apply 3`

Nothing was applied. Comment on this PR with the number you want.

<sub>tend · comment `tend apply N`, `tend skip` or `tend retry`</sub>
<!-- tend id="npm/moment@" base="abc1234" rev="1" -->
