// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"context"
	"slices"
	"strings"
)

type tokenPermsKey struct{}

// WithTokenPermissions marks ctx as acting through an API token limited to perms. Every node permission
// check made with this context (GetEffectiveNodePermissions and everything built on it) intersects the
// user's permissions with perms, so a token never exceeds either its own grant or its owner's.
func WithTokenPermissions(ctx context.Context, perms []string) context.Context {
	return context.WithValue(ctx, tokenPermsKey{}, slices.Clone(perms))
}

// tokenPermissions returns the API token limit carried by ctx, if any.
func tokenPermissions(ctx context.Context) ([]string, bool) {
	perms, ok := ctx.Value(tokenPermsKey{}).([]string)
	return perms, ok
}

// ReadNodePermissions returns every *:read node permission.
func ReadNodePermissions() []string {
	var out []string
	for _, p := range AllNodePermissions {
		if strings.HasSuffix(p, ":read") {
			out = append(out, p)
		}
	}
	return out
}

// intersectPermissions returns the permissions present in both lists, honoring "*" on either side. An
// empty token list grants nothing (NormalizePermissions would otherwise add node:read).
func intersectPermissions(user, token []string) []string {
	switch {
	case len(token) == 0:
		return nil
	case slices.Contains(token, PermWildcard):
		return user
	case slices.Contains(user, PermWildcard):
		return NormalizePermissions(token)
	}
	userSet := NormalizePermissions(user)
	var out []string
	for _, p := range NormalizePermissions(token) {
		if slices.Contains(userSet, p) {
			out = append(out, p)
		}
	}
	return out
}
