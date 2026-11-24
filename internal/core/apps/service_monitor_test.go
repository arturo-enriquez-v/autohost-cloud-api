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

// fakeStatsRepo returns fixed results and records the arguments.
type fakeStatsRepo struct {
	from      time.Time
	bucket    time.Duration
	threshold int
}

func (r *fakeStatsRepo) StatsBuckets(_ string, from time.Time, bucket time.Duration) ([]*MonitorStatsBucket, error) {
	r.from, r.bucket = from, bucket
	return nil, nil
}
func (r *fakeStatsRepo) StatsSummary(string, time.Time) (*MonitorStatsSummary, error) {
	return &MonitorStatsSummary{Checks: 200, Passed: 199, Failed: 1}, nil
}
func (r *fakeStatsRepo) Incidents(_ string, _ time.Time, threshold, _ int) ([]*MonitorIncident, error) {
	r.threshold = threshold
	return []*MonitorIncident{{}, {}}, nil
}

func TestMonitorStats(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	repo := &fakeStatsRepo{}
	svc := NewMonitorStatsService(repo)
	svc.now = func() time.Time { return now }

	stats, err := svc.Stats(&ServiceMonitor{ID: "mon-1", FailureThreshold: 5}, "7d")
	if err != nil {
		t.Fatal(err)
	}
	if repo.from != now.Add(-7*24*time.Hour) || repo.bucket != time.Hour || stats.BucketSeconds != 3600 {
		t.Errorf("7d range: from %s, bucket %s", repo.from, repo.bucket)
	}
	if repo.threshold != 5 {
		t.Errorf("incidents use threshold %d, want the monitor's 5", repo.threshold)
	}
	if p := stats.Summary.UptimePercent; p == nil || *p != 99.5 {
		t.Errorf("uptime = %v, want 99.5", p)
	}
	if stats.Summary.Incidents != 2 || stats.Buckets == nil {
		t.Errorf("incidents = %d, buckets nil = %v", stats.Summary.Incidents, stats.Buckets == nil)
	}

	if _, err := svc.Stats(&ServiceMonitor{ID: "mon-1"}, "90d"); err != ErrInvalidStatsRange {
		t.Errorf("90d: err = %v, want ErrInvalidStatsRange", err)
	}
	// Every range must fit in the retention, or its oldest buckets would always be empty.
	for name, r := range statsRanges {
		if r.Window > HealthEventRetention {
			t.Errorf("range %s (%s) is longer than the retention (%s)", name, r.Window, HealthEventRetention)
		}
	}
}
