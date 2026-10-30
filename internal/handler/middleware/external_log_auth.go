// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
)

type externalLogCtxKey int

const tokenKey externalLogCtxKey = iota

func ExternalLogAuth(service *node.ExternalLogService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var raw string

			if authz := r.Header.Get("Authorization"); authz != "" {
				parts := strings.SplitN(authz, " ", 2)
				if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
					raw = parts[1]
				} else {
					raw = authz
				}
			} else if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
				raw = apiKey
			} else if apiKey := r.Header.Get("x-api-key"); apiKey != "" {
				raw = apiKey
			}

			if raw == "" {
				http.Error(w, `{"error":"missing authorization token or X-API-Key header"}`, http.StatusUnauthorized)
				return
			}

			token, err := service.AuthenticateToken(r.Context(), raw)
			if err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), tokenKey, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetExternalLogToken(ctx context.Context) *node.ExternalLogToken {
	if v := ctx.Value(tokenKey); v != nil {
		if t, ok := v.(*node.ExternalLogToken); ok {
			return t
		}
	}
	return nil
}
