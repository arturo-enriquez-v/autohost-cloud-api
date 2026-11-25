// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"context"
	"slices"
	"testing"
)

func TestProjectPermissions(t *testing.T) {
	repo := newMockRBACRepo()
	repo.users["admin"] = &User{ID: "admin", Role: RoleAdmin, IsActive: true}
	repo.users["owner"] = &User{ID: "owner", Role: RoleMember, IsActive: true}
	repo.users["viewer"] = &User{ID: "viewer", Role: RoleMember, IsActive: true}
	repo.users["operator"] = &User{ID: "operator", Role: RoleMember, IsActive: true}
	repo.users["editor"] = &User{ID: "editor", Role: RoleMember, IsActive: true}
	repo.users["custom"] = &User{ID: "custom", Role: RoleMember, IsActive: true}
	repo.users["outsider"] = &User{ID: "outsider", Role: RoleMember, IsActive: true}
	repo.users["inactive"] = &User{ID: "inactive", Role: RoleAdmin, IsActive: false}
	member := func(user, role string, perms []string) {
		repo.projects[user+":p1"] = &ProjectMembership{ProjectID: "p1", UserID: user, DefaultRole: role, DefaultPermissions: perms}
	}
	// Stored with the old preset lists, as saved before connectors existed.
	member("viewer", NodePresetViewer, PresetViewerPermissions)
	member("operator", NodePresetOperator, PresetOperatorPermissions)
	member("editor", NodePresetEditor, PresetEditorPermissions)
	member("custom", NodePresetCustom, []string{PermMetricsRead, PermConnectorsRun})
	svc := NewRBACService(repo)

	cases := []struct {
		user              string
		read, run, manage bool
	}{
		{"admin", true, true, true},
		{"owner", true, true, true},
		{"viewer", true, false, false},
		{"operator", true, true, false},
		{"editor", true, true, true},
		{"custom", true, true, false},
		{"outsider", false, false, false},
		{"inactive", false, false, false},
	}
	for _, c := range cases {
		perms, err := svc.ProjectPermissions(context.Background(), c.user, "p1", "owner")
		if err != nil {
			t.Fatalf("%s: %v", c.user, err)
		}
		got := [3]bool{HasPermission(perms, PermConnectorsRead), HasPermission(perms, PermConnectorsRun), HasPermission(perms, PermConnectorsManage)}
		if want := [3]bool{c.read, c.run, c.manage}; got != want {
			t.Errorf("%s: read/run/manage = %v, want %v (perms %v)", c.user, got, want, perms)
		}
	}
}

func TestProjectPermissionsTokenLimit(t *testing.T) {
	repo := newMockRBACRepo()
	repo.users["owner"] = &User{ID: "owner", Role: RoleMember, IsActive: true}
	svc := NewRBACService(repo)

	ctx := WithTokenPermissions(context.Background(), ReadNodePermissions())
	perms, err := svc.ProjectPermissions(ctx, "owner", "p1", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !HasPermission(perms, PermConnectorsRead) || HasPermission(perms, PermConnectorsRun) || slices.Contains(perms, PermWildcard) {
		t.Fatalf("read-only token on owner: %v", perms)
	}
}
