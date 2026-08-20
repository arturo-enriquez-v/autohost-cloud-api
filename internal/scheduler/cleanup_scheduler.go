package scheduler

import (
	"context"
	"log"
	"time"

	logdiagnostic "github.com/arturo/autohost-cloud-api/internal/domain/log_diagnostic"
)

// DiagnosticCleanupScheduler runs periodic background cleanups for expired diagnostics
// based on the retention policy:
// - pending / rejected: older than 48 hours
// - approved: older than 30 days
type DiagnosticCleanupScheduler struct {
	diagnosticSvc *logdiagnostic.Service
}

func NewDiagnosticCleanupScheduler(diagnosticSvc *logdiagnostic.Service) *DiagnosticCleanupScheduler {
	return &DiagnosticCleanupScheduler{
		diagnosticSvc: diagnosticSvc,
	}
}

func (s *DiagnosticCleanupScheduler) Start(ctx context.Context) {
	// Run cleanup once on startup
	s.runCleanup(ctx)

	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	log.Println("[cleanup-scheduler] started, checking every 6 hours for expired diagnostics")

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

func (s *DiagnosticCleanupScheduler) runCleanup(ctx context.Context) {
	if s.diagnosticSvc == nil {
		return
	}
	deleted, err := s.diagnosticSvc.CleanupExpired(ctx)
	if err != nil {
		log.Printf("[cleanup-scheduler] error cleaning up expired diagnostics: %v", err)
	} else if deleted > 0 {
		log.Printf("[cleanup-scheduler] purged %d expired log diagnostic record(s)", deleted)
	}
}
