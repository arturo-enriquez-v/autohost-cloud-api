// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// TestMonitorStatsRepository runs against a real Postgres (AUTOHOST_DB_INTEGRATION=1 + DATABASE_URL) with a
// throwaway user, node and monitor that are deleted afterwards.
func TestMonitorStatsRepository(t *testing.T) {
	if os.Getenv("AUTOHOST_DB_INTEGRATION") != "1" || os.Getenv("DATABASE_URL") == "" {
		t.Skip("set AUTOHOST_DB_INTEGRATION=1 and DATABASE_URL to run")
	}
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var userID, nodeID, monitorID string
	if err := db.Get(&userID, `INSERT INTO users (email, name) VALUES ('monitor-stats@autohost.invalid', 'test') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM users WHERE id = $1`, userID)
	if err := db.Get(&nodeID, `INSERT INTO nodes (hostname) VALUES ('monitor-stats-test') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM nodes WHERE id = $1`, nodeID)
	if err := db.Get(&monitorID, `INSERT INTO service_monitors (node_id, user_id, service_name, check_type)
		VALUES ($1, $2, 'web', 'http') RETURNING id`, nodeID, userID); err != nil {
		t.Fatal(err)
	}

	// 240 checks every 30 s over the last 2 hours; i counts back from now (0 = latest check):
	//   i 100-103: 4 failures -> an incident (threshold 3), resolved by i = 99
	//   i 50-51:   2 failures -> below the threshold, not an incident
	//   i 0-2:     3 failures -> an ongoing incident
	if _, err := db.Exec(`
		INSERT INTO service_health_events (monitor_id, node_id, user_id, status, latency_ms, message, checked_at)
		SELECT $1, $2, $3,
		       CASE WHEN i BETWEEN 100 AND 103 OR i BETWEEN 50 AND 51 OR i <= 2 THEN 'down' ELSE 'up' END,
		       CASE WHEN i BETWEEN 100 AND 103 OR i BETWEEN 50 AND 51 OR i <= 2 THEN 10000 ELSE 20 END,
		       CASE WHEN i BETWEEN 100 AND 103 THEN 'status 502 (expected 200)'
		            WHEN i <= 2 THEN 'http error: timeout' ELSE 'HTTP 200' END,
		       NOW() - make_interval(secs => i * 30)
		FROM generate_series(0, 239) i`, monitorID, nodeID, userID); err != nil {
		t.Fatal(err)
	}

	repo := NewMonitorStatsRepository(db)
	from := time.Now().Add(-24 * time.Hour)

	summary, err := repo.StatsSummary(monitorID, from)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Checks != 240 || summary.Failed != 9 || summary.Passed != 231 {
		t.Errorf("summary = %+v, want 240 checks, 231 passed, 9 failed", summary)
	}
	// Failed checks (10 s timeouts) must not count towards latency.
	if summary.AvgLatencyMs == nil || *summary.AvgLatencyMs != 20 || summary.P95LatencyMs == nil || *summary.P95LatencyMs != 20 {
		t.Errorf("latency = %v / %v, want 20 / 20", summary.AvgLatencyMs, summary.P95LatencyMs)
	}

	buckets, err := repo.StatsBuckets(monitorID, from, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(buckets); n < 96 || n > 97 {
		t.Errorf("got %d 15-minute buckets over 24 h, want 96 or 97", n)
	}
	passed, failed := 0, 0
	for i, b := range buckets {
		passed += b.Passed
		failed += b.Failed
		if i > 0 && b.Bucket.Sub(buckets[i-1].Bucket) != 15*time.Minute {
			t.Fatalf("buckets %d and %d are %s apart", i-1, i, b.Bucket.Sub(buckets[i-1].Bucket))
		}
		if b.Passed == 0 && b.AvgLatencyMs != nil {
			t.Errorf("bucket %s has latency without passed checks", b.Bucket)
		}
	}
	if passed != 231 || failed != 9 {
		t.Errorf("buckets add up to %d passed / %d failed, want 231 / 9", passed, failed)
	}

	incidents, err := repo.Incidents(monitorID, from, 3, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 2 {
		t.Fatalf("got %d incidents, want 2: %+v", len(incidents), incidents)
	}
	ongoing, resolved := incidents[0], incidents[1]
	if ongoing.FailedChecks != 3 || ongoing.ResolvedAt != nil || ongoing.Message != "http error: timeout" {
		t.Errorf("ongoing incident = %+v", ongoing)
	}
	if resolved.FailedChecks != 4 || resolved.ResolvedAt == nil || resolved.Message != "status 502 (expected 200)" {
		t.Errorf("resolved incident = %+v", resolved)
	}
	if resolved.ResolvedAt != nil {
		if d := resolved.ResolvedAt.Sub(resolved.LastFailedAt); d != 30*time.Second {
			t.Errorf("resolved %s after the last failure, want 30s", d)
		}
	}
}
