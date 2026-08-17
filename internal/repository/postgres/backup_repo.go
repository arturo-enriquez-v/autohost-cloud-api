package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/domain/backup"
)

type BackupRepository struct {
	db *sqlx.DB
}

func NewBackupRepository(db *sqlx.DB) *BackupRepository {
	return &BackupRepository{db: db}
}

// ─── Storage Credentials ─────────────────────────────────────────────────────

func (r *BackupRepository) CreateCredential(cred *backup.StorageCredential) (*backup.StorageCredential, error) {
	var res backup.StorageCredential
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

func (r *BackupRepository) GetCredentialByID(id, userID string) (*backup.StorageCredential, error) {
	var cred backup.StorageCredential
	err := r.db.GetContext(context.Background(), &cred, `
		SELECT id, user_id, name, provider, endpoint, bucket, region, access_key_id, secret_access_key, is_default, created_at, updated_at
		FROM storage_credentials
		WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, backup.ErrNotFound
		}
		return nil, err
	}
	return &cred, nil
}

func (r *BackupRepository) ListCredentialsByUser(userID string) ([]*backup.StorageCredential, error) {
	var creds []*backup.StorageCredential
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
		return backup.ErrNotFound
	}
	return nil
}

// ─── Backup Configs ──────────────────────────────────────────────────────────

func (r *BackupRepository) UpsertConfig(cfg *backup.BackupConfig) (*backup.BackupConfig, error) {
	var res backup.BackupConfig
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO backup_configs (user_id, node_id, project_id, container_name, volumes, cron_expression, retention_count, pause_container, storage_credential_id, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (node_id, container_name) DO UPDATE
			SET volumes               = EXCLUDED.volumes,
				cron_expression       = EXCLUDED.cron_expression,
				retention_count       = EXCLUDED.retention_count,
				pause_container       = EXCLUDED.pause_container,
				storage_credential_id = EXCLUDED.storage_credential_id,
				enabled               = EXCLUDED.enabled,
				updated_at            = NOW()
		RETURNING id, user_id, node_id, project_id, container_name, volumes, cron_expression, retention_count, pause_container, storage_credential_id, enabled, created_at, updated_at`,
		cfg.UserID, cfg.NodeID, cfg.ProjectID, cfg.ContainerName, cfg.Volumes, cfg.CronExpression, cfg.RetentionCount, cfg.PauseContainer, cfg.StorageCredentialID, cfg.Enabled,
	).Scan(&res.ID, &res.UserID, &res.NodeID, &res.ProjectID, &res.ContainerName, &res.Volumes, &res.CronExpression, &res.RetentionCount, &res.PauseContainer, &res.StorageCredentialID, &res.Enabled, &res.CreatedAt, &res.UpdatedAt)

	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *BackupRepository) GetConfigByContainer(nodeID, containerName string) (*backup.BackupConfig, error) {
	var cfg backup.BackupConfig
	err := r.db.GetContext(context.Background(), &cfg, `
		SELECT id, user_id, node_id, project_id, container_name, volumes, cron_expression, retention_count, pause_container, storage_credential_id, enabled, created_at, updated_at
		FROM backup_configs
		WHERE node_id = $1 AND container_name = $2`, nodeID, containerName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, backup.ErrNotFound
		}
		return nil, err
	}
	return &cfg, nil
}

func (r *BackupRepository) GetConfigByID(id string) (*backup.BackupConfig, error) {
	var cfg backup.BackupConfig
	err := r.db.GetContext(context.Background(), &cfg, `
		SELECT id, user_id, node_id, project_id, container_name, volumes, cron_expression, retention_count, pause_container, storage_credential_id, enabled, created_at, updated_at
		FROM backup_configs
		WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, backup.ErrNotFound
		}
		return nil, err
	}
	return &cfg, nil
}

// ─── Backup Records ──────────────────────────────────────────────────────────

func (r *BackupRepository) CreateRecord(rec *backup.BackupRecord) (*backup.BackupRecord, error) {
	var res backup.BackupRecord
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO backup_records (backup_config_id, node_id, user_id, container_name, s3_key, file_size_bytes, trigger_type, status, error_message, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		RETURNING id, backup_config_id, node_id, user_id, container_name, s3_key, file_size_bytes, trigger_type, status, error_message, started_at, completed_at`,
		rec.BackupConfigID, rec.NodeID, rec.UserID, rec.ContainerName, rec.S3Key, rec.FileSizeBytes, rec.TriggerType, rec.Status, rec.ErrorMessage,
	).Scan(&res.ID, &res.BackupConfigID, &res.NodeID, &res.UserID, &res.ContainerName, &res.S3Key, &res.FileSizeBytes, &res.TriggerType, &res.Status, &res.ErrorMessage, &res.StartedAt, &res.CompletedAt)

	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *BackupRepository) UpdateRecordStatus(id string, status string, s3Key string, fileSize int64, errMsg *string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE backup_records
		SET status = $2, s3_key = CASE WHEN $3 <> '' THEN $3 ELSE s3_key END, file_size_bytes = CASE WHEN $4 > 0 THEN $4 ELSE file_size_bytes END, error_message = $5, completed_at = NOW()
		WHERE id = $1`, id, status, s3Key, fileSize, errMsg)
	return err
}

func (r *BackupRepository) UpdateRecordByJobID(jobID string, status string, s3Key string, fileSize int64, errMsg *string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE backup_records
		SET status = $2,
		    s3_key = CASE WHEN $3 <> '' THEN $3 ELSE s3_key END,
		    file_size_bytes = CASE WHEN $4 > 0 THEN $4 ELSE file_size_bytes END,
		    error_message = $5,
		    completed_at = NOW()
		WHERE job_id = $1`, jobID, status, s3Key, fileSize, errMsg)
	return err
}

func (r *BackupRepository) UpdateRecordJobID(id string, jobID string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE backup_records
		SET job_id = $2
		WHERE id = $1`, id, jobID)
	return err
}

func (r *BackupRepository) ListRecordsByContainer(nodeID, containerName string) ([]*backup.BackupRecord, error) {
	var recs []*backup.BackupRecord
	err := r.db.SelectContext(context.Background(), &recs, `
		SELECT id, backup_config_id, node_id, user_id, job_id, container_name, s3_key, file_size_bytes, trigger_type, status, error_message, started_at, completed_at
		FROM backup_records
		WHERE node_id = $1 AND container_name = $2
		ORDER BY started_at DESC LIMIT 50`, nodeID, containerName)
	if err != nil {
		return nil, err
	}
	return recs, nil
}

func (r *BackupRepository) GetRecordByID(id string) (*backup.BackupRecord, error) {
	var rec backup.BackupRecord
	err := r.db.GetContext(context.Background(), &rec, `
		SELECT id, backup_config_id, node_id, user_id, job_id, container_name, s3_key, file_size_bytes, trigger_type, status, error_message, started_at, completed_at
		FROM backup_records
		WHERE id = $1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, backup.ErrNotFound
		}
		return nil, err
	}
	return &rec, nil
}
