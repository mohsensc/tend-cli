package selfupdate

import (
	"crypto/ed25519"
	"encoding/hex"
)

// PublicKey verifies checksums.txt.sig of every release.
var PublicKey = mustHex("9ee9b726746cddce4d249a2d824168e89e2a987ea50625e19e1524eee8d5a1e3")

func mustHex(s string) ed25519.PublicKey {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("selfupdate: bad public key")
	}
	return b
}
