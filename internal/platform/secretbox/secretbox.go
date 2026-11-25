// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

// Package secretbox encrypts secrets at rest (third-party credentials stored in the database) with
// AES-256-GCM. The key comes from SECRETS_ENCRYPTION_KEY and never touches the database, so a database
// dump alone does not reveal the secrets.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// EnvKey is the environment variable holding the 32-byte key, as 64 hex characters or base64.
const EnvKey = "SECRETS_ENCRYPTION_KEY"

// version prefixes every sealed value so the format (or the key) can be rotated later.
const version = "v1:"

var (
	// ErrNotConfigured is returned when SECRETS_ENCRYPTION_KEY is not set.
	ErrNotConfigured = errors.New("secret encryption is not configured (" + EnvKey + ")")
	// ErrInvalidKey is returned when the key is not 32 bytes of hex or base64.
	ErrInvalidKey = errors.New(EnvKey + " must be 32 bytes, as 64 hex characters or base64")
	// ErrDecrypt is returned when a sealed value is malformed, was sealed with another key, or was moved
	// to a different record (the associated data does not match).
	ErrDecrypt = errors.New("could not decrypt secret")
)

// Box seals and opens secrets. A nil *Box is valid and fails every operation with ErrNotConfigured.
type Box struct {
	aead cipher.AEAD
}

// New builds a Box from a raw 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// ParseKey decodes a key given as 64 hex characters or as standard/URL base64 of 32 bytes.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) == 64 {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, ErrInvalidKey
}

// FromEnv builds a Box from SECRETS_ENCRYPTION_KEY. It returns (nil, nil) when the variable is empty, so
// the API still starts and only features that store secrets are unavailable; an invalid key is an error.
func FromEnv() (*Box, error) {
	raw := strings.TrimSpace(os.Getenv(EnvKey))
	if raw == "" {
		return nil, nil
	}
	key, err := ParseKey(raw)
	if err != nil {
		return nil, err
	}
	return New(key)
}

// Configured reports whether the box can seal and open secrets.
func (b *Box) Configured() bool { return b != nil && b.aead != nil }

// Seal encrypts plaintext. The associated data (for example the owning record's ID) is authenticated
// but not stored: Open fails unless it gets the same value, so a ciphertext cannot be copied to another
// record and used there.
func (b *Box) Seal(plaintext, associatedData string) (string, error) {
	if !b.Configured() {
		return "", ErrNotConfigured
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(associatedData))
	return version + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value produced by Seal with the same associated data.
func (b *Box) Open(sealed, associatedData string) (string, error) {
	if !b.Configured() {
		return "", ErrNotConfigured
	}
	payload, ok := strings.CutPrefix(sealed, version)
	if !ok {
		return "", ErrDecrypt
	}
	raw, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", ErrDecrypt
	}
	nonce, ciphertext := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, ciphertext, []byte(associatedData))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}
