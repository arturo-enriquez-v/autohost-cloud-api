// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package platform

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

const (
	TokenApiPrefix = "autohost-node_"
	tokenApiBytes  = 32 // 32 bytes => ~43 chars base64
)

// GenerateTokenApi genera un token de enrolamiento seguro y su hash
func GenerateTokenApi() (plain string, hash string, err error) {
	// 1. bytes aleatorios seguros
	buf := make([]byte, tokenApiBytes)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("rand.Read: %w", err)
	}

	// 2. codificar en Base64 URL-safe (sin +, /, =)
	id := base64.RawURLEncoding.EncodeToString(buf)

	// 3. token final con prefijo
	plain = TokenApiPrefix + id

	// 4. hash SHA-256 del token completo
	sum := sha256.Sum256([]byte(plain))
	hash = hex.EncodeToString(sum[:])

	return plain, hash, nil
}

// HashEnrollToken genera el hash de un token
func HashTokenApi(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
