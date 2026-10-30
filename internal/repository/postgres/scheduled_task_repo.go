// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"database/sql"
	"errors"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/jmoiron/sqlx"
)

type ScheduledTaskRepository struct {
	db *sqlx.DB
}

func NewScheduledTaskRepository(db *sqlx.DB) *ScheduledTaskRepository {
	return &ScheduledTaskRepository{db: db}
}

const taskCols = `
	st.id, st.user_id, st.node_id, st.command_id, st.command_name,
	st.schedule_type, st.interval_seconds, st.run_at_time,
	st.enabled, st.last_run_at, st.next_run_at, st.last_job_id,
	st.created_at, st.updated_at,
	j.status      AS last_job_status,
	j.output      AS last_job_output,
	j.error       AS last_job_error,
	j.finished_at AS last_job_finished_at`

const taskJoin = `
	FROM scheduled_tasks st
	LEFT JOIN jobs j ON j.id = st.last_job_id`

func (r *ScheduledTaskRepository) Create(task *jobs.ScheduledTask) (*jobs.ScheduledTask, error) {
	query := `
		INSERT INTO scheduled_tasks
			(user_id, node_id, command_id, command_name, schedule_type, interval_seconds, run_at_time, enabled, next_run_at)
		VALUES
			(:user_id, :node_id, :command_id, :command_name, :schedule_type, :interval_seconds, :run_at_time, :enabled, :next_run_at)
		RETURNING
			id, user_id, node_id, command_id, command_name, schedule_type, interval_seconds, run_at_time,
			enabled, last_run_at, next_run_at, last_job_id, created_at, updated_at`

	rows, err := r.db.NamedQuery(query, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, errors.New("create returned no rows")
	}
	var result jobs.ScheduledTask
	if err := rows.StructScan(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *ScheduledTaskRepository) List() ([]*jobs.ScheduledTask, error) {
	var tasks []*jobs.ScheduledTask
	err := r.db.Select(&tasks, `SELECT`+taskCols+taskJoin+` ORDER BY st.created_at DESC`)
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		tasks = []*jobs.ScheduledTask{}
	}
	return tasks, nil
}

func (r *ScheduledTaskRepository) ListByUserID(userID string) ([]*jobs.ScheduledTask, error) {
	var tasks []*jobs.ScheduledTask
	err := r.db.Select(&tasks, `SELECT`+taskCols+taskJoin+` WHERE st.user_id = $1 ORDER BY st.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		tasks = []*jobs.ScheduledTask{}
	}
	return tasks, nil
}

func (r *ScheduledTaskRepository) ListEnabled() ([]*jobs.ScheduledTask, error) {
	var tasks []*jobs.ScheduledTask
	err := r.db.Select(&tasks, `
		SELECT id, node_id, command_id, command_name, schedule_type, interval_seconds, run_at_time,
		       enabled, last_run_at, next_run_at, last_job_id, created_at, updated_at
		FROM scheduled_tasks
		WHERE enabled = TRUE AND next_run_at <= $1
		ORDER BY next_run_at ASC`, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

func (r *ScheduledTaskRepository) FindByID(id string) (*jobs.ScheduledTask, error) {
	var task jobs.ScheduledTask
	err := r.db.Get(&task, `
		SELECT id, user_id, node_id, command_id, command_name, schedule_type, interval_seconds, run_at_time,
		       enabled, last_run_at, next_run_at, last_job_id, created_at, updated_at
		FROM scheduled_tasks
		WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, jobs.ErrTaskNotFound
		}
		return nil, err
	}
	return &task, nil
}

func (r *ScheduledTaskRepository) Update(task *jobs.ScheduledTask) (*jobs.ScheduledTask, error) {
	query := `
		UPDATE scheduled_tasks SET
			command_id       = :command_id,
			command_name     = :command_name,
			schedule_type    = :schedule_type,
			interval_seconds = :interval_seconds,
			run_at_time      = :run_at_time,
			enabled          = :enabled,
			next_run_at      = :next_run_at,
			updated_at       = NOW()
		WHERE id = :id
		RETURNING
			id, node_id, command_id, command_name, schedule_type, interval_seconds, run_at_time,
			enabled, last_run_at, next_run_at, last_job_id, created_at, updated_at`

	rows, err := r.db.NamedQuery(query, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, jobs.ErrTaskNotFound
	}
	var result jobs.ScheduledTask
	if err := rows.StructScan(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *ScheduledTaskRepository) Delete(id string) error {
	res, err := r.db.Exec(`DELETE FROM scheduled_tasks WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return jobs.ErrTaskNotFound
	}
	return nil
}

func (r *ScheduledTaskRepository) MarkRan(id string, lastRunAt, nextRunAt time.Time, jobID *string) error {
	_, err := r.db.Exec(`
		UPDATE scheduled_tasks
		SET last_run_at = $1, next_run_at = $2, last_job_id = $3, updated_at = NOW()
		WHERE id = $4`, lastRunAt, nextRunAt, jobID, id)
	return err
}
