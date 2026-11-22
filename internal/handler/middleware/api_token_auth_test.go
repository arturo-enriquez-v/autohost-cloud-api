// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	testReadToken  = "ahp_read"
	testWriteToken = "ahp_write"
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
			return &Claims{UserID: "u1", APITokenID: "t2", APITokenScope: "write"}, nil
		case "ahp_broken":
			return nil, errors.New("database down")
		}
		return nil, ErrAPITokenRejected
	})
	t.Cleanup(func() { SetAPITokenAuthenticator(nil) })
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
