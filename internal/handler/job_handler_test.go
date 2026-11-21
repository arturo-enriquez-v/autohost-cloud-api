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

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
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

// ownerRBAC returns an RBAC service where user-1 owns node-123.
func ownerRBAC() *identity.RBACService {
	repo := newMockRBACRepoHandler()
	owner := "user-1"
	repo.users[owner] = &identity.User{ID: owner, Role: identity.RoleMember, IsActive: true}
	repo.nodeOwners["node-123"] = &owner
	return identity.NewRBACService(repo)
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
	h.SetRBACService(ownerRBAC())

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
	h.SetRBACService(ownerRBAC())

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

func TestJobHandler_Dispatch_RejectsPrivilegedCommands(t *testing.T) {
	h := handler.NewJobHandler(nil, &dummyDispatcher{}, nil)
	h.SetRBACService(ownerRBAC())

	for _, tc := range []struct{ name, params string }{
		{"swarm.join", `{"manager_addr":"evil:2377","token":"x"}`},
		{"docker.volume.restore", `{"volumes":["/"]}`},
		{"script.exec", `{"script_path":"/tmp/x.sh"}`},
		{"caddy.upsert-route", `{"domain":"x"}`},
	} {
		body, _ := json.Marshal(map[string]string{
			"node_id":      "node-123",
			"command_name": tc.name,
			"command_type": "default",
			"params":       tc.params,
		})
		req := httptest.NewRequest("POST", "/jobs", bytes.NewReader(body))
		req = req.WithContext(middleware.ContextWithClaims(req.Context(), &middleware.Claims{UserID: "user-1"}))
		rec := httptest.NewRecorder()
		h.Dispatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400 even for the node owner, got %d (%s)", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestJobHandler_Dispatch_DeniedWithoutPermission(t *testing.T) {
	h := handler.NewJobHandler(nil, &dummyDispatcher{}, nil)
	h.SetRBACService(ownerRBAC())

	body, _ := json.Marshal(map[string]string{
		"node_id":      "node-123",
		"command_name": "container.list",
		"command_type": "default",
	})
	req := httptest.NewRequest("POST", "/jobs", bytes.NewReader(body))
	req = req.WithContext(middleware.ContextWithClaims(req.Context(), &middleware.Claims{UserID: "user-stranger"}))
	rec := httptest.NewRecorder()
	h.Dispatch(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a user without access, got %d", rec.Code)
	}
}
