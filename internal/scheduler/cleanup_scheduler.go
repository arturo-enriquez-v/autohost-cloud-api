// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
)

// CleanupScheduler periodically deletes expired data:
// - log diagnostics: pending / rejected after 48 hours, approved after 30 days
// - service health events: after apps.HealthEventRetention
type CleanupScheduler struct {
	diagnosticSvc *node.LogDiagnosticService
	monitorSvc    *apps.ServiceMonitorService
}

func NewCleanupScheduler(diagnosticSvc *node.LogDiagnosticService, monitorSvc *apps.ServiceMonitorService) *CleanupScheduler {
	return &CleanupScheduler{diagnosticSvc: diagnosticSvc, monitorSvc: monitorSvc}
}

func (s *CleanupScheduler) Start(ctx context.Context) {
	// Run cleanup once on startup
	s.runCleanup(ctx)

	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	log.Println("[cleanup-scheduler] started, checking every 6 hours for expired data")

	for {
		select {
		case <-ctx.Done():
			log.Println("[cleanup-scheduler] shutting down")
			return
		case <-ticker.C:
			s.runCleanup(ctx)
		}
	}
}

func (s *CleanupScheduler) runCleanup(ctx context.Context) {
	if s.diagnosticSvc != nil {
		deleted, err := s.diagnosticSvc.CleanupExpired(ctx)
		if err != nil {
			log.Printf("[cleanup-scheduler] error cleaning up expired diagnostics: %v", err)
		} else if deleted > 0 {
			log.Printf("[cleanup-scheduler] purged %d expired log diagnostic record(s)", deleted)
		}
	}
	if s.monitorSvc != nil {
		deleted, err := s.monitorSvc.PurgeOldEvents(ctx)
		if err != nil {
			log.Printf("[cleanup-scheduler] error purging old health events: %v", err)
		} else if deleted > 0 {
			log.Printf("[cleanup-scheduler] purged %d old service health event(s)", deleted)
		}
	}
}
