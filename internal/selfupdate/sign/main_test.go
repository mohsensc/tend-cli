package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func keygen(t *testing.T) (priv string, pub ed25519.PublicKey, pemText string) {
	t.Helper()
	var out bytes.Buffer
	if err := run([]string{"-keygen"}, &out, os.Stderr); err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(out.String(), "\n", 3)
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "private ") || !strings.HasPrefix(lines[1], "public  ") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
	priv = strings.TrimPrefix(lines[0], "private ")
	pubHex := strings.TrimPrefix(lines[1], "public  ")
	b, err := hex.DecodeString(pubHex)
	if err != nil {
		t.Fatal(err)
	}
	return priv, b, lines[2]
}

func TestKeygen(t *testing.T) {
	priv, pub, pemText := keygen(t)
	if len(priv) != 2*ed25519.SeedSize {
		t.Errorf("private key is %d hex chars", len(priv))
	}
	if !strings.HasPrefix(pemText, "-----BEGIN PUBLIC KEY-----\n") || !strings.HasSuffix(pemText, "-----END PUBLIC KEY-----\n") {
		t.Fatalf("pem block:\n%s", pemText)
	}
	block, _ := pem.Decode([]byte(pemText))
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed.(ed25519.PublicKey), pub) {
		t.Error("the PEM and the hex public keys differ")
	}
	seed, _ := hex.DecodeString(priv)
	if !bytes.Equal(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), pub) {
		t.Error("the private key does not belong to the public key")
	}
	priv2, _, _ := keygen(t)
	if priv == priv2 {
		t.Error("two runs made the same key")
	}
}

func TestSign(t *testing.T) {
	priv, pub, pemText := keygen(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "checksums.txt")
	data := []byte("abc123  tend_1.0.0_linux_amd64.tar.gz\n")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"-key", priv, "-in", file}, &out, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "\n") || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("signature should be one line: %q", out.String())
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pub, data, sig) {
		t.Fatal("signature does not verify")
	}
	if ed25519.Verify(pub, append(data, '\n'), sig) {
		t.Fatal("signature verifies for different data")
	}
	t.Setenv("TEND_SIGNING_KEY", priv)
	var fromEnv bytes.Buffer
	if err := run([]string{"-in", file}, &fromEnv, os.Stderr); err != nil || fromEnv.String() != out.String() {
		t.Errorf("signing with the key from the environment: %q, %v", fromEnv.String(), err)
	}

	if _, err := exec.LookPath("openssl"); err != nil {
		t.Log("openssl not found, skipping the cross-check")
		return
	}
	pubFile, sigFile := filepath.Join(dir, "pub.pem"), filepath.Join(dir, "sig.bin")
	if err := os.WriteFile(pubFile, []byte(pemText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigFile, sig, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := exec.Command("openssl", "pkeyutl", "-verify", "-pubin", "-inkey", pubFile, "-rawin", "-in", file, "-sigfile", sigFile).CombinedOutput()
	if err != nil && strings.Contains(string(res), "Unknown option") {
		t.Skip("this openssl has no -rawin")
	}
	if err != nil {
		t.Errorf("openssl rejects the signature: %v\n%s", err, res)
	}
}

func TestSignAcceptsFullPrivateKey(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	full := ed25519.NewKeyFromSeed(seed)
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var a, b bytes.Buffer
	if err := run([]string{"-key", hex.EncodeToString(seed), "-in", file}, &a, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-key", hex.EncodeToString(full), "-in", file}, &b, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Error("seed and full key sign differently")
	}
}

func TestRunErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no args", nil, "usage"},
		{"key without file", []string{"-key", "00"}, "usage"},
		{"not hex", []string{"-key", "zz", "-in", file}, "not hex"},
		{"short key", []string{"-key", "0011", "-in", file}, "2 bytes"},
		{"missing file", []string{"-key", strings.Repeat("00", 32), "-in", file + ".nope"}, "no such file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := run(tc.args, &out, &errOut)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if out.Len() != 0 {
				t.Errorf("wrote to stdout on failure: %q", out.String())
			}
		})
	}
}
