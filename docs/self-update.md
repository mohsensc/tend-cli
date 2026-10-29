# Releases and self-update

Each release publishes archives for Linux, macOS and Windows on amd64 and
arm64, a `checksums.txt`, and `checksums.txt.sig`, an ed25519 signature over
the exact bytes of the checksums file. The public key is compiled into the
binary and also kept in [action/pubkey.pem](../action/pubkey.pem). The
signing key lives in a protected release environment, not in the repository's
secrets.

`tend update` fetches the latest release, verifies the signature with the
built-in key, checks the archive against `checksums.txt`, and replaces its own
binary. Any mismatch aborts and changes nothing. `tend update --check` only
says whether a newer release exists. `run` and `watch` look once a day and
print one line on stderr when there is one; they never update by themselves.
A binary built by `go install` reports the module version it was built from.
Prereleases (`v1.3.0-rc.1`) update too, to the next full release. If tend
lives in a directory you cannot write, `tend update` says so before it
downloads anything; rerun it with `sudo` or reinstall tend where you own it.

The Action does the same check with `openssl` before it puts the binary on
the path. `@v1` installs the newest `v1.x.y` release, or the one before it if
the newest has no signed checksums; `@v1.2` stays inside `v1.2.x`. Pin
`@v1.2.3`, or the commit that release is tagged at, to stay put: a commit pin
installs the release tagged at that commit and fails if there is none.

The release workflow builds with the newest Go toolchain, so the binaries
carry its security fixes, and refuses to publish when `govulncheck` reports a
reachable vulnerability or when the signature does not verify against
`action/pubkey.pem`.

## Installing by hand

On Linux or macOS. The `openssl` that ships with macOS is LibreSSL, which
cannot read ed25519 keys; install OpenSSL 3 with `brew install openssl@3` and
set `openssl=$(brew --prefix openssl@3)/bin/openssl`. On Linux,
`openssl=openssl`.

```sh
os=$(uname -s | tr A-Z a-z)
arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
tag=$(curl -fsSL https://api.github.com/repos/mohsensc/tend-cli/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
file=tend_${tag#v}_${os}_${arch}.tar.gz
base=https://github.com/mohsensc/tend-cli/releases/download/$tag
curl -fsSLO $base/$file -O $base/checksums.txt -O $base/checksums.txt.sig
base64 -d < checksums.txt.sig > sig.bin
"$openssl" pkeyutl -verify -pubin -inkey pubkey.pem -rawin -in checksums.txt -sigfile sig.bin
grep " $file\$" checksums.txt | sha256sum -c -     # macOS: shasum -a 256 -c -
mkdir -p ~/.local/bin && tar -xzf $file tend && install -m 0755 tend ~/.local/bin/tend
```

`pubkey.pem` is [action/pubkey.pem](../action/pubkey.pem) from this
repository. `~/.local/bin` has to be on your `PATH`; use
`sudo install -m 0755 tend /usr/local/bin/tend` to install for every user. On
Windows, download the `.zip` from the
[releases page](https://github.com/mohsensc/tend-cli/releases) and check it
the same way.

## Running on its own repository

tend maintains itself: [.github/workflows/tend.yml](../.github/workflows/tend.yml)
runs the action from this checkout twice a week on this repository, with
`updates: {minor: true}`. Its PRs start CI only when the `TEND_TOKEN` secret
holds a GitHub App or fine-grained token; with the default token, "Allow
GitHub Actions to create and approve pull requests" has to be on.
