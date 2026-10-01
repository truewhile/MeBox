// Package service — AES-GCM crypto helper for at-rest secrets.
//
// Sensitive fields (third-party API keys, service passwords, …) are
// stored in SQLite. We encrypt them with AES-256-GCM keyed off the JWT
// secret so a stolen DB file alone is not enough to recover the
// plaintext credentials.
//
// Format on disk:  "enc:v1:" + base64(nonce || ciphertext || tag)
//
// Legacy plaintext rows (no prefix) round-trip unchanged so an upgraded
// install does not need a migration step.
package service

import (
	"errors"
	"strings"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
)

// encPrefix tags ciphertext rows so we can tell them apart from legacy
// plaintext values. Kept as an alias of the shared helper's prefix so both
// implementations stay wire-compatible.
const encPrefix = helper.EncPrefix

// CryptoService wraps an AES-GCM cipher derived from a stable per-install
// secret (the JWT secret).
//
// The cipher itself lives in helper.SecretCipher so lower layers (e.g. the
// reader subsystem, which cannot import this package) can share one
// implementation; this type keeps the service-layer logging and API.
type CryptoService struct {
	log    *zap.Logger
	cipher *helper.SecretCipher
}

// NewCryptoService derives a 256-bit key from the given secret via
// SHA-256 and constructs an AES-GCM AEAD. Empty secrets yield a service
// whose Encrypt/Decrypt methods are pass-throughs (used in unit tests).
func NewCryptoService(secret string, log *zap.Logger) *CryptoService {
	return &CryptoService{log: log, cipher: helper.NewSecretCipher(secret)}
}

// Encrypt returns the base64-encoded ciphertext (with prefix) for plain.
// Empty inputs round-trip unchanged.
func (c *CryptoService) Encrypt(plain string) string {
	return c.cipher.Encrypt(plain)
}

// Decrypt returns the plaintext for an encrypted value. Plaintext rows
// (no prefix) are returned unchanged.
func (c *CryptoService) Decrypt(value string) string {
	return c.cipher.Decrypt(value)
}

// IsEncrypted returns true if value carries the encrypted prefix.
func (c *CryptoService) IsEncrypted(value string) bool {
	return c.cipher.IsEncrypted(value)
}

// MaskAPIKey returns "abcd****wxyz" so the key can be displayed in the
// admin UI without leaking it. Inputs shorter than 8 chars become "****".
func MaskAPIKey(plain string) string {
	plain = strings.TrimSpace(plain)
	if len(plain) < 8 {
		return "****"
	}
	return plain[:4] + "****" + plain[len(plain)-4:]
}

// ErrCryptoUnavailable is returned when callers expect crypto and the
// service is degraded (empty secret, init failure).
var ErrCryptoUnavailable = errors.New("crypto unavailable")
