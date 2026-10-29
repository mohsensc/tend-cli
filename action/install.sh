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

releases() {
	local body
	body=$(api_get "$api/repos/$repo/releases?per_page=100") || die "could not list releases of $repo"
	echo "$body" | tags
}

# tagged prints the release tags that point at commit $1. /tags gives each
# tag's commit already dereferenced, so annotated tags match too.
tagged() {
	local page=1 body count
	while [ "$page" -le 50 ]; do
		body=$(api_get "$api/repos/$repo/tags?per_page=100&page=$page") || die "could not list tags of $repo"
		count=$(echo "$body" | grep -o '"name": *"[^"]*"' | wc -l)
		echo "$body" | grep -oE '"(name|sha)": *"[^"]*"' | awk -F'"' -v want="$1" '
			$2 == "name" { name = $4; next }
			$2 == "sha" && name != "" { if ($4 == want) print name; name = "" }'
		[ "$count" -ge 100 ] || return 0
		page=$((page + 1))
	done
}

newest() {
	sort -t. -k1.2,1nr -k2,2nr -k3,3nr | head -n "$1"
}

# An explicit version wins. Then the action ref: a full tag is used as is; a
# commit installs the release tagged at that commit; a major (@v1) or a minor
# (@v1.2) installs the newest release inside it, followed by the one before
# it. Any other ref, a branch, gets the latest release.
resolve() {
	local list sha found pick
	if [ -n "${1:-}" ]; then
		echo "v${1#v}"
		return
	fi
	if [[ $ref =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
		echo "$ref"
	elif [[ $ref =~ ^v[0-9]+$ ]]; then
		list=$(releases) || exit 1
		echo "$list" | grep -E "^${ref}\.[0-9]+\.[0-9]+\$" | newest 2 || true
	elif [[ $ref =~ ^v[0-9]+\.[0-9]+$ ]]; then
		list=$(releases) || exit 1
		echo "$list" | grep -E "^${ref%.*}\.${ref#*.}\.[0-9]+\$" | newest 2 || true
	elif [[ $ref =~ ^[0-9a-fA-F]{40}$ ]]; then
		sha=$(echo "$ref" | tr A-F a-f)
		found=$(tagged "$sha") || exit 1
		pick=$(echo "$found" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | newest 1 || true)
		[ -n "$pick" ] || pick=$(echo "$found" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.]+$' | head -n 1 || true)
		[ -n "$pick" ] || die "commit $sha of $repo is not a release; pin a release commit or set the version input"
		echo "$pick"
	elif [[ $ref =~ ^v[0-9] ]]; then
		die "action ref $ref is not a release tag; use @vN, @vN.M, @vN.M.P or a release commit, or set the version input"
	else
		[ -z "$ref" ] || echo "tend install: action ref $ref is not a release; installing the latest release" >&2
		api_get "$api/repos/$repo/releases/latest" | tags | sed -n 1p
	fi
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

tags=$(resolve "${1:-}")
tag=$(echo "$tags" | sed -n 1p)
[ -n "$tag" ] || die "no release of $repo matches ${1:-${ref:-the latest release}}"
# A major or minor ref falls back to the release before the newest when the
# newest has no signed checksums. Either is checked the same way below.
older=$(echo "$tags" | sed -n 2p)
if [ -n "$older" ] && ! curl -fsSL --retry 2 -o /dev/null "$dl/$repo/releases/download/$tag/checksums.txt.sig" 2>/dev/null; then
	echo "tend install: $tag has no signed checksums yet; installing $older" >&2
	tag=$older
fi
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
entry=$dest
if [ "$os" = windows ] && command -v cygpath >/dev/null 2>&1; then
	dest=$(cygpath -u "$dest")
	entry=$(cygpath -w "$dest")
fi
mkdir -p "$dest"
install -m 0755 "$work/$bin" "$dest/$bin"
if [ -n "${GITHUB_PATH:-}" ]; then
	echo "$entry" >>"$GITHUB_PATH"
else
	echo "add to PATH: $dest"
fi
echo "tend install: $tag installed in $dest"
