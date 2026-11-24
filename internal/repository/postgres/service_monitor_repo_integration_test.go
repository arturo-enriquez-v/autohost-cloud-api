// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
)

// TestServiceMonitorAutoHealEvents runs against a real Postgres (AUTOHOST_DB_INTEGRATION=1 + DATABASE_URL)
// with a throwaway user, node and monitor that are deleted afterwards.
func TestServiceMonitorAutoHealEvents(t *testing.T) {
	if os.Getenv("AUTOHOST_DB_INTEGRATION") != "1" || os.Getenv("DATABASE_URL") == "" {
		t.Skip("set AUTOHOST_DB_INTEGRATION=1 and DATABASE_URL to run")
	}
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var userID, nodeID, monitorID string
	if err := db.Get(&userID, `INSERT INTO users (email, name) VALUES ('autoheal-events@autohost.invalid', 'test') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM users WHERE id = $1`, userID)
	if err := db.Get(&nodeID, `INSERT INTO nodes (hostname) VALUES ('autoheal-events-test') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM nodes WHERE id = $1`, nodeID)
	if err := db.Get(&monitorID, `INSERT INTO service_monitors (node_id, user_id, service_name, check_type)
		VALUES ($1, $2, 'web', 'process') RETURNING id`, nodeID, userID); err != nil {
		t.Fatal(err)
	}

	repo := NewServiceMonitorRepository(db)
	now := time.Now().Truncate(time.Second)
	downtime := 45
	for _, e := range []*apps.AutoHealEvent{
		{Action: apps.AutoHealRestart, Attempt: 1, MaxAttempts: 3, Error: "No such container: web", CreatedAt: now.Add(-40 * 24 * time.Hour)},
		{Action: apps.AutoHealRestart, Attempt: 1, MaxAttempts: 3, CheckMessage: "container state: exited", CreatedAt: now.Add(-time.Minute)},
		{Action: apps.AutoHealRecovered, DowntimeSeconds: &downtime, CreatedAt: now},
	} {
		e.MonitorID, e.NodeID = monitorID, nodeID
		if err := repo.InsertAutoHealEvent(e); err != nil {
			t.Fatal(err)
		}
	}

	events, err := repo.ListAutoHealEvents(monitorID, now.Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Action != apps.AutoHealRecovered || events[1].Action != apps.AutoHealRestart {
		t.Fatalf("events in the last day = %+v, want recovered then restart", events)
	}
	if d := events[0].DowntimeSeconds; d == nil || *d != 45 {
		t.Errorf("downtime = %v, want 45", d)
	}
	if events[1].CheckMessage != "container state: exited" || events[1].MaxAttempts != 3 {
		t.Errorf("restart event = %+v", events[1])
	}

	if _, err := repo.PurgeEventsBefore(context.Background(), now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.Get(&left, `SELECT count(*) FROM service_autoheal_events WHERE monitor_id = $1`, monitorID); err != nil {
		t.Fatal(err)
	}
	if left != 2 {
		t.Errorf("%d events left after purging those older than 30 days, want 2", left)
	}
}
