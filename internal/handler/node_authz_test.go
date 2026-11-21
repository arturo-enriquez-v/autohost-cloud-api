// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

func TestRequireNodePerm(t *testing.T) {
	rbac := newOwnerRBAC("owner", "node-1")
	cases := []struct {
		name   string
		rbac   *identity.RBACService
		userID string
		path   string
		want   int
	}{
		{"owner allowed", rbac, "owner", "/nodes/node-1", http.StatusOK},
		{"stranger denied", rbac, "stranger", "/nodes/node-1", http.StatusForbidden},
		{"unknown node denied", rbac, "owner", "/nodes/node-404", http.StatusForbidden},
		{"nil rbac fails closed", nil, "owner", "/nodes/node-1", http.StatusForbidden},
		{"no claims", rbac, "", "/nodes/node-1", http.StatusUnauthorized},
		{"no node param", rbac, "owner", "/other", http.StatusBadRequest},
	}
	for _, tc := range cases {
		svc := tc.rbac
		r := chi.NewRouter()
		ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		guard := requireNodePerm(func() *identity.RBACService { return svc }, identity.PermFilesRead)
		r.With(guard).Get("/nodes/{nodeId}", ok)
		r.With(guard).Get("/other", ok)

		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.userID != "" {
			req = req.WithContext(middleware.ContextWithClaims(req.Context(), &middleware.Claims{UserID: tc.userID}))
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}
