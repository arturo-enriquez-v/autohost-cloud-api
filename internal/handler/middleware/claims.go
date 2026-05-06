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
