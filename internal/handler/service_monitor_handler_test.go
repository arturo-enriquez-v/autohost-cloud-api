// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpsertMonitorRequest_UnmarshalAutoHealConfig(t *testing.T) {
	payload := []byte(`{
		"node_id": "cd0a1ca1-9c77-4737-8b9f-dc41d34c72e9",
		"service_name": "api-ventas",
		"check_type": "process",
		"http_url": "",
		"http_expected_status": 200,
		"tcp_host": "",
		"tcp_port": 0,
		"interval_seconds": 30,
		"failure_threshold": 3,
		"notify_telegram": false,
		"enabled": true,
		"auto_heal_config": {
			"enabled": true,
			"consecutive_failures_threshold": 3,
			"grace_period_seconds": 30,
			"restart": { "enabled": true, "max_attempts": 3, "window_seconds": 900 }
		}
	}`)

	var req upsertMonitorRequest
	if err := json.NewDecoder(bytes.NewReader(payload)).Decode(&req); err != nil {
		t.Fatalf("Failed to decode upsertMonitorRequest: %v", err)
	}

	if req.ServiceName != "api-ventas" {
		t.Errorf("expected service_name api-ventas, got %s", req.ServiceName)
	}

	if len(req.AutoHealConfig) == 0 {
		t.Fatal("expected AutoHealConfig to be populated, got empty")
	}

	var ah map[string]interface{}
	if err := json.Unmarshal(req.AutoHealConfig, &ah); err != nil {
		t.Fatalf("Failed to unmarshal AutoHealConfig: %v", err)
	}

	if ah["enabled"] != true {
		t.Errorf("expected auto_heal_config.enabled to be true, got %v", ah["enabled"])
	}
}

func TestCreateHandler_Unauthorized(t *testing.T) {
	h := NewServiceMonitorHandler(nil, nil, nil)
	req := httptest.NewRequest("POST", "/service-monitors", bytes.NewReader([]byte("{}")))
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

// Auto-heal "recreate" runs "docker compose up" as root on the node, so it requires terminal:exec and
// validated arguments.
func TestAuthorizeAutoHeal(t *testing.T) {
	h := NewServiceMonitorHandler(nil, nil, nil)
	h.SetRBACService(newOwnerRBAC("owner", "node-1"), nil)

	recreate := func(composePath, service string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"recreate": map[string]any{
			"enabled": true, "compose_file_path": composePath, "service_name": service,
		}})
		return b
	}
	tests := []struct {
		name, user string
		cfg        json.RawMessage
		want       int
	}{
		{"no auto-heal", "other", nil, http.StatusOK},
		{"restart only", "other", json.RawMessage(`{"enabled":true,"restart":{"enabled":true}}`), http.StatusOK},
		{"recreate with terminal access", "owner", recreate("/srv/app/compose.yml", "api"), http.StatusOK},
		{"recreate without terminal access", "other", recreate("/srv/app/compose.yml", "api"), http.StatusForbidden},
		{"relative compose path", "owner", recreate("compose.yml", "api"), http.StatusBadRequest},
		{"flag as service name", "owner", recreate("/srv/app/compose.yml", "--project-directory=/"), http.StatusBadRequest},
		{"malformed config", "owner", json.RawMessage(`{"recreate":"yes"}`), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, msg := h.authorizeAutoHeal(context.Background(), tt.user, "node-1", tt.cfg); got != tt.want {
				t.Errorf("status = %d (%s), want %d", got, msg, tt.want)
			}
		})
	}
}
