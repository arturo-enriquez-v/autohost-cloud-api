// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
)

// ReportTrigger can build and push a node status report for a given user.
type ReportTrigger interface {
	BuildAndPushForUser(ctx context.Context, userID string) (map[string]any, error)
}

// ReportScheduler checks every minute whether any users have a scheduled report
// for the current UTC hour and triggers it via ReportTrigger.
type ReportScheduler struct {
	settingsSvc *identity.ReportSettingsService
	trigger     ReportTrigger
}

func NewReportScheduler(settingsSvc *identity.ReportSettingsService, trigger ReportTrigger) *ReportScheduler {
	return &ReportScheduler{
		settingsSvc: settingsSvc,
		trigger:     trigger,
	}
}

// Start runs the report scheduler loop until the context is cancelled.
// It aligns to the top of each UTC hour before firing.
func (s *ReportScheduler) Start(ctx context.Context) {
	now := time.Now().UTC()
	nextHour := now.Truncate(time.Hour).Add(time.Hour)
	delay := nextHour.Sub(now)

	log.Printf("[report-scheduler] started, next run at %s UTC", nextHour.Format("15:04"))

	timer := time.NewTimer(delay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[report-scheduler] shutting down")
			return
		case t := <-timer.C:
			s.runHour(ctx, t.UTC().Hour())
			// Schedule next top-of-hour
			next := t.UTC().Truncate(time.Hour).Add(time.Hour)
			timer.Reset(next.Sub(time.Now().UTC()))
		}
	}
}

func (s *ReportScheduler) runHour(ctx context.Context, hourUTC int) {
	users, err := s.settingsSvc.ListEnabledAtHour(hourUTC)
	if err != nil {
		log.Printf("[report-scheduler] error fetching users for hour %d: %v", hourUTC, err)
		return
	}

	if len(users) == 0 {
		return
	}

	log.Printf("[report-scheduler] hour=%02d:00 UTC — sending reports for %d user(s)", hourUTC, len(users))

	for _, u := range users {
		_, err := s.trigger.BuildAndPushForUser(ctx, u.UserID)
		if err != nil {
			log.Printf("[report-scheduler] error building report for user %s: %v", u.UserID, err)
		}
	}
}
