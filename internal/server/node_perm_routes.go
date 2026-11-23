// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package server

import (
	"net/http"
	"reflect"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"

	handlerMiddleware "github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// isRequireNodePerm reports whether mw was built by handler.requireNodePerm (identified by the name of
// its closure, as TestNodeRoutesRequireNodePermission does).
func isRequireNodePerm(mw func(http.Handler) http.Handler) bool {
	return strings.Contains(runtime.FuncForPC(reflect.ValueOf(mw).Pointer()).Name(), "requireNodePerm")
}

// nodePermRoutes returns every route guarded by requireNodePerm, keyed by handlerMiddleware.RouteKey.
// Custom API tokens may only write through these routes.
func nodePermRoutes(r chi.Routes) map[string]bool {
	routes := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		for _, mw := range mws {
			if isRequireNodePerm(mw) {
				routes[handlerMiddleware.RouteKey(method, route)] = true
				break
			}
		}
		return nil
	})
	return routes
}
