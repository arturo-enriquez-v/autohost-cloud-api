// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-chi/chi/v5"
)

type BackupHandler struct {
	backupSvc  *apps.BackupService
	jobSvc     *jobs.JobService
	dispatcher dispatcher.ParamDispatcher
	nodeSvc    *node.NodeService
}

func NewBackupHandler(
	backupSvc *apps.BackupService,
	jobSvc *jobs.JobService,
	disp dispatcher.ParamDispatcher,
	nodeSvc *node.NodeService,
) *BackupHandler {
	return &BackupHandler{
		backupSvc:  backupSvc,
		jobSvc:     jobSvc,
		dispatcher: disp,
		nodeSvc:    nodeSvc,
	}
}

func (h *BackupHandler) Routes(nodeAuthMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	// 1. Node Agent endpoints (authenticated with node_token)
	r.Group(func(nodeRouter chi.Router) {
		if nodeAuthMiddleware != nil {
			nodeRouter.Use(nodeAuthMiddleware)
		}
		nodeRouter.Post("/upload/{nodeId}/{recordId}", h.UploadReplicatedBackup)
		nodeRouter.Get("/node-download/{nodeId}/{recordId}", h.DownloadForNode)
	})

	// 2. User frontend endpoints (authenticated with JWT)
	r.Group(func(userRouter chi.Router) {
		userRouter.Use(middleware.Auth)

		// Configs
		userRouter.Get("/config/{nodeId}/{containerName}", h.GetConfig)
		userRouter.Put("/config/{nodeId}/{containerName}", h.UpsertConfig)

		// Records
		userRouter.Get("/records/{nodeId}/{containerName}", h.ListRecords)

		// Run / Restore / Download Actions
		userRouter.Post("/run/{nodeId}/{containerName}", h.RunManualBackup)
		userRouter.Post("/restore/{recordId}", h.RestoreBackup)
		userRouter.Get("/download/{recordId}", h.DownloadBackup)
		userRouter.Get("/storage-usage", h.GetStorageUsage)
	})

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
			json.NewEncoder(w).Encode(map[string]any{
				"configured":          false,
				"storage_destination": "local_and_parent",
				"backup_method":       "volume",
				"retention_count":     7,
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
	Volumes            []string `json:"volumes"`
	CronExpression     string   `json:"cron_expression"`
	RetentionCount     int      `json:"retention_count"`
	PauseContainer     bool     `json:"pause_container"`
	BackupMethod       string   `json:"backup_method"`
	StorageDestination string   `json:"storage_destination"`
	PgUser             string   `json:"pg_user"`
	Enabled            bool     `json:"enabled"`
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

	backupMethod := strings.ToLower(strings.TrimSpace(req.BackupMethod))
	if backupMethod != "pg_dump" && backupMethod != "image" {
		backupMethod = "volume"
	}

	storageDest := strings.ToLower(strings.TrimSpace(req.StorageDestination))
	if storageDest != "local" && storageDest != "s3" {
		storageDest = "local_and_parent"
	}

	pgUser := req.PgUser
	if pgUser == "" {
		pgUser = "postgres"
	}

	cfg, err := h.backupSvc.UpsertConfig(&apps.BackupConfig{
		UserID:             claims.UserID,
		NodeID:             nodeID,
		ContainerName:      containerName,
		Volumes:            volsRaw,
		CronExpression:     cronExpr,
		RetentionCount:     retention,
		PauseContainer:     req.PauseContainer,
		BackupMethod:       backupMethod,
		StorageDestination: storageDest,
		PgUser:             pgUser,
		Enabled:            req.Enabled,
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

	cfg, err := h.backupSvc.GetConfigByContainer(nodeID, containerName)
	if err != nil || cfg == nil {
		volsRaw, _ := json.Marshal([]string{})
		cfg, err = h.backupSvc.UpsertConfig(&apps.BackupConfig{
			UserID:             claims.UserID,
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
		})
		if err != nil {
			log.Printf("[ERROR] auto-initialize backup config failed: %v", err)
			writeJSONError(w, fmt.Sprintf("could not initialize backup config: %v", err), http.StatusInternalServerError)
			return
		}
	}

	storageDest := cfg.StorageDestination
	if storageDest == "" {
		storageDest = "local_and_parent"
	}

	var cred *apps.StorageCredential
	if storageDest == "s3" {
		cred = getSystemR2Credential()
		if cred == nil {
			writeJSONError(w, "Cloudflare R2 storage not configured in server environment variables", http.StatusBadRequest)
			return
		}
	}

	var vols []string
	_ = json.Unmarshal(cfg.Volumes, &vols)

	nowStr := time.Now().Format("2006-01-02_150405")
	backupMethod := cfg.BackupMethod
	if backupMethod != "pg_dump" && backupMethod != "image" {
		backupMethod = "volume"
	}

	var s3KeyPtr *string
	if storageDest == "s3" {
		var ext string
		switch backupMethod {
		case "pg_dump":
			ext = "sql.gz"
		case "image":
			ext = "image.tar.gz"
		default:
			ext = "tar.gz"
		}
		key := fmt.Sprintf("backups/users/%s/nodes/%s/apps/%s/%s/%s.%s", claims.UserID, nodeID, containerName, nowStr, containerName, ext)
		s3KeyPtr = &key
	}

	rec, err := h.backupSvc.CreateRecord(&apps.BackupRecord{
		BackupConfigID:     cfg.ID,
		NodeID:             nodeID,
		UserID:             claims.UserID,
		ContainerName:      containerName,
		StorageDestination: storageDest,
		S3Key:              s3KeyPtr,
		BackupMethod:       backupMethod,
		TriggerType:        "manual",
		Status:             "running",
	})
	if err != nil {
		log.Printf("[ERROR] create backup record failed: %v", err)
		writeJSONError(w, fmt.Sprintf("could not initiate backup record: %v", err), http.StatusInternalServerError)
		return
	}

	apiURL := resolveAPIURL(r)

	var commandName string
	paramsMap := map[string]any{
		"container_name":      containerName,
		"backup_record_id":    rec.ID,
		"storage_destination": storageDest,
		"retention_count":     cfg.RetentionCount,
		"api_url":             apiURL,
	}

	if storageDest == "s3" && cred != nil && s3KeyPtr != nil {
		paramsMap["s3"] = map[string]any{
			"endpoint":          cred.Endpoint,
			"bucket":            cred.Bucket,
			"region":            cred.Region,
			"access_key_id":     cred.AccessKeyID,
			"secret_access_key": cred.SecretAccessKey,
			"s3_key":            *s3KeyPtr,
		}
	}

	switch backupMethod {
	case "pg_dump":
		commandName = "docker.pg.backup"
		pgUser := cfg.PgUser
		if pgUser == "" {
			pgUser = "postgres"
		}
		paramsMap["pg_user"] = pgUser
	case "image":
		commandName = "docker.image.backup"
	default:
		commandName = "docker.volume.backup"
		paramsMap["volumes"] = vols
		paramsMap["pause_container"] = cfg.PauseContainer
	}

	paramsBytes, _ := json.Marshal(paramsMap)

	j, err := h.jobSvc.DispatchWithParams(nodeID, commandName, jobs.CommandTypeDefault, string(paramsBytes))
	if err != nil {
		_ = h.backupSvc.UpdateRecordStatus(rec.ID, "failed", "", "", "", 0, strPtr(err.Error()))
		log.Printf("[ERROR] dispatch backup job failed: %v", err)
		writeJSONError(w, fmt.Sprintf("failed to dispatch backup job: %v", err), http.StatusInternalServerError)
		return
	}

	_ = h.backupSvc.UpdateRecordJobID(rec.ID, j.ID)
	rec.JobID = &j.ID

	_ = h.dispatcher.DispatchJobWithParams(nodeID, j.ID, commandName, jobs.CommandTypeDefault, string(paramsBytes))

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

	cfg, err := h.backupSvc.GetConfigByID(rec.BackupConfigID)
	if err != nil || cfg == nil {
		http.Error(w, "backup configuration missing", http.StatusBadRequest)
		return
	}

	apiURL := resolveAPIURL(r)

	restoreMethod := rec.BackupMethod
	if restoreMethod == "" {
		restoreMethod = "volume"
	}

	paramsMap := map[string]any{
		"container_name": rec.ContainerName,
	}

	if rec.LocalPath != nil && *rec.LocalPath != "" {
		paramsMap["local_path"] = *rec.LocalPath
	}
	if rec.ParentPath != nil && *rec.ParentPath != "" {
		paramsMap["parent_download_url"] = fmt.Sprintf("%s/v1/backups/node-download/%s/%s", apiURL, rec.NodeID, rec.ID)
	}

	if rec.S3Key != nil && *rec.S3Key != "" {
		if cred := getSystemR2Credential(); cred != nil {
			paramsMap["s3"] = map[string]any{
				"endpoint":          cred.Endpoint,
				"bucket":            cred.Bucket,
				"region":            cred.Region,
				"access_key_id":     cred.AccessKeyID,
				"secret_access_key": cred.SecretAccessKey,
				"s3_key":            *rec.S3Key,
			}
		}
	}

	var restoreCommandName string
	switch restoreMethod {
	case "pg_dump":
		restoreCommandName = "docker.pg.restore"
		pgUser := cfg.PgUser
		if pgUser == "" {
			pgUser = "postgres"
		}
		paramsMap["pg_user"] = pgUser
	case "image":
		restoreCommandName = "docker.image.restore"
	default:
		restoreCommandName = "docker.volume.restore"
		var vols []string
		_ = json.Unmarshal(cfg.Volumes, &vols)
		paramsMap["volumes"] = vols
	}

	paramsBytes, _ := json.Marshal(paramsMap)

	restoreRec, err := h.backupSvc.CreateRecord(&apps.BackupRecord{
		BackupConfigID:     cfg.ID,
		NodeID:             rec.NodeID,
		UserID:             claims.UserID,
		ContainerName:      rec.ContainerName,
		StorageDestination: rec.StorageDestination,
		LocalPath:          rec.LocalPath,
		ParentPath:         rec.ParentPath,
		S3Key:              rec.S3Key,
		FileSizeBytes:      rec.FileSizeBytes,
		BackupMethod:       restoreMethod,
		TriggerType:        "restore",
		Status:             "running",
	})
	if err != nil {
		log.Printf("[ERROR] create restore record failed: %v", err)
		writeJSONError(w, fmt.Sprintf("could not initiate restore record: %v", err), http.StatusInternalServerError)
		return
	}

	j, err := h.jobSvc.DispatchWithParams(rec.NodeID, restoreCommandName, jobs.CommandTypeDefault, string(paramsBytes))
	if err != nil {
		_ = h.backupSvc.UpdateRecordStatus(restoreRec.ID, "failed", "", "", "", 0, strPtr(err.Error()))
		writeJSONError(w, "failed to dispatch restore job", http.StatusInternalServerError)
		return
	}

	_ = h.backupSvc.UpdateRecordJobID(restoreRec.ID, j.ID)

	_ = h.dispatcher.DispatchJobWithParams(rec.NodeID, j.ID, restoreCommandName, jobs.CommandTypeDefault, string(paramsBytes))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":    "restoring",
		"job_id":    j.ID,
		"record_id": restoreRec.ID,
	})
}

// ─── Upload & Download Handlers ─────────────────────────────────────────────

func (h *BackupHandler) UploadReplicatedBackup(w http.ResponseWriter, r *http.Request) {
	nodeToken := middleware.GetNodeToken(r.Context())
	nodeID := chi.URLParam(r, "nodeId")
	recordID := chi.URLParam(r, "recordId")

	if nodeToken == nil || nodeToken.NodeID != nodeID {
		writeJSONError(w, "forbidden: node_id mismatch", http.StatusForbidden)
		return
	}

	rec, err := h.backupSvc.GetRecordByID(recordID)
	if err != nil || rec == nil {
		writeJSONError(w, "backup record not found", http.StatusNotFound)
		return
	}

	filename := r.URL.Query().Get("filename")
	if filename == "" {
		filename = fmt.Sprintf("%s.tar.gz", recordID)
	}
	filename = filepath.Base(filename)

	baseDir := platform.BackupsDir()
	if usage, err := platform.GetStorageUsage(baseDir); err == nil {
		if usage.UsagePercent >= 98.0 {
			log.Printf("[ERROR] parent storage %s is critically full (%.1f%%), rejecting backup upload", baseDir, usage.UsagePercent)
			writeJSONError(w, "parent node storage is full (>= 98% used)", http.StatusInsufficientStorage)
			return
		} else if usage.WarningHigh {
			log.Printf("[WARN] parent storage %s usage is high (%.1f%%)", baseDir, usage.UsagePercent)
		}
	}
	targetDir := filepath.Join(baseDir, "nodes", nodeID, rec.ContainerName)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		log.Printf("[ERROR] create parent backup dir %s: %v", targetDir, err)
		writeJSONError(w, "failed to create storage directory", http.StatusInternalServerError)
		return
	}

	targetPath := filepath.Join(targetDir, filename)
	tmpPath := targetPath + ".part"

	out, err := os.Create(tmpPath)
	if err != nil {
		log.Printf("[ERROR] create temp backup file %s: %v", tmpPath, err)
		writeJSONError(w, "failed to create file", http.StatusInternalServerError)
		return
	}

	n, err := io.Copy(out, r.Body)
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		log.Printf("[ERROR] stream copy backup: %v", err)
		writeJSONError(w, "failed to write backup stream", http.StatusInternalServerError)
		return
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		writeJSONError(w, "failed to commit backup file", http.StatusInternalServerError)
		return
	}

	// Update database with parent path and file size
	_ = h.backupSvc.UpdateRecordParentPath(recordID, targetPath, n)

	// Clean up old replicated backups on parent node if retention count is configured
	if cfg, err := h.backupSvc.GetConfigByID(rec.BackupConfigID); err == nil && cfg.RetentionCount > 0 {
		h.cleanupOldReplicas(targetDir, cfg.RetentionCount)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":          "replicated",
		"parent_path":     targetPath,
		"file_size_bytes": n,
	})
}

func (h *BackupHandler) DownloadForNode(w http.ResponseWriter, r *http.Request) {
	nodeToken := middleware.GetNodeToken(r.Context())
	nodeID := chi.URLParam(r, "nodeId")
	recordID := chi.URLParam(r, "recordId")

	if nodeToken == nil || nodeToken.NodeID != nodeID {
		writeJSONError(w, "forbidden: node_id mismatch", http.StatusForbidden)
		return
	}

	rec, err := h.backupSvc.GetRecordByID(recordID)
	if err != nil || rec == nil {
		writeJSONError(w, "backup record not found", http.StatusNotFound)
		return
	}

	if rec.ParentPath == nil || *rec.ParentPath == "" {
		writeJSONError(w, "no replicated backup file available on parent node", http.StatusNotFound)
		return
	}

	if _, err := os.Stat(*rec.ParentPath); err != nil {
		writeJSONError(w, "replicated file not found on disk", http.StatusNotFound)
		return
	}

	http.ServeFile(w, r, *rec.ParentPath)
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

	// 1. If stored on parent node disk, serve directly
	if rec.ParentPath != nil && *rec.ParentPath != "" {
		if _, err := os.Stat(*rec.ParentPath); err == nil {
			filename := filepath.Base(*rec.ParentPath)
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
			http.ServeFile(w, r, *rec.ParentPath)
			return
		}
	}

	// 2. If stored in S3/R2, generate presigned URL
	if rec.S3Key != nil && *rec.S3Key != "" {
		cred := getSystemR2Credential()
		if cred != nil {
			s3Client := s3.New(s3.Options{
				Region:       cred.Region,
				Credentials:  credentials.NewStaticCredentialsProvider(cred.AccessKeyID, cred.SecretAccessKey, ""),
				BaseEndpoint: aws.String(cred.Endpoint),
				UsePathStyle: true,
			})

			presignClient := s3.NewPresignClient(s3Client)
			filename := filepath.Base(*rec.S3Key)
			disposition := fmt.Sprintf("attachment; filename=\"%s\"", filename)

			presignedReq, err := presignClient.PresignGetObject(r.Context(), &s3.GetObjectInput{
				Bucket:                     aws.String(cred.Bucket),
				Key:                        aws.String(*rec.S3Key),
				ResponseContentDisposition: aws.String(disposition),
			}, s3.WithPresignExpires(15*time.Minute))

			if err == nil {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{
					"download_url": presignedReq.URL,
					"filename":     filename,
				})
				return
			}
			log.Printf("[ERROR] presign download URL failed: %v", err)
		}
	}

	writeJSONError(w, "backup file is not available on parent server (stored locally on remote node)", http.StatusNotFound)
}

func (h *BackupHandler) cleanupOldReplicas(dir string, maxCount int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	type fileInfo struct {
		path    string
		modTime time.Time
	}
	var files []fileInfo

	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		info, err := e.Info()
		if err == nil {
			files = append(files, fileInfo{
				path:    filepath.Join(dir, e.Name()),
				modTime: info.ModTime(),
			})
		}
	}

	if len(files) <= maxCount {
		return
	}

	// Sort newest first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	// Remove older files beyond maxCount
	for i := maxCount; i < len(files); i++ {
		_ = os.Remove(files[i].path)
	}
}

func (h *BackupHandler) GetStorageUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := platform.GetStorageUsage(platform.BackupsDir())
	if err != nil {
		writeJSONError(w, "could not inspect storage usage", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usage)
}

func resolveAPIURL(r *http.Request) string {
	if u := os.Getenv("API_PUBLIC_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	if u := os.Getenv("API_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	if r != nil && r.Host != "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		return fmt.Sprintf("%s://%s", scheme, r.Host)
	}
	return "http://localhost:8080"
}

func strPtr(s string) *string {
	return &s
}
