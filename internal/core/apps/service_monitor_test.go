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
