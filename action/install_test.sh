#!/usr/bin/env bash
# Runs install.sh once and checks the outcome.
# usage: install_test.sh ok|fail <text> [install.sh args...]
#   ok:   install.sh succeeds and the installed binary prints <text>
#   fail: install.sh exits non-zero, says <text>, and installs nothing
# The caller points TEND_API, TEND_DOWNLOAD and GITHUB_ACTION_PATH at a fixture.
set -u

here=$(cd "$(dirname "$0")" && pwd)
expect=$1
match=$2
shift 2

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export RUNNER_TEMP="$tmp/runner" GITHUB_PATH="$tmp/path"
mkdir -p "$RUNNER_TEMP"
: >"$GITHUB_PATH"

out=$("$here/install.sh" "$@" 2>&1)
status=$?
dest="$RUNNER_TEMP/tend/bin"
bin=tend
[ "${RUNNER_OS:-}" = Windows ] && bin=tend.exe

fail() {
	echo "FAIL: $*" >&2
	echo "--- output" >&2
	echo "$out" >&2
	exit 1
}

case "$expect" in
ok)
	[ "$status" -eq 0 ] || fail "exit status $status"
	[ -x "$dest/$bin" ] || fail "no executable at $dest/$bin"
	[ "$("$dest/$bin")" = "$match" ] || fail "binary printed '$("$dest/$bin")', want '$match'"
	grep -qx "$dest" "$GITHUB_PATH" || fail "GITHUB_PATH does not list $dest"
	;;
fail)
	[ "$status" -ne 0 ] || fail "install.sh succeeded"
	echo "$out" | grep -qF -- "$match" || fail "output does not mention '$match'"
	[ ! -e "$dest/$bin" ] || fail "a binary was installed anyway"
	[ ! -s "$GITHUB_PATH" ] || fail "GITHUB_PATH was written"
	;;
*)
	fail "expect must be ok or fail"
	;;
esac
