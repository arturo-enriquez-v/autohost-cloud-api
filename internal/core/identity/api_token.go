// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Personal API tokens are long-lived, revocable credentials for scripts and MCP clients. They act as
// their owner, limited by scope:
//   - "read": read requests only, and only the *:read node permissions.
//   - "custom": an explicit list of node permissions. Reads are allowed everywhere; writes only on node
//     routes, where the token's permissions are intersected with the owner's.
//   - "write": the owner's full permissions.
const (
	APITokenScopeRead   = "read"
	APITokenScopeCustom = "custom"
	APITokenScopeWrite  = "write"

	// APITokenPrefix marks a bearer credential as a personal API token rather than a JWT.
	APITokenPrefix = "ahp_"

	APITokenDefaultTTLDays = 90
	APITokenMaxTTLDays     = 365
	APITokenMaxNameLength  = 100
	APITokenMaxActive      = 20
)

var (
	ErrAPITokenNotFound     = errors.New("api token not found")
	ErrAPITokenInvalid      = errors.New("invalid, expired or revoked api token")
	ErrAPITokenNameRequired = errors.New("name is required")
	ErrAPITokenNameTooLong  = fmt.Errorf("name must be at most %d characters", APITokenMaxNameLength)
	ErrAPITokenInvalidScope = errors.New("scope must be 'read', 'custom' or 'write'")
	// ErrAPITokenPermissionsRequired: a custom token needs at least one permission.
	ErrAPITokenPermissionsRequired = errors.New("a custom token needs at least one permission")
	// ErrAPITokenInvalidPermission: unknown permission, or permissions given for a non-custom scope.
	ErrAPITokenInvalidPermission = errors.New("permissions must be node permissions from the catalog, and only for scope 'custom'")
	ErrAPITokenInvalidTTL        = fmt.Errorf("expires_in_days must be between 1 and %d", APITokenMaxTTLDays)
	ErrAPITokenLimitReached      = fmt.Errorf("a user can have at most %d active api tokens", APITokenMaxActive)
)

// APIToken is the stored metadata of a personal API token. The raw token is never stored.
type APIToken struct {
	ID          string `db:"id" json:"id"`
	UserID      string `db:"user_id" json:"-"`
	Name        string `db:"name" json:"name"`
	TokenHash   string `db:"token_hash" json:"-"`
	TokenPrefix string `db:"token_prefix" json:"token_prefix"`
	Scope       string `db:"scope" json:"scope"`
	// Permissions is the node permission list of a custom token (normalized); empty for other scopes.
	Permissions []string   `db:"-" json:"permissions"`
	ExpiresAt   time.Time  `db:"expires_at" json:"expires_at"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	LastUsedAt  *time.Time `db:"last_used_at" json:"last_used_at,omitempty"`
	RevokedAt   *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
}

// CreateAPITokenInput is the request to create a token. ExpiresInDays nil means the default (90 days).
type CreateAPITokenInput struct {
	Name          string   `json:"name"`
	Scope         string   `json:"scope"`
	Permissions   []string `json:"permissions,omitempty"`
	ExpiresInDays *int     `json:"expires_in_days,omitempty"`
}

// CreatedAPIToken is returned once, at creation: it is the only time the raw token is available.
type CreatedAPIToken struct {
	Token    string    `json:"token"`
	APIToken *APIToken `json:"api_token"`
}

// APITokenPrincipal is the identity an authenticated API token acts as.
type APITokenPrincipal struct {
	TokenID string
	Scope   string
	// Permissions are the node permissions the token may use (see EffectivePermissions).
	Permissions []string
	User        *User
}

// EffectivePermissions returns the node permissions a token may use, before intersecting them with its
// owner's: every *:read permission for "read", ["*"] for "write" and the stored list for "custom".
func (t *APIToken) EffectivePermissions() []string {
	switch t.Scope {
	case APITokenScopeWrite:
		return []string{PermWildcard}
	case APITokenScopeCustom:
		return NormalizePermissions(t.Permissions)
	default:
		return ReadNodePermissions()
	}
}

type APITokenRepository interface {
	Create(ctx context.Context, t *APIToken) (*APIToken, error)
	GetByHash(ctx context.Context, tokenHash string) (*APIToken, error)
	ListByUser(ctx context.Context, userID string) ([]*APIToken, error)
	CountActiveByUser(ctx context.Context, userID string) (int, error)
	// Revoke revokes the token only if it belongs to userID; it returns ErrAPITokenNotFound otherwise.
	Revoke(ctx context.Context, userID, tokenID string) error
	TouchLastUsed(ctx context.Context, tokenID string) error
}

// apiTokenUserLookup is the part of AuthRepository the service needs.
type apiTokenUserLookup interface {
	FindUserByID(id string) (*User, error)
}

type APITokenService struct {
	repo  APITokenRepository
	users apiTokenUserLookup
	now   func() time.Time
}

func NewAPITokenService(repo APITokenRepository, users AuthRepository) *APITokenService {
	return &APITokenService{repo: repo, users: users, now: time.Now}
}

// Create generates a token for userID and stores its hash.
func (s *APITokenService) Create(ctx context.Context, userID string, in CreateAPITokenInput) (*CreatedAPIToken, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrAPITokenNameRequired
	}
	if utf8.RuneCountInString(name) > APITokenMaxNameLength {
		return nil, ErrAPITokenNameTooLong
	}
	scope := strings.ToLower(strings.TrimSpace(in.Scope))
	var perms []string
	switch scope {
	case APITokenScopeRead, APITokenScopeWrite:
		if len(in.Permissions) > 0 {
			return nil, ErrAPITokenInvalidPermission
		}
	case APITokenScopeCustom:
		var err error
		if perms, err = validateTokenPermissions(in.Permissions); err != nil {
			return nil, err
		}
	default:
		return nil, ErrAPITokenInvalidScope
	}
	days := APITokenDefaultTTLDays
	if in.ExpiresInDays != nil {
		days = *in.ExpiresInDays
	}
	if days < 1 || days > APITokenMaxTTLDays {
		return nil, ErrAPITokenInvalidTTL
	}

	active, err := s.repo.CountActiveByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if active >= APITokenMaxActive {
		return nil, ErrAPITokenLimitReached
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate api token: %w", err)
	}
	raw := APITokenPrefix + hex.EncodeToString(secret)

	created, err := s.repo.Create(ctx, &APIToken{
		UserID:      userID,
		Name:        name,
		TokenHash:   HashAPIToken(raw),
		TokenPrefix: raw[:len(APITokenPrefix)+8],
		Scope:       scope,
		Permissions: perms,
		ExpiresAt:   s.now().Add(time.Duration(days) * 24 * time.Hour),
	})
	if err != nil {
		return nil, err
	}
	return &CreatedAPIToken{Token: raw, APIToken: created}, nil
}

func (s *APITokenService) List(ctx context.Context, userID string) ([]*APIToken, error) {
	return s.repo.ListByUser(ctx, userID)
}

func (s *APITokenService) Revoke(ctx context.Context, userID, tokenID string) error {
	return s.repo.Revoke(ctx, userID, tokenID)
}

// Authenticate resolves a raw token to its owner. It fails with ErrAPITokenInvalid when the token is
// unknown, revoked or expired, or when its owner no longer exists or is deactivated.
func (s *APITokenService) Authenticate(ctx context.Context, raw string) (*APITokenPrincipal, error) {
	if !strings.HasPrefix(raw, APITokenPrefix) {
		return nil, ErrAPITokenInvalid
	}
	t, err := s.repo.GetByHash(ctx, HashAPIToken(raw))
	if errors.Is(err, ErrAPITokenNotFound) {
		return nil, ErrAPITokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if t.RevokedAt != nil || !s.now().Before(t.ExpiresAt) {
		return nil, ErrAPITokenInvalid
	}
	u, err := s.users.FindUserByID(t.UserID)
	if err != nil {
		return nil, err
	}
	if u == nil || !u.IsActive {
		return nil, ErrAPITokenInvalid
	}

	go func(id string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repo.TouchLastUsed(ctx, id)
	}(t.ID)

	return &APITokenPrincipal{TokenID: t.ID, Scope: t.Scope, Permissions: t.EffectivePermissions(), User: u}, nil
}

// validateTokenPermissions checks a custom token's list: every entry must be a known node permission
// ("*" is not accepted: use scope "write"). It returns the normalized list.
func validateTokenPermissions(perms []string) ([]string, error) {
	if len(perms) == 0 {
		return nil, ErrAPITokenPermissionsRequired
	}
	for _, p := range perms {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == PermWildcard || !slices.Contains(AllNodePermissions, p) {
			return nil, ErrAPITokenInvalidPermission
		}
	}
	return NormalizePermissions(perms), nil
}

// HashAPIToken returns the hex SHA-256 of a raw token, as stored in api_tokens.token_hash.
func HashAPIToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
