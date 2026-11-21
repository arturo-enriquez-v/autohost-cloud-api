// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"context"
	"errors"
	"testing"
)

type mockRBACRepo struct {
	users          map[string]*User
	projects       map[string]*ProjectMembership // key: userID + ":" + projectID
	nodePerms      map[string]*NodePermissionGrant
	nodeOwners     map[string]*string
	nodeProjects   map[string]*string
	nextUserNumber int
}

func newMockRBACRepo() *mockRBACRepo {
	return &mockRBACRepo{
		users:        make(map[string]*User),
		projects:     make(map[string]*ProjectMembership),
		nodePerms:    make(map[string]*NodePermissionGrant),
		nodeOwners:   make(map[string]*string),
		nodeProjects: make(map[string]*string),
	}
}

func (m *mockRBACRepo) ListUsers() ([]*User, error) {
	var list []*User
	for _, u := range m.users {
		list = append(list, u)
	}
	return list, nil
}

func (m *mockRBACRepo) GetUserByID(_ context.Context, id string) (*User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}

func (m *mockRBACRepo) GetUserByEmail(email string) (*User, error) {
	for _, u := range m.users {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockRBACRepo) CreateUserWithRole(email, name, passwordHash, role string, isActive bool, createdBy *string) (*User, error) {
	m.nextUserNumber++
	id := "user-" + email
	n := name
	ph := passwordHash
	u := &User{
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

func (m *mockRBACRepo) UpdateUser(id string, email, name, passwordHash, role *string, isActive *bool) (*User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	if email != nil {
		u.Email = *email
	}
	if name != nil {
		n := *name
		u.Name = &n
	}
	if role != nil {
		u.Role = *role
	}
	if isActive != nil {
		u.IsActive = *isActive
	}
	if passwordHash != nil {
		ph := *passwordHash
		u.PasswordHash = &ph
	}
	cp := *u
	return &cp, nil
}

func (m *mockRBACRepo) DeleteUser(id string) error {
	delete(m.users, id)
	return nil
}

func (m *mockRBACRepo) CountActiveAdmins() (int, error) {
	count := 0
	for _, u := range m.users {
		if u.Role == RoleAdmin && u.IsActive {
			count++
		}
	}
	return count, nil
}

func (m *mockRBACRepo) RevokeAllUserRefreshTokens(userID string) error {
	return nil
}

func (m *mockRBACRepo) ListUserProjects(userID string) ([]*ProjectMembership, error) {
	var out []*ProjectMembership
	for _, pm := range m.projects {
		if pm.UserID == userID {
			cp := *pm
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *mockRBACRepo) GetProjectMembership(_ context.Context, projectID, userID string) (*ProjectMembership, error) {
	pm, ok := m.projects[userID+":"+projectID]
	if !ok {
		return nil, nil
	}
	cp := *pm
	return &cp, nil
}

func (m *mockRBACRepo) UpsertProjectMembership(pm *ProjectMembership) (*ProjectMembership, error) {
	cp := *pm
	m.projects[pm.UserID+":"+pm.ProjectID] = &cp
	return &cp, nil
}

func (m *mockRBACRepo) DeleteProjectMembership(projectID, userID string) error {
	delete(m.projects, userID+":"+projectID)
	return nil
}

func (m *mockRBACRepo) ReplaceUserProjects(userID string, memberships []*ProjectMembership) error {
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

func (m *mockRBACRepo) ListProjectMembers(projectID string) ([]*ProjectMemberDetail, error) {
	var out []*ProjectMemberDetail
	for _, pm := range m.projects {
		if pm.ProjectID == projectID {
			out = append(out, &ProjectMemberDetail{
				ProjectID: projectID,
				UserID:    pm.UserID,
			})
		}
	}
	return out, nil
}

func (m *mockRBACRepo) ListUserNodePermissions(_ context.Context, userID string) ([]*NodePermissionGrant, error) {
	var out []*NodePermissionGrant
	for _, np := range m.nodePerms {
		if np.UserID == userID {
			cp := *np
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *mockRBACRepo) GetUserNodePermission(_ context.Context, userID, nodeID string) (*NodePermissionGrant, error) {
	np, ok := m.nodePerms[userID+":"+nodeID]
	if !ok {
		return nil, nil
	}
	cp := *np
	return &cp, nil
}

func (m *mockRBACRepo) UpsertUserNodePermission(grant *NodePermissionGrant) (*NodePermissionGrant, error) {
	cp := *grant
	m.nodePerms[grant.UserID+":"+grant.NodeID] = &cp
	return &cp, nil
}

func (m *mockRBACRepo) DeleteUserNodePermission(userID, nodeID string) error {
	delete(m.nodePerms, userID+":"+nodeID)
	return nil
}

func (m *mockRBACRepo) DeleteUserNodePermissionsByProject(userID, projectID string) error {
	for k, np := range m.nodePerms {
		if np.UserID == userID && np.ProjectID != nil && *np.ProjectID == projectID {
			delete(m.nodePerms, k)
		}
	}
	return nil
}

func (m *mockRBACRepo) ReplaceUserNodePermissions(userID string, grants []*NodePermissionGrant) error {
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

func (m *mockRBACRepo) GetNodeProjectAndOwner(_ context.Context, nodeID string) (projectID *string, ownerID *string, exists bool, err error) {
	owner, ok := m.nodeOwners[nodeID]
	return m.nodeProjects[nodeID], owner, ok, nil
}

func TestRBACUserManagementAndGranularPermissions(t *testing.T) {
	repo := newMockRBACRepo()
	adminName := "Main Admin"
	adminID := "user-admin"
	repo.users[adminID] = &User{
		ID:       adminID,
		Email:    "admin@autohost.local",
		Name:     &adminName,
		Role:     RoleAdmin,
		IsActive: true,
	}

	projA := "proj-alpha"
	node1 := "node-1"
	node2 := "node-2"
	repo.nodeOwners[node1] = &adminID
	repo.nodeProjects[node1] = &projA
	repo.nodeOwners[node2] = &adminID
	repo.nodeProjects[node2] = &projA

	svc := NewRBACService(repo)

	// 1. Admin cannot delete themselves or demote the last admin
	if err := svc.DeleteUser(adminID, adminID); !errors.Is(err, ErrCannotDeleteSelf) {
		t.Fatalf("expected ErrCannotDeleteSelf, got %v", err)
	}
	memberRole := RoleMember
	if _, err := svc.UpdateUser(adminID, adminID, UpdateUserInput{Role: &memberRole}); !errors.Is(err, ErrCannotDemoteLastAdmin) {
		t.Fatalf("expected ErrCannotDemoteLastAdmin when demoting sole admin, got %v", err)
	}

	// 2. Admin creates a member assigned to projA with granular permissions on node1 (operator) and custom on node2
	created, err := svc.CreateUser(adminID, CreateUserInput{
		Email:    "dev@autohost.local",
		Name:     "Dev Operator",
		Password: "SecretPass123",
		Role:     RoleMember,
		Projects: []ProjectMembershipInput{
			{ProjectID: projA, DefaultRole: NodePresetViewer, AllNodes: false},
		},
		NodePermissions: []NodePermissionGrantInput{
			{
				NodeID:     node1,
				RolePreset: NodePresetOperator,
			},
			{
				NodeID:      node2,
				RolePreset:  NodePresetCustom,
				Permissions: []string{PermFilesWrite}, // should auto-imply files:read and node:read
			},
		},
	})
	if err != nil {
		t.Fatalf("expected CreateUser to succeed, got %v", err)
	}
	devID := created.ID

	// 3. Verify preset 'operator' permissions on node1
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node1, PermContainersAction); !ok {
		t.Fatalf("expected operator to have %s on node1", PermContainersAction)
	}
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node1, PermJobsRun); !ok {
		t.Fatalf("expected operator to have %s on node1", PermJobsRun)
	}
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node1, PermTerminalExec); ok {
		t.Fatalf("expected operator NOT to have %s on node1 without explicit grant", PermTerminalExec)
	}
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node1, PermNodeDelete); ok {
		t.Fatalf("expected operator NOT to have %s on node1", PermNodeDelete)
	}
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node1, PermContainersDeploy); ok {
		t.Fatalf("expected operator NOT to have %s on node1", PermContainersDeploy)
	}

	// 4. Verify custom permissions + automatic read implication on node2
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node2, PermFilesWrite); !ok {
		t.Fatalf("expected dev to have %s on node2", PermFilesWrite)
	}
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node2, PermFilesRead); !ok {
		t.Fatalf("expected %s to imply %s on node2", PermFilesWrite, PermFilesRead)
	}
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node2, PermNodeRead); !ok {
		t.Fatalf("expected any node permission to imply %s on node2", PermNodeRead)
	}
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node2, PermFilesDelete); ok {
		t.Fatalf("expected dev NOT to have %s on node2", PermFilesDelete)
	}

	// 5. Removing member from project projA revokes their node permissions in projA
	if err := svc.RemoveProjectMember(projA, devID); err != nil {
		t.Fatalf("unexpected error removing project member: %v", err)
	}
	if ok, _ := svc.HasNodePermission(context.Background(), devID, node1, PermNodeRead); ok {
		t.Fatalf("expected member removed from project to lose node access")
	}
}
