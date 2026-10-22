// Package crypto signs messages with named keys.
package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Key is a signing key and the name it is published under.
type Key struct{ ID, Secret string }

func Sign(msg []byte, k Key) string {
	m := hmac.New(sha256.New, []byte(k.Secret))
	m.Write(msg)
	return k.ID + ":" + hex.EncodeToString(m.Sum(nil))
}

func Verify(msg []byte, k Key, sig string) bool {
	return hmac.Equal([]byte(Sign(msg, k)), []byte(sig))
}
