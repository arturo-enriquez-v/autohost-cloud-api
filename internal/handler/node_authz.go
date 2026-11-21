// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// nodeIDParam returns the node ID from the route ({nodeId} or the legacy {nodeID} spelling).
func nodeIDParam(r *http.Request) string {
	if id := chi.URLParam(r, "nodeId"); id != "" {
		return id
	}
	return chi.URLParam(r, "nodeID")
}

// requireNodePerm is the route-level authorization for every endpoint that addresses a node in its URL.
// It requires an authenticated user (middleware.Auth must run first) holding perm on the {nodeId} node.
//
// The RBAC service is resolved lazily through rbac() because handlers receive it via SetRBACService after
// construction. A nil service, a missing node, or a lookup error all deny the request (fail closed);
// RBAC reports non-existent nodes as "not allowed", so no separate node lookup is needed.
//
// Handlers keep their own checks as defense in depth; this middleware guarantees that a route cannot be
// added without authorization (see TestNodeRoutesRequireNodePermission).
func requireNodePerm(rbac func() *identity.RBACService, perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := middleware.GetClaims(r.Context())
			if claims == nil || claims.UserID == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			nodeID := nodeIDParam(r)
			if nodeID == "" {
				http.Error(w, "missing node id", http.StatusBadRequest)
				return
			}
			ok, err := rbac().HasNodePermission(r.Context(), claims.UserID, nodeID, perm)
			if err != nil {
				log.Printf("[AUTHZ] permission check %s on node %s for user %s failed: %v", perm, nodeID, claims.UserID, err)
			}
			if err != nil || !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
