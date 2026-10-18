// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import "net/http"

func RequireUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims := GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	return claims.UserID, true
}
