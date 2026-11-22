// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

// A monitor's history and uptime are only visible to users who can read monitors on its node.
func TestServiceMonitorHandler_HistoryRequiresAccess(t *testing.T) {
	owner := "user-1"
	nodeRepo := &MockNodeRepo{nodes: map[string]*node.Node{"node-123": {ID: "node-123", OwnerID: &owner}}}
	h := handler.NewServiceMonitorHandler(apps.NewServiceMonitorService(monitorRepo{}), nil)
	h.SetRBACService(ownerRBAC(), node.NewNodeService(nodeRepo))
	r := chi.NewRouter()
	r.Mount("/v1/service-monitors", h.Routes())

	for _, path := range []string{"history", "uptime"} {
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
