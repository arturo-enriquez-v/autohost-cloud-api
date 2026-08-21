package backup

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("backup entity not found")
)

type StorageCredential struct {
	ID              string    `db:"id"                json:"id"`
	UserID          string    `db:"user_id"           json:"user_id"`
	Name            string    `db:"name"              json:"name"`
	Provider        string    `db:"provider"          json:"provider"`
	Endpoint        string    `db:"endpoint"          json:"endpoint"`
	Bucket          string    `db:"bucket"            json:"bucket"`
	Region          string    `db:"region"            json:"region"`
	AccessKeyID     string    `db:"access_key_id"     json:"access_key_id"`
	SecretAccessKey string    `db:"secret_access_key" json:"secret_access_key,omitempty"`
	IsDefault       bool      `db:"is_default"        json:"is_default"`
	CreatedAt       time.Time `db:"created_at"        json:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"        json:"updated_at"`
}

type BackupConfig struct {
	ID                  string          `db:"id"                    json:"id"`
	UserID              string          `db:"user_id"               json:"user_id"`
	NodeID              string          `db:"node_id"               json:"node_id"`
	ProjectID           *string         `db:"project_id"            json:"project_id,omitempty"`
	ContainerName       string          `db:"container_name"        json:"container_name"`
	Volumes             json.RawMessage `db:"volumes"               json:"volumes"`
	CronExpression      string          `db:"cron_expression"       json:"cron_expression"`
	RetentionCount      int             `db:"retention_count"       json:"retention_count"`
	PauseContainer      bool            `db:"pause_container"       json:"pause_container"`
	BackupMethod        string          `db:"backup_method"         json:"backup_method"`
	PgUser              string          `db:"pg_user"               json:"pg_user"`
	StorageCredentialID *string         `db:"storage_credential_id" json:"storage_credential_id,omitempty"`
	Enabled             bool            `db:"enabled"               json:"enabled"`
	CreatedAt           time.Time       `db:"created_at"            json:"created_at"`
	UpdatedAt           time.Time       `db:"updated_at"            json:"updated_at"`
}

type BackupRecord struct {
	ID             string     `db:"id"               json:"id"`
	BackupConfigID string     `db:"backup_config_id" json:"backup_config_id"`
	NodeID         string     `db:"node_id"          json:"node_id"`
	UserID         string     `db:"user_id"          json:"user_id"`
	JobID          *string    `db:"job_id"           json:"job_id,omitempty"`
	ContainerName  string     `db:"container_name"   json:"container_name"`
	S3Key          string     `db:"s3_key"           json:"s3_key"`
	FileSizeBytes  int64      `db:"file_size_bytes"  json:"file_size_bytes"`
	BackupMethod   string     `db:"backup_method"    json:"backup_method"`
	TriggerType    string     `db:"trigger_type"     json:"trigger_type"`
	Status         string     `db:"status"           json:"status"`
	ErrorMessage   *string    `db:"error_message"    json:"error_message,omitempty"`
	UserEmail      *string    `db:"user_email"       json:"user_email,omitempty"`
	StartedAt      time.Time  `db:"started_at"       json:"started_at"`
	CompletedAt    *time.Time `db:"completed_at"     json:"completed_at,omitempty"`
}

type Repository interface {
	// Storage Credentials
	CreateCredential(cred *StorageCredential) (*StorageCredential, error)
	GetCredentialByID(id, userID string) (*StorageCredential, error)
	ListCredentialsByUser(userID string) ([]*StorageCredential, error)
	DeleteCredential(id, userID string) error

	// Backup Configs
	UpsertConfig(cfg *BackupConfig) (*BackupConfig, error)
	GetConfigByContainer(nodeID, containerName string) (*BackupConfig, error)
	GetConfigByID(id string) (*BackupConfig, error)

	// Backup Records
	CreateRecord(rec *BackupRecord) (*BackupRecord, error)
	UpdateRecordStatus(id string, status string, s3Key string, fileSize int64, errMsg *string) error
	UpdateRecordByJobID(jobID string, status string, s3Key string, fileSize int64, errMsg *string) error
	UpdateRecordJobID(id string, jobID string) error
	ListRecordsByContainer(nodeID, containerName string) ([]*BackupRecord, error)
	GetRecordByID(id string) (*BackupRecord, error)
}

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateCredential(cred *StorageCredential) (*StorageCredential, error) {
	return s.repo.CreateCredential(cred)
}

func (s *Service) ListCredentials(userID string) ([]*StorageCredential, error) {
	return s.repo.ListCredentialsByUser(userID)
}

func (s *Service) DeleteCredential(id, userID string) error {
	return s.repo.DeleteCredential(id, userID)
}

func (s *Service) GetCredentialByID(id, userID string) (*StorageCredential, error) {
	return s.repo.GetCredentialByID(id, userID)
}

func (s *Service) UpsertConfig(cfg *BackupConfig) (*BackupConfig, error) {
	return s.repo.UpsertConfig(cfg)
}

func (s *Service) GetConfigByContainer(nodeID, containerName string) (*BackupConfig, error) {
	return s.repo.GetConfigByContainer(nodeID, containerName)
}

func (s *Service) GetConfigByID(id string) (*BackupConfig, error) {
	return s.repo.GetConfigByID(id)
}

func (s *Service) CreateRecord(rec *BackupRecord) (*BackupRecord, error) {
	return s.repo.CreateRecord(rec)
}

func (s *Service) UpdateRecordStatus(id string, status string, s3Key string, fileSize int64, errMsg *string) error {
	return s.repo.UpdateRecordStatus(id, status, s3Key, fileSize, errMsg)
}

func (s *Service) UpdateRecordByJobID(jobID string, status string, s3Key string, fileSize int64, errMsg *string) error {
	return s.repo.UpdateRecordByJobID(jobID, status, s3Key, fileSize, errMsg)
}

func (s *Service) UpdateRecordJobID(id string, jobID string) error {
	return s.repo.UpdateRecordJobID(id, jobID)
}

func (s *Service) ListRecordsByContainer(nodeID, containerName string) ([]*BackupRecord, error) {
	return s.repo.ListRecordsByContainer(nodeID, containerName)
}

func (s *Service) GetRecordByID(id string) (*BackupRecord, error) {
	return s.repo.GetRecordByID(id)
}
