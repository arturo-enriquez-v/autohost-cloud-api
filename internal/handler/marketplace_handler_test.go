// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

type mockMarketplaceRepo struct {
	created []*apps.MarketplaceApp
}

func (m *mockMarketplaceRepo) Create(app *apps.MarketplaceApp) (*apps.MarketplaceApp, error) {
	app.ID = "test-app-id-123"
	m.created = append(m.created, app)
	return app, nil
}

func (m *mockMarketplaceRepo) FindByNodeID(nodeID string) ([]*apps.MarketplaceApp, error) {
	return nil, nil
}

func (m *mockMarketplaceRepo) FindByJobID(jobID string) (*apps.MarketplaceApp, error) {
	return nil, nil
}

func (m *mockMarketplaceRepo) UpdateStatus(id string, status apps.AppStatus) error {
	return nil
}

func (m *mockMarketplaceRepo) UpdateJobID(id, jobID string) error {
	return nil
}

type mockJobRepo struct {
	created []*jobs.Job
}

func (m *mockJobRepo) Create(j *jobs.Job) (*jobs.Job, error) {
	j.ID = "job-id-1"
	m.created = append(m.created, j)
	return j, nil
}
func (m *mockJobRepo) FindByID(id string) (*jobs.Job, error) { return nil, nil }
func (m *mockJobRepo) FindByNodeID(nodeID string) ([]*jobs.Job, error) { return nil, nil }
func (m *mockJobRepo) FindPendingByNodeID(nodeID string, commandType jobs.CommandType) ([]*jobs.Job, error) {
	return nil, nil
}
func (m *mockJobRepo) UpdateStatus(id string, status jobs.JobStatus, output, errMsg string) error {
	return nil
}

type mockParamDispatcher struct {
	lastParams string
}

func (d *mockParamDispatcher) DispatchJob(nodeID, jobID, commandName string, commandType jobs.CommandType) error {
	return nil
}

func (d *mockParamDispatcher) DispatchJobWithParams(nodeID, jobID, commandName string, commandType jobs.CommandType, params string) error {
	d.lastParams = params
	return nil
}

func TestMarketplaceHandler_ListApps(t *testing.T) {
	h := NewMarketplaceHandler(nil, &mockMarketplaceRepo{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/marketplace/apps", nil)
	w := httptest.NewRecorder()

	h.ListApps(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	var catalog []apps.AppCatalogEntry
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("failed to decode catalog response: %v", err)
	}

	if len(catalog) == 0 {
		t.Fatalf("expected non-empty catalog")
	}
}

func TestMarketplaceHandler_DeployApp_AuthRequired(t *testing.T) {
	h := NewMarketplaceHandler(nil, &mockMarketplaceRepo{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/marketplace/apps/deploy", nil)
	w := httptest.NewRecorder()

	h.DeployApp(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", w.Code)
	}
}

func TestMarketplaceHandler_DeployApp_ValidatesCatalog(t *testing.T) {
	h := NewMarketplaceHandler(nil, &mockMarketplaceRepo{}, nil)

	body := []byte(`{"node_id": "node-1", "app_name": "unknown_app_xyz"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/marketplace/apps/deploy", bytes.NewReader(body))
	ctx := middleware.ContextWithClaims(req.Context(), &middleware.Claims{
		UserID: "user-1",
	})
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	h.DeployApp(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for unknown app, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMarketplaceHandler_DeployApp_SuccessSelfHosted(t *testing.T) {
	jobRepo := &mockJobRepo{}
	jobService := jobs.NewJobService(jobRepo)
	appRepo := &mockMarketplaceRepo{}
	disp := &mockParamDispatcher{}

	h := NewMarketplaceHandler(jobService, appRepo, disp)

	body := []byte(`{"node_id": "node-1", "app_name": "joplin", "env_vars": {"service-name": "joplin", "port": "22300"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/marketplace/apps/deploy", bytes.NewReader(body))
	ctx := middleware.ContextWithClaims(req.Context(), &middleware.Claims{
		UserID: "user-1",
	})
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	h.DeployApp(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	// Verify params sent to dispatcher contain the compose template loaded locally without R2
	var params InstallAppParams
	if err := json.Unmarshal([]byte(disp.lastParams), &params); err != nil {
		t.Fatalf("failed to unmarshal dispatched params: %v", err)
	}

	if params.AppName != "joplin" {
		t.Errorf("expected AppName 'joplin', got %q", params.AppName)
	}
	if len(params.Compose) == 0 {
		t.Errorf("expected non-empty compose template embedded/loaded locally")
	}
	if params.Env["port"] != "22300" {
		t.Errorf("expected port '22300', got %q", params.Env["port"])
	}
}
