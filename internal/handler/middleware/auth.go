// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret []byte

type ctxKey int

const claimsKey ctxKey = iota

type Claims struct {
	UserID string `json:"uid"`
	Email  string `json:"email"`
	Name   string `json:"name,omitempty"`
	jwt.RegisteredClaims
}

func Auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Lazy-load JWT secret on first use (after .env is loaded)
		if jwtSecret == nil {
			jwtSecret = mustJWTSecret()
		}

		// Priority 1: Authorization: Bearer <token> (APIs, CLI, agents)
		// Priority 2: access_token cookie (browser sessions via Next.js)
		// Priority 3: ?token= or ?access_token= query parameter (WebSocket / SSE connections)
		var raw string
		if authz := r.Header.Get("Authorization"); authz != "" {
			parts := strings.SplitN(authz, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				http.Error(w, "invalid Authorization header", http.StatusUnauthorized)
				return
			}
			raw = parts[1]
		} else if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
			raw = c.Value
		} else if q := r.URL.Query().Get("token"); q != "" {
			raw = q
		} else if q := r.URL.Query().Get("access_token"); q != "" {
			raw = q
		} else {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		parser := jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}),
			jwt.WithLeeway(30*time.Second),
		)

		token, err := parser.ParseWithClaims(raw, &Claims{}, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %s", t.Method.Alg())
			}
			return jwtSecret, nil
		})
		if err != nil {
			// Distingue expirado para mejor DX
			if errors.Is(err, jwt.ErrTokenExpired) {
				http.Error(w, "token expired", http.StatusUnauthorized)
				return
			}
			log.Printf("[AUTH] jwt parse/validate error: %v", err)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if !token.Valid {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(*Claims)
		if !ok {
			http.Error(w, "invalid claims", http.StatusUnauthorized)
			return
		}

		// (Opcional extra) valida issuer/audience si los usas:
		// if claims.Issuer != "autohost-cloud" { ... }

		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func GetClaims(ctx context.Context) *Claims {
	if v := ctx.Value(claimsKey); v != nil {
		if c, ok := v.(*Claims); ok {
			return c
		}
	}
	return nil
}

func ContextWithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

func mustJWTSecret() []byte {
	s := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if s == "" {
		log.Fatal("JWT_SECRET missing")
	}
	return []byte(s) // mismo tratamiento que al firmar
}
