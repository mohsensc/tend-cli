package core

import "unicode/utf8"

// Clip cuts s to at most n bytes without splitting a rune.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Short is a commit sha as people read it in output and PR bodies.
func Short(sha string) string { return sha[:min(7, len(sha))] }
