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

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/go-chi/chi/v5"
)

type mockRBACRepoHandler struct {
	users        map[string]*identity.User
	projects     map[string]*identity.ProjectMembership
	nodePerms    map[string]*identity.NodePermissionGrant
	nodeOwners   map[string]*string
	nodeProjects map[string]*string
}

func newMockRBACRepoHandler() *mockRBACRepoHandler {
	return &mockRBACRepoHandler{
		users:        make(map[string]*identity.User),
		projects:     make(map[string]*identity.ProjectMembership),
		nodePerms:    make(map[string]*identity.NodePermissionGrant),
		nodeOwners:   make(map[string]*string),
		nodeProjects: make(map[string]*string),
	}
}

func (m *mockRBACRepoHandler) ListUsers() ([]*identity.User, error) {
	var out []*identity.User
	for _, u := range m.users {
		out = append(out, u)
	}
	return out, nil
}
func (m *mockRBACRepoHandler) GetUserByID(_ context.Context, id string) (*identity.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}
func (m *mockRBACRepoHandler) GetUserByEmail(email string) (*identity.User, error) {
	for _, u := range m.users {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, nil
}
func (m *mockRBACRepoHandler) CreateUserWithRole(email, name, passwordHash, role string, isActive bool, createdBy *string) (*identity.User, error) {
	id := "u-" + email
	n := name
	ph := passwordHash
	u := &identity.User{
		ID:           id,
		Email:        email,
		Name:         &n,
		PasswordHash: &ph,
		Role:         role,
		IsActive:     isActive,
		CreatedBy:    createdBy,
	}
	m.users[id] = u
	cp := *u
	return &cp, nil
}
func (m *mockRBACRepoHandler) UpdateUser(id string, email, name, passwordHash, role *string, isActive *bool) (*identity.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, identity.ErrUserNotFound
	}
	if role != nil {
		u.Role = *role
	}
	if isActive != nil {
		u.IsActive = *isActive
	}
	cp := *u
	return &cp, nil
}
func (m *mockRBACRepoHandler) DeleteUser(id string) error {
	delete(m.users, id)
	return nil
}
func (m *mockRBACRepoHandler) CountActiveAdmins() (int, error) {
	c := 0
	for _, u := range m.users {
		if u.Role == identity.RoleAdmin && u.IsActive {
			c++
		}
	}
	return c, nil
}
func (m *mockRBACRepoHandler) RevokeAllUserRefreshTokens(userID string) error {
	return nil
}
func (m *mockRBACRepoHandler) ListUserProjects(userID string) ([]*identity.ProjectMembership, error) {
	var out []*identity.ProjectMembership
	for _, pm := range m.projects {
		if pm.UserID == userID {
			cp := *pm
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (m *mockRBACRepoHandler) GetProjectMembership(_ context.Context, projectID, userID string) (*identity.ProjectMembership, error) {
	pm, ok := m.projects[userID+":"+projectID]
	if !ok {
		return nil, nil
	}
	cp := *pm
	return &cp, nil
}
func (m *mockRBACRepoHandler) UpsertProjectMembership(pm *identity.ProjectMembership) (*identity.ProjectMembership, error) {
	cp := *pm
	m.projects[pm.UserID+":"+pm.ProjectID] = &cp
	return &cp, nil
}
func (m *mockRBACRepoHandler) DeleteProjectMembership(projectID, userID string) error {
	delete(m.projects, userID+":"+projectID)
	return nil
}
func (m *mockRBACRepoHandler) ReplaceUserProjects(userID string, memberships []*identity.ProjectMembership) error {
	for k, pm := range m.projects {
		if pm.UserID == userID {
			delete(m.projects, k)
		}
	}
	for _, pm := range memberships {
		cp := *pm
		m.projects[userID+":"+pm.ProjectID] = &cp
	}
	return nil
}
func (m *mockRBACRepoHandler) ListProjectMembers(projectID string) ([]*identity.ProjectMemberDetail, error) {
	return nil, nil
}
func (m *mockRBACRepoHandler) ListUserNodePermissions(_ context.Context, userID string) ([]*identity.NodePermissionGrant, error) {
	var out []*identity.NodePermissionGrant
	for _, np := range m.nodePerms {
		if np.UserID == userID {
			cp := *np
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (m *mockRBACRepoHandler) GetUserNodePermission(_ context.Context, userID, nodeID string) (*identity.NodePermissionGrant, error) {
	np, ok := m.nodePerms[userID+":"+nodeID]
	if !ok {
		return nil, nil
	}
	cp := *np
	return &cp, nil
}
func (m *mockRBACRepoHandler) UpsertUserNodePermission(grant *identity.NodePermissionGrant) (*identity.NodePermissionGrant, error) {
	cp := *grant
	m.nodePerms[grant.UserID+":"+grant.NodeID] = &cp
	return &cp, nil
}
func (m *mockRBACRepoHandler) DeleteUserNodePermission(userID, nodeID string) error {
	delete(m.nodePerms, userID+":"+nodeID)
	return nil
}
func (m *mockRBACRepoHandler) DeleteUserNodePermissionsByProject(userID, projectID string) error {
	for k, np := range m.nodePerms {
		if np.UserID == userID && np.ProjectID != nil && *np.ProjectID == projectID {
			delete(m.nodePerms, k)
		}
	}
	return nil
}
func (m *mockRBACRepoHandler) ReplaceUserNodePermissions(userID string, grants []*identity.NodePermissionGrant) error {
	for k, np := range m.nodePerms {
		if np.UserID == userID {
			delete(m.nodePerms, k)
		}
	}
	for _, g := range grants {
		cp := *g
		cp.UserID = userID
		m.nodePerms[userID+":"+g.NodeID] = &cp
	}
	return nil
}
func (m *mockRBACRepoHandler) GetNodeProjectAndOwner(_ context.Context, nodeID string) (*string, *string, bool, error) {
	owner, ok := m.nodeOwners[nodeID]
	return m.nodeProjects[nodeID], owner, ok, nil
}

// FindAll & FindAllWithMetrics on MockNodeRepo so ExtendedNodeRepository is satisfied
func (m *MockNodeRepo) FindAll() ([]*node.Node, error) {
	var out []*node.Node
	for _, n := range m.nodes {
		out = append(out, n)
	}
	return out, nil
}

func (m *MockNodeRepo) FindAllWithMetrics() ([]*node.NodeWithMetrics, error) {
	var out []*node.NodeWithMetrics
	for _, n := range m.nodes {
		out = append(out, &node.NodeWithMetrics{
			ID:        n.ID,
			OwnerID:   n.OwnerID,
			ProjectID: n.ProjectID,
			Hostname:  n.Hostname,
		})
	}
	return out, nil
}

func TestRBACHandler_UsersAndNodeGranularPermissions(t *testing.T) {
	adminID := "admin-1"
	adminName := "Admin"
	projID := "proj-1"
	node1 := "node-1"
	node2 := "node-2"

	rbacRepo := newMockRBACRepoHandler()
	rbacRepo.users[adminID] = &identity.User{
		ID:       adminID,
		Email:    "admin@autohost.local",
		Name:     &adminName,
		Role:     identity.RoleAdmin,
		IsActive: true,
	}
	rbacRepo.nodeOwners[node1] = &adminID
	rbacRepo.nodeProjects[node1] = &projID
	rbacRepo.nodeOwners[node2] = &adminID
	rbacRepo.nodeProjects[node2] = &projID

	nodeRepo := &MockNodeRepo{
		nodes: map[string]*node.Node{
			node1: {ID: node1, Hostname: "Prod Server 1", OwnerID: &adminID, ProjectID: &projID},
			node2: {ID: node2, Hostname: "Prod Server 2", OwnerID: &adminID, ProjectID: &projID},
		},
	}
	projRepo := &MockProjectRepo{
		projects: map[string]*apps.Project{
			projID: {ID: projID, UserID: adminID, Name: "Core Project"},
		},
	}

	rbacSvc := identity.NewRBACService(rbacRepo)
	nodeSvc := node.NewNodeService(nodeRepo)
	projSvc := apps.NewProjectService(projRepo)
	nodeTokenSvc := node.NewNodeTokenService(&MockNodeTokenRepo{})
	caddySvc := apps.NewCaddyRouteService(&MockCaddyRouteRepo{})

	userHandler := handler.NewUserHandler(rbacSvc)
	nodeHandler := handler.NewNodeHandler(nodeSvc, nodeTokenSvc)
	nodeHandler.SetRBACService(rbacSvc)
	projHandler := handler.NewProjectHandler(projSvc, nodeSvc)
	projHandler.SetRBACService(rbacSvc)
	caddyHandler := handler.NewCaddyRouteHandler(caddySvc, nil, nil, nodeSvc)
	caddyHandler.SetRBACService(rbacSvc)

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Mount("/users", userHandler.Routes())
		r.Mount("/nodes", nodeHandler.Routes())
		r.Mount("/projects", projHandler.Routes())
		r.Mount("/caddy-routes", caddyHandler.Routes())
	})

	adminToken := generateToken(adminID, "admin@autohost.local")

	// 1. Admin creates a member with viewer preset on node1 only (not node2)
	createBody := `{
		"email": "member@autohost.local",
		"name": "Member Viewer",
		"password": "Password123!",
		"role": "member",
		"projects": [{"project_id": "proj-1", "default_role": "viewer", "all_nodes": false}],
		"node_permissions": [
			{"node_id": "node-1", "role_preset": "viewer"}
		]
	}`
	req := httptest.NewRequest("POST", "/v1/users", bytes.NewBufferString(createBody))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating member user, got %d: %s", rec.Code, rec.Body.String())
	}

	var createdUser identity.UserWithPermissions
	if err := json.Unmarshal(rec.Body.Bytes(), &createdUser); err != nil {
		t.Fatalf("failed to unmarshal created user: %v", err)
	}
	memberID := createdUser.ID
	memberToken := generateToken(memberID, "member@autohost.local")

	// 2. Member lists /v1/nodes -> only sees node-1 (not node-2), with role_preset="viewer"
	req = httptest.NewRequest("GET", "/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+memberToken)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 listing nodes for member, got %d: %s", rec.Code, rec.Body.String())
	}
	var visibleNodes []node.NodeWithMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &visibleNodes); err != nil {
		t.Fatalf("failed to unmarshal visible nodes: %v", err)
	}
	if len(visibleNodes) != 1 || visibleNodes[0].ID != node1 {
		t.Fatalf("expected member to only see node-1, got %+v", visibleNodes)
	}
	if visibleNodes[0].RolePreset != identity.NodePresetViewer {
		t.Fatalf("expected node-1 RolePreset='viewer', got %q", visibleNodes[0].RolePreset)
	}

	// 3. Member with viewer preset can GET /v1/caddy-routes/node-1 (network:read) but CANNOT PUT /v1/caddy-routes/node-1 (network:manage)
	req = httptest.NewRequest("GET", "/v1/caddy-routes/node-1", nil)
	req.Header.Set("Authorization", "Bearer "+memberToken)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected viewer to read caddy routes (200), got %d", rec.Code)
	}

	req = httptest.NewRequest("PUT", "/v1/caddy-routes/node-1", bytes.NewBufferString(`{"domain":"app.local","upstream":"localhost:3000"}`))
	req.Header.Set("Authorization", "Bearer "+memberToken)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected viewer to get 403 on PUT caddy route (missing network:manage), got %d", rec.Code)
	}

	// 4. Admin grants custom network:manage permission on node-1 to member
	grantBody := `{"role_preset": "custom", "permissions": ["network:manage"]}`
	req = httptest.NewRequest("PUT", "/v1/users/"+memberID+"/nodes/node-1/permissions", bytes.NewBufferString(grantBody))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 updating node permission, got %d: %s", rec.Code, rec.Body.String())
	}

	// 5. Now member CAN PUT /v1/caddy-routes/node-1!
	req = httptest.NewRequest("PUT", "/v1/caddy-routes/node-1", bytes.NewBufferString(`{"domain":"app.local","upstream":"localhost:3000"}`))
	req.Header.Set("Authorization", "Bearer "+memberToken)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected member with network:manage to get 200 on PUT caddy route, got %d: %s", rec.Code, rec.Body.String())
	}
}
