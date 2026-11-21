// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/go-chi/chi/v5"
)

// Removing a node from project A must not detach it from the project it actually belongs to.
func TestProjectHandler_RemoveNode_OnlyFromThisProject(t *testing.T) {
	owner := "user-1"
	projB := "proj-b"
	nodeRepo := &MockNodeRepo{nodes: map[string]*node.Node{
		"node-123": {ID: "node-123", OwnerID: &owner, ProjectID: &projB},
	}}
	projectRepo := &MockProjectRepo{projects: map[string]*apps.Project{
		"proj-a": {ID: "proj-a", UserID: owner, Name: "A"},
		"proj-b": {ID: "proj-b", UserID: owner, Name: "B"},
	}}
	h := handler.NewProjectHandler(apps.NewProjectService(projectRepo), node.NewNodeService(nodeRepo))
	h.SetRBACService(ownerRBAC())
	r := chi.NewRouter()
	r.Mount("/v1/projects", h.Routes())

	for _, tc := range []struct {
		name, url string
		want      int
	}{
		{"node belongs to another project", "/v1/projects/proj-a/nodes/node-123", http.StatusNotFound},
		{"node belongs to this project", "/v1/projects/proj-b/nodes/node-123", http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, tc.url, nil)
			req.Header.Set("Authorization", "Bearer "+generateToken(owner, "owner@example.com"))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
