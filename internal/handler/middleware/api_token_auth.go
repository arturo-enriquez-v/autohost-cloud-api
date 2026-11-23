// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
)

// apiTokenAuthenticatorService is the part of identity.APITokenService that Auth needs.
type apiTokenAuthenticatorService interface {
	Authenticate(ctx context.Context, raw string) (*identity.APITokenPrincipal, error)
}

// NewAPITokenAuthenticator adapts the API token service to Auth: the claims carry the owner's current
// role (read from the database, not from the token) plus the token ID and scope.
func NewAPITokenAuthenticator(svc apiTokenAuthenticatorService) APITokenAuthenticator {
	return func(ctx context.Context, raw string) (*Claims, error) {
		p, err := svc.Authenticate(ctx, raw)
		if errors.Is(err, identity.ErrAPITokenInvalid) {
			return nil, ErrAPITokenRejected
		}
		if err != nil {
			return nil, err
		}
		name := ""
		if p.User.Name != nil {
			name = *p.User.Name
		}
		return &Claims{
			UserID:              p.User.ID,
			Email:               p.User.Email,
			Name:                name,
			Role:                p.User.Role,
			APITokenID:          p.TokenID,
			APITokenScope:       p.Scope,
			APITokenPermissions: p.Permissions,
		}, nil
	}
}

// nodePermRouteGate knows which routes are guarded by requireNodePerm, so that custom API tokens can be
// limited to writing through them. A route not in the set is denied (fail closed).
type nodePermRouteGate struct {
	root   chi.Routes
	routes map[string]bool // "METHOD /pattern" as produced by RouteKey
}

var nodePermGate atomic.Pointer[nodePermRouteGate]

// SetNodePermRoutes registers the routes guarded by requireNodePerm (keys from RouteKey) and the root
// router used to resolve a request to its route pattern.
func SetNodePermRoutes(root chi.Routes, routes map[string]bool) {
	nodePermGate.Store(&nodePermRouteGate{root: root, routes: routes})
}

// RouteKey normalizes a method and chi route pattern into the key used by SetNodePermRoutes. Patterns
// from chi.Walk and from Routes.Find differ in mount wildcards and trailing slashes.
func RouteKey(method, pattern string) string {
	pattern = strings.ReplaceAll(pattern, "/*/", "/")
	pattern = strings.TrimSuffix(pattern, "/*")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		pattern = "/"
	}
	return method + " " + pattern
}

// isNodePermRoute reports whether the request resolves to a route guarded by requireNodePerm.
func isNodePermRoute(r *http.Request) bool {
	g := nodePermGate.Load()
	if g == nil || g.root == nil {
		return false
	}
	pattern := g.root.Find(chi.NewRouteContext(), r.Method, r.URL.Path)
	return pattern != "" && g.routes[RouteKey(r.Method, pattern)]
}
