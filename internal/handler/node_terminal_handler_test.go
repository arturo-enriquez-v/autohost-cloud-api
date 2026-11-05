// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

func TestNodeTerminalRoutes_Routing(t *testing.T) {
	h := handler.NewNodeTerminalHandler(nil, nil, nil)

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Mount("/node-terminal", h.Routes())
		r.Mount("/nodes/{nodeId}/terminal", h.NodeScopedRoutes())
	})

	// Create valid JWT token using testSecret from bola_isolation_test.go
	tokenObj := jwt.NewWithClaims(jwt.SigningMethodHS256, &middleware.Claims{
		UserID: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	tokenStr, err := tokenObj.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	// Test GET /v1/nodes/node-123/terminal?token=...
	reqAuth := httptest.NewRequest(http.MethodGet, "/v1/nodes/node-123/terminal?token="+tokenStr, nil)
	wAuth := httptest.NewRecorder()
	r.ServeHTTP(wAuth, reqAuth)
	t.Logf("/v1/nodes/node-123/terminal with query token -> status %d (body: %s)", wAuth.Code, wAuth.Body.String())
}
