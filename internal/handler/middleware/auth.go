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
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
)

var jwtSecret []byte

type ctxKey int

const claimsKey ctxKey = iota

type Claims struct {
	UserID string `json:"uid"`
	Email  string `json:"email"`
	Name   string `json:"name,omitempty"`
	Role   string `json:"role,omitempty"`
	jwt.RegisteredClaims

	// Set only when the request authenticated with a personal API token instead of a session JWT.
	APITokenID    string `json:"-"`
	APITokenScope string `json:"-"`
	// APITokenPermissions are the node permissions the token may use (["*"] for write tokens).
	APITokenPermissions []string `json:"-"`
}

// ViaAPIToken reports whether the request authenticated with a personal API token.
func (c *Claims) ViaAPIToken() bool { return c != nil && c.APITokenID != "" }

// ReadOnly reports whether the credential may only perform read operations. Unknown scopes are treated
// as read-only (fail closed).
func (c *Claims) ReadOnly() bool {
	return c.ViaAPIToken() && c.APITokenScope != "write" && c.APITokenScope != "custom"
}

// NodeScopedWrites reports whether the credential may only write through node routes guarded by
// requireNodePerm (custom API tokens).
func (c *Claims) NodeScopedWrites() bool { return c.ViaAPIToken() && c.APITokenScope == "custom" }

// APITokenAuthenticator resolves a personal API token (Authorization: Bearer ahp_...) to claims.
type APITokenAuthenticator func(ctx context.Context, raw string) (*Claims, error)

var apiTokenAuth atomic.Pointer[APITokenAuthenticator]

// SetAPITokenAuthenticator enables personal API tokens in Auth. Without it they are rejected as invalid.
func SetAPITokenAuthenticator(fn APITokenAuthenticator) {
	if fn == nil {
		apiTokenAuth.Store(nil)
		return
	}
	apiTokenAuth.Store(&fn)
}

// ErrAPITokenRejected is returned by an APITokenAuthenticator for an unknown, expired or revoked token.
var ErrAPITokenRejected = errors.New("invalid api token")

const apiTokenPrefix = "ahp_"

func Auth(next http.Handler) http.Handler {
	return authenticate(next, false)
}

// AuthIdentity authenticates like Auth but only from the Authorization header (no cookie or query
// token, so a browser cannot be made to call it with the user's session) and without applying the API
// token scope to this request's method. It is for /mcp: the MCP transport is always POST, and each API
// call it makes on the caller's behalf goes through Auth again with its real method, where the token
// scope is enforced.
func AuthIdentity(next http.Handler) http.Handler {
	return authenticate(next, true)
}

func authenticate(next http.Handler, identityOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Lazy-load JWT secret on first use (after .env is loaded)
		if jwtSecret == nil {
			jwtSecret = mustJWTSecret()
		}

		// Priority 1: Authorization: Bearer <token> (APIs, CLI, agents)
		// Priority 2: access_token cookie (browser sessions via Next.js)
		// Priority 3: ?token= or ?access_token= query parameter (WebSocket / SSE connections)
		var raw string
		fromHeader := false
		if authz := r.Header.Get("Authorization"); authz != "" {
			parts := strings.SplitN(authz, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				http.Error(w, "invalid Authorization header", http.StatusUnauthorized)
				return
			}
			raw = parts[1]
			fromHeader = true
		} else if identityOnly {
			http.Error(w, "unauthorized: send the token in the Authorization header", http.StatusUnauthorized)
			return
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

		if strings.HasPrefix(raw, apiTokenPrefix) {
			// API tokens are only accepted in the Authorization header: query strings end up in logs.
			if !fromHeader {
				http.Error(w, "api tokens must be sent in the Authorization header", http.StatusUnauthorized)
				return
			}
			authAPIToken(w, r, next, raw, !identityOnly)
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

func authAPIToken(w http.ResponseWriter, r *http.Request, next http.Handler, raw string, enforceScope bool) {
	fn := apiTokenAuth.Load()
	if fn == nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	claims, err := (*fn)(r.Context(), raw)
	if err != nil {
		if !errors.Is(err, ErrAPITokenRejected) {
			log.Printf("[ERROR] api token authentication: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	switch {
	case !enforceScope:
	case claims.ReadOnly() && !isReadOnlyRequest(r):
		http.Error(w, "read-only api token", http.StatusForbidden)
		return
	case claims.NodeScopedWrites() && !isReadOnlyRequest(r) && !isNodePermRoute(r):
		http.Error(w, "this api token can only make changes on nodes", http.StatusForbidden)
		return
	}
	ctx := context.WithValue(r.Context(), claimsKey, claims)
	// Every node permission check made for this request is limited to the token's permissions.
	ctx = identity.WithTokenPermissions(ctx, claims.APITokenPermissions)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// isReadOnlyRequest reports whether a read-only token may make the request: safe methods only, and no
// WebSocket upgrades (the terminal is a GET that becomes an interactive shell).
func isReadOnlyRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		return false
	}
	return r.Header.Get("Upgrade") == "" && !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// RequireSession rejects requests authenticated with a personal API token. It goes after Auth on routes
// a token must not reach, such as managing API tokens themselves.
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetClaims(r.Context()).ViaAPIToken() {
			http.Error(w, "this action requires a session login, not an api token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
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
