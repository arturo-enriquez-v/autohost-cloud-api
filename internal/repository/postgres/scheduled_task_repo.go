package postgres

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	scheduledtask "github.com/arturo/autohost-cloud-api/internal/domain/scheduled_task"
)

type ScheduledTaskRepository struct {
	db *sqlx.DB
}

func NewScheduledTaskRepository(db *sqlx.DB) *ScheduledTaskRepository {
	return &ScheduledTaskRepository{db: db}
}

const taskCols = `
	st.id, st.node_id, st.command_id, st.command_name,
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

func (r *ScheduledTaskRepository) Create(task *scheduledtask.ScheduledTask) (*scheduledtask.ScheduledTask, error) {
	query := `
		INSERT INTO scheduled_tasks
			(node_id, command_id, command_name, schedule_type, interval_seconds, run_at_time, enabled, next_run_at)
		VALUES
			(:node_id, :command_id, :command_name, :schedule_type, :interval_seconds, :run_at_time, :enabled, :next_run_at)
		RETURNING
			id, node_id, command_id, command_name, schedule_type, interval_seconds, run_at_time,
			enabled, last_run_at, next_run_at, last_job_id, created_at, updated_at`

	rows, err := r.db.NamedQuery(query, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, errors.New("create returned no rows")
	}
	var result scheduledtask.ScheduledTask
	if err := rows.StructScan(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *ScheduledTaskRepository) List() ([]*scheduledtask.ScheduledTask, error) {
	var tasks []*scheduledtask.ScheduledTask
	err := r.db.Select(&tasks, `SELECT`+taskCols+taskJoin+` ORDER BY st.created_at DESC`)
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		tasks = []*scheduledtask.ScheduledTask{}
	}
	return tasks, nil
}

func (r *ScheduledTaskRepository) ListEnabled() ([]*scheduledtask.ScheduledTask, error) {
	var tasks []*scheduledtask.ScheduledTask
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

func (r *ScheduledTaskRepository) FindByID(id string) (*scheduledtask.ScheduledTask, error) {
	var task scheduledtask.ScheduledTask
	err := r.db.Get(&task, `
		SELECT id, node_id, command_id, command_name, schedule_type, interval_seconds, run_at_time,
		       enabled, last_run_at, next_run_at, last_job_id, created_at, updated_at
		FROM scheduled_tasks
		WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, scheduledtask.ErrNotFound
		}
		return nil, err
	}
	return &task, nil
}

func (r *ScheduledTaskRepository) Update(task *scheduledtask.ScheduledTask) (*scheduledtask.ScheduledTask, error) {
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
		return nil, scheduledtask.ErrNotFound
	}
	var result scheduledtask.ScheduledTask
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
		return scheduledtask.ErrNotFound
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
