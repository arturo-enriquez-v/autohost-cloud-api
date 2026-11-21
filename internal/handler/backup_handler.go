// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
)

type BackupHandler struct {
	backupSvc       *apps.BackupService
	jobSvc          *jobs.JobService
	dispatcher      dispatcher.ParamDispatcher
	nodeSvc         *node.NodeService
	rbacSvc         *identity.RBACService
	nodeFileHandler *NodeFileHandler
	files           *apps.BackupFileManager
}

func (h *BackupHandler) SetRBACService(rbac *identity.RBACService) {
	h.rbacSvc = rbac
}

func NewBackupHandler(
	backupSvc *apps.BackupService,
	jobSvc *jobs.JobService,
	disp dispatcher.ParamDispatcher,
	nodeSvc *node.NodeService,
	nodeFileHandler *NodeFileHandler,
) *BackupHandler {
	bh := &BackupHandler{
		backupSvc:       backupSvc,
		jobSvc:          jobSvc,
		dispatcher:      disp,
		nodeSvc:         nodeSvc,
		nodeFileHandler: nodeFileHandler,
	}
	// Only hand real (non-nil) dependencies to the file manager: a nil pointer wrapped in an interface
	// would look configured and panic on use.
	var nodes apps.BackupNodeLookup
	if nodeSvc != nil {
		nodes = nodeSvc
	}
	var files apps.SharedFileOps
	if nodeFileHandler != nil && nodeFileHandler.SharedFiles() != nil {
		files = nodeFileHandler.SharedFiles()
	}
	bh.files = apps.NewBackupFileManager(backupSvc, nodes, files)
	if backupSvc != nil {
		backupSvc.SetOnJobCompleted(bh.files.ReplicateAfterJob)
	}
	return bh
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
		userRouter.With(h.nodePerm(identity.PermBackupsRead)).Get("/config/{nodeId}/{containerName}", h.GetConfig)
		userRouter.With(h.nodePerm(identity.PermBackupsManage)).Put("/config/{nodeId}/{containerName}", h.UpsertConfig)

		// Records
		userRouter.With(h.nodePerm(identity.PermBackupsRead)).Get("/records/{nodeId}/{containerName}", h.ListRecords)
		userRouter.Delete("/records/{recordId}", h.DeleteRecord)

		// Run / Restore / Download Actions
		userRouter.With(h.nodePerm(identity.PermBackupsCreate)).Post("/run/{nodeId}/{containerName}", h.RunManualBackup)
		userRouter.Post("/restore/{recordId}", h.RestoreBackup)
		userRouter.Get("/download/{recordId}", h.DownloadBackup)
		userRouter.Get("/storage-usage", h.GetStorageUsage)
	})

	return r
}

// ─── Config Handlers ────────────────────────────────────────────────────────

func (h *BackupHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	containerName := chi.URLParam(r, "containerName")

	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, nodeID, identity.PermBackupsRead); err != nil || !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	cfg, err := h.backupSvc.GetConfigByContainer(nodeID, containerName)
	if err != nil {
		if err == apps.ErrBackupNotFound {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"configured":          false,
				"storage_destination": "local",
				"replica_node_id":     nil,
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
	ReplicaNodeID      *string  `json:"replica_node_id"`
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

	if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, nodeID, identity.PermBackupsManage); err != nil || !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req upsertConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := validateContainerName(containerName); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, v := range req.Volumes {
		if err := validateBackupVolume(v); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
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
	switch storageDest {
	case "local", "local_and_parent", "local_and_replica", "remote_node":
	default:
		storageDest = "local"
	}

	var replicaNodeID *string
	if req.ReplicaNodeID != nil && strings.TrimSpace(*req.ReplicaNodeID) != "" {
		rid := strings.TrimSpace(*req.ReplicaNodeID)
		if rid == nodeID {
			writeJSONError(w, "replica_node_id cannot be the same as the source node", http.StatusBadRequest)
			return
		}
		if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, rid, identity.PermFilesWrite); err != nil || !ok {
			writeJSONError(w, "forbidden: no write permission on selected replica node", http.StatusForbidden)
			return
		}
		replicaNodeID = &rid
	}

	if (storageDest == "remote_node" || storageDest == "local_and_replica") && replicaNodeID == nil {
		writeJSONError(w, "replica_node_id is required for remote_node or local_and_replica storage", http.StatusBadRequest)
		return
	}

	pgUser := strings.TrimSpace(req.PgUser)
	if pgUser == "" {
		pgUser = "postgres"
	}
	if err := validatePgIdentifier("user", pgUser); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
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
		ReplicaNodeID:      replicaNodeID,
		PgUser:             pgUser,
		Enabled:            req.Enabled,
	})
	if err != nil {
		log.Printf("[ERROR] upsert backup config: %v", err)
		writeJSONError(w, fmt.Sprintf("failed to save backup config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

// ─── Records Handlers ────────────────────────────────────────────────────────

func (h *BackupHandler) ListRecords(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	containerName := chi.URLParam(r, "containerName")

	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, nodeID, identity.PermBackupsRead); err != nil || !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	recs, err := h.backupSvc.ListRecordsByContainer(nodeID, containerName)
	if err != nil {
		http.Error(w, "failed to fetch backup records", http.StatusInternalServerError)
		return
	}

	h.files.ReconcileRecords(r.Context(), recs)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(recs)
}

func (h *BackupHandler) DeleteRecord(w http.ResponseWriter, r *http.Request) {
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

	if h.rbacSvc != nil {
		if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, rec.NodeID, identity.PermBackupsManage); err != nil || !ok {
			writeJSONError(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if rec.UserID != claims.UserID {
		writeJSONError(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := h.files.DeleteBackup(r.Context(), rec, deletedByLabel(claims)); err != nil {
		var partial *apps.PartialDeleteError
		switch {
		case errors.Is(err, apps.ErrBackupStillRunning):
			writeJSONError(w, err.Error(), http.StatusConflict)
		case errors.Is(err, apps.ErrBackupInObjectStorage):
			writeJSONError(w, err.Error(), http.StatusNotImplemented)
		case errors.As(err, &partial):
			writeJSONError(w, partial.Error(), http.StatusBadGateway)
		default:
			writeJSONError(w, "archive removed but failed to update record", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

func deletedByLabel(c *middleware.Claims) string {
	if c.Email != "" {
		return c.Email
	}
	return c.UserID
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

	if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, nodeID, identity.PermBackupsCreate); err != nil || !ok {
		writeJSONError(w, "forbidden", http.StatusForbidden)
		return
	}

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
	if storageDest == "" || storageDest == "s3" {
		storageDest = "local"
	}

	var vols []string
	_ = json.Unmarshal(cfg.Volumes, &vols)

	backupMethod := cfg.BackupMethod
	if backupMethod != "pg_dump" && backupMethod != "image" {
		backupMethod = "volume"
	}

	rec, err := h.backupSvc.CreateRecord(&apps.BackupRecord{
		BackupConfigID:     cfg.ID,
		NodeID:             nodeID,
		UserID:             claims.UserID,
		ContainerName:      containerName,
		StorageDestination: storageDest,
		ReplicaNodeID:      cfg.ReplicaNodeID,
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
	if h.nodeFileHandler != nil {
		apiURL = h.nodeFileHandler.ResolveMasterBaseURL(r)
	}

	var commandName string
	paramsMap := map[string]any{
		"node_id":             nodeID,
		"container_name":      containerName,
		"backup_record_id":    rec.ID,
		"storage_destination": storageDest,
		"retention_count":     cfg.RetentionCount,
		"api_url":             apiURL,
	}
	if cfg.ReplicaNodeID != nil && *cfg.ReplicaNodeID != "" {
		paramsMap["replica_node_id"] = *cfg.ReplicaNodeID
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

	if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, rec.NodeID, identity.PermBackupsRestore); err != nil || !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	cfg, err := h.backupSvc.GetConfigByID(rec.BackupConfigID)
	if err != nil || cfg == nil {
		http.Error(w, "backup configuration missing", http.StatusBadRequest)
		return
	}

	apiURL := resolveAPIURL(r)
	if h.nodeFileHandler != nil {
		apiURL = h.nodeFileHandler.ResolveMasterBaseURL(r)
	}

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
	if (rec.ParentPath != nil && *rec.ParentPath != "") || (rec.ReplicaNodeID != nil && rec.ReplicaPath != nil && *rec.ReplicaPath != "") {
		paramsMap["parent_download_url"] = fmt.Sprintf("%s/v1/backups/node-download/%s/%s", apiURL, rec.NodeID, rec.ID)
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
	if rec.NodeID != nodeID {
		writeJSONError(w, "forbidden: backup record belongs to another node", http.StatusForbidden)
		return
	}
	if err := validateContainerName(rec.ContainerName); err != nil {
		writeJSONError(w, "invalid container name on backup record", http.StatusBadRequest)
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
	targetDir, targetPath := h.files.ParentArchiveLocation(nodeID, rec.ContainerName, filename)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		log.Printf("[ERROR] create parent backup dir %s: %v", targetDir, err)
		writeJSONError(w, "failed to create storage directory", http.StatusInternalServerError)
		return
	}

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

	// Record the parent copy, start the replica copy (if configured) and apply retention.
	baseURL := resolveAPIURL(r)
	if h.nodeFileHandler != nil {
		baseURL = h.nodeFileHandler.ResolveMasterBaseURL(r)
	}
	h.files.AfterParentUpload(rec, targetDir, targetPath, n, baseURL)

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
	// Restores are dispatched to the record's own node; no other node may fetch its archive.
	if rec.NodeID != nodeID {
		writeJSONError(w, "forbidden: backup record belongs to another node", http.StatusForbidden)
		return
	}

	if parentPath := h.files.TrustedParentPath(rec); parentPath != "" {
		if _, err := os.Stat(parentPath); err == nil {
			http.ServeFile(w, r, parentPath)
			return
		}
	}

	if rec.ReplicaNodeID != nil && *rec.ReplicaNodeID != "" && rec.ReplicaPath != nil && *rec.ReplicaPath != "" && h.nodeFileHandler != nil && h.nodeSvc != nil {
		if replicaNode, err := h.nodeSvc.GetByID(*rec.ReplicaNodeID); err == nil && replicaNode != nil {
			if err := h.nodeFileHandler.StreamSharedFileAttachment(w, r, replicaNode, *rec.ReplicaPath, filepath.Base(*rec.ReplicaPath)); err == nil {
				return
			}
		}
	}

	writeJSONError(w, "no replicated backup file available on parent or replica node", http.StatusNotFound)
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

	if h.rbacSvc != nil {
		if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, rec.NodeID, identity.PermBackupsCreate); err != nil || !ok {
			writeJSONError(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if rec.UserID != claims.UserID {
		writeJSONError(w, "forbidden", http.StatusForbidden)
		return
	}

	// 1. If stored on parent node disk, serve directly
	if parentPath := h.files.TrustedParentPath(rec); parentPath != "" {
		if _, err := os.Stat(parentPath); err == nil {
			filename := filepath.Base(parentPath)
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
			http.ServeFile(w, r, parentPath)
			return
		}
	}

	// 2. If stored on the source node's shared directory, stream via gRPC/HTTP
	if rec.LocalPath != nil && *rec.LocalPath != "" && h.nodeFileHandler != nil && h.nodeSvc != nil {
		if srcNode, err := h.nodeSvc.GetByID(rec.NodeID); err == nil && srcNode != nil {
			relPath := apps.SharedBackupRelPath(*rec.LocalPath, rec.ContainerName)
			filename := filepath.Base(*rec.LocalPath)
			if err := h.nodeFileHandler.StreamSharedFileAttachment(w, r, srcNode, relPath, filename); err == nil {
				return
			} else {
				log.Printf("[WARN] stream backup from source node %s (%s) failed: %v", rec.NodeID, relPath, err)
			}
		}
	}

	// 3. If replicated to another node's shared directory, stream from replica node
	if rec.ReplicaNodeID != nil && *rec.ReplicaNodeID != "" && rec.ReplicaPath != nil && *rec.ReplicaPath != "" && h.nodeFileHandler != nil && h.nodeSvc != nil {
		if replicaNode, err := h.nodeSvc.GetByID(*rec.ReplicaNodeID); err == nil && replicaNode != nil {
			filename := filepath.Base(*rec.ReplicaPath)
			if err := h.nodeFileHandler.StreamSharedFileAttachment(w, r, replicaNode, *rec.ReplicaPath, filename); err == nil {
				return
			} else {
				log.Printf("[WARN] stream backup from replica node %s (%s) failed: %v", *rec.ReplicaNodeID, *rec.ReplicaPath, err)
			}
		}
	}

	writeJSONError(w, "backup file is not available on parent, source, or replica node", http.StatusNotFound)
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

// nodePerm returns the route-level node authorization middleware (see requireNodePerm).
func (h *BackupHandler) nodePerm(perm string) func(http.Handler) http.Handler {
	return requireNodePerm(func() *identity.RBACService { return h.rbacSvc }, perm)
}
