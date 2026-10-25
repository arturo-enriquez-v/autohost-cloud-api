// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-chi/chi/v5"
)

type BackupHandler struct {
	backupSvc  *apps.BackupService
	jobSvc     *jobs.JobService
	dispatcher dispatcher.ParamDispatcher
	nodeSvc    *node.Service
}

func NewBackupHandler(
	backupSvc *apps.BackupService,
	jobSvc *jobs.JobService,
	disp dispatcher.ParamDispatcher,
	nodeSvc *node.Service,
) *BackupHandler {
	return &BackupHandler{
		backupSvc:  backupSvc,
		jobSvc:     jobSvc,
		dispatcher: disp,
		nodeSvc:    nodeSvc,
	}
}

func (h *BackupHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)

	// Configs
	r.Get("/config/{nodeId}/{containerName}", h.GetConfig)
	r.Put("/config/{nodeId}/{containerName}", h.UpsertConfig)

	// Records
	r.Get("/records/{nodeId}/{containerName}", h.ListRecords)

	// Run / Restore / Download Actions
	r.Post("/run/{nodeId}/{containerName}", h.RunManualBackup)
	r.Post("/restore/{recordId}", h.RestoreBackup)
	r.Get("/download/{recordId}", h.DownloadBackup)

	return r
}

func getSystemR2Credential() *apps.StorageCredential {
	accountID := os.Getenv("R2_ACCOUNT_ID")
	accessKey := os.Getenv("R2_ACCESS_KEY_ID")
	secretKey := os.Getenv("R2_SECRET_ACCESS_KEY")
	bucketRaw := os.Getenv("R2_BUCKET_NAME")

	if accountID == "" || accessKey == "" || secretKey == "" || bucketRaw == "" {
		return nil
	}

	// Extract the actual root bucket name (before any slashes)
	bucketName := strings.Split(strings.TrimSpace(bucketRaw), "/")[0]

	return &apps.StorageCredential{
		ID:              "system-managed-r2",
		Name:            "Autohost Cloud Storage (Incluido)",
		Provider:        "r2",
		Endpoint:        fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID),
		Bucket:          bucketName,
		Region:          "auto",
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
		IsDefault:       true,
	}
}

// ─── Config Handlers ────────────────────────────────────────────────────────

func (h *BackupHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	containerName := chi.URLParam(r, "containerName")

	cfg, err := h.backupSvc.GetConfigByContainer(nodeID, containerName)
	if err != nil {
		if err == apps.ErrBackupNotFound {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"enabled":        false,
				"container_name": containerName,
			})
			return
		}
		http.Error(w, "failed to fetch backup config", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

type upsertConfigRequest struct {
	Volumes        []string `json:"volumes"`
	CronExpression string   `json:"cron_expression"`
	RetentionCount int      `json:"retention_count"`
	PauseContainer bool     `json:"pause_container"`
	BackupMethod   string   `json:"backup_method"`
	PgUser         string   `json:"pg_user"`
	Enabled        bool     `json:"enabled"`
}

func (h *BackupHandler) UpsertConfig(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	containerName := chi.URLParam(r, "containerName")

	var req upsertConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	volsRaw, _ := json.Marshal(req.Volumes)
	cronExpr := req.CronExpression
	if cronExpr == "" {
		cronExpr = "0 3 * * *"
	}
	retention := req.RetentionCount
	if retention <= 0 {
		retention = 7
	}

	backupMethod := req.BackupMethod
	if backupMethod != "pg_dump" {
		backupMethod = "volume"
	}
	pgUser := req.PgUser
	if pgUser == "" {
		pgUser = "postgres"
	}

	cfg, err := h.backupSvc.UpsertConfig(&apps.BackupConfig{
		UserID:         claims.UserID,
		NodeID:         nodeID,
		ContainerName:  containerName,
		Volumes:        volsRaw,
		CronExpression: cronExpr,
		RetentionCount: retention,
		PauseContainer: req.PauseContainer,
		BackupMethod:   backupMethod,
		PgUser:         pgUser,
		Enabled:        req.Enabled,
	})
	if err != nil {
		log.Printf("[ERROR] upsert backup config: %v", err)
		http.Error(w, "failed to save backup config", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

// ─── Records Handlers ────────────────────────────────────────────────────────

func (h *BackupHandler) ListRecords(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	containerName := chi.URLParam(r, "containerName")

	recs, err := h.backupSvc.ListRecordsByContainer(nodeID, containerName)
	if err != nil {
		http.Error(w, "failed to fetch backup records", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(recs)
}

func writeJSONError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ─── Run & Restore Actions ───────────────────────────────────────────────────

func (h *BackupHandler) RunManualBackup(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	containerName := chi.URLParam(r, "containerName")

	cred := getSystemR2Credential()
	if cred == nil {
		writeJSONError(w, "Cloudflare R2 system storage not configured in server environment variables", http.StatusBadRequest)
		return
	}

	cfg, err := h.backupSvc.GetConfigByContainer(nodeID, containerName)
	if err != nil || cfg == nil {
		volsRaw, _ := json.Marshal([]string{})
		cfg, err = h.backupSvc.UpsertConfig(&apps.BackupConfig{
			UserID:         claims.UserID,
			NodeID:         nodeID,
			ContainerName:  containerName,
			Volumes:        volsRaw,
			CronExpression: "0 3 * * *",
			RetentionCount: 7,
			PauseContainer: true,
			BackupMethod:   "volume",
			PgUser:         "postgres",
			Enabled:        true,
		})
		if err != nil {
			log.Printf("[ERROR] auto-initialize backup config failed: %v", err)
			writeJSONError(w, fmt.Sprintf("could not initialize backup config: %v", err), http.StatusInternalServerError)
			return
		}
	}

	var vols []string
	_ = json.Unmarshal(cfg.Volumes, &vols)

	nowStr := time.Now().Format("2006-01-02_150405")

	backupMethod := cfg.BackupMethod
	if backupMethod == "" {
		backupMethod = "volume"
	}

	var s3Key string
	if backupMethod == "pg_dump" {
		s3Key = fmt.Sprintf("backups/users/%s/nodes/%s/apps/%s/%s/%s.sql.gz", claims.UserID, nodeID, containerName, nowStr, containerName)
	} else {
		s3Key = fmt.Sprintf("backups/users/%s/nodes/%s/apps/%s/%s/%s.tar.gz", claims.UserID, nodeID, containerName, nowStr, containerName)
	}

	rec, err := h.backupSvc.CreateRecord(&apps.BackupRecord{
		BackupConfigID: cfg.ID,
		NodeID:         nodeID,
		UserID:         claims.UserID,
		ContainerName:  containerName,
		S3Key:          s3Key,
		BackupMethod:   backupMethod,
		TriggerType:    "manual",
		Status:         "running",
	})
	if err != nil {
		log.Printf("[ERROR] create backup record failed: %v", err)
		writeJSONError(w, fmt.Sprintf("could not initiate backup record: %v", err), http.StatusInternalServerError)
		return
	}

	var commandName string
	var params []byte

	if backupMethod == "pg_dump" {
		commandName = "docker.pg.backup"
		pgUser := cfg.PgUser
		if pgUser == "" {
			pgUser = "postgres"
		}
		params, _ = json.Marshal(map[string]any{
			"container_name": containerName,
			"pg_user":        pgUser,
			"s3": map[string]any{
				"endpoint":          cred.Endpoint,
				"bucket":            cred.Bucket,
				"region":            cred.Region,
				"access_key_id":     cred.AccessKeyID,
				"secret_access_key": cred.SecretAccessKey,
				"s3_key":            s3Key,
			},
		})
	} else {
		commandName = "docker.volume.backup"
		params, _ = json.Marshal(map[string]any{
			"container_name":  containerName,
			"volumes":         vols,
			"pause_container": cfg.PauseContainer,
			"s3": map[string]any{
				"endpoint":          cred.Endpoint,
				"bucket":            cred.Bucket,
				"region":            cred.Region,
				"access_key_id":     cred.AccessKeyID,
				"secret_access_key": cred.SecretAccessKey,
				"s3_key":            s3Key,
			},
		})
	}

	j, err := h.jobSvc.Dispatch(nodeID, commandName, jobs.CommandTypeDefault)
	if err != nil {
		_ = h.backupSvc.UpdateRecordStatus(rec.ID, "failed", "", 0, strPtr(err.Error()))
		log.Printf("[ERROR] dispatch backup job failed: %v", err)
		writeJSONError(w, fmt.Sprintf("failed to dispatch backup job: %v", err), http.StatusInternalServerError)
		return
	}

	_ = h.backupSvc.UpdateRecordJobID(rec.ID, j.ID)
	rec.JobID = &j.ID

	_ = h.dispatcher.DispatchJobWithParams(nodeID, j.ID, commandName, jobs.CommandTypeDefault, string(params))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

func (h *BackupHandler) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	recordID := chi.URLParam(r, "recordId")
	rec, err := h.backupSvc.GetRecordByID(recordID)
	if err != nil || rec == nil {
		http.Error(w, "backup record not found", http.StatusNotFound)
		return
	}

	cred := getSystemR2Credential()
	if cred == nil {
		http.Error(w, "Cloudflare R2 system storage not configured in server environment variables", http.StatusBadRequest)
		return
	}

	cfg, err := h.backupSvc.GetConfigByID(rec.BackupConfigID)
	if err != nil || cfg == nil {
		http.Error(w, "backup configuration missing", http.StatusBadRequest)
		return
	}

	var vols []string
	_ = json.Unmarshal(cfg.Volumes, &vols)

	// Determine the restore method from the original backup record
	restoreMethod := rec.BackupMethod
	if restoreMethod == "" {
		restoreMethod = "volume"
	}

	var restoreCommandName string
	var params []byte

	if restoreMethod == "pg_dump" {
		restoreCommandName = "docker.pg.restore"
		pgUser := cfg.PgUser
		if pgUser == "" {
			pgUser = "postgres"
		}
		params, _ = json.Marshal(map[string]any{
			"container_name": rec.ContainerName,
			"pg_user":        pgUser,
			"s3": map[string]any{
				"endpoint":          cred.Endpoint,
				"bucket":            cred.Bucket,
				"region":            cred.Region,
				"access_key_id":     cred.AccessKeyID,
				"secret_access_key": cred.SecretAccessKey,
				"s3_key":            rec.S3Key,
			},
		})
	} else {
		restoreCommandName = "docker.volume.restore"
		params, _ = json.Marshal(map[string]any{
			"container_name": rec.ContainerName,
			"volumes":        vols,
			"s3": map[string]any{
				"endpoint":          cred.Endpoint,
				"bucket":            cred.Bucket,
				"region":            cred.Region,
				"access_key_id":     cred.AccessKeyID,
				"secret_access_key": cred.SecretAccessKey,
				"s3_key":            rec.S3Key,
			},
		})
	}

	restoreRec, err := h.backupSvc.CreateRecord(&apps.BackupRecord{
		BackupConfigID: cfg.ID,
		NodeID:         rec.NodeID,
		UserID:         claims.UserID,
		ContainerName:  rec.ContainerName,
		S3Key:          rec.S3Key,
		FileSizeBytes:  rec.FileSizeBytes,
		BackupMethod:   restoreMethod,
		TriggerType:    "restore",
		Status:         "running",
	})
	if err != nil {
		log.Printf("[ERROR] create restore record failed: %v", err)
		writeJSONError(w, fmt.Sprintf("could not initiate restore record: %v", err), http.StatusInternalServerError)
		return
	}

	j, err := h.jobSvc.Dispatch(rec.NodeID, restoreCommandName, jobs.CommandTypeDefault)
	if err != nil {
		_ = h.backupSvc.UpdateRecordStatus(restoreRec.ID, "failed", "", 0, strPtr(err.Error()))
		writeJSONError(w, "failed to dispatch restore job", http.StatusInternalServerError)
		return
	}

	_ = h.backupSvc.UpdateRecordJobID(restoreRec.ID, j.ID)

	_ = h.dispatcher.DispatchJobWithParams(rec.NodeID, j.ID, restoreCommandName, jobs.CommandTypeDefault, string(params))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":    "restoring",
		"job_id":    j.ID,
		"record_id": restoreRec.ID,
	})
}

func (h *BackupHandler) DownloadBackup(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	recordID := chi.URLParam(r, "recordId")
	rec, err := h.backupSvc.GetRecordByID(recordID)
	if err != nil || rec == nil {
		writeJSONError(w, "backup record not found", http.StatusNotFound)
		return
	}

	if rec.UserID != claims.UserID {
		writeJSONError(w, "forbidden", http.StatusForbidden)
		return
	}

	cred := getSystemR2Credential()
	if cred == nil {
		writeJSONError(w, "Cloudflare R2 system storage not configured", http.StatusBadRequest)
		return
	}

	s3Client := s3.New(s3.Options{
		Region:       cred.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(cred.AccessKeyID, cred.SecretAccessKey, ""),
		BaseEndpoint: aws.String(cred.Endpoint),
		UsePathStyle: true,
	})

	presignClient := s3.NewPresignClient(s3Client)
	filename := filepath.Base(rec.S3Key)
	disposition := fmt.Sprintf("attachment; filename=\"%s\"", filename)

	presignedReq, err := presignClient.PresignGetObject(r.Context(), &s3.GetObjectInput{
		Bucket:                     aws.String(cred.Bucket),
		Key:                        aws.String(rec.S3Key),
		ResponseContentDisposition: aws.String(disposition),
	}, s3.WithPresignExpires(15*time.Minute))

	if err != nil {
		log.Printf("[ERROR] presign download URL failed: %v", err)
		writeJSONError(w, "failed to generate download URL", http.StatusInternalServerError)
		return
	}

	// Log the download event in backup_records for audit trail
	_, _ = h.backupSvc.CreateRecord(&apps.BackupRecord{
		BackupConfigID: rec.BackupConfigID,
		NodeID:         rec.NodeID,
		UserID:         claims.UserID,
		ContainerName:  rec.ContainerName,
		S3Key:          rec.S3Key,
		FileSizeBytes:  rec.FileSizeBytes,
		BackupMethod:   rec.BackupMethod,
		TriggerType:    "download",
		Status:         "completed",
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"download_url": presignedReq.URL,
		"filename":     filename,
	})
}

func strPtr(s string) *string {
	return &s
}
