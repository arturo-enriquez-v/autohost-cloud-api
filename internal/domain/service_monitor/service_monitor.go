package servicemonitor

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned when no service monitor exists with the given ID.
var ErrNotFound = errors.New("service monitor not found")

// Check types
const (
	CheckTypeProcess = "process"
	CheckTypeTCP     = "tcp"
	CheckTypeHTTP    = "http"
)

// Monitor statuses
const (
	StatusHealthy  = "healthy"
	StatusDegraded = "degraded"
	StatusDown     = "down"
	StatusUnknown  = "unknown"
)

// ServiceMonitor holds the configuration for monitoring a single service on a node.
type ServiceMonitor struct {
	ID                  string     `db:"id"                   json:"id"`
	NodeID              string     `db:"node_id"              json:"node_id"`
	UserID              string     `db:"user_id"              json:"user_id"`
	ServiceName         string     `db:"service_name"         json:"service_name"`
	CheckType           string     `db:"check_type"           json:"check_type"`
	HTTPURL             string     `db:"http_url"             json:"http_url"`
	HTTPExpectedStatus  int        `db:"http_expected_status" json:"http_expected_status"`
	TCPHost             string     `db:"tcp_host"             json:"tcp_host"`
	TCPPort             int        `db:"tcp_port"             json:"tcp_port"`
	IntervalSeconds     int        `db:"interval_seconds"     json:"interval_seconds"`
	FailureThreshold    int        `db:"failure_threshold"    json:"failure_threshold"`
	NotifyTelegram      bool       `db:"notify_telegram"      json:"notify_telegram"`
	Enabled             bool       `db:"enabled"              json:"enabled"`
	Status              string     `db:"status"               json:"status"`
	ConsecutiveFailures int        `db:"consecutive_failures" json:"consecutive_failures"`
	LastCheckedAt       *time.Time `db:"last_checked_at"      json:"last_checked_at"`
	CreatedAt           time.Time       `db:"created_at"           json:"created_at"`
	UpdatedAt           time.Time       `db:"updated_at"           json:"updated_at"`
	AutoHealConfig      json.RawMessage `db:"auto_heal_config"     json:"auto_heal_config,omitempty"`
	AutoHealState       json.RawMessage `db:"auto_heal_state"      json:"auto_heal_state,omitempty"`
}

// ServiceHealthEvent records the outcome of a single health check execution.
type ServiceHealthEvent struct {
	ID        string    `db:"id"         json:"id"`
	MonitorID string    `db:"monitor_id" json:"monitor_id"`
	NodeID    string    `db:"node_id"    json:"node_id"`
	UserID    string    `db:"user_id"    json:"user_id"`
	Status    string    `db:"status"     json:"status"` // "up" | "down"
	LatencyMs int       `db:"latency_ms" json:"latency_ms"`
	Message   string    `db:"message"    json:"message"`
	CheckedAt time.Time `db:"checked_at" json:"checked_at"`
}

// Repository defines persistence operations for service monitors and their events.
type Repository interface {
	// Monitors
	Create(m *ServiceMonitor) (*ServiceMonitor, error)
	Update(m *ServiceMonitor) (*ServiceMonitor, error)
	Delete(id string) error
	FindByID(id string) (*ServiceMonitor, error)
	ListByNodeID(nodeID string) ([]*ServiceMonitor, error)
	UpdateStatus(id, status string, consecutiveFailures int) error

	// Health events
	InsertEvent(e *ServiceHealthEvent) error
	ListEventsByMonitorID(monitorID string, limit int) ([]*ServiceHealthEvent, error)
	// Returns 24h of bucketed uptime data (one point per 5-minute window).
	UptimeHistory24h(monitorID string) ([]*UptimePoint, error)
}

// UptimePoint is a 5-minute bucketed aggregate used to render the uptime bar.
type UptimePoint struct {
	Bucket string `db:"bucket" json:"bucket"` // ISO timestamp
	Status string `db:"status" json:"status"` // "up" | "down" | "no_data"
}

// Service contains business logic for service monitors.
type Service struct {
	repo Repository
}

// NewService creates a new service monitor service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(m *ServiceMonitor) (*ServiceMonitor, error) {
	if m.IntervalSeconds <= 0 {
		m.IntervalSeconds = 30
	}
	if m.FailureThreshold <= 0 {
		m.FailureThreshold = 3
	}
	if m.HTTPExpectedStatus == 0 {
		m.HTTPExpectedStatus = 200
	}
	if m.Status == "" {
		m.Status = StatusUnknown
	}
	return s.repo.Create(m)
}

func (s *Service) Update(m *ServiceMonitor) (*ServiceMonitor, error) {
	return s.repo.Update(m)
}

func (s *Service) Delete(id string) error {
	return s.repo.Delete(id)
}

func (s *Service) FindByID(id string) (*ServiceMonitor, error) {
	return s.repo.FindByID(id)
}

func (s *Service) ListByNodeID(nodeID string) ([]*ServiceMonitor, error) {
	return s.repo.ListByNodeID(nodeID)
}

func (s *Service) UpdateStatus(id, status string, consecutiveFailures int) error {
	return s.repo.UpdateStatus(id, status, consecutiveFailures)
}

func (s *Service) RecordEvent(e *ServiceHealthEvent) error {
	return s.repo.InsertEvent(e)
}

func (s *Service) EventHistory(monitorID string, limit int) ([]*ServiceHealthEvent, error) {
	return s.repo.ListEventsByMonitorID(monitorID, limit)
}

func (s *Service) UptimeHistory24h(monitorID string) ([]*UptimePoint, error) {
	return s.repo.UptimeHistory24h(monitorID)
}
