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
- A security fix that goes ahead of the first PR marks it as still owed in
  its own marker. A state lost after that fix merged, before the next run,
  no longer finds the mark, and the first PR is not opened.
- Without a model, a real code migration becomes a decision PR. With one, the
  result is as good as your tests: if they do not cover a call site, a passing
  proof proves little. The PR says what ran, and vetting flags what it cannot
  verify.
- The proof runs your tests on your runner. Tests that need services, secrets
  or hardware need those provided, or a `verify.test` that avoids them.
- A transitive dependency is moved alone only where the native tool can do
  that; elsewhere tend says to declare it directly. npm, pnpm, yarn and
  Poetry move one only to the newest release its ranges allow; when that is
  not the release tend chose, the bump fails with a note instead of landing
  a version nobody picked.
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
