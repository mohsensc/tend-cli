#!/usr/bin/env bash
# Installs the tend release binary for this runner after checking its signature and checksum.
# usage: install.sh [version]
set -euo pipefail

repo=mohsensc/tend-cli
api=${TEND_API:-https://api.github.com}
dl=${TEND_DOWNLOAD:-https://github.com}
here=$(cd "$(dirname "$0")/.." && pwd)
pubkey=${GITHUB_ACTION_PATH:-$here}/action/pubkey.pem
ref=${GITHUB_ACTION_REF:-}

die() {
	echo "tend install: $*" >&2
	exit 1
}

api_get() {
	curl -fsSL --retry 3 --retry-delay 2 -H "Accept: application/vnd.github+json" \
		${GITHUB_TOKEN:+-H "Authorization: Bearer $GITHUB_TOKEN"} "$1"
}

tags() {
	grep -o '"tag_name": *"[^"]*"' | sed 's/.*"\([^"]*\)"$/\1/' || true
}

# An explicit version wins. Then a full tag in the action ref. Then the newest
# release inside the ref's major (uses: ...@v1), else the newest release.
resolve() {
	local major list
	if [ -n "${1:-}" ]; then
		echo "v${1#v}"
		return
	fi
	case "$ref" in
	v[0-9]*.[0-9]*.[0-9]*)
		echo "$ref"
		return
		;;
	v[0-9]*)
		major=${ref%%.*}
		list=$(api_get "$api/repos/$repo/releases?per_page=100" | tags) || die "could not list releases of $repo"
		{ echo "$list" | grep -E "^${major}\.[0-9]+\.[0-9]+\$" || true; } | sort -t. -k2,2n -k3,3n | tail -n 1
		return
		;;
	esac
	api_get "$api/repos/$repo/releases/latest" | tags | sed -n 1p
}

find_openssl() {
	local c
	for c in openssl /opt/homebrew/opt/openssl@3/bin/openssl /usr/local/opt/openssl@3/bin/openssl; do
		if command -v "$c" >/dev/null 2>&1 && "$c" version 2>/dev/null | grep -Eq '^OpenSSL (3|1\.1\.1)'; then
			command -v "$c"
			return
		fi
	done
	die "an OpenSSL 1.1.1 or newer is needed to check the signature; the openssl in PATH is $(openssl version 2>/dev/null || echo missing)"
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

tag=$(resolve "${1:-}")
[ -n "$tag" ] || die "no release of $repo matches ${1:-${ref:-the latest release}}"
version=${tag#v}

case "${RUNNER_OS:-$(uname -s)}" in
Linux) os=linux ;;
macOS | Darwin) os=darwin ;;
Windows | MINGW* | MSYS* | CYGWIN*) os=windows ;;
*) die "unsupported OS ${RUNNER_OS:-$(uname -s)}" ;;
esac
case "${RUNNER_ARCH:-$(uname -m)}" in
X64 | x86_64 | amd64) arch=amd64 ;;
ARM64 | arm64 | aarch64) arch=arm64 ;;
*) die "unsupported architecture ${RUNNER_ARCH:-$(uname -m)}" ;;
esac
ext=tar.gz
bin=tend
if [ "$os" = windows ]; then
	ext=zip
	bin=tend.exe
fi
asset="tend_${version}_${os}_${arch}.${ext}"
base="$dl/$repo/releases/download/$tag"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fetch() {
	curl -fsSL --retry 3 --retry-delay 2 -o "$work/$1" "$base/$1" || die "could not download $base/$1"
}
fetch checksums.txt
fetch checksums.txt.sig

openssl=$(find_openssl)
tr -d '[:space:]' <"$work/checksums.txt.sig" | "$openssl" base64 -d -A -out "$work/sig.bin" 2>/dev/null ||
	die "checksums.txt.sig for $tag is not base64"
"$openssl" pkeyutl -verify -pubin -inkey "$pubkey" -rawin -in "$work/checksums.txt" -sigfile "$work/sig.bin" >/dev/null 2>&1 ||
	die "signature of checksums.txt for $tag does not verify against $pubkey; refusing to install"

want=$(awk -v n="$asset" '$2 == n || $2 == "*" n { print $1; exit }' "$work/checksums.txt")
[ -n "$want" ] || die "checksums.txt for $tag has no entry for $asset"
fetch "$asset"
got=$(sha256_of "$work/$asset")
[ "$got" = "$want" ] || die "sha256 mismatch for $asset: checksums.txt says $want, the download is $got"

if [ "$os" = windows ]; then
	unzip -q -o "$work/$asset" "$bin" -d "$work" || die "$asset does not contain $bin"
else
	tar -xzf "$work/$asset" -C "$work" "$bin" || die "$asset does not contain $bin"
fi

dest=${RUNNER_TEMP:-${TMPDIR:-/tmp}}/tend/bin
mkdir -p "$dest"
install -m 0755 "$work/$bin" "$dest/$bin"
if [ -n "${GITHUB_PATH:-}" ]; then
	echo "$dest" >>"$GITHUB_PATH"
else
	echo "add to PATH: $dest"
fi
echo "tend install: $tag installed in $dest"
