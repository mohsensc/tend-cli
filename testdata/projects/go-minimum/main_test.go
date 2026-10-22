package main

import "testing"

func TestGreeting(t *testing.T) {
	if got := greeting(); got != "hello" {
		t.Fatalf("greeting() = %q", got)
	}
}
