// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
)

// ownerOnlyRBACRepo is a minimal identity.RBACRepository for in-package handler tests: every listed
// user is an active member and owns the listed nodes. Methods not overridden panic if called.
type ownerOnlyRBACRepo struct {
	identity.RBACRepository
	owners map[string]string // nodeID -> ownerUserID
}

func (m *ownerOnlyRBACRepo) GetUserByID(_ context.Context, id string) (*identity.User, error) {
	return &identity.User{ID: id, Role: identity.RoleMember, IsActive: true}, nil
}

func (m *ownerOnlyRBACRepo) GetNodeProjectAndOwner(_ context.Context, nodeID string) (*string, *string, bool, error) {
	owner, ok := m.owners[nodeID]
	if !ok {
		return nil, nil, false, nil
	}
	return nil, &owner, true, nil
}

func (m *ownerOnlyRBACRepo) GetUserNodePermission(_ context.Context, _, _ string) (*identity.NodePermissionGrant, error) {
	return nil, nil
}

func (m *ownerOnlyRBACRepo) GetProjectMembership(_ context.Context, _, _ string) (*identity.ProjectMembership, error) {
	return nil, nil
}

// newOwnerRBAC returns an RBAC service where ownerID owns each of nodeIDs.
func newOwnerRBAC(ownerID string, nodeIDs ...string) *identity.RBACService {
	owners := map[string]string{}
	for _, n := range nodeIDs {
		owners[n] = ownerID
	}
	return identity.NewRBACService(&ownerOnlyRBACRepo{owners: owners})
}

// withNodeID sets the {nodeId} route parameter as chi would.
func withNodeID(req *http.Request, nodeID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeId", nodeID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
