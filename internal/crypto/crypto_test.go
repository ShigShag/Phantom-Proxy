package crypto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestDeriveKey(t *testing.T) {
	salt := []byte("test-salt-16byte")

	// Deterministic: same inputs produce same output.
	k1 := DeriveKey("secret", salt)
	k2 := DeriveKey("secret", salt)
	if !bytes.Equal(k1, k2) {
		t.Fatal("DeriveKey not deterministic")
	}
	if len(k1) != KeySize {
		t.Fatalf("key length = %d, want %d", len(k1), KeySize)
	}

	// Different secret produces different key.
	k3 := DeriveKey("other-secret", salt)
	if bytes.Equal(k1, k3) {
		t.Fatal("different secrets produced same key")
	}

	// Different salt produces different key.
	k4 := DeriveKey("secret", []byte("other-salt-16byt"))
	if bytes.Equal(k1, k4) {
		t.Fatal("different salts produced same key")
	}
}

func TestDeterministicSalt(t *testing.T) {
	s1 := DeterministicSalt("label-a")
	s2 := DeterministicSalt("label-a")
	if !bytes.Equal(s1, s2) {
		t.Fatal("DeterministicSalt not deterministic")
	}
	if len(s1) != 16 {
		t.Fatalf("salt length = %d, want 16", len(s1))
	}

	s3 := DeterministicSalt("label-b")
	if bytes.Equal(s1, s3) {
		t.Fatal("different labels produced same salt")
	}
}

func TestGenerateNonce(t *testing.T) {
	n1, err := GenerateNonce()
	if err != nil {
		t.Fatal(err)
	}
	if len(n1) != NonceSize {
		t.Fatalf("nonce length = %d, want %d", len(n1), NonceSize)
	}

	n2, err := GenerateNonce()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(n1, n2) {
		t.Fatal("two nonces are identical")
	}
}

func TestComputeHMAC(t *testing.T) {
	key := []byte("test-key")
	data := []byte("test-data")

	h1 := ComputeHMAC(key, data)
	h2 := ComputeHMAC(key, data)
	if !bytes.Equal(h1, h2) {
		t.Fatal("ComputeHMAC not deterministic")
	}
	if len(h1) != 32 { // SHA-256 output
		t.Fatalf("HMAC length = %d, want 32", len(h1))
	}
}

func TestVerifyHMAC(t *testing.T) {
	key := []byte("test-key")
	data := []byte("test-data")
	tag := ComputeHMAC(key, data)

	if !VerifyHMAC(key, data, tag) {
		t.Fatal("valid HMAC rejected")
	}

	// Tampered tag.
	bad := make([]byte, len(tag))
	copy(bad, tag)
	bad[0] ^= 0xff
	if VerifyHMAC(key, data, bad) {
		t.Fatal("tampered HMAC accepted")
	}

	// Wrong key.
	if VerifyHMAC([]byte("wrong-key"), data, tag) {
		t.Fatal("wrong key HMAC accepted")
	}
}

func TestGenerateED25519Key(t *testing.T) {
	priv, err := GenerateED25519Key()
	if err != nil {
		t.Fatal(err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("key length = %d, want %d", len(priv), ed25519.PrivateKeySize)
	}

	// Verify it can sign and verify.
	msg := []byte("hello")
	sig := ed25519.Sign(priv, msg)
	if !ed25519.Verify(priv.Public().(ed25519.PublicKey), msg, sig) {
		t.Fatal("signature verification failed")
	}
}

func TestGenerateSelfSignedCert(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}

	// Parse cert PEM.
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode cert PEM")
	}
	if block.Type != "CERTIFICATE" {
		t.Fatalf("PEM type = %q, want CERTIFICATE", block.Type)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	if cert.Subject.CommonName != "phantom-proxy" {
		t.Fatalf("CN = %q, want phantom-proxy", cert.Subject.CommonName)
	}

	// Parse key PEM.
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("failed to decode key PEM")
	}
	if keyBlock.Type != "EC PRIVATE KEY" {
		t.Fatalf("key PEM type = %q, want EC PRIVATE KEY", keyBlock.Type)
	}
	if _, err := x509.ParseECPrivateKey(keyBlock.Bytes); err != nil {
		t.Fatalf("parse EC key: %v", err)
	}
}
