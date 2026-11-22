// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
)

// TestAPITokenRepository runs against a real Postgres (AUTOHOST_DB_INTEGRATION=1 + DATABASE_URL) using
// throwaway users that are deleted afterwards (tokens cascade).
func TestAPITokenRepository(t *testing.T) {
	if os.Getenv("AUTOHOST_DB_INTEGRATION") != "1" || os.Getenv("DATABASE_URL") == "" {
		t.Skip("set AUTOHOST_DB_INTEGRATION=1 and DATABASE_URL to run")
	}
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	var owner, other string
	if err := db.Get(&owner, `INSERT INTO users (email, name) VALUES ('api-token-owner@autohost.invalid', 'test') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM users WHERE id = $1`, owner)
	if err := db.Get(&other, `INSERT INTO users (email, name) VALUES ('api-token-other@autohost.invalid', 'test') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM users WHERE id = $1`, other)

	repo := NewAPITokenRepository(db)
	created, err := repo.Create(ctx, &identity.APIToken{
		UserID: owner, Name: "mcp", TokenHash: "hash-1", TokenPrefix: "ahp_1234", Scope: "read",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	expired, err := repo.Create(ctx, &identity.APIToken{
		UserID: owner, Name: "old", TokenHash: "hash-2", TokenPrefix: "ahp_5678", Scope: "write",
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetByHash(ctx, "hash-1")
	if err != nil || got.ID != created.ID || got.UserID != owner || got.Scope != "read" {
		t.Fatalf("GetByHash = %+v, %v", got, err)
	}
	if _, err := repo.GetByHash(ctx, "missing"); !errors.Is(err, identity.ErrAPITokenNotFound) {
		t.Fatalf("GetByHash(missing) = %v, want ErrAPITokenNotFound", err)
	}

	if n, err := repo.CountActiveByUser(ctx, owner); err != nil || n != 1 {
		t.Fatalf("CountActiveByUser = %d, %v; want 1 (expired tokens do not count)", n, err)
	}
	if list, err := repo.ListByUser(ctx, owner); err != nil || len(list) != 2 {
		t.Fatalf("ListByUser(owner) = %d tokens, %v; want 2", len(list), err)
	}
	if list, err := repo.ListByUser(ctx, other); err != nil || len(list) != 0 {
		t.Fatalf("ListByUser(other) = %d tokens, %v; want 0", len(list), err)
	}

	if err := repo.TouchLastUsed(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetByHash(ctx, "hash-1"); got.LastUsedAt == nil {
		t.Fatal("TouchLastUsed did not set last_used_at")
	}

	for name, id := range map[string]string{"other user's token": created.ID, "malformed id": "not-a-uuid"} {
		if err := repo.Revoke(ctx, other, id); !errors.Is(err, identity.ErrAPITokenNotFound) {
			t.Errorf("Revoke(%s) = %v, want ErrAPITokenNotFound", name, err)
		}
	}
	if err := repo.Revoke(ctx, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Revoke(ctx, owner, created.ID); !errors.Is(err, identity.ErrAPITokenNotFound) {
		t.Fatalf("second Revoke = %v, want ErrAPITokenNotFound", err)
	}
	if got, _ := repo.GetByHash(ctx, "hash-1"); got.RevokedAt == nil {
		t.Fatal("Revoke did not set revoked_at")
	}
	_ = expired
}
