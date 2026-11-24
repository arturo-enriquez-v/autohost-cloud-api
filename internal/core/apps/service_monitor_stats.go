// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"errors"
	"time"
)

// ErrInvalidStatsRange is returned for a stats range other than 1d, 7d or 30d.
var ErrInvalidStatsRange = errors.New("range must be 1d, 7d or 30d")

// StatsRange is a window of monitor statistics and the size of its buckets.
type StatsRange struct {
	Window time.Duration
	Bucket time.Duration
}

// statsRanges match the node metric ranges of the web client. Every window fits in HealthEventRetention.
var statsRanges = map[string]StatsRange{
	"1d":  {Window: 24 * time.Hour, Bucket: 15 * time.Minute},
	"7d":  {Window: 7 * 24 * time.Hour, Bucket: time.Hour},
	"30d": {Window: 30 * 24 * time.Hour, Bucket: 6 * time.Hour},
}

// maxIncidents bounds the incidents returned for a range.
const maxIncidents = 100

// MonitorStatsBucket aggregates the checks of one time bucket. Latencies only count passed checks: a failed
// check's duration is usually a timeout.
type MonitorStatsBucket struct {
	Bucket       time.Time `db:"bucket"         json:"bucket"`
	Passed       int       `db:"passed"         json:"passed"`
	Failed       int       `db:"failed"         json:"failed"`
	AvgLatencyMs *float64  `db:"avg_latency_ms" json:"avg_latency_ms"`
	P95LatencyMs *float64  `db:"p95_latency_ms" json:"p95_latency_ms"`
}

// MonitorStatsSummary aggregates every check in the range.
type MonitorStatsSummary struct {
	Checks        int      `db:"checks"         json:"checks"`
	Passed        int      `db:"passed"         json:"passed"`
	Failed        int      `db:"failed"         json:"failed"`
	UptimePercent *float64 `db:"-"              json:"uptime_percent"`
	AvgLatencyMs  *float64 `db:"avg_latency_ms" json:"avg_latency_ms"`
	P95LatencyMs  *float64 `db:"p95_latency_ms" json:"p95_latency_ms"`
	Incidents     int      `db:"-"              json:"incidents"`
}

// MonitorStats is a monitor's history over a range, for charts.
type MonitorStats struct {
	Range         string                `json:"range"`
	From          time.Time             `json:"from"`
	BucketSeconds int                   `json:"bucket_seconds"`
	Summary       MonitorStatsSummary   `json:"summary"`
	Buckets       []*MonitorStatsBucket `json:"buckets"`
}

// MonitorIncident is a run of failed checks long enough to mark the monitor down (its failure threshold).
type MonitorIncident struct {
	StartedAt    time.Time  `db:"started_at"     json:"started_at"`
	LastFailedAt time.Time  `db:"last_failed_at" json:"last_failed_at"`
	ResolvedAt   *time.Time `db:"resolved_at"    json:"resolved_at"` // first passed check after it; null while ongoing
	FailedChecks int        `db:"failed_checks"  json:"failed_checks"`
	Message      string     `db:"message"        json:"message"` // detail of the first failed check
}

// MonitorStatsRepository reads aggregated health events.
type MonitorStatsRepository interface {
	StatsBuckets(monitorID string, from time.Time, bucket time.Duration) ([]*MonitorStatsBucket, error)
	StatsSummary(monitorID string, from time.Time) (*MonitorStatsSummary, error)
	// Incidents returns runs of at least threshold failed checks that started at or after from, newest
	// first.
	Incidents(monitorID string, from time.Time, threshold, limit int) ([]*MonitorIncident, error)
}

// ParseStatsRange returns the range named 1d, 7d or 30d.
func ParseStatsRange(name string) (StatsRange, error) {
	r, ok := statsRanges[name]
	if !ok {
		return StatsRange{}, ErrInvalidStatsRange
	}
	return r, nil
}

// MonitorStatsService builds monitor statistics.
type MonitorStatsService struct {
	repo MonitorStatsRepository
	now  func() time.Time
}

func NewMonitorStatsService(repo MonitorStatsRepository) *MonitorStatsService {
	return &MonitorStatsService{repo: repo, now: time.Now}
}

// Stats returns the summary and buckets of a monitor over the named range.
func (s *MonitorStatsService) Stats(m *ServiceMonitor, rangeName string) (*MonitorStats, error) {
	r, err := ParseStatsRange(rangeName)
	if err != nil {
		return nil, err
	}
	from := s.now().Add(-r.Window)

	buckets, err := s.repo.StatsBuckets(m.ID, from, r.Bucket)
	if err != nil {
		return nil, err
	}
	summary, err := s.repo.StatsSummary(m.ID, from)
	if err != nil {
		return nil, err
	}
	if summary.Checks > 0 {
		p := float64(summary.Passed) / float64(summary.Checks) * 100
		summary.UptimePercent = &p
	}
	incidents, err := s.repo.Incidents(m.ID, from, incidentThreshold(m), maxIncidents)
	if err != nil {
		return nil, err
	}
	summary.Incidents = len(incidents)
	if buckets == nil {
		buckets = []*MonitorStatsBucket{}
	}

	return &MonitorStats{
		Range:         rangeName,
		From:          from,
		BucketSeconds: int(r.Bucket.Seconds()),
		Summary:       *summary,
		Buckets:       buckets,
	}, nil
}

// Incidents returns the monitor's incidents over the named range, newest first.
func (s *MonitorStatsService) Incidents(m *ServiceMonitor, rangeName string) ([]*MonitorIncident, error) {
	r, err := ParseStatsRange(rangeName)
	if err != nil {
		return nil, err
	}
	incidents, err := s.repo.Incidents(m.ID, s.now().Add(-r.Window), incidentThreshold(m), maxIncidents)
	if err != nil {
		return nil, err
	}
	if incidents == nil {
		incidents = []*MonitorIncident{}
	}
	return incidents, nil
}

func incidentThreshold(m *ServiceMonitor) int {
	if m.FailureThreshold > 0 {
		return m.FailureThreshold
	}
	return 3
}
