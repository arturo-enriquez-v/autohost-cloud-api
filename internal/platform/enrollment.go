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
	EnrollTokenPrefix = "autohost-enr_"
	enrollTokenBytes  = 32 // 32 bytes => ~43 chars base64
)

// GenerateEnrollToken genera un token de enrolamiento seguro y su hash
func GenerateEnrollToken() (plain string, hash string, err error) {
	// 1. bytes aleatorios seguros
	buf := make([]byte, enrollTokenBytes)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("rand.Read: %w", err)
	}

	// 2. codificar en Base64 URL-safe (sin +, /, =)
	id := base64.RawURLEncoding.EncodeToString(buf)

	// 3. token final con prefijo
	plain = EnrollTokenPrefix + id

	// 4. hash SHA-256 del token completo
	sum := sha256.Sum256([]byte(plain))
	hash = hex.EncodeToString(sum[:])

	return plain, hash, nil
}

// HashEnrollToken genera el hash de un token de enrolamiento
func HashEnrollToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
