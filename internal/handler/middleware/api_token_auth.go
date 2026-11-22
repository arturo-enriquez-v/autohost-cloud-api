// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"context"
	"errors"

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
			UserID:        p.User.ID,
			Email:         p.User.Email,
			Name:          name,
			Role:          p.User.Role,
			APITokenID:    p.TokenID,
			APITokenScope: p.Scope,
		}, nil
	}
}
