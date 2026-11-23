// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package server

import (
	"net/http"
	"reflect"
	"regexp"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	handlerMiddleware "github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

func newInspectionRouter() chi.Routes {
	var p RouterParams
	v := reflect.ValueOf(&p).Elem()
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.Ptr && f.CanSet() {
			f.Set(reflect.New(f.Type().Elem()))
		}
	}
	p.RBACService = identity.NewRBACService(nil)
	p.NodeTokenService = node.NewNodeTokenService(nil)
	return NewRouter(p)
}

var routeParam = regexp.MustCompile(`\{[^}]+\}`)

// TestNodePermRoutesResolveFromRequests checks that a concrete request path resolves (Routes.Find, as the
// Auth middleware does) to the same key chi.Walk produced at startup, for every guarded route.
func TestNodePermRoutesResolveFromRequests(t *testing.T) {
	r := newInspectionRouter()
	routes := nodePermRoutes(r)
	if len(routes) < 30 {
		t.Fatalf("only %d routes guarded by requireNodePerm: detection is broken", len(routes))
	}

	_ = chi.Walk(r, func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		key := handlerMiddleware.RouteKey(method, route)
		if !routes[key] {
			return nil
		}
		path := routeParam.ReplaceAllString(route, "x")
		pattern := r.Find(chi.NewRouteContext(), method, path)
		if got := handlerMiddleware.RouteKey(method, pattern); got != key {
			t.Errorf("%s %s resolves to %q, want %q", method, path, got, key)
		}
		return nil
	})
}

func TestNodePermRoutesExcludeOtherRoutes(t *testing.T) {
	routes := nodePermRoutes(newInspectionRouter())
	for _, key := range []string{
		"POST /v1/nodes/{nodeId}/files/upload",
		"GET /v1/nodes/{nodeId}/terminal",
		"GET /v1/node-containers/{nodeId}",
	} {
		if !routes[key] {
			t.Errorf("%s must be guarded by requireNodePerm", key)
		}
	}
	// Routes with {nodeId} that authorize differently (nodeRouteExceptions) and plain user routes must
	// not let a custom token write.
	for _, key := range []string{
		"DELETE /v1/clusters/{clusterId}/nodes/{nodeId}",
		"PUT /v1/users/{id}/nodes/{nodeId}/permissions",
		"POST /v1/api-tokens",
		"POST /v1/projects",
		"DELETE /v1/nodes/{id}",
	} {
		if routes[key] {
			t.Errorf("%s must not be in the node permission route set", key)
		}
	}
}
