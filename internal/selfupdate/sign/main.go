// Sign makes the release signing key and signs checksums.txt. It refuses a key
// whose public half is not the one built into tend, so a bad secret fails
// before anything is published.
//
//	go run ./internal/selfupdate/sign -keygen
//	TEND_SIGNING_KEY=<hex> go run ./internal/selfupdate/sign -in checksums.txt > checksums.txt.sig
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mohsensc/tend-cli/internal/selfupdate"
)

var wantPublic = selfupdate.PublicKey

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keygen := fs.Bool("keygen", false, "print a new key pair")
	key := fs.String("key", "", "private key, hex; default $TEND_SIGNING_KEY, which keeps it off the command line")
	in := fs.String("in", "", "file to sign")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *key == "" {
		*key = os.Getenv("TEND_SIGNING_KEY")
	}
	switch {
	case *keygen:
		return generate(stdout)
	case *key != "" && *in != "":
		return sign(stdout, *key, *in)
	}
	return errors.New("usage: sign -keygen | sign [-key <hex>] -in <file>")
}

func generate(w io.Writer) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "private %s\npublic  %s\n", hex.EncodeToString(priv.Seed()), hex.EncodeToString(pub))
	return pem.Encode(w, &pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func sign(w io.Writer, keyHex, path string) error {
	b, err := hex.DecodeString(keyHex)
	if err != nil {
		return fmt.Errorf("key is not hex: %w", err)
	}
	if len(b) != ed25519.SeedSize && len(b) != ed25519.PrivateKeySize {
		return fmt.Errorf("key is %d bytes, want %d or %d", len(b), ed25519.SeedSize, ed25519.PrivateKeySize)
	}
	priv := ed25519.NewKeyFromSeed(b[:ed25519.SeedSize])
	if len(b) == ed25519.PrivateKeySize && !bytes.Equal(b[ed25519.SeedSize:], priv[ed25519.SeedSize:]) {
		return errors.New("key's public half does not match its seed")
	}
	if pub := priv.Public().(ed25519.PublicKey); !pub.Equal(wantPublic) {
		return fmt.Errorf("key's public half is %x, but key.go and action/pubkey.pem hold %x; releases signed with it would not install", []byte(pub), []byte(wantPublic))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, data)
	if !ed25519.Verify(wantPublic, data, sig) {
		return errors.New("signature does not verify against the public key in key.go")
	}
	_, err = fmt.Fprintln(w, base64.StdEncoding.EncodeToString(sig))
	return err
}
