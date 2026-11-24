// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"context"
	"testing"
	"time"
)

// purgeRecorder records the cutoff passed to PurgeEventsBefore.
type purgeRecorder struct {
	ServiceMonitorRepository
	cutoff time.Time
}

func (r *purgeRecorder) PurgeEventsBefore(_ context.Context, cutoff time.Time) (int64, error) {
	r.cutoff = cutoff
	return 3, nil
}

func TestPurgeOldEvents_KeepsRetentionWindow(t *testing.T) {
	repo := &purgeRecorder{}
	n, err := NewServiceMonitorService(repo).PurgeOldEvents(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("PurgeOldEvents = %d, %v", n, err)
	}
	want := time.Now().Add(-HealthEventRetention)
	if d := repo.cutoff.Sub(want); d < -time.Minute || d > time.Minute {
		t.Errorf("cutoff = %s, want about %s", repo.cutoff, want)
	}
}

func TestServiceMonitor_IsStale(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	tests := []struct {
		name string
		m    ServiceMonitor
		want bool
	}{
		{"recent result", ServiceMonitor{Enabled: true, IntervalSeconds: 30, UpdatedAt: *ago(40 * time.Second), LastCheckedAt: ago(40 * time.Second)}, false},
		{"node offline", ServiceMonitor{Enabled: true, IntervalSeconds: 30, UpdatedAt: *ago(10 * time.Minute), LastCheckedAt: ago(10 * time.Minute)}, true},
		{"disabled", ServiceMonitor{Enabled: false, IntervalSeconds: 30, UpdatedAt: *ago(time.Hour)}, false},
		{"just re-enabled", ServiceMonitor{Enabled: true, IntervalSeconds: 30, UpdatedAt: *ago(5 * time.Second), LastCheckedAt: ago(time.Hour)}, false},
		{"never checked", ServiceMonitor{Enabled: true, IntervalSeconds: 60, UpdatedAt: *ago(time.Hour)}, true},
		{"long interval", ServiceMonitor{Enabled: true, IntervalSeconds: 3600, UpdatedAt: *ago(2 * time.Hour), LastCheckedAt: ago(2 * time.Hour)}, false},
	}
	for _, tt := range tests {
		if got := tt.m.IsStale(now); got != tt.want {
			t.Errorf("%s: IsStale = %v, want %v", tt.name, got, tt.want)
		}
	}
}
