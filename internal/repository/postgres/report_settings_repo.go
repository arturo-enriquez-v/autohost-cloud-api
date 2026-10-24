// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"database/sql"
	"encoding/json"

	reportsettings "github.com/arturo/autohost-cloud-api/internal/domain/report_settings"
	"github.com/jmoiron/sqlx"
)

type ReportSettingsRepository struct {
	db *sqlx.DB
}

func NewReportSettingsRepository(db *sqlx.DB) *ReportSettingsRepository {
	return &ReportSettingsRepository{db: db}
}

func (r *ReportSettingsRepository) Get(userID string) (*reportsettings.ReportSettings, error) {
	var row struct {
		Enabled         bool   `db:"enabled"`
		HourUTC         int    `db:"hour_utc"`
		ExcludedNodeIDs []byte `db:"excluded_node_ids"`
	}
	err := r.db.Get(&row,
		`SELECT enabled, hour_utc, excluded_node_ids FROM user_report_settings WHERE user_id = $1`, userID)
	if err == sql.ErrNoRows {
		return &reportsettings.ReportSettings{UserID: userID, Enabled: false, HourUTC: 8, ExcludedNodeIDs: []string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var excluded []string
	if len(row.ExcludedNodeIDs) > 0 {
		_ = json.Unmarshal(row.ExcludedNodeIDs, &excluded)
	}
	if excluded == nil {
		excluded = []string{}
	}
	return &reportsettings.ReportSettings{
		UserID:          userID,
		Enabled:         row.Enabled,
		HourUTC:         row.HourUTC,
		ExcludedNodeIDs: excluded,
	}, nil
}

func (r *ReportSettingsRepository) Upsert(userID string, enabled bool, hourUTC int, excludedNodeIDs []string) error {
	if excludedNodeIDs == nil {
		excludedNodeIDs = []string{}
	}
	excludedJSON, err := json.Marshal(excludedNodeIDs)
	if err != nil {
		excludedJSON = []byte("[]")
	}
	_, err = r.db.Exec(`
		INSERT INTO user_report_settings (user_id, enabled, hour_utc, excluded_node_ids)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE
		  SET enabled = EXCLUDED.enabled,
		      hour_utc = EXCLUDED.hour_utc,
		      excluded_node_ids = EXCLUDED.excluded_node_ids,
		      updated_at = now()
	`, userID, enabled, hourUTC, excludedJSON)
	return err
}

func (r *ReportSettingsRepository) ListEnabledAtHour(hourUTC int) ([]*reportsettings.ReportSettings, error) {
	var rows []struct {
		UserID  string `db:"user_id"`
		HourUTC int    `db:"hour_utc"`
	}
	err := r.db.Select(&rows,
		`SELECT user_id, hour_utc FROM user_report_settings WHERE enabled = true AND hour_utc = $1`,
		hourUTC)
	if err != nil {
		return nil, err
	}
	out := make([]*reportsettings.ReportSettings, len(rows))
	for i, row := range rows {
		out[i] = &reportsettings.ReportSettings{UserID: row.UserID, Enabled: true, HourUTC: row.HourUTC}
	}
	return out, nil
}
