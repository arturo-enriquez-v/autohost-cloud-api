// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"fmt"
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// TestSessionLimitAndPruning runs against a real Postgres (AUTOHOST_DB_INTEGRATION=1 + DATABASE_URL) using a
// throwaway user that is deleted afterwards (refresh tokens cascade).
func TestSessionLimitAndPruning(t *testing.T) {
	if os.Getenv("AUTOHOST_DB_INTEGRATION") != "1" || os.Getenv("DATABASE_URL") == "" {
		t.Skip("set AUTOHOST_DB_INTEGRATION=1 and DATABASE_URL to run")
	}
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var userID string
	if err := db.Get(&userID, `INSERT INTO users (email, name) VALUES ('session-limit-test@autohost.invalid', 'test') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM users WHERE id = $1`, userID)

	// Old rows that should be pruned: expired long ago, and revoked long ago.
	if _, err := db.Exec(`
		INSERT INTO refresh_tokens (user_id, token_hash, created_at, expires_at)
		VALUES ($1, 'old-expired', now() - interval '40 days', now() - interval '10 days')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO refresh_tokens (user_id, token_hash, created_at, revoked_at, expires_at)
		VALUES ($1, 'old-revoked', now() - interval '3 days', now() - interval '2 days', now() + interval '20 days')`, userID); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MAX_SESSIONS_PER_USER", "5")
	repo := NewAuthRepository(db)
	for i := 0; i < 8; i++ {
		if err := repo.StoreRefreshToken(userID, fmt.Sprintf("hash-%d", i), "test", ""); err != nil {
			t.Fatal(err)
		}
	}

	var active, total int
	_ = db.Get(&active, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	_ = db.Get(&total, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, userID)
	if active != 5 {
		t.Fatalf("expected 5 active sessions, got %d", active)
	}
	var newestActive int
	_ = db.Get(&newestActive, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL
		AND token_hash IN ('hash-3','hash-4','hash-5','hash-6','hash-7')`, userID)
	if newestActive != 5 {
		t.Fatalf("the newest sessions must be the ones kept, got %d of 5", newestActive)
	}
	var stale int
	_ = db.Get(&stale, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND token_hash IN ('old-expired','old-revoked')`, userID)
	if stale != 0 {
		t.Fatalf("old expired/revoked tokens must be pruned, %d left", stale)
	}
	if total != 8 { // 5 active + 3 just-revoked (kept for a day)
		t.Fatalf("expected 8 rows (5 active + 3 recently revoked), got %d", total)
	}
}
