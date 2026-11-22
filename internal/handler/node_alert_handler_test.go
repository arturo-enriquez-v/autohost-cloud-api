// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/go-chi/chi/v5"
)

type alertRepo struct{ alert *node.NodeAlert }

func (r *alertRepo) Upsert(a *node.NodeAlert) (*node.NodeAlert, error) { return a, nil }
func (r *alertRepo) FindByNodeID(string) (*node.NodeAlert, error)      { return r.alert, nil }
func (r *alertRepo) FindAllByOwnerID(string) ([]*node.NodeAlert, error) {
	return []*node.NodeAlert{r.alert}, nil
}

// The Telegram bot token controls the bot: users who can only read alerts must not receive it.
func TestNodeAlertHandler_BotTokenOnlyForManagers(t *testing.T) {
	owner := "user-1"
	nodeRepo := &MockNodeRepo{nodes: map[string]*node.Node{"node-123": {ID: "node-123", OwnerID: &owner}}}
	alerts := &alertRepo{alert: &node.NodeAlert{NodeID: "node-123", TelegramBotToken: "123:secret", TelegramChatID: "-100"}}

	rbacRepo := newMockRBACRepoHandler()
	rbacRepo.users[owner] = &identity.User{ID: owner, Role: identity.RoleMember, IsActive: true}
	rbacRepo.users["viewer"] = &identity.User{ID: "viewer", Role: identity.RoleMember, IsActive: true}
	rbacRepo.nodeOwners["node-123"] = &owner
	rbacRepo.nodePerms["viewer:node-123"] = &identity.NodePermissionGrant{
		UserID: "viewer", NodeID: "node-123", RolePreset: identity.NodePresetViewer,
		Permissions: identity.PresetViewerPermissions,
	}

	h := handler.NewNodeAlertHandler(node.NewNodeAlertService(alerts), node.NewNodeService(nodeRepo))
	h.SetRBACService(identity.NewRBACService(rbacRepo))
	r := chi.NewRouter()
	r.Mount("/v1/node-alerts", h.Routes())

	for _, tc := range []struct{ user, wantToken string }{
		{owner, "123:secret"},
		{"viewer", ""},
	} {
		t.Run(tc.user, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/node-alerts/node-123", nil)
			req.Header.Set("Authorization", "Bearer "+generateToken(tc.user, tc.user+"@example.com"))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			var got node.NodeAlert
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.TelegramBotToken != tc.wantToken {
				t.Errorf("telegram_bot_token = %q, want %q", got.TelegramBotToken, tc.wantToken)
			}
			if got.TelegramChatID != "-100" {
				t.Errorf("telegram_chat_id = %q, want -100", got.TelegramChatID)
			}
		})
	}
	if alerts.alert.TelegramBotToken != "123:secret" {
		t.Errorf("redaction modified the stored config")
	}
}
