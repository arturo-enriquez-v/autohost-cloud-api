// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/go-chi/chi/v5"
)

// monitorRepo holds one monitor on node-123.
type monitorRepo struct{}

func (monitorRepo) FindByID(id string) (*apps.ServiceMonitor, error) {
	if id != "mon-1" {
		return nil, apps.ErrServiceMonitorNotFound
	}
	return &apps.ServiceMonitor{ID: "mon-1", NodeID: "node-123", UserID: "user-1"}, nil
}
func (monitorRepo) Create(m *apps.ServiceMonitor) (*apps.ServiceMonitor, error) { return m, nil }
func (monitorRepo) Update(m *apps.ServiceMonitor) (*apps.ServiceMonitor, error) { return m, nil }
func (monitorRepo) Delete(string) error                                         { return nil }
func (monitorRepo) ListByNodeID(string) ([]*apps.ServiceMonitor, error)         { return nil, nil }
func (monitorRepo) UpdateStatus(string, string, int) error                      { return nil }
func (monitorRepo) UpdateAutoHealState(string, json.RawMessage) error           { return nil }
func (monitorRepo) InsertEvent(*apps.ServiceHealthEvent) error                  { return nil }
func (monitorRepo) ListEventsByMonitorID(string, int) ([]*apps.ServiceHealthEvent, error) {
	return []*apps.ServiceHealthEvent{}, nil
}
func (monitorRepo) UptimeHistory24h(string) ([]*apps.UptimePoint, error) {
	return []*apps.UptimePoint{}, nil
}
func (monitorRepo) PurgeEventsBefore(context.Context, time.Time) (int64, error) { return 0, nil }
func (monitorRepo) InsertAutoHealEvent(*apps.AutoHealEvent) error               { return nil }
func (monitorRepo) ListAutoHealEvents(string, time.Time, int) ([]*apps.AutoHealEvent, error) {
	return nil, nil
}

// statsRepo has no health events.
type statsRepo struct{}

func (statsRepo) StatsBuckets(string, time.Time, time.Duration) ([]*apps.MonitorStatsBucket, error) {
	return nil, nil
}
func (statsRepo) StatsSummary(string, time.Time) (*apps.MonitorStatsSummary, error) {
	return &apps.MonitorStatsSummary{}, nil
}
func (statsRepo) Incidents(string, time.Time, int, int) ([]*apps.MonitorIncident, error) {
	return nil, nil
}

// A monitor's history, uptime, stats and incidents are only visible to users who can read monitors on its node.
func TestServiceMonitorHandler_HistoryRequiresAccess(t *testing.T) {
	owner := "user-1"
	nodeRepo := &MockNodeRepo{nodes: map[string]*node.Node{"node-123": {ID: "node-123", OwnerID: &owner}}}
	h := handler.NewServiceMonitorHandler(apps.NewServiceMonitorService(monitorRepo{}), apps.NewMonitorStatsService(statsRepo{}), nil)
	h.SetRBACService(ownerRBAC(), node.NewNodeService(nodeRepo))
	r := chi.NewRouter()
	r.Mount("/v1/service-monitors", h.Routes())

	for _, path := range []string{"history", "uptime", "stats", "incidents", "autoheal-events"} {
		for _, tc := range []struct {
			user string
			want int
		}{{owner, http.StatusOK}, {"user-2", http.StatusNotFound}} {
			req := httptest.NewRequest(http.MethodGet, "/v1/service-monitors/mon-1/"+path, nil)
			req.Header.Set("Authorization", "Bearer "+generateToken(tc.user, tc.user+"@example.com"))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s as %s: status = %d, want %d (body: %s)", path, tc.user, rec.Code, tc.want, rec.Body.String())
			}
		}
	}
}

// storedMonitorRepo returns one monitor on node-123 with auto-heal enabled and echoes updates back.
type storedMonitorRepo struct {
	monitorRepo
	state json.RawMessage
}

func (r storedMonitorRepo) FindByID(id string) (*apps.ServiceMonitor, error) {
	if id != "mon-1" {
		return nil, apps.ErrServiceMonitorNotFound
	}
	return &apps.ServiceMonitor{
		ID: "mon-1", NodeID: "node-123", UserID: "user-1", ServiceName: "web", CheckType: "process",
		AutoHealConfig: json.RawMessage(`{"enabled": true, "restart": {"enabled": true, "max_attempts": 3}}`),
		AutoHealState:  r.state,
	}, nil
}

func newMonitorRouter(repo apps.ServiceMonitorRepository) chi.Router {
	owner := "user-1"
	nodeRepo := &MockNodeRepo{nodes: map[string]*node.Node{"node-123": {ID: "node-123", OwnerID: &owner}}}
	h := handler.NewServiceMonitorHandler(apps.NewServiceMonitorService(repo), apps.NewMonitorStatsService(statsRepo{}), nil)
	h.SetRBACService(ownerRBAC(), node.NewNodeService(nodeRepo))
	r := chi.NewRouter()
	r.Mount("/v1/service-monitors", h.Routes())
	return r
}

func serveMonitorRequest(r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+generateToken("user-1", "user-1@example.com"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// Only the agent writes the auto-heal state: a client sending an old copy must not reset the attempts
// counted by the agent. A new auto-heal config starts from a clean state.
func TestServiceMonitorHandler_UpdateKeepsAgentState(t *testing.T) {
	agentState := json.RawMessage(`{"status":"CRITICAL_FAILED","recreate_attempts":1}`)
	r := newMonitorRouter(storedMonitorRepo{state: agentState})

	tests := []struct {
		name, config, wantState string
	}{
		{"same config", `{"restart":{"max_attempts":3,"enabled":true},"enabled":true}`, string(agentState)},
		{"config omitted", ``, string(agentState)},
		{"new config", `{"enabled":true,"restart":{"enabled":true,"max_attempts":5}}`, `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"service_name":"web","check_type":"process","enabled":true,` +
				`"auto_heal_state":{"status":"HEALTHY","recreate_attempts":0}`
			if tt.config != "" {
				body += `,"auto_heal_config":` + tt.config
			}
			rec := serveMonitorRequest(r, http.MethodPut, "/v1/service-monitors/mon-1", body+"}")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
			}
			var got apps.ServiceMonitor
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if string(got.AutoHealState) != tt.wantState {
				t.Errorf("auto_heal_state = %s, want %s", got.AutoHealState, tt.wantState)
			}
		})
	}
}

// Invalid settings are rejected with a JSON error the web client can show.
func TestServiceMonitorHandler_ValidatesBody(t *testing.T) {
	r := newMonitorRouter(storedMonitorRepo{state: json.RawMessage(`{}`)})

	tests := []struct {
		name, method, path, body, wantErr string
	}{
		{"unknown check type", http.MethodPost, "/v1/service-monitors",
			`{"node_id":"node-123","service_name":"web","check_type":"ping"}`, "check_type must be process, tcp or http"},
		{"interval too short", http.MethodPost, "/v1/service-monitors",
			`{"node_id":"node-123","service_name":"web","check_type":"process","interval_seconds":1}`, "interval_seconds must be between 5 and 3600"},
		{"http without url", http.MethodPost, "/v1/service-monitors",
			`{"node_id":"node-123","service_name":"site","check_type":"http"}`, "http_url must be an http:// or https:// URL"},
		{"tcp without port", http.MethodPost, "/v1/service-monitors",
			`{"node_id":"node-123","service_name":"db","check_type":"tcp","tcp_host":"localhost"}`, "tcp_port must be between 1 and 65535"},
		{"container name as flag", http.MethodPost, "/v1/service-monitors",
			`{"node_id":"node-123","service_name":"-t","check_type":"process"}`, "service_name must be a container name for process checks"},
		{"auto-heal needs a container", http.MethodPut, "/v1/service-monitors/mon-1",
			`{"service_name":"Public API","check_type":"http","http_url":"https://example.com"}`, "auto-heal needs a container: set auto_heal_config.container"},
		{"invalid auto-heal container", http.MethodPut, "/v1/service-monitors/mon-1",
			`{"service_name":"Public API","check_type":"http","http_url":"https://example.com","auto_heal_config":{"enabled":true,"container":"-t","restart":{"enabled":true}}}`, "auto_heal_config.container must be a container name"},
		{"empty update", http.MethodPut, "/v1/service-monitors/mon-1", `{}`, "service_name is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveMonitorRequest(r, tt.method, tt.path, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
			var got struct{ Error string }
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Error != tt.wantErr {
				t.Errorf("body = %s, want error %q", rec.Body.String(), tt.wantErr)
			}
		})
	}

	// An HTTP monitor named with a label can auto-heal the container set in its config.
	rec := serveMonitorRequest(r, http.MethodPut, "/v1/service-monitors/mon-1",
		`{"service_name":"Public API","check_type":"http","http_url":"https://example.com",`+
			`"auto_heal_config":{"enabled":true,"container":"shop-web-1","restart":{"enabled":true}}}`)
	if rec.Code != http.StatusOK {
		t.Errorf("label with auto-heal container: status %d, body %s", rec.Code, rec.Body.String())
	}

	// Omitted settings get their defaults on update too.
	rec = serveMonitorRequest(r, http.MethodPut, "/v1/service-monitors/mon-1", `{"service_name":"web","check_type":"process"}`)
	var got apps.ServiceMonitor
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.IntervalSeconds != 30 || got.FailureThreshold != 3 {
		t.Errorf("defaults not applied: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// pausedMonitorRepo holds a disabled monitor.
type pausedMonitorRepo struct{ monitorRepo }

func (pausedMonitorRepo) FindByID(string) (*apps.ServiceMonitor, error) {
	return &apps.ServiceMonitor{ID: "mon-1", NodeID: "node-123", UserID: "user-1", Enabled: false}, nil
}

// "Check now" needs the agent: a paused monitor or an offline node answer 409 with the reason.
func TestServiceMonitorHandler_CheckNow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		repo    apps.ServiceMonitorRepository
		wantErr string
	}{
		{"paused", pausedMonitorRepo{}, "monitor is paused"},
		{"offline", enabledRepo{storedMonitorRepo{state: json.RawMessage(`{}`)}}, "node is offline"},
	} {
		rec := serveMonitorRequest(newMonitorRouter(tc.repo), http.MethodPost, "/v1/service-monitors/mon-1/check", "")
		var got struct{ Error string }
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusConflict || got.Error != tc.wantErr {
			t.Errorf("%s: status %d body %s, want 409 %q", tc.name, rec.Code, rec.Body.String(), tc.wantErr)
		}
	}
}

// enabledRepo is storedMonitorRepo with its monitor enabled.
type enabledRepo struct{ storedMonitorRepo }

func (r enabledRepo) FindByID(id string) (*apps.ServiceMonitor, error) {
	m, err := r.storedMonitorRepo.FindByID(id)
	if m != nil {
		m.Enabled = true
	}
	return m, err
}

func TestServiceMonitorHandler_StatsRange(t *testing.T) {
	r := newMonitorRouter(storedMonitorRepo{state: json.RawMessage(`{}`)})
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v1/service-monitors/mon-1/stats", http.StatusOK},
		{"/v1/service-monitors/mon-1/stats?range=30d", http.StatusOK},
		{"/v1/service-monitors/mon-1/stats?range=1y", http.StatusBadRequest},
		{"/v1/service-monitors/mon-1/incidents?range=7d", http.StatusOK},
		{"/v1/service-monitors/mon-1/incidents?range=2h", http.StatusBadRequest},
		{"/v1/service-monitors/mon-1/autoheal-events?range=30d", http.StatusOK},
		{"/v1/service-monitors/mon-1/autoheal-events?range=1h", http.StatusBadRequest},
	} {
		if rec := serveMonitorRequest(r, http.MethodGet, tc.path, ""); rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d (body %s)", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}
