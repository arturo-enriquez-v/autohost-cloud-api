package reportsettings

// ReportSettings holds the email report preferences for a user.
type ReportSettings struct {
	UserID  string
	Enabled bool
	HourUTC int
}

// Repository defines persistence operations for report settings.
type Repository interface {
	Get(userID string) (*ReportSettings, error)
	Upsert(userID string, enabled bool, hourUTC int) error
	ListEnabledAtHour(hourUTC int) ([]*ReportSettings, error)
}

// Service encapsulates business logic for report settings.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Get(userID string) (*ReportSettings, error) {
	return s.repo.Get(userID)
}

func (s *Service) Upsert(userID string, enabled bool, hourUTC int) error {
	if hourUTC < 0 || hourUTC > 23 {
		hourUTC = 8
	}
	return s.repo.Upsert(userID, enabled, hourUTC)
}

func (s *Service) ListEnabledAtHour(hourUTC int) ([]*ReportSettings, error) {
	return s.repo.ListEnabledAtHour(hourUTC)
}
