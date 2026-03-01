package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"golang.org/x/crypto/argon2"

	"github.com/ShigShag/Phantom-Proxy/internal/buildcfg"
)

const (
	NonceSize   = 32
	KeySize     = 32
	argonTime   = 1
	argonMemory = 64 * 1024
	argonThread = 4
)

// DeriveKey uses Argon2id to derive a key from a password and salt.
func DeriveKey(secret string, salt []byte) []byte {
	return argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThread, KeySize)
}

// DeterministicSalt generates a deterministic salt from a label string.
// Used so both sides derive the same key without exchanging a salt.
func DeterministicSalt(label string) []byte {
	h := sha256.Sum256([]byte(buildcfg.SaltPrefix + label))
	return h[:16]
}

// GenerateNonce returns a cryptographically random nonce.
func GenerateNonce() ([]byte, error) {
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return nonce, nil
}

// ComputeHMAC computes HMAC-SHA256 over data using the given key.
func ComputeHMAC(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// VerifyHMAC verifies an HMAC-SHA256 tag.
func VerifyHMAC(key, data, tag []byte) bool {
	expected := ComputeHMAC(key, data)
	return hmac.Equal(expected, tag)
}
