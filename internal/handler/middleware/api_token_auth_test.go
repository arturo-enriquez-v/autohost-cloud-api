// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
)

const (
	testReadToken   = "ahp_read"
	testWriteToken  = "ahp_write"
	testCustomToken = "ahp_custom"
)

// withTestAPITokens installs an authenticator that knows one read and one write token.
func withTestAPITokens(t *testing.T) {
	t.Helper()
	jwtSecret = []byte("test-secret")
	SetAPITokenAuthenticator(func(_ context.Context, raw string) (*Claims, error) {
		switch raw {
		case testReadToken:
			return &Claims{UserID: "u1", APITokenID: "t1", APITokenScope: "read"}, nil
		case testWriteToken:
			return &Claims{UserID: "u1", APITokenID: "t2", APITokenScope: "write", APITokenPermissions: []string{"*"}}, nil
		case testCustomToken:
			return &Claims{UserID: "u1", APITokenID: "t3", APITokenScope: "custom", APITokenPermissions: []string{"node:read", "files:read", "files:write"}}, nil
		case "ahp_broken":
			return nil, errors.New("database down")
		}
		return nil, ErrAPITokenRejected
	})
	// The node permission route set as the server would compute it: only the upload route is guarded.
	root := chi.NewRouter()
	root.Post("/v1/nodes/{nodeId}/files/upload", func(http.ResponseWriter, *http.Request) {})
	root.Get("/v1/nodes/{nodeId}/terminal", func(http.ResponseWriter, *http.Request) {})
	root.Post("/v1/projects", func(http.ResponseWriter, *http.Request) {})
	SetNodePermRoutes(root, map[string]bool{
		RouteKey(http.MethodPost, "/v1/nodes/{nodeId}/files/upload"): true,
		RouteKey(http.MethodGet, "/v1/nodes/{nodeId}/terminal"):      true,
	})
	t.Cleanup(func() {
		SetAPITokenAuthenticator(nil)
		nodePermGate.Store(nil)
	})
}

func serveAuth(req *http.Request, next http.Handler) *httptest.ResponseRecorder {
	if next == nil {
		next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if GetClaims(r.Context()) == nil {
				w.WriteHeader(http.StatusTeapot)
			}
		})
	}
	rec := httptest.NewRecorder()
	Auth(next).ServeHTTP(rec, req)
	return rec
}

func bearer(method, token string) *http.Request {
	req := httptest.NewRequest(method, "/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestAPITokenReadScope(t *testing.T) {
	withTestAPITokens(t)

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if rec := serveAuth(bearer(m, testReadToken), nil); rec.Code != http.StatusOK {
			t.Errorf("read token %s: got %d, want 200", m, rec.Code)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if rec := serveAuth(bearer(m, testReadToken), nil); rec.Code != http.StatusForbidden {
			t.Errorf("read token %s: got %d, want 403", m, rec.Code)
		}
	}

	// The terminal is a GET upgraded to a WebSocket shell: a read token must not open it.
	ws := bearer(http.MethodGet, testReadToken)
	ws.Header.Set("Connection", "Upgrade")
	ws.Header.Set("Upgrade", "websocket")
	if rec := serveAuth(ws, nil); rec.Code != http.StatusForbidden {
		t.Errorf("read token websocket upgrade: got %d, want 403", rec.Code)
	}
}

func TestAPITokenWriteScope(t *testing.T) {
	withTestAPITokens(t)
	var got *Claims
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = GetClaims(r.Context()) })

	if rec := serveAuth(bearer(http.MethodPost, testWriteToken), next); rec.Code != http.StatusOK {
		t.Fatalf("write token POST: got %d, want 200", rec.Code)
	}
	if got == nil || got.UserID != "u1" || !got.ViaAPIToken() || got.ReadOnly() {
		t.Fatalf("unexpected claims %+v", got)
	}
}

func TestAPITokenRejections(t *testing.T) {
	withTestAPITokens(t)

	if rec := serveAuth(bearer(http.MethodGet, "ahp_unknown"), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown token: got %d, want 401", rec.Code)
	}
	if rec := serveAuth(bearer(http.MethodGet, "ahp_broken"), nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("lookup failure: got %d, want 500", rec.Code)
	}
	// Tokens in query strings end up in access logs: only the Authorization header is accepted.
	for _, q := range []string{"token", "access_token"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/nodes?"+q+"="+testWriteToken, nil)
		if rec := serveAuth(req, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("token in ?%s=: got %d, want 401", q, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/nodes", nil)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: testWriteToken})
	if rec := serveAuth(req, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("token in cookie: got %d, want 401", rec.Code)
	}
}

func TestAPITokenRejectedWithoutAuthenticator(t *testing.T) {
	jwtSecret = []byte("test-secret")
	SetAPITokenAuthenticator(nil)
	if rec := serveAuth(bearer(http.MethodGet, testWriteToken), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestRequireSession(t *testing.T) {
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	cases := []struct {
		name   string
		claims *Claims
		want   int
	}{
		{"session", &Claims{UserID: "u1"}, http.StatusOK},
		{"api token", &Claims{UserID: "u1", APITokenID: "t1", APITokenScope: "write"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/v1/api-tokens", nil)
		req = req.WithContext(ContextWithClaims(req.Context(), tc.claims))
		rec := httptest.NewRecorder()
		RequireSession(ok).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestAPITokenCustomScope(t *testing.T) {
	withTestAPITokens(t)
	req := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+testCustomToken)
		return r
	}

	cases := []struct {
		name string
		req  *http.Request
		want int
	}{
		{"read anywhere", req(http.MethodGet, "/v1/projects"), http.StatusOK},
		{"write on a node route", req(http.MethodPost, "/v1/nodes/n1/files/upload"), http.StatusOK},
		{"write on a non-node route", req(http.MethodPost, "/v1/projects"), http.StatusForbidden},
		{"write on an unknown route", req(http.MethodDelete, "/v1/whatever"), http.StatusForbidden},
	}
	for _, tc := range cases {
		if rec := serveAuth(tc.req, nil); rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.want)
		}
	}

	// A WebSocket upgrade is not a plain read: it is only allowed through a node route, where
	// requireNodePerm (terminal:exec) decides.
	ws := req(http.MethodGet, "/v1/projects")
	ws.Header.Set("Upgrade", "websocket")
	if rec := serveAuth(ws, nil); rec.Code != http.StatusForbidden {
		t.Errorf("upgrade on a non-node route: got %d, want 403", rec.Code)
	}
	ws = req(http.MethodGet, "/v1/nodes/n1/terminal")
	ws.Header.Set("Upgrade", "websocket")
	if rec := serveAuth(ws, nil); rec.Code != http.StatusOK {
		t.Errorf("upgrade on a node route: got %d, want 200 (left to requireNodePerm)", rec.Code)
	}
}

func TestAPITokenLimitsNodePermissionsInContext(t *testing.T) {
	withTestAPITokens(t)
	rbac := identity.NewRBACService(adminOnlyRBACRepo{})
	var canWrite, canExec bool
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		canWrite, _ = rbac.HasNodePermission(r.Context(), "u1", "n1", identity.PermFilesWrite)
		canExec, _ = rbac.HasNodePermission(r.Context(), "u1", "n1", identity.PermTerminalExec)
	})
	serveAuth(bearer(http.MethodGet, testCustomToken), next)
	if !canWrite || canExec {
		t.Fatalf("custom token on an admin: files:write=%v terminal:exec=%v, want true/false", canWrite, canExec)
	}

	serveAuth(bearer(http.MethodGet, testWriteToken), next)
	if !canWrite || !canExec {
		t.Fatalf("write token on an admin: files:write=%v terminal:exec=%v, want true/true", canWrite, canExec)
	}
}

// adminOnlyRBACRepo makes every user a global admin.
type adminOnlyRBACRepo struct{ identity.RBACRepository }

func (adminOnlyRBACRepo) GetUserByID(_ context.Context, id string) (*identity.User, error) {
	return &identity.User{ID: id, Role: identity.RoleAdmin, IsActive: true}, nil
}

func TestAPITokenWithoutPermissionsGetsNone(t *testing.T) {
	// Fail closed: claims from a token that carry no permission list grant nothing on nodes.
	jwtSecret = []byte("test-secret")
	SetAPITokenAuthenticator(func(context.Context, string) (*Claims, error) {
		return &Claims{UserID: "u1", APITokenID: "t1", APITokenScope: "custom"}, nil
	})
	t.Cleanup(func() { SetAPITokenAuthenticator(nil) })

	rbac := identity.NewRBACService(adminOnlyRBACRepo{})
	var canRead bool
	serveAuth(bearer(http.MethodGet, "ahp_x"), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		canRead, _ = rbac.HasNodePermission(r.Context(), "u1", "n1", identity.PermNodeRead)
	}))
	if canRead {
		t.Fatal("a token without permissions must not get node:read")
	}
}
