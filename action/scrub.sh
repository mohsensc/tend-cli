#!/usr/bin/env bash
# Removes the token actions/checkout left behind: the extraheader in the
# repository's config and the credentials file its includeIf entries point at.
# Only files under RUNNER_TEMP are deleted, the same rule checkout follows.
# usage: scrub.sh (in the checked-out repository)
set -uo pipefail

unix() {
	if command -v cygpath >/dev/null 2>&1; then
		cygpath -u "$1"
	else
		printf '%s\n' "${1//\\//}"
	fi
}

temp=
[ -z "${RUNNER_TEMP:-}" ] || temp=$(unix "$RUNNER_TEMP")

git config --local --unset-all http.https://github.com/.extraheader 2>/dev/null || true
git config --local --null --get-regexp '^includeif\..*\.path$' 2>/dev/null | while IFS= read -r -d '' entry; do
	key=${entry%%$'\n'*}
	val=${entry#*$'\n'}
	case "$val" in
	*git-credentials-*.config)
		git config --local --unset-all "$key" 2>/dev/null || true
		file=$(unix "$val")
		if [ -n "$temp" ]; then
			case "$file" in "$temp"/*) rm -f "$file" ;; esac
		fi
		;;
	esac
done || true
if [ -n "$temp" ]; then
	rm -f "$temp"/git-credentials-*.config
fi
