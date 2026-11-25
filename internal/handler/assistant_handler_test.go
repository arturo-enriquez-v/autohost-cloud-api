// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/api"
	"github.com/arturo/autohost-cloud-api/internal/core/assistant"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/mcpserver"
)

type asstSettingsRepo struct{ s *assistant.Settings }

func (m *asstSettingsRepo) Get(context.Context) (*assistant.Settings, error) {
	if m.s == nil {
		return nil, nil
	}
	cp := *m.s
	return &cp, nil
}

func (m *asstSettingsRepo) Save(_ context.Context, s *assistant.Settings, _ string) (*assistant.Settings, error) {
	cp := *s
	m.s = &cp
	out := cp
	return &out, nil
}

// asstRBACRepo: "admin" is a global admin, everyone else a member.
type asstRBACRepo struct{ identity.RBACRepository }

func (asstRBACRepo) GetUserByID(_ context.Context, id string) (*identity.User, error) {
	role := identity.RoleMember
	if id == "admin" {
		role = identity.RoleAdmin
	}
	return &identity.User{ID: id, Role: role, IsActive: true}, nil
}

func newAssistantRouter(t *testing.T, settings *assistant.Settings) http.Handler {
	t.Helper()
	idx, err := mcpserver.LoadIndex(api.OpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	svc := assistant.NewServiceWith(&asstSettingsRepo{s: settings}, nil, idx, http.DefaultClient)
	h := handler.NewAssistantHandler(svc)
	h.SetRBACService(identity.NewRBACService(asstRBACRepo{}))
	r := chi.NewRouter()
	r.Mount("/v1/assistant", h.Routes())
	h.SetAPI(r)
	return r
}

func asstDo(r http.Handler, user, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+generateToken(user, user+"@example.com"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAssistantSettingsAdminOnly(t *testing.T) {
	r := newAssistantRouter(t, nil)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/assistant/settings", ""},
		{http.MethodPut, "/v1/assistant/settings", `{"enabled":false}`},
		{http.MethodGet, "/v1/assistant/models", ""},
	} {
		if rec := asstDo(r, "member", c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("member %s %s: %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if rec := asstDo(r, "admin", http.MethodGet, "/v1/assistant/settings", ""); rec.Code != http.StatusOK {
		t.Fatalf("admin get: %d %s", rec.Code, rec.Body.String())
	}
	if rec := asstDo(r, "member", http.MethodGet, "/v1/assistant/status", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("member status: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAssistantChatDisabledIsJSON503(t *testing.T) {
	r := newAssistantRouter(t, &assistant.Settings{Enabled: false})
	rec := asstDo(r, "member", http.MethodPost, "/v1/assistant/chat", `{"messages":[{"role":"user","content":"hola"}]}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Fatalf("got %d %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestAssistantChatStreams(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hola"}}]}`+"\n\n"+"data: [DONE]\n\n")
	}))
	defer provider.Close()
	r := newAssistantRouter(t, &assistant.Settings{Enabled: true, BaseURL: provider.URL + "/v1", DefaultModel: "m"})
	rec := asstDo(r, "member", http.MethodPost, "/v1/assistant/chat", `{"messages":[{"role":"user","content":"hola"}]}`)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(body, "event: delta") || !strings.Contains(body, "event: done") {
		t.Fatalf("got %d %q:\n%s", rec.Code, rec.Header().Get("Content-Type"), body)
	}
	if rec := asstDo(r, "member", http.MethodPost, "/v1/assistant/chat", `{"messages":[{"role":"system","content":"x"}]}`); !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("invalid history: %s", rec.Body.String())
	}
}
