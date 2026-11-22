// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
)

// ErrBackupConfigMissing is returned when restoring a backup whose configuration no longer exists.
var ErrBackupConfigMissing = errors.New("backup configuration missing")

// errDispatchFailed is the message stored on a record whose job could not be dispatched. The underlying
// error is only logged: records are shown to users.
const errDispatchFailed = "could not dispatch the job to the node"

// BackupJobDispatcher sends a job with parameters to a connected node.
type BackupJobDispatcher interface {
	DispatchJobWithParams(nodeID, jobID, commandName string, commandType jobs.CommandType, params string) error
}

// BackupRunner starts backup and restore jobs: it creates the record, the job and dispatches it to the node.
type BackupRunner struct {
	svc        *BackupService
	jobSvc     *jobs.JobService
	dispatcher BackupJobDispatcher
}

func NewBackupRunner(svc *BackupService, jobSvc *jobs.JobService, dispatcher BackupJobDispatcher) *BackupRunner {
	return &BackupRunner{svc: svc, jobSvc: jobSvc, dispatcher: dispatcher}
}

// defaultBackupConfig is created the first time a container is backed up without a configuration.
func defaultBackupConfig(userID, nodeID, containerName string) *BackupConfig {
	volsRaw, _ := json.Marshal([]string{})
	return &BackupConfig{
		UserID:             userID,
		NodeID:             nodeID,
		ContainerName:      containerName,
		Volumes:            volsRaw,
		CronExpression:     "0 3 * * *",
		RetentionCount:     7,
		PauseContainer:     true,
		BackupMethod:       "volume",
		StorageDestination: "local_and_parent",
		PgUser:             "postgres",
		Enabled:            true,
	}
}

// normalizedBackupMethod maps unknown methods to "volume".
func normalizedBackupMethod(method string) string {
	if method != "pg_dump" && method != "image" {
		return "volume"
	}
	return method
}

func pgUserOrDefault(user string) string {
	if user == "" {
		return "postgres"
	}
	return user
}

// backupJob returns the agent command and parameters that back up a container for the given record.
func backupJob(cfg *BackupConfig, rec *BackupRecord, apiURL string) (string, map[string]any) {
	params := map[string]any{
		"node_id":             rec.NodeID,
		"container_name":      rec.ContainerName,
		"backup_record_id":    rec.ID,
		"storage_destination": rec.StorageDestination,
		"retention_count":     cfg.RetentionCount,
		"api_url":             apiURL,
	}
	if cfg.ReplicaNodeID != nil && *cfg.ReplicaNodeID != "" {
		params["replica_node_id"] = *cfg.ReplicaNodeID
	}

	switch rec.BackupMethod {
	case "pg_dump":
		params["pg_user"] = pgUserOrDefault(cfg.PgUser)
		return "docker.pg.backup", params
	case "image":
		return "docker.image.backup", params
	default:
		var vols []string
		_ = json.Unmarshal(cfg.Volumes, &vols)
		params["volumes"] = vols
		params["pause_container"] = cfg.PauseContainer
		return "docker.volume.backup", params
	}
}

// restoreJob returns the agent command and parameters that restore the backup in rec.
func restoreJob(cfg *BackupConfig, rec *BackupRecord, apiURL string) (string, map[string]any) {
	params := map[string]any{
		"container_name": rec.ContainerName,
	}
	if rec.LocalPath != nil && *rec.LocalPath != "" {
		params["local_path"] = *rec.LocalPath
	}
	hasParentCopy := rec.ParentPath != nil && *rec.ParentPath != ""
	hasReplicaCopy := rec.ReplicaNodeID != nil && rec.ReplicaPath != nil && *rec.ReplicaPath != ""
	if hasParentCopy || hasReplicaCopy {
		params["parent_download_url"] = fmt.Sprintf("%s/v1/backups/node-download/%s/%s", apiURL, rec.NodeID, rec.ID)
	}

	switch normalizedBackupMethod(rec.BackupMethod) {
	case "pg_dump":
		params["pg_user"] = pgUserOrDefault(cfg.PgUser)
		return "docker.pg.restore", params
	case "image":
		return "docker.image.restore", params
	default:
		var vols []string
		_ = json.Unmarshal(cfg.Volumes, &vols)
		params["volumes"] = vols
		return "docker.volume.restore", params
	}
}

// RunManual backs up a container now, creating a default configuration if it has none. apiURL is the URL
// the agent uses to upload the archive to the parent. It returns the new, running record.
func (r *BackupRunner) RunManual(userID, nodeID, containerName, apiURL string) (*BackupRecord, error) {
	cfg, err := r.svc.GetConfigByContainer(nodeID, containerName)
	if err != nil || cfg == nil {
		if cfg, err = r.svc.UpsertConfig(defaultBackupConfig(userID, nodeID, containerName)); err != nil {
			return nil, fmt.Errorf("initialize backup config: %w", err)
		}
	}

	storageDest := cfg.StorageDestination
	if storageDest == "" || storageDest == "s3" {
		storageDest = "local"
	}

	rec, err := r.svc.CreateRecord(&BackupRecord{
		BackupConfigID:     cfg.ID,
		NodeID:             nodeID,
		UserID:             userID,
		ContainerName:      containerName,
		StorageDestination: storageDest,
		ReplicaNodeID:      cfg.ReplicaNodeID,
		BackupMethod:       normalizedBackupMethod(cfg.BackupMethod),
		TriggerType:        "manual",
		Status:             "running",
	})
	if err != nil {
		return nil, fmt.Errorf("create backup record: %w", err)
	}

	command, params := backupJob(cfg, rec, apiURL)
	jobID, err := r.dispatch(rec, command, params)
	if err != nil {
		return nil, err
	}
	rec.JobID = &jobID
	return rec, nil
}

// Restore starts restoring the backup in rec onto its container. It returns the new restore record and the
// restore job's ID.
func (r *BackupRunner) Restore(userID string, rec *BackupRecord, apiURL string) (*BackupRecord, string, error) {
	cfg, err := r.svc.GetConfigByID(rec.BackupConfigID)
	if err != nil || cfg == nil {
		return nil, "", ErrBackupConfigMissing
	}

	command, params := restoreJob(cfg, rec, apiURL)
	restoreRec, err := r.svc.CreateRecord(&BackupRecord{
		BackupConfigID:     cfg.ID,
		NodeID:             rec.NodeID,
		UserID:             userID,
		ContainerName:      rec.ContainerName,
		StorageDestination: rec.StorageDestination,
		LocalPath:          rec.LocalPath,
		ParentPath:         rec.ParentPath,
		S3Key:              rec.S3Key,
		FileSizeBytes:      rec.FileSizeBytes,
		BackupMethod:       normalizedBackupMethod(rec.BackupMethod),
		TriggerType:        "restore",
		Status:             "running",
	})
	if err != nil {
		return nil, "", fmt.Errorf("create restore record: %w", err)
	}

	jobID, err := r.dispatch(restoreRec, command, params)
	if err != nil {
		return nil, "", err
	}
	return restoreRec, jobID, nil
}

// dispatch creates the job for rec and sends it to the node. If the job cannot be created, rec is marked
// failed. A node that is offline does not fail the call: the job stays pending.
func (r *BackupRunner) dispatch(rec *BackupRecord, command string, params map[string]any) (string, error) {
	paramsJSON, _ := json.Marshal(params)
	j, err := r.jobSvc.DispatchWithParams(rec.NodeID, command, jobs.CommandTypeDefault, string(paramsJSON))
	if err != nil {
		msg := errDispatchFailed
		_ = r.svc.UpdateRecordStatus(rec.ID, "failed", "", "", "", 0, &msg)
		return "", fmt.Errorf("dispatch %s job: %w", command, err)
	}
	_ = r.svc.UpdateRecordJobID(rec.ID, j.ID)
	if r.dispatcher != nil {
		if err := r.dispatcher.DispatchJobWithParams(rec.NodeID, j.ID, command, jobs.CommandTypeDefault, string(paramsJSON)); err != nil {
			log.Printf("[backup] node %s not connected, job %s stays pending: %v", rec.NodeID, j.ID, err)
		}
	}
	return j.ID, nil
}
