// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// refreshRepo extends mockAuthRepo with single-use refresh token semantics, mirroring the atomic
// UPDATE ... RETURNING in the Postgres repository.
type refreshRepo struct {
	*mockAuthRepo
	active map[string]string // tokenHash -> userID
}

func (r *refreshRepo) StoreRefreshToken(userID, tokenHash, _, _ string) error {
	r.active[tokenHash] = userID
	return nil
}

func (r *refreshRepo) ConsumeRefreshToken(tokenHash string) (string, error) {
	uid, ok := r.active[tokenHash]
	if !ok {
		return "", errors.New("not found")
	}
	delete(r.active, tokenHash)
	return uid, nil
}

func TestRefreshTokens_SingleUse(t *testing.T) {
	os.Setenv("JWT_SECRET", "test-secret-refresh")
	base := newMockAuthRepo()
	base.users["a@test"] = &User{ID: "u1", Email: "a@test", Role: RoleMember, IsActive: true}
	repo := &refreshRepo{mockAuthRepo: base, active: map[string]string{}}
	svc := NewAuthService(repo)

	plain, hash := platform.MakeRefreshPair()
	repo.active[hash] = "u1"

	now := time.Unix(2_000_000, 0)
	svc.now = func() time.Time { return now }

	pair, err := svc.RefreshTokens(plain, "ua", "127.0.0.1")
	if err != nil || pair.RefreshToken == "" || pair.RefreshToken == plain {
		t.Fatalf("first refresh should rotate the token, got %+v, %v", pair, err)
	}

	// A concurrent request presenting the same token within the grace period gets the same pair,
	// instead of failing and making the frontend clear the session cookies.
	now = now.Add(5 * time.Second)
	again, err := svc.RefreshTokens(plain, "ua", "127.0.0.1")
	if err != nil || again != pair {
		t.Fatalf("re-presenting within the grace period should return the same pair, got %+v, %v", again, err)
	}

	// After the grace period the consumed token is dead.
	now = now.Add(refreshGracePeriod)
	if _, err := svc.RefreshTokens(plain, "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("reusing a consumed refresh token after the grace period must fail, got %v", err)
	}
	if _, err := svc.RefreshTokens(pair.RefreshToken, "ua", "127.0.0.1"); err != nil {
		t.Fatalf("the rotated token should work once: %v", err)
	}
}

func TestRefreshTokens_DeactivatedUserRejected(t *testing.T) {
	os.Setenv("JWT_SECRET", "test-secret-refresh")
	base := newMockAuthRepo()
	base.users["b@test"] = &User{ID: "u2", Email: "b@test", Role: RoleMember, IsActive: false}
	repo := &refreshRepo{mockAuthRepo: base, active: map[string]string{}}
	svc := NewAuthService(repo)

	plain, hash := platform.MakeRefreshPair()
	repo.active[hash] = "u2"
	if _, err := svc.RefreshTokens(plain, "ua", ""); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("deactivated user must not refresh, got %v", err)
	}
}
