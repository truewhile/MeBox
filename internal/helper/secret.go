// Package helper — AES-256-GCM at-rest secret encryption.
//
// Sensitive values (third-party API keys, service passwords, book-source
// login credentials) are stored in the database. Encrypting them keyed off
// a per-install secret means a stolen DB file alone is not enough to
// recover the plaintext.
//
// Format on disk: "enc:v1:" + base64(nonce || ciphertext || tag).
// Legacy plaintext rows (no prefix) round-trip unchanged so an upgraded
// install needs no migration step.
package helper

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// EncPrefix tags ciphertext rows so they can be told apart from plaintext.
const EncPrefix = "enc:v1:"

// SecretCipher encrypts/decrypts strings with AES-256-GCM derived from a
// stable per-install secret. A zero-value SecretCipher (empty key) is a
// pass-through, which keeps unit tests and keyless dev setups working.
type SecretCipher struct {
	aead cipher.AEAD
}

// NewSecretCipher derives a 256-bit key from secret via SHA-256.
func NewSecretCipher(secret string) *SecretCipher {
	if strings.TrimSpace(secret) == "" {
		return &SecretCipher{}
	}
	sum := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return &SecretCipher{}
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return &SecretCipher{}
	}
	return &SecretCipher{aead: aead}
}

// Ready 表示是否真的能加解密（密钥有效）。
func (c *SecretCipher) Ready() bool { return c != nil && c.aead != nil }

// Encrypt returns the prefixed base64 ciphertext for plain.
// Empty inputs and already-encrypted values round-trip unchanged.
func (c *SecretCipher) Encrypt(plain string) string {
	if plain == "" || !c.Ready() || strings.HasPrefix(plain, EncPrefix) {
		return plain
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return plain
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return EncPrefix + base64.StdEncoding.EncodeToString(sealed)
}

// Decrypt returns the plaintext for a stored value. Plaintext rows (no
// prefix) are returned unchanged, as are values that fail to authenticate.
func (c *SecretCipher) Decrypt(value string) string {
	if value == "" || !c.Ready() || !strings.HasPrefix(value, EncPrefix) {
		return value
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, EncPrefix))
	if err != nil || len(data) < c.aead.NonceSize() {
		return value
	}
	nonce, body := data[:c.aead.NonceSize()], data[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return value
	}
	return string(plain)
}

// IsEncrypted reports whether value carries the encrypted prefix.
func (c *SecretCipher) IsEncrypted(value string) bool {
	return strings.HasPrefix(value, EncPrefix)
}
