// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"errors"
	"testing"
)

// A project manager must not be able to grant permissions on nodes outside their project.
func TestAssignProjectMember_RejectsNodesOutsideProject(t *testing.T) {
	strp := func(s string) *string { return &s }
	repo := newMockRBACRepo()
	repo.users["manager"] = &User{ID: "manager", Role: RoleMember, IsActive: true}
	repo.users["target"] = &User{ID: "target", Role: RoleMember, IsActive: true}
	repo.nodeOwners["node-in"] = strp("manager")
	repo.nodeProjects["node-in"] = strp("proj-a")
	repo.nodeOwners["node-other-project"] = strp("someone-else")
	repo.nodeProjects["node-other-project"] = strp("proj-b")
	repo.nodeOwners["node-no-project"] = strp("someone-else")
	svc := NewRBACService(repo)

	for _, tc := range []struct {
		name   string
		nodeID string
		want   error
	}{
		{"node in another project", "node-other-project", ErrNodeNotInProject},
		{"node without project", "node-no-project", ErrNodeNotInProject},
		{"unknown node", "node-missing", ErrNodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.AssignProjectMember("manager", "proj-a", "target", NodePresetViewer, false, nil,
				[]NodePermissionGrantInput{{NodeID: tc.nodeID, RolePreset: NodePresetAdmin}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if _, ok := repo.nodePerms["target:"+tc.nodeID]; ok {
				t.Errorf("a grant on %s was written", tc.nodeID)
			}
			if _, ok := repo.projects["target:proj-a"]; ok {
				t.Errorf("membership was written although the request was rejected")
			}
		})
	}

	t.Run("node in the project is accepted", func(t *testing.T) {
		err := svc.AssignProjectMember("manager", "proj-a", "target", NodePresetViewer, false, nil,
			[]NodePermissionGrantInput{{NodeID: "node-in", RolePreset: NodePresetOperator}})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if g := repo.nodePerms["target:node-in"]; g == nil || g.RolePreset != NodePresetOperator {
			t.Errorf("grant = %+v, want operator on node-in", g)
		}
	})
}
