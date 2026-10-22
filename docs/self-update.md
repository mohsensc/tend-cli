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

The Action does the same check with `openssl` before it puts the binary on
the path. `@v1` is a moving tag that points at the newest `v1.x.y`; pin
`@v1.2.3` to stay put.

## Installing by hand

On Linux or macOS:

```sh
os=$(uname -s | tr A-Z a-z)
arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
tag=$(curl -fsSL https://api.github.com/repos/mohsensc/tend-cli/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
file=tend_${tag#v}_${os}_${arch}.tar.gz
base=https://github.com/mohsensc/tend-cli/releases/download/$tag
curl -fsSLO $base/$file -O $base/checksums.txt -O $base/checksums.txt.sig
base64 -d < checksums.txt.sig > sig.bin
openssl pkeyutl -verify -pubin -inkey pubkey.pem -rawin -in checksums.txt -sigfile sig.bin
grep " $file\$" checksums.txt | sha256sum -c -     # macOS: shasum -a 256 -c -
tar -xzf $file tend && install -m 0755 tend ~/.local/bin/tend
```

`pubkey.pem` is [action/pubkey.pem](../action/pubkey.pem) from this
repository. `~/.local/bin` has to be on your `PATH`; use
`sudo install -m 0755 tend /usr/local/bin/tend` to install for every user. On
Windows, download the `.zip` from the
[releases page](https://github.com/mohsensc/tend-cli/releases) and check it
the same way.

## Running on its own repository

tend maintains itself: [.github/workflows/tend.yml](../.github/workflows/tend.yml)
runs it daily on this repository, with `updates: {minor: true}`.
