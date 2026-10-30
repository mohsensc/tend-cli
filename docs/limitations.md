# Limitations

- GitHub only. Pull requests and pushes go to github.com repositories; GitHub
  Enterprise Server and other forges are not supported.
- Private registries are read only where a dotfile makes it free: `.npmrc`
  `registry=` and `GOPROXY`. Packages tend cannot look up are listed as notes
  by `tend check`.
- Call-site detection is exact for Go, which tend reads with the Go parser,
  and lexical for the other languages: tend reads imports and member chains
  but does not resolve types. A call through a variable whose type tend
  cannot see is missed, and a method with a common name on an unrelated
  object can be matched wrongly. Strings and comments are never matched.
- API matching needs an OpenAPI document. Removals are found by comparing
  with the previous snapshot, so the first run on a repository sees
  deprecations but not removals. A URL tend cannot map to an operation in the
  spec is reported as a note and never as a change. An API with a changelog
  but no spec can only tell tend that a path the code calls or a field it
  reads is going away, in a sentence that names it or, with a model, in an
  entry the model reads that way. A changelog that cannot be fetched is a
  note; the other providers are still checked.
- The first PR looks for unused dependencies in npm, Go and pip only. A
  dependency stays when anything outside documentation names it, so a
  package whose name is a common word is rarely removed. A Python package is
  removed only when tend knows the modules it installs: from an installed
  virtualenv in the repo, or a short built-in table. Without a virtualenv,
  tend cannot see what other packages require, so a pin on a package the
  table knows, such as `python-dateutil` for pandas, can look unused when
  nothing imports it. A use tend cannot see,
  such as a package loaded by a name built at run time with no part of the
  name written out, is caught only by the proof, and only if the tests reach
  it.
- A PR that went ahead of the first PR, as an earlier tend let a security
  fix do, marks the first PR as owed in its own marker. If that PR merges and
  tend's state is lost before the next run, the mark is gone and the first
  PR is not opened.
- A catch-up PR's proof covers its whole batch. A part that fails only next
  to another is found by splitting, which takes more runs; past its budget of
  runs, the parts not yet settled wait for the next stage even when they would
  pass.
- Without a model, a real code migration becomes a decision PR, and so does a
  choice between replacements, unless a rename a codemod makes passes the
  proof. With one, the result is as good as your tests: if they do not cover
  a call site, a passing proof proves little. The PR says what ran, and
  vetting flags what it cannot verify.
- The test baseline is only as good as what it can see. Weak tests are told
  by whether a test file names or imports the files the catch-up changes, not
  by coverage, so a test that imports a file and checks nothing counts. The
  smoke test proves a module still loads, not that it works. Tests a model
  writes pin down behavior it noticed, and pass on the base by construction;
  they miss what it did not read. Generated tests are written for npm (as
  `node --test` files, which need Node 18 or later where the proof runs), pip
  and Go only; elsewhere a project with weak tests is caught up on whatever
  its own commands prove.
- Stages are fixed by rules, not by what a project needs: a runtime that
  only a major upgrade requires still moves in stage 2, and a deprecation
  that needs a new runtime fails in stage 1 and is tried again in stage 2.
  A major is climbed only through the majors a registry lists; a library
  whose own migration guide skips majors, or needs two packages to move in
  step that no group or peer links, may still not get there.
- tend chooses among options by a fixed order, not by what fits the code
  best. The catalog of replacements is short and curated; a deprecated
  package it does not list and whose registry names none stays where it is,
  and the tracking issue says so.
- Unattended commit mode trusts the repository's CI to catch what the
  project's own commands do not. With no CI, or with pushes made by a token
  that starts no workflows, a commit that passed tend's proof stays. A revert
  onto commits someone pushed since is proven only by the project's commands.
  A reverted catch-up commit is not split further: its changes come back one
  at a time.
- A project whose base commit no longer installs on a current runtime, with
  an old native addon say, or a registry that is gone, has nothing to prove
  a change against; the catch-up gets only as far as its proof does. A
  provider's rate or daily token limit stops a model midway as well, and the
  part it was on waits for the next stage, with the error as its reason.
- A runtime pin moves the file that names it, not the runtime CI or the
  container uses; the proof runs on whatever runtime the runner has, unless
  `verify.container` names an image.
- The proof runs your tests on your runner. Tests that need services, secrets
  or hardware need those provided, or a `verify.test` that avoids them.
- A transitive dependency is moved alone only where the native tool can do
  that; elsewhere tend says to declare it directly. npm, pnpm, yarn and
  Poetry move one only to the newest release its ranges allow. tend takes
  that release when it is the one it asked for or a later one of the same
  major, proves it and names it in the PR; a release on another major fails
  the bump with a note instead of landing a version nobody picked.
- A Maven dependency whose version comes from `dependencyManagement`, in its
  own pom or a parent, is not swapped for another artifact: renaming it
  would leave the new one without a version. One whose version comes from a
  parent outside the repository is not bumped at all. Either way the PR is a
  decision that says where the version lives.
- Docker end-of-life data comes from endoflife.date and covers the images in
  a built-in table.
- A Docker image pinned by digest moves only when the registry answers for
  the new tag's digest without credentials; otherwise the PR is a decision
  that says so, rather than a bump that drops the pin.
- A Terraform bump moves only the bumped provider in `.terraform.lock.hcl`;
  the others keep their locked versions. A Helm bump runs
  `helm dependency update`, which may move the chart's other dependencies
  within their ranges.
- A runtime minimum moves only once its release line is past end of life, and
  only as far as the oldest line still maintained. That is a breaking change
  for anyone still on an older runtime; the PR says so, and an `ignore` entry
  keeps it where it is.
- Git submodules are pinned by commit and are not moved; `tend check` lists
  them as untracked.
- `untracked` lists manifests tend cannot read, submodules, and hosts the
  code calls that look like an API and that tend has no spec for. A host
  looks like an API when its name starts with `api.` or has `.api.` in it, or
  when a path called on it starts with `/api/` or a version segment such as
  `/v2/`. Left out are hosts a catalog or `apis` entry covers, the package
  registries tend reads anyway (registry.npmjs.org, pypi.org,
  proxy.golang.org, repo1.maven.org, repo.packagist.org, crates.io,
  rubygems.org, api.nuget.org, hub.docker.com, registry.terraform.io and a
  few more), hosts named for links or examples (github.com, w3.org,
  example.com, localhost, `.example` and `.test`), and URLs only in test
  files. Any other host the code names is not reported.
- Terraform and Helm changes are proven with `terraform validate` and
  `helm lint` only where those tools are installed.
- Projects locked with `bun.lock`, `bun.lockb` or `pdm.lock` are read and
  listed, but tend does not bump or remove their dependencies, since it
  cannot regenerate those lockfiles.
