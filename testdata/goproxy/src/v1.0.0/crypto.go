// Package crypto signs messages with a shared key.
package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func Sign(msg []byte, key string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write(msg)
	return hex.EncodeToString(m.Sum([]byte(key)))
}

func Verify(msg []byte, key, sig string) bool {
	return Sign(msg, key) == sig
}
