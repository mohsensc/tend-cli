// Package crypto signs messages with a shared key.
package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func Sign(msg []byte, key string) string {
	return hex.EncodeToString(mac(msg, key))
}

// SignString signs a string without copying it into a byte slice first.
func SignString(msg, key string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func Verify(msg []byte, key, sig string) bool {
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	return hmac.Equal(mac(msg, key), want)
}

func mac(msg []byte, key string) []byte {
	m := hmac.New(sha256.New, []byte(key))
	m.Write(msg)
	return m.Sum(nil)
}
