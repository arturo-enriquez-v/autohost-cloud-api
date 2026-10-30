// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

type mockSettingRepo struct {
	locks map[string]bool
}

func (m *mockSettingRepo) GetByNodeID(ctx context.Context, nodeID string) (*node.ContainerSetting, error) {
	return &node.ContainerSetting{
		NodeID:           nodeID,
		SortOrder:        []string{},
		LockedContainers: m.locks,
	}, nil
}

func (m *mockSettingRepo) Upsert(ctx context.Context, s *node.ContainerSetting) (*node.ContainerSetting, error) {
	m.locks = s.LockedContainers
	return s, nil
}

func (m *mockSettingRepo) IsContainerLocked(ctx context.Context, nodeID, containerName string) (bool, error) {
	return m.locks[containerName], nil
}

type dummyDispatcher struct{}

func (d *dummyDispatcher) DispatchJob(nodeID, jobID, commandName string, commandType jobs.CommandType) error {
	return nil
}

func TestJobHandler_Dispatch_LockedContainer(t *testing.T) {
	repo := &mockSettingRepo{
		locks: map[string]bool{
			"production-db": true,
		},
	}
	settingSvc := node.NewContainerSettingService(repo)
	h := handler.NewJobHandler(nil, &dummyDispatcher{}, settingSvc)

	body, _ := json.Marshal(map[string]string{
		"node_id":      "node-123",
		"command_name": "container.stop.production-db",
		"command_type": "custom",
	})

	req := httptest.NewRequest("POST", "/jobs", bytes.NewReader(body))
	req = req.WithContext(middleware.ContextWithClaims(req.Context(), &middleware.Claims{
		UserID: "user-1",
	}))

	rec := httptest.NewRecorder()
	h.Dispatch(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 Forbidden, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	expectedMsg := "action blocked: container 'production-db' is locked\n"
	if rec.Body.String() != expectedMsg {
		t.Fatalf("expected body %q, got %q", expectedMsg, rec.Body.String())
	}
}

func TestJobHandler_Dispatch_UnlockedContainer_NotForbidden(t *testing.T) {
	repo := &mockSettingRepo{
		locks: map[string]bool{
			"production-db": true,
		},
	}
	settingSvc := node.NewContainerSettingService(repo)
	// jobService is nil so it would panic/fail when calling jobService.Dispatch, but it must pass the lock check first
	h := handler.NewJobHandler(nil, &dummyDispatcher{}, settingSvc)

	body, _ := json.Marshal(map[string]string{
		"node_id":      "node-123",
		"command_name": "container.stop.other-container",
		"command_type": "custom",
	})

	req := httptest.NewRequest("POST", "/jobs", bytes.NewReader(body))
	req = req.WithContext(middleware.ContextWithClaims(req.Context(), &middleware.Claims{
		UserID: "user-1",
	}))

	rec := httptest.NewRecorder()
	defer func() {
		recover()
	}()
	h.Dispatch(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("expected not to be forbidden for unlocked container")
	}
}
