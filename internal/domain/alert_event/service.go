package alertevent

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
