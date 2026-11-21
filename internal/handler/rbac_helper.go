// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"net/http"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
)

// checkNodeAccess verifies if userID has the required granular permission on node n.
// When rbac is nil (e.g., in legacy unit tests), it falls back to strict OwnerID equality.
func checkNodeAccess(ctx context.Context, rbac *identity.RBACService, n *node.Node, userID string, requiredPerm string) bool {
	if userID == "" || n == nil {
		return false
	}
	if rbac != nil {
		ok, err := rbac.HasNodePermission(ctx, userID, n.ID, requiredPerm)
		if err == nil {
			return ok
		}
	}
	return n.OwnerID != nil && *n.OwnerID == userID
}

// checkNodeIDAccess resolves the node (when nodeSvc is available) and checks granular permission via rbac.
func checkNodeIDAccess(ctx context.Context, rbac *identity.RBACService, nodeSvc *node.NodeService, nodeID, userID, requiredPerm string) (*node.Node, int) {
	if userID == "" {
		return nil, http.StatusUnauthorized
	}
	if nodeSvc != nil {
		n, err := nodeSvc.GetByID(nodeID)
		if err != nil || n == nil {
			return nil, http.StatusNotFound
		}
		if !checkNodeAccess(ctx, rbac, n, userID, requiredPerm) {
			return nil, http.StatusForbidden
		}
		return n, http.StatusOK
	}
	if rbac != nil {
		ok, err := rbac.HasNodePermission(ctx, userID, nodeID, requiredPerm)
		if err != nil || !ok {
			return nil, http.StatusForbidden
		}
	}
	return nil, http.StatusOK
}

// checkAdminAccess verifies if userID is a global platform administrator.
func checkAdminAccess(ctx context.Context, rbac *identity.RBACService, userID string) bool {
	if userID == "" || rbac == nil {
		return false
	}
	ok, err := rbac.IsAdmin(ctx, userID)
	return err == nil && ok
}

// effectiveNodeOwnerID returns the node's OwnerID if present, falling back to callerUserID.
// This allows authorized project members to read/create node-scoped resources (e.g. VMs, backups, shares)
// under the node owner's tenant scope.
func effectiveNodeOwnerID(n *node.Node, callerUserID string) string {
	if n != nil && n.OwnerID != nil && *n.OwnerID != "" {
		return *n.OwnerID
	}
	return callerUserID
}
