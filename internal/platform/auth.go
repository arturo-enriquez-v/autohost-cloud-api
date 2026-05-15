package platform

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type JWTClaims struct {
	UserID string `json:"uid"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

func jwtSecret() []byte {
	return []byte(strings.TrimSpace(os.Getenv("JWT_SECRET")))
}

func SignAccessToken(userID string, email string) (string, error) {
	ttl := 15 * time.Minute
	if v := os.Getenv("ACCESS_TOKEN_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			ttl = d
		}
	}
	secret := jwtSecret()
	now := time.Now().UTC()
	claims := JWTClaims{
		UserID: userID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func ParseAccessToken(tok string) (*JWTClaims, error) {
	secret := jwtSecret()
	token, err := jwt.ParseWithClaims(tok, &JWTClaims{}, func(t *jwt.Token) (interface{}, error) {
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}
	return nil, jwt.ErrSignatureInvalid
}

// MakeRefreshPair genera un token de refresco y su hash
func MakeRefreshPair() (plain string, hash string) {
	plain = uuid.NewString()
	return plain, HashRefreshToken(plain)
}

// HashRefreshToken crea un hash SHA-256 del token
func HashRefreshToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ParseRefreshToken extrae información del token de refresco
// En MVP retorna vacío, en producción podría ser un JWT
func ParseRefreshToken(plain string) (userID string, email string, err error) {
	// MVP: el refresh token no lleva datos embebidos
	// En producción podría ser un JWT con claims
	return "", "", nil
}
