// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

// ReportSettings holds the email report preferences for a user.
type ReportSettings struct {
	UserID          string
	Enabled         bool
	HourUTC         int
	ExcludedNodeIDs []string
}

// ReportSettingsRepository defines persistence operations for report settings.
type ReportSettingsRepository interface {
	Get(userID string) (*ReportSettings, error)
	Upsert(userID string, enabled bool, hourUTC int, excludedNodeIDs []string) error
	ListEnabledAtHour(hourUTC int) ([]*ReportSettings, error)
}

// ReportSettingsService encapsulates business logic for report settings.
type ReportSettingsService struct {
	repo ReportSettingsRepository
}

func NewReportSettingsService(repo ReportSettingsRepository) *ReportSettingsService {
	return &ReportSettingsService{repo: repo}
}

func (s *ReportSettingsService) Get(userID string) (*ReportSettings, error) {
	return s.repo.Get(userID)
}

func (s *ReportSettingsService) Upsert(userID string, enabled bool, hourUTC int, excludedNodeIDs []string) error {
	if hourUTC < 0 || hourUTC > 23 {
		hourUTC = 8
	}
	if excludedNodeIDs == nil {
		excludedNodeIDs = []string{}
	}
	return s.repo.Upsert(userID, enabled, hourUTC, excludedNodeIDs)
}

func (s *ReportSettingsService) ListEnabledAtHour(hourUTC int) ([]*ReportSettings, error) {
	return s.repo.ListEnabledAtHour(hourUTC)
}
