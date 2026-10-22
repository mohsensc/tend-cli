package main

import "testing"

func TestSign(t *testing.T) {
	sig := sign("hello", "k1")
	if sig == "" || sig != sign("hello", "k1") {
		t.Fatalf("sign is not stable: %q", sig)
	}
	if !valid("hello", "k1", sig) {
		t.Error("a fresh signature does not verify")
	}
	if valid("hello", "k2", sig) || valid("hello", "k1", "") {
		t.Error("a wrong key or an empty signature verified")
	}
}
