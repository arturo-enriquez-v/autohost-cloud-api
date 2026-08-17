package handler

import (
	"bytes"
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
	h := NewServiceMonitorHandler(nil, nil)
	req := httptest.NewRequest("POST", "/service-monitors", bytes.NewReader([]byte("{}")))
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}
