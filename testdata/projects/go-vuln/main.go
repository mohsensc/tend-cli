package main

import (
	"example.com/fixture/crypto"
	"fmt"
	"os"
)

func sign(msg, key string) string { return crypto.Sign([]byte(msg), key) }

func valid(msg, key, sig string) bool {
	if sig == "" {
		return false
	}
	return crypto.Verify([]byte(msg), key, sig)
}

func main() {
	key := os.Getenv("SIGNING_KEY")
	if len(os.Args) == 3 {
		fmt.Println(valid(os.Args[1], key, os.Args[2]))
		return
	}
	fmt.Println(sign(os.Args[1], key))
}
