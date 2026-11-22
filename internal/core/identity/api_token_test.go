// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAPITokenRepo struct {
	mu     sync.Mutex
	tokens map[string]*APIToken // id -> token
	nextID int
}

func newFakeAPITokenRepo() *fakeAPITokenRepo {
	return &fakeAPITokenRepo{tokens: map[string]*APIToken{}}
}

func (r *fakeAPITokenRepo) Create(_ context.Context, t *APIToken) (*APIToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	c := *t
	c.ID = string(rune('a' + r.nextID))
	c.CreatedAt = time.Now()
	r.tokens[c.ID] = &c
	return &c, nil
}

func (r *fakeAPITokenRepo) GetByHash(_ context.Context, h string) (*APIToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tokens {
		if t.TokenHash == h {
			c := *t
			return &c, nil
		}
	}
	return nil, ErrAPITokenNotFound
}

func (r *fakeAPITokenRepo) ListByUser(_ context.Context, userID string) ([]*APIToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*APIToken
	for _, t := range r.tokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *fakeAPITokenRepo) CountActiveByUser(_ context.Context, userID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range r.tokens {
		if t.UserID == userID && t.RevokedAt == nil {
			n++
		}
	}
	return n, nil
}

func (r *fakeAPITokenRepo) Revoke(_ context.Context, userID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tokens[id]
	if !ok || t.UserID != userID || t.RevokedAt != nil {
		return ErrAPITokenNotFound
	}
	now := time.Now()
	t.RevokedAt = &now
	return nil
}

func (r *fakeAPITokenRepo) TouchLastUsed(context.Context, string) error { return nil }

type fakeUserLookup map[string]*User

func (f fakeUserLookup) FindUserByID(id string) (*User, error) { return f[id], nil }

func newTestAPITokenService(users fakeUserLookup) (*APITokenService, *fakeAPITokenRepo) {
	repo := newFakeAPITokenRepo()
	return &APITokenService{repo: repo, users: users, now: time.Now}, repo
}

func intPtr(v int) *int { return &v }

func TestAPITokenCreateValidatesInput(t *testing.T) {
	svc, _ := newTestAPITokenService(nil)
	cases := []struct {
		name string
		in   CreateAPITokenInput
		want error
	}{
		{"empty name", CreateAPITokenInput{Name: "  ", Scope: "read"}, ErrAPITokenNameRequired},
		{"long name", CreateAPITokenInput{Name: strings.Repeat("x", 101), Scope: "read"}, ErrAPITokenNameTooLong},
		{"bad scope", CreateAPITokenInput{Name: "mcp", Scope: "admin"}, ErrAPITokenInvalidScope},
		{"zero ttl", CreateAPITokenInput{Name: "mcp", Scope: "read", ExpiresInDays: intPtr(0)}, ErrAPITokenInvalidTTL},
		{"ttl too long", CreateAPITokenInput{Name: "mcp", Scope: "read", ExpiresInDays: intPtr(366)}, ErrAPITokenInvalidTTL},
	}
	for _, tc := range cases {
		if _, err := svc.Create(context.Background(), "u1", tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestAPITokenCreateStoresOnlyTheHash(t *testing.T) {
	svc, repo := newTestAPITokenService(nil)
	created, err := svc.Create(context.Background(), "u1", CreateAPITokenInput{Name: " mcp ", Scope: "READ"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, APITokenPrefix) || len(created.Token) != len(APITokenPrefix)+64 {
		t.Fatalf("unexpected token format %q", created.Token)
	}
	stored := repo.tokens[created.APIToken.ID]
	if stored.TokenHash != HashAPIToken(created.Token) || strings.Contains(stored.TokenHash, created.Token) {
		t.Fatal("the repository must receive the hash, never the raw token")
	}
	if !strings.HasPrefix(created.Token, stored.TokenPrefix) || len(stored.TokenPrefix) >= len(created.Token) {
		t.Fatalf("prefix %q must be a short leading part of the token", stored.TokenPrefix)
	}
	if stored.Name != "mcp" || stored.Scope != APITokenScopeRead || stored.UserID != "u1" {
		t.Fatalf("unexpected stored token %+v", stored)
	}
	wantExpiry := time.Now().Add(APITokenDefaultTTLDays * 24 * time.Hour)
	if d := stored.ExpiresAt.Sub(wantExpiry); d > time.Minute || d < -time.Minute {
		t.Fatalf("default expiry = %v, want about %v", stored.ExpiresAt, wantExpiry)
	}
}

func TestAPITokenCreateEnforcesActiveLimit(t *testing.T) {
	svc, _ := newTestAPITokenService(nil)
	for i := 0; i < APITokenMaxActive; i++ {
		if _, err := svc.Create(context.Background(), "u1", CreateAPITokenInput{Name: "t", Scope: "read"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Create(context.Background(), "u1", CreateAPITokenInput{Name: "t", Scope: "read"}); !errors.Is(err, ErrAPITokenLimitReached) {
		t.Fatalf("got %v, want ErrAPITokenLimitReached", err)
	}
	if _, err := svc.Create(context.Background(), "u2", CreateAPITokenInput{Name: "t", Scope: "read"}); err != nil {
		t.Fatalf("the limit is per user: %v", err)
	}
}

func TestAPITokenAuthenticate(t *testing.T) {
	users := fakeUserLookup{
		"u1": {ID: "u1", Email: "a@x", Role: RoleAdmin, IsActive: true},
		"u2": {ID: "u2", Email: "b@x", Role: RoleMember, IsActive: false},
	}
	svc, repo := newTestAPITokenService(users)
	ctx := context.Background()

	valid, _ := svc.Create(ctx, "u1", CreateAPITokenInput{Name: "ok", Scope: "write"})
	p, err := svc.Authenticate(ctx, valid.Token)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if p.User.ID != "u1" || p.Scope != APITokenScopeWrite || p.TokenID != valid.APIToken.ID {
		t.Fatalf("unexpected principal %+v", p)
	}

	revoked, _ := svc.Create(ctx, "u1", CreateAPITokenInput{Name: "revoked", Scope: "read"})
	if err := svc.Revoke(ctx, "u1", revoked.APIToken.ID); err != nil {
		t.Fatal(err)
	}
	expired, _ := svc.Create(ctx, "u1", CreateAPITokenInput{Name: "expired", Scope: "read"})
	repo.tokens[expired.APIToken.ID].ExpiresAt = time.Now().Add(-time.Second)
	deactivated, _ := svc.Create(ctx, "u2", CreateAPITokenInput{Name: "inactive owner", Scope: "read"})
	deleted, _ := svc.Create(ctx, "gone", CreateAPITokenInput{Name: "deleted owner", Scope: "read"})

	for name, raw := range map[string]string{
		"revoked":             revoked.Token,
		"expired":             expired.Token,
		"deactivated owner":   deactivated.Token,
		"deleted owner":       deleted.Token,
		"unknown":             APITokenPrefix + strings.Repeat("0", 64),
		"jwt-looking garbage": "eyJhbGciOi.x.y",
		"tampered":            valid.Token[:len(valid.Token)-1] + "x",
	} {
		if _, err := svc.Authenticate(ctx, raw); !errors.Is(err, ErrAPITokenInvalid) {
			t.Errorf("%s: got %v, want ErrAPITokenInvalid", name, err)
		}
	}
}

func TestAPITokenRevokeIsScopedToOwner(t *testing.T) {
	svc, _ := newTestAPITokenService(fakeUserLookup{"u1": {ID: "u1", IsActive: true}})
	ctx := context.Background()
	created, _ := svc.Create(ctx, "u1", CreateAPITokenInput{Name: "mine", Scope: "read"})

	if err := svc.Revoke(ctx, "intruder", created.APIToken.ID); !errors.Is(err, ErrAPITokenNotFound) {
		t.Fatalf("revoking another user's token: got %v, want ErrAPITokenNotFound", err)
	}
	if _, err := svc.Authenticate(ctx, created.Token); err != nil {
		t.Fatalf("token must stay valid after a foreign revoke attempt: %v", err)
	}
}
