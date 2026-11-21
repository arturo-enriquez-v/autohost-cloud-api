// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package server_test

import (
	"net/http"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/server"
)

// nodeRouteExceptions lists node-addressed routes that intentionally do NOT use requireNodePerm, with the
// reason their authorization is enforced elsewhere. Keep it short: every entry is reviewed by hand.
var nodeRouteExceptions = map[string]string{
	"POST /v1/backups/upload/{nodeId}/{recordId}":        "agent endpoint: node token must match {nodeId} and the record's node",
	"GET /v1/backups/node-download/{nodeId}/{recordId}":  "agent endpoint: node token must match {nodeId} and the record's node",
	"DELETE /v1/clusters/{clusterId}/nodes/{nodeId}":     "cluster ownership (authorizeCluster) + node must be a member",
	"PUT /v1/users/{id}/nodes/{nodeId}/permissions":      "global admin only (UserHandler.requireAdmin)",
	"DELETE /v1/users/{id}/nodes/{nodeId}/permissions":   "global admin only (UserHandler.requireAdmin)",
}

// newRouterForInspection builds the real router with zero-value handlers. Routes are only registered,
// never served, so no dependency is exercised.
func newRouterForInspection() http.Handler {
	var p server.RouterParams
	v := reflect.ValueOf(&p).Elem()
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.Ptr && f.CanSet() {
			f.Set(reflect.New(f.Type().Elem()))
		}
	}
	p.RBACService = identity.NewRBACService(nil)
	p.NodeTokenService = node.NewNodeTokenService(nil)
	return server.NewRouter(p)
}

func funcName(f any) string {
	return runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
}

// TestNodeRoutesRequireNodePermission fails when a route that addresses a node in its URL is not guarded by
// the centralized requireNodePerm middleware (and is not a documented exception).
func TestNodeRoutesRequireNodePermission(t *testing.T) {
	r := newRouterForInspection()
	seenExceptions := map[string]bool{}
	var missing []string

	err := chi.Walk(r.(chi.Routes), func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		if !strings.Contains(route, "{nodeId}") && !strings.Contains(route, "{nodeID}") {
			return nil
		}
		key := method + " " + strings.TrimSuffix(route, "/")
		if _, ok := nodeRouteExceptions[key]; ok {
			seenExceptions[key] = true
			return nil
		}
		for _, mw := range mws {
			if strings.Contains(funcName(mw), "requireNodePerm") {
				return nil
			}
		}
		missing = append(missing, key)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("route %s addresses a node but has no requireNodePerm middleware (add .With(h.nodePerm(...)) or document an exception)", m)
	}
	for key := range nodeRouteExceptions {
		if !seenExceptions[key] {
			t.Errorf("stale exception %q: route no longer exists, remove it from nodeRouteExceptions", key)
		}
	}
}
