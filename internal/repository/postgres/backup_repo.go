// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
)

type BackupRepository struct {
	db *sqlx.DB
}

func NewBackupRepository(db *sqlx.DB) *BackupRepository {
	return &BackupRepository{db: db}
}

// ─── Storage Credentials ─────────────────────────────────────────────────────

func (r *BackupRepository) CreateCredential(cred *apps.StorageCredential) (*apps.StorageCredential, error) {
	var res apps.StorageCredential
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO storage_credentials (user_id, name, provider, endpoint, bucket, region, access_key_id, secret_access_key, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, user_id, name, provider, endpoint, bucket, region, access_key_id, is_default, created_at, updated_at`,
		cred.UserID, cred.Name, cred.Provider, cred.Endpoint, cred.Bucket, cred.Region, cred.AccessKeyID, cred.SecretAccessKey, cred.IsDefault,
	).Scan(&res.ID, &res.UserID, &res.Name, &res.Provider, &res.Endpoint, &res.Bucket, &res.Region, &res.AccessKeyID, &res.IsDefault, &res.CreatedAt, &res.UpdatedAt)

	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *BackupRepository) GetCredentialByID(id, userID string) (*apps.StorageCredential, error) {
	var cred apps.StorageCredential
	err := r.db.GetContext(context.Background(), &cred, `
		SELECT id, user_id, name, provider, endpoint, bucket, region, access_key_id, secret_access_key, is_default, created_at, updated_at
		FROM storage_credentials
		WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apps.ErrBackupNotFound
		}
		return nil, err
	}
	return &cred, nil
}

func (r *BackupRepository) ListCredentialsByUser(userID string) ([]*apps.StorageCredential, error) {
	var creds []*apps.StorageCredential
	err := r.db.SelectContext(context.Background(), &creds, `
		SELECT id, user_id, name, provider, endpoint, bucket, region, access_key_id, is_default, created_at, updated_at
		FROM storage_credentials
		WHERE user_id = $1
		ORDER BY name ASC`, userID)
	if err != nil {
		return nil, err
	}
	return creds, nil
}

func (r *BackupRepository) DeleteCredential(id, userID string) error {
	res, err := r.db.ExecContext(context.Background(), `
		DELETE FROM storage_credentials WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apps.ErrBackupNotFound
	}
	return nil
}

// ─── Backup Configs ──────────────────────────────────────────────────────────

func (r *BackupRepository) UpsertConfig(cfg *apps.BackupConfig) (*apps.BackupConfig, error) {
	var res apps.BackupConfig
	dest := cfg.StorageDestination
	if dest == "" {
		dest = "local_and_parent"
	}
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO backup_configs (user_id, node_id, project_id, container_name, volumes, cron_expression, retention_count, pause_container, backup_method, storage_destination, pg_user, storage_credential_id, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (node_id, container_name) DO UPDATE
			SET volumes               = EXCLUDED.volumes,
				cron_expression       = EXCLUDED.cron_expression,
				retention_count       = EXCLUDED.retention_count,
				pause_container       = EXCLUDED.pause_container,
				backup_method         = EXCLUDED.backup_method,
				storage_destination   = EXCLUDED.storage_destination,
				pg_user               = EXCLUDED.pg_user,
				storage_credential_id = EXCLUDED.storage_credential_id,
				enabled               = EXCLUDED.enabled,
				updated_at            = NOW()
		RETURNING id, user_id, node_id, project_id, container_name, volumes, cron_expression, retention_count, pause_container, backup_method, storage_destination, pg_user, storage_credential_id, enabled, created_at, updated_at`,
		cfg.UserID, cfg.NodeID, cfg.ProjectID, cfg.ContainerName, cfg.Volumes, cfg.CronExpression, cfg.RetentionCount, cfg.PauseContainer, cfg.BackupMethod, dest, cfg.PgUser, cfg.StorageCredentialID, cfg.Enabled,
	).Scan(&res.ID, &res.UserID, &res.NodeID, &res.ProjectID, &res.ContainerName, &res.Volumes, &res.CronExpression, &res.RetentionCount, &res.PauseContainer, &res.BackupMethod, &res.StorageDestination, &res.PgUser, &res.StorageCredentialID, &res.Enabled, &res.CreatedAt, &res.UpdatedAt)

	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *BackupRepository) GetConfigByContainer(nodeID, containerName string) (*apps.BackupConfig, error) {
	var cfg apps.BackupConfig
	err := r.db.GetContext(context.Background(), &cfg, `
		SELECT id, user_id, node_id, project_id, container_name, volumes, cron_expression, retention_count, pause_container, backup_method, storage_destination, pg_user, storage_credential_id, enabled, created_at, updated_at
		FROM backup_configs
		WHERE node_id = $1 AND container_name = $2`, nodeID, containerName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apps.ErrBackupNotFound
		}
		return nil, err
	}
	return &cfg, nil
}

func (r *BackupRepository) GetConfigByID(id string) (*apps.BackupConfig, error) {
	var cfg apps.BackupConfig
	err := r.db.GetContext(context.Background(), &cfg, `
		SELECT id, user_id, node_id, project_id, container_name, volumes, cron_expression, retention_count, pause_container, backup_method, storage_destination, pg_user, storage_credential_id, enabled, created_at, updated_at
		FROM backup_configs
		WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apps.ErrBackupNotFound
		}
		return nil, err
	}
	return &cfg, nil
}

// ─── Backup Records ──────────────────────────────────────────────────────────

func (r *BackupRepository) CreateRecord(rec *apps.BackupRecord) (*apps.BackupRecord, error) {
	var res apps.BackupRecord
	method := rec.BackupMethod
	if method == "" {
		method = "volume"
	}
	dest := rec.StorageDestination
	if dest == "" {
		dest = "local_and_parent"
	}
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO backup_records (backup_config_id, node_id, user_id, container_name, storage_destination, local_path, parent_path, s3_key, file_size_bytes, backup_method, trigger_type, status, error_message, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW())
		RETURNING id, backup_config_id, node_id, user_id, container_name, storage_destination, local_path, parent_path, s3_key, file_size_bytes, backup_method, trigger_type, status, error_message, started_at, completed_at`,
		rec.BackupConfigID, rec.NodeID, rec.UserID, rec.ContainerName, dest, rec.LocalPath, rec.ParentPath, rec.S3Key, rec.FileSizeBytes, method, rec.TriggerType, rec.Status, rec.ErrorMessage,
	).Scan(&res.ID, &res.BackupConfigID, &res.NodeID, &res.UserID, &res.ContainerName, &res.StorageDestination, &res.LocalPath, &res.ParentPath, &res.S3Key, &res.FileSizeBytes, &res.BackupMethod, &res.TriggerType, &res.Status, &res.ErrorMessage, &res.StartedAt, &res.CompletedAt)

	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *BackupRepository) UpdateRecordStatus(id string, status string, s3Key, localPath, parentPath string, fileSize int64, errMsg *string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE backup_records
		SET status = $2,
		    s3_key = CASE WHEN $3 <> '' THEN $3 ELSE s3_key END,
		    local_path = CASE WHEN $4 <> '' THEN $4 ELSE local_path END,
		    parent_path = CASE WHEN $5 <> '' THEN $5 ELSE parent_path END,
		    file_size_bytes = CASE WHEN $6 > 0 THEN $6 ELSE file_size_bytes END,
		    error_message = $7,
		    completed_at = NOW()
		WHERE id = $1`, id, status, s3Key, localPath, parentPath, fileSize, errMsg)
	return err
}

func (r *BackupRepository) UpdateRecordByJobID(jobID string, status string, s3Key, localPath, parentPath string, fileSize int64, errMsg *string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE backup_records
		SET status = $2,
		    s3_key = CASE WHEN $3 <> '' THEN $3 ELSE s3_key END,
		    local_path = CASE WHEN $4 <> '' THEN $4 ELSE local_path END,
		    parent_path = CASE WHEN $5 <> '' THEN $5 ELSE parent_path END,
		    file_size_bytes = CASE WHEN $6 > 0 THEN $6 ELSE file_size_bytes END,
		    error_message = $7,
		    completed_at = NOW()
		WHERE job_id = $1`, jobID, status, s3Key, localPath, parentPath, fileSize, errMsg)
	return err
}

func (r *BackupRepository) UpdateRecordParentPath(id string, parentPath string, fileSize int64) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE backup_records
		SET parent_path = $2,
		    file_size_bytes = CASE WHEN $3 > 0 THEN $3 ELSE file_size_bytes END
		WHERE id = $1`, id, parentPath, fileSize)
	return err
}

func (r *BackupRepository) UpdateRecordJobID(id string, jobID string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE backup_records
		SET job_id = $2
		WHERE id = $1`, id, jobID)
	return err
}

func (r *BackupRepository) ListRecordsByContainer(nodeID, containerName string) ([]*apps.BackupRecord, error) {
	var recs []*apps.BackupRecord
	err := r.db.SelectContext(context.Background(), &recs, `
		SELECT br.id, br.backup_config_id, br.node_id, br.user_id, br.job_id, br.container_name, br.storage_destination, br.local_path, br.parent_path, br.s3_key, br.file_size_bytes, br.backup_method, br.trigger_type, br.status, br.error_message, br.started_at, br.completed_at,
		       u.email AS user_email
		FROM backup_records br
		LEFT JOIN users u ON br.user_id = u.id
		WHERE br.node_id = $1 AND br.container_name = $2
		ORDER BY br.started_at DESC LIMIT 50`, nodeID, containerName)
	if err != nil {
		return nil, err
	}
	return recs, nil
}

func (r *BackupRepository) GetRecordByID(id string) (*apps.BackupRecord, error) {
	var rec apps.BackupRecord
	err := r.db.GetContext(context.Background(), &rec, `
		SELECT br.id, br.backup_config_id, br.node_id, br.user_id, br.job_id, br.container_name, br.storage_destination, br.local_path, br.parent_path, br.s3_key, br.file_size_bytes, br.backup_method, br.trigger_type, br.status, br.error_message, br.started_at, br.completed_at,
		       u.email AS user_email
		FROM backup_records br
		LEFT JOIN users u ON br.user_id = u.id
		WHERE br.id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apps.ErrBackupNotFound
		}
		return nil, err
	}
	return &rec, nil
}
