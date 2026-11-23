// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

type fakeAPITokenService struct {
	createErr  error
	revokedFor string
}

func (f *fakeAPITokenService) Create(_ context.Context, _ string, _ identity.CreateAPITokenInput) (*identity.CreatedAPIToken, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &identity.CreatedAPIToken{Token: "ahp_raw", APIToken: &identity.APIToken{ID: "t1", TokenHash: "secret-hash"}}, nil
}

func (f *fakeAPITokenService) List(context.Context, string) ([]*identity.APIToken, error) {
	return []*identity.APIToken{{ID: "t1", UserID: "u1", TokenHash: "secret-hash"}}, nil
}

func (f *fakeAPITokenService) Revoke(_ context.Context, userID, _ string) error {
	if userID != "u1" {
		return identity.ErrAPITokenNotFound
	}
	f.revokedFor = userID
	return nil
}

func serveAPITokens(h *APITokenHandler, method, path, body string, claims *middleware.Claims) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	// Routes() starts with Auth; inject the claims directly and mount the handlers behind RequireSession.
	req = req.WithContext(middleware.ContextWithClaims(req.Context(), claims))
	handler := middleware.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case method == http.MethodGet:
			h.List(w, r)
		case method == http.MethodPost:
			h.Create(w, r)
		default:
			h.Revoke(w, r)
		}
	}))
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAPITokenRoutesRequireSession(t *testing.T) {
	h := &APITokenHandler{svc: &fakeAPITokenService{}}
	routes := h.Routes()
	if n := len(routes.Middlewares()); n != 2 {
		t.Fatalf("api token routes must use Auth and RequireSession, got %d middlewares", n)
	}

	viaToken := &middleware.Claims{UserID: "u1", APITokenID: "t9", APITokenScope: "write"}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if rec := serveAPITokens(h, m, "/", `{"name":"x","scope":"write"}`, viaToken); rec.Code != http.StatusForbidden {
			t.Errorf("%s with an api token: got %d, want 403", m, rec.Code)
		}
	}
}

func TestAPITokenHandlerNeverReturnsHash(t *testing.T) {
	h := &APITokenHandler{svc: &fakeAPITokenService{}}
	session := &middleware.Claims{UserID: "u1"}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		rec := serveAPITokens(h, m, "/", `{"name":"x","scope":"read"}`, session)
		if rec.Code >= 300 {
			t.Fatalf("%s: got %d", m, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "secret-hash") || strings.Contains(rec.Body.String(), `"user_id"`) {
			t.Errorf("%s leaks internal fields: %s", m, rec.Body.String())
		}
	}
}

func TestAPITokenCreateErrors(t *testing.T) {
	cases := []struct {
		err  error
		body string
		want int
	}{
		{nil, `not json`, http.StatusBadRequest},
		{identity.ErrAPITokenInvalidScope, `{}`, http.StatusBadRequest},
		{identity.ErrAPITokenInvalidTTL, `{}`, http.StatusBadRequest},
		{identity.ErrAPITokenLimitReached, `{}`, http.StatusConflict},
		{context.DeadlineExceeded, `{}`, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		h := &APITokenHandler{svc: &fakeAPITokenService{createErr: tc.err}}
		rec := serveAPITokens(h, http.MethodPost, "/", tc.body, &middleware.Claims{UserID: "u1"})
		if rec.Code != tc.want {
			t.Errorf("%v: got %d, want %d", tc.err, rec.Code, tc.want)
		}
		if tc.want == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "deadline") {
			t.Errorf("internal error leaked to the client: %s", rec.Body.String())
		}
	}
}

func TestAPITokenRevokeOtherUsersTokenIs404(t *testing.T) {
	svc := &fakeAPITokenService{}
	h := &APITokenHandler{svc: svc}
	if rec := serveAPITokens(h, http.MethodDelete, "/t1", "", &middleware.Claims{UserID: "intruder"}); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
	if rec := serveAPITokens(h, http.MethodDelete, "/t1", "", &middleware.Claims{UserID: "u1"}); rec.Code != http.StatusNoContent {
		t.Fatalf("owner revoke: got %d, want 204", rec.Code)
	}
}

func TestRequireNodePermReadOnlyToken(t *testing.T) {
	rbac := newOwnerRBAC("u1", "node-1")
	readToken := &middleware.Claims{UserID: "u1", APITokenID: "t1", APITokenScope: "read"}
	writeToken := &middleware.Claims{UserID: "u1", APITokenID: "t2", APITokenScope: "write"}

	cases := []struct {
		claims *middleware.Claims
		perm   string
		want   int
	}{
		{readToken, identity.PermContainersRead, http.StatusOK},
		{readToken, identity.PermTerminalExec, http.StatusForbidden},
		{readToken, identity.PermContainersAction, http.StatusForbidden},
		{writeToken, identity.PermTerminalExec, http.StatusOK},
	}
	for _, tc := range cases {
		mw := requireNodePerm(func() *identity.RBACService { return rbac }, tc.perm)
		req := httptest.NewRequest(http.MethodGet, "/v1/nodes/node-1", nil)
		req = withNodeID(req.WithContext(middleware.ContextWithClaims(req.Context(), tc.claims)), "node-1")
		rec := httptest.NewRecorder()
		mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("scope %s, perm %s: got %d, want %d", tc.claims.APITokenScope, tc.perm, rec.Code, tc.want)
		}
	}
}

func TestRequireNodePermCustomToken(t *testing.T) {
	rbac := newOwnerRBAC("u1", "node-1")
	claims := &middleware.Claims{UserID: "u1", APITokenID: "t3", APITokenScope: "custom"}
	tokenPerms := identity.NormalizePermissions([]string{identity.PermFilesWrite})

	cases := []struct {
		perm string
		want int
	}{
		{identity.PermFilesRead, http.StatusOK},
		{identity.PermFilesWrite, http.StatusOK},
		{identity.PermFilesDelete, http.StatusForbidden},
		{identity.PermTerminalExec, http.StatusForbidden},
		{identity.PermContainersRead, http.StatusForbidden},
	}
	for _, tc := range cases {
		mw := requireNodePerm(func() *identity.RBACService { return rbac }, tc.perm)
		req := httptest.NewRequest(http.MethodPost, "/v1/nodes/node-1/files/upload", nil)
		ctx := identity.WithTokenPermissions(middleware.ContextWithClaims(req.Context(), claims), tokenPerms)
		req = withNodeID(req.WithContext(ctx), "node-1")
		rec := httptest.NewRecorder()
		mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("files:write token, perm %s on an owned node: got %d, want %d", tc.perm, rec.Code, tc.want)
		}
	}
}
