// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
)

// runnerRepo is an in-memory BackupRepository for the runner. Unused methods panic via the embedded nil
// interface.
type runnerRepo struct {
	BackupRepository
	configs  map[string]*BackupConfig // by node/container
	records  []*BackupRecord
	statuses map[string]string // record ID -> status
	errMsgs  map[string]string
	jobIDs   map[string]string
}

func newRunnerRepo() *runnerRepo {
	return &runnerRepo{
		configs: map[string]*BackupConfig{}, statuses: map[string]string{},
		errMsgs: map[string]string{}, jobIDs: map[string]string{},
	}
}

func (r *runnerRepo) GetConfigByContainer(nodeID, container string) (*BackupConfig, error) {
	if c, ok := r.configs[nodeID+"/"+container]; ok {
		return c, nil
	}
	return nil, ErrBackupNotFound
}
func (r *runnerRepo) GetConfigByID(id string) (*BackupConfig, error) {
	for _, c := range r.configs {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, ErrBackupNotFound
}
func (r *runnerRepo) UpsertConfig(c *BackupConfig) (*BackupConfig, error) {
	c.ID = "cfg-" + c.ContainerName
	r.configs[c.NodeID+"/"+c.ContainerName] = c
	return c, nil
}
func (r *runnerRepo) CreateRecord(rec *BackupRecord) (*BackupRecord, error) {
	rec.ID = "rec-" + rec.TriggerType
	r.records = append(r.records, rec)
	return rec, nil
}
func (r *runnerRepo) UpdateRecordStatus(id, status, _, _, _ string, _ int64, errMsg *string) error {
	r.statuses[id] = status
	if errMsg != nil {
		r.errMsgs[id] = *errMsg
	}
	return nil
}
func (r *runnerRepo) UpdateRecordJobID(id, jobID string) error {
	r.jobIDs[id] = jobID
	return nil
}

// runnerJobs creates jobs, or fails with a database-like error.
type runnerJobs struct {
	fail    bool
	command string
	params  map[string]any
}

func (j *runnerJobs) Create(job *jobs.Job) (*jobs.Job, error) {
	if j.fail {
		return nil, errors.New(`pq: relation "jobs" does not exist`)
	}
	j.command = job.CommandName
	_ = json.Unmarshal([]byte(job.Params), &j.params)
	job.ID = "job-1"
	return job, nil
}
func (j *runnerJobs) FindByID(string) (*jobs.Job, error)       { return nil, jobs.ErrJobNotFound }
func (j *runnerJobs) FindByNodeID(string) ([]*jobs.Job, error) { return nil, nil }
func (j *runnerJobs) FindPendingByNodeID(string, jobs.CommandType) ([]*jobs.Job, error) {
	return nil, nil
}
func (j *runnerJobs) UpdateStatus(string, jobs.JobStatus, string, string) error { return nil }

type offlineDispatcher struct{ calls int }

func (d *offlineDispatcher) DispatchJobWithParams(string, string, string, jobs.CommandType, string) error {
	d.calls++
	return errors.New("node not connected")
}

func TestBackupJob(t *testing.T) {
	replica := "node-r"
	vols, _ := json.Marshal([]string{"db_data"})
	rec := &BackupRecord{ID: "rec-1", NodeID: "node-1", ContainerName: "db", StorageDestination: "local_and_replica"}

	tests := []struct {
		method      string
		cfg         *BackupConfig
		wantCommand string
		wantExtra   map[string]any
	}{
		{"volume", &BackupConfig{RetentionCount: 5, Volumes: vols, PauseContainer: true, ReplicaNodeID: &replica},
			"docker.volume.backup", map[string]any{"volumes": []string{"db_data"}, "pause_container": true, "replica_node_id": "node-r"}},
		{"pg_dump", &BackupConfig{RetentionCount: 5}, "docker.pg.backup", map[string]any{"pg_user": "postgres"}},
		{"image", &BackupConfig{RetentionCount: 5}, "docker.image.backup", map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			r := *rec
			r.BackupMethod = tt.method
			command, params := backupJob(tt.cfg, &r, "http://api:8080")
			if command != tt.wantCommand {
				t.Errorf("command = %q, want %q", command, tt.wantCommand)
			}
			want := map[string]any{
				"node_id": "node-1", "container_name": "db", "backup_record_id": "rec-1",
				"storage_destination": "local_and_replica", "retention_count": 5, "api_url": "http://api:8080",
			}
			for k, v := range tt.wantExtra {
				want[k] = v
			}
			if !reflect.DeepEqual(params, want) {
				t.Errorf("params = %v\nwant     %v", params, want)
			}
		})
	}
}

func TestRestoreJob_DownloadURLOnlyWithRemoteCopy(t *testing.T) {
	cfg := &BackupConfig{PgUser: "admin"}
	local := "/var/lib/autohost/backups/db/a.tar.gz"
	parent := "/srv/backups/node-1/db/a.tar.gz"

	command, params := restoreJob(cfg, &BackupRecord{ID: "rec-1", NodeID: "node-1", ContainerName: "db", BackupMethod: "pg_dump", LocalPath: &local}, "http://api")
	if command != "docker.pg.restore" || params["pg_user"] != "admin" || params["local_path"] != local {
		t.Errorf("pg restore = %q %v", command, params)
	}
	if _, ok := params["parent_download_url"]; ok {
		t.Errorf("parent_download_url set without a parent or replica copy")
	}

	_, params = restoreJob(cfg, &BackupRecord{ID: "rec-1", NodeID: "node-1", ContainerName: "db", ParentPath: &parent}, "http://api")
	if got := params["parent_download_url"]; got != "http://api/v1/backups/node-download/node-1/rec-1" {
		t.Errorf("parent_download_url = %v", got)
	}
}

func TestRunManual_CreatesDefaultConfigAndKeepsJobPendingWhenOffline(t *testing.T) {
	repo := newRunnerRepo()
	jr := &runnerJobs{}
	disp := &offlineDispatcher{}
	runner := NewBackupRunner(NewBackupService(repo), jobs.NewJobService(jr), disp)

	rec, err := runner.RunManual("user-1", "node-1", "db", "http://api")
	if err != nil {
		t.Fatalf("RunManual: %v", err)
	}
	if cfg := repo.configs["node-1/db"]; cfg == nil || cfg.StorageDestination != "local_and_parent" {
		t.Fatalf("default config not created: %+v", cfg)
	}
	if rec.Status != "running" || rec.TriggerType != "manual" || rec.JobID == nil || *rec.JobID != "job-1" {
		t.Errorf("record = %+v", rec)
	}
	if repo.jobIDs[rec.ID] != "job-1" || jr.command != "docker.volume.backup" {
		t.Errorf("job not linked: jobIDs=%v command=%q", repo.jobIDs, jr.command)
	}
	if disp.calls != 1 {
		t.Errorf("dispatcher calls = %d, want 1", disp.calls)
	}
	if _, failed := repo.statuses[rec.ID]; failed {
		t.Errorf("an offline node must leave the job pending, not fail the record")
	}
}

// Records are shown to users: a failed dispatch must not store the internal error.
func TestRunManual_JobCreationFailureMarksRecordFailed(t *testing.T) {
	repo := newRunnerRepo()
	runner := NewBackupRunner(NewBackupService(repo), jobs.NewJobService(&runnerJobs{fail: true}), nil)

	if _, err := runner.RunManual("user-1", "node-1", "db", "http://api"); err == nil {
		t.Fatal("expected an error")
	}
	if repo.statuses["rec-manual"] != "failed" {
		t.Errorf("record status = %q, want failed", repo.statuses["rec-manual"])
	}
	if msg := repo.errMsgs["rec-manual"]; strings.Contains(msg, "pq:") || msg == "" {
		t.Errorf("record error message = %q", msg)
	}
}

func TestRestore_MissingConfig(t *testing.T) {
	runner := NewBackupRunner(NewBackupService(newRunnerRepo()), jobs.NewJobService(&runnerJobs{}), nil)
	_, _, err := runner.Restore("user-1", &BackupRecord{ID: "rec-1", BackupConfigID: "gone"}, "http://api")
	if !errors.Is(err, ErrBackupConfigMissing) {
		t.Fatalf("err = %v, want ErrBackupConfigMissing", err)
	}
}
