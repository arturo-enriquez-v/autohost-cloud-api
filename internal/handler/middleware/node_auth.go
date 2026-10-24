// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"context"
	"log"
	"net/http"
	"strings"

	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

type nodeCtxKey int

const nodeTokenKey nodeCtxKey = iota

// NodeAuth middleware valida el node_token enviado por el agente
func NodeAuth(nodeTokenService *nodetoken.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authz := r.Header.Get("Authorization")
			parts := strings.SplitN(authz, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				http.Error(w, "invalid Authorization header", http.StatusUnauthorized)
				return
			}
			plainToken := parts[1]

			// Validar formato del token
			if !strings.HasPrefix(plainToken, platform.TokenApiPrefix) {
				http.Error(w, "invalid node token format", http.StatusUnauthorized)
				return
			}

			// Hash del token para buscarlo en BD
			tokenHash := platform.HashTokenApi(plainToken)

			// Buscar token en BD
			nodeToken, err := nodeTokenService.FindNodeTokenByHash(tokenHash)
			if err != nil {
				log.Printf("[NODE_AUTH] token not found: %v", err)
				http.Error(w, "invalid node token", http.StatusUnauthorized)
				return
			}

			// Verificar que no esté revocado
			if nodeToken.RevokedAt != nil {
				http.Error(w, "node token revoked", http.StatusUnauthorized)
				return
			}

			// Agregar información del token al contexto
			ctx := context.WithValue(r.Context(), nodeTokenKey, nodeToken)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetNodeToken obtiene el node token del contexto
func GetNodeToken(ctx context.Context) *nodetoken.NodeToken {
	if v := ctx.Value(nodeTokenKey); v != nil {
		if nt, ok := v.(*nodetoken.NodeToken); ok {
			return nt
		}
	}
	return nil
}
