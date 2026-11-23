// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"context"
	"slices"
	"testing"
)

// tokenScopeRBACRepo: "admin" is a global admin, "owner" owns node-1, "viewer" has a viewer grant on it.
type tokenScopeRBACRepo struct {
	RBACRepository
}

func (tokenScopeRBACRepo) GetUserByID(_ context.Context, id string) (*User, error) {
	role := RoleMember
	if id == "admin" {
		role = RoleAdmin
	}
	return &User{ID: id, Role: role, IsActive: true}, nil
}

func (tokenScopeRBACRepo) GetNodeProjectAndOwner(_ context.Context, nodeID string) (*string, *string, bool, error) {
	if nodeID != "node-1" {
		return nil, nil, false, nil
	}
	owner := "owner"
	return nil, &owner, true, nil
}

func (tokenScopeRBACRepo) GetUserNodePermission(_ context.Context, userID, _ string) (*NodePermissionGrant, error) {
	if userID == "viewer" {
		return &NodePermissionGrant{RolePreset: NodePresetViewer}, nil
	}
	return nil, nil
}

func (tokenScopeRBACRepo) GetProjectMembership(context.Context, string, string) (*ProjectMembership, error) {
	return nil, nil
}

func TestTokenPermissionsLimitNodeAccess(t *testing.T) {
	rbac := NewRBACService(tokenScopeRBACRepo{})
	filesToken := WithTokenPermissions(context.Background(), NormalizePermissions([]string{PermFilesWrite}))
	writeToken := WithTokenPermissions(context.Background(), []string{PermWildcard})
	readToken := WithTokenPermissions(context.Background(), ReadNodePermissions())

	cases := []struct {
		name string
		ctx  context.Context
		user string
		perm string
		want bool
	}{
		{"session admin keeps everything", context.Background(), "admin", PermTerminalExec, true},
		{"files token on admin: files:write", filesToken, "admin", PermFilesWrite, true},
		{"files token on admin: no terminal", filesToken, "admin", PermTerminalExec, false},
		{"files token on admin: no delete", filesToken, "admin", PermFilesDelete, false},
		{"files token on owner: files:write", filesToken, "owner", PermFilesWrite, true},
		{"files token on owner: no containers", filesToken, "owner", PermContainersAction, false},
		{"files token never exceeds the user", filesToken, "viewer", PermFilesWrite, false},
		{"files token keeps the user's reads it was given", filesToken, "viewer", PermFilesRead, true},
		{"write token on owner: terminal", writeToken, "owner", PermTerminalExec, true},
		{"read token on owner: metrics", readToken, "owner", PermMetricsRead, true},
		{"read token on owner: no files:write", readToken, "owner", PermFilesWrite, false},
		{"no access stays no access", filesToken, "stranger", PermFilesRead, false},
	}
	for _, tc := range cases {
		got, err := rbac.HasNodePermission(tc.ctx, tc.user, "node-1", tc.perm)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	preset, perms, _, _ := rbac.GetEffectiveNodePermissions(filesToken, "admin", "node-1")
	if preset != NodePresetCustom || !slices.Equal(perms, []string{PermNodeRead, PermFilesRead, PermFilesWrite}) {
		t.Errorf("effective permissions through a files token = %s %v", preset, perms)
	}
}
