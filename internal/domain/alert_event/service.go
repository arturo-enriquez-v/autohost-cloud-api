package alertevent

import "time"

// Service wraps the repository for alert event business logic.
type Service struct {
	repo Repository
}

// NewService creates a new AlertEvent service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Create persists a new alert event.
func (s *Service) Create(event *AlertEvent) (*AlertEvent, error) {
	return s.repo.Create(event)
}

// ListByUserID returns the most recent alert events for a user.
func (s *Service) ListByUserID(userID string, limit, offset int) ([]*AlertEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repo.ListByUserID(userID, limit, offset)
}

// CountDistinctNodesWithRecentAlerts returns how many distinct nodes fired
// alerts within the given duration window (e.g. last 24h).
func (s *Service) CountDistinctNodesWithRecentAlerts(userID string, since time.Time) (int, error) {
	return s.repo.CountDistinctNodesWithRecentAlerts(userID, since)
}
