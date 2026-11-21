// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
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
	if backupSvc != nil {
		backupSvc.SetOnJobCompleted(bh.HandlePostBackupReplication)
	}
	return bh
}

// HandlePostBackupReplication runs asynchronously when a backup job finishes on a node agent.
// It uses gRPC file operations over the node's shared folder to replicate or move the backup
// to the parent server and/or the selected replica node.
func (h *BackupHandler) HandlePostBackupReplication(jobID string) {
	if h.backupSvc == nil || h.nodeFileHandler == nil || h.nodeSvc == nil {
		return
	}

	rec, err := h.backupSvc.GetRecordByJobID(jobID)
	if err != nil || rec == nil || rec.TriggerType == "restore" {
		return
	}
	if rec.LocalPath == nil || *rec.LocalPath == "" {
		return
	}

	dest := strings.ToLower(strings.TrimSpace(rec.StorageDestination))
	hasReplicaNode := rec.ReplicaNodeID != nil && strings.TrimSpace(*rec.ReplicaNodeID) != ""
	needsParentCopy := dest == "local_and_parent" && (rec.ParentPath == nil || *rec.ParentPath == "")
	needsReplicaCopy := hasReplicaNode && (rec.ReplicaPath == nil || *rec.ReplicaPath == "")

	if !needsParentCopy && !needsReplicaCopy && dest != "remote_node" {
		return
	}

	srcNode, err := h.nodeSvc.GetByID(rec.NodeID)
	if err != nil || srcNode == nil {
		log.Printf("[BACKUP] post-backup replication: source node %s not found: %v", rec.NodeID, err)
		return
	}

	filename := filepath.Base(*rec.LocalPath)
	srcRelPath := extractSharedBackupRelPath(*rec.LocalPath, rec.ContainerName)
	baseURL := h.nodeFileHandler.ResolveMasterBaseURL(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// 1. Pull to parent disk if needed for local_and_parent
	if needsParentCopy {
		targetDir := filepath.Join(platform.BackupsDir(), "nodes", rec.NodeID, rec.ContainerName)
		targetPath := filepath.Join(targetDir, filename)
		sizeBytes, err := h.nodeFileHandler.PullNodeSharedFileToLocal(ctx, baseURL, srcNode, srcRelPath, targetPath)
		if err != nil {
			log.Printf("[ERROR] post-backup pull to parent for record %s failed: %v", rec.ID, err)
		} else {
			_ = h.backupSvc.UpdateRecordParentPath(rec.ID, targetPath, sizeBytes)
			rec.ParentPath = &targetPath
			if cfg, err := h.backupSvc.GetConfigByID(rec.BackupConfigID); err == nil && cfg.RetentionCount > 0 {
				h.cleanupOldReplicas(targetDir, cfg.RetentionCount)
			}
			log.Printf("[BACKUP] pulled backup %s to parent path %s", rec.ID, targetPath)
		}
	}

	// 2. Replicate to target replica node if configured
	replicaSucceeded := rec.ReplicaPath != nil && *rec.ReplicaPath != ""
	if needsReplicaCopy {
		replicaNodeID := strings.TrimSpace(*rec.ReplicaNodeID)
		replicaNode, err := h.nodeSvc.GetByID(replicaNodeID)
		if err != nil || replicaNode == nil {
			log.Printf("[ERROR] post-backup replica node %s not found: %v", replicaNodeID, err)
			return
		}
		replicaRelPath := fmt.Sprintf("/backup/%s/%s", rec.ContainerName, filename)

		var repErr error
		parentPath := trustedParentPath(rec)
		if parentPath != "" {
			if _, statErr := os.Stat(parentPath); statErr == nil {
				repErr = h.nodeFileHandler.PushLocalFileToNodeShared(ctx, baseURL, replicaNode, parentPath, replicaRelPath)
			}
		}
		if repErr != nil || parentPath == "" {
			_, repErr = h.nodeFileHandler.TransferSharedFile(ctx, baseURL, srcNode, srcRelPath, replicaNode, replicaRelPath)
		}

		if repErr != nil {
			log.Printf("[ERROR] post-backup transfer of %s to replica node %s failed: %v", rec.ID, replicaNodeID, repErr)
			return
		}

		_ = h.backupSvc.UpdateRecordReplica(rec.ID, replicaNodeID, replicaRelPath)
		replicaSucceeded = true
		log.Printf("[BACKUP] transferred backup %s to replica node %s (%s)", rec.ID, replicaNodeID, replicaRelPath)
	}

	// 3. If destination is remote_node only and replica succeeded, remove the source node copy
	if dest == "remote_node" && replicaSucceeded {
		if err := h.nodeFileHandler.DeleteNodeSharedFile(ctx, srcNode, srcRelPath); err != nil {
			log.Printf("[WARN] failed to delete temporary source backup %s on node %s: %v", srcRelPath, rec.NodeID, err)
		} else {
			_ = h.backupSvc.ClearRecordLocalPath(rec.ID)
			log.Printf("[BACKUP] removed source copy %s on node %s (remote_node mode)", srcRelPath, rec.NodeID)
		}
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
	if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, nodeID, identity.PermBackupsRead); err != nil || !ok {
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

	if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, nodeID, identity.PermBackupsManage); err != nil || !ok {
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
		if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, rid, identity.PermFilesWrite); err != nil || !ok {
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
	if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, nodeID, identity.PermBackupsRead); err != nil || !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	recs, err := h.backupSvc.ListRecordsByContainer(nodeID, containerName)
	if err != nil {
		http.Error(w, "failed to fetch backup records", http.StatusInternalServerError)
		return
	}

	h.reconcileRecordFiles(r.Context(), recs)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(recs)
}

// fileState is the result of checking a single storage location of a backup archive.
type fileState int

const (
	fileUnknown fileState = iota // could not verify (node offline, timeout, ...)
	filePresent
	fileMissing
)

// reconcileRecordFiles checks every completed backup record against the storage locations it points to
// (source node shared folder, parent server disk, replica node shared folder). Locations whose archive was
// removed (manually, or by retention cleanup) are cleared, and records left without any archive are marked
// as "deleted" so they disappear from the restorable snapshots while remaining in the audit log.
// Locations that cannot be verified (e.g. node offline) are left untouched.
func (h *BackupHandler) reconcileRecordFiles(ctx context.Context, recs []*apps.BackupRecord) {
	if h.nodeSvc == nil || h.nodeFileHandler == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	nodes := map[string]*node.Node{}
	nodeMissing := map[string]bool{}
	getNode := func(id string) *node.Node {
		if n, ok := nodes[id]; ok {
			return n
		}
		if nodeMissing[id] {
			return nil
		}
		n, err := h.nodeSvc.GetByID(id)
		if err != nil || n == nil {
			nodeMissing[id] = true
			return nil
		}
		nodes[id] = n
		return n
	}

	type dirListing struct {
		names map[string]struct{}
		ok    bool
	}
	listings := map[string]dirListing{}
	checkShared := func(nodeID, relPath string) fileState {
		n := getNode(nodeID)
		if n == nil {
			return fileUnknown
		}
		relPath = filepath.ToSlash(filepath.Clean("/" + relPath))
		dir, name := filepath.ToSlash(filepath.Dir(relPath)), filepath.Base(relPath)
		key := nodeID + "|" + dir
		l, cached := listings[key]
		if !cached {
			names, err := h.nodeFileHandler.ListNodeSharedFileNames(ctx, n, dir)
			if err != nil {
				log.Printf("[BACKUP] reconcile: cannot list %s on node %s: %v", dir, nodeID, err)
			}
			l = dirListing{names: names, ok: err == nil}
			listings[key] = l
		}
		if !l.ok {
			return fileUnknown
		}
		if _, found := l.names[name]; found {
			return filePresent
		}
		return fileMissing
	}

	for _, rec := range recs {
		if rec == nil || rec.Status != "completed" || rec.TriggerType == "restore" || rec.TriggerType == "download" {
			continue
		}
		// Archives stored in object storage cannot be verified here; leave them alone.
		if rec.S3Key != nil && *rec.S3Key != "" {
			continue
		}

		hasLocal := rec.LocalPath != nil && *rec.LocalPath != ""
		hasParent := rec.ParentPath != nil && *rec.ParentPath != ""
		hasReplica := rec.ReplicaPath != nil && *rec.ReplicaPath != "" && rec.ReplicaNodeID != nil && *rec.ReplicaNodeID != ""
		if !hasLocal && !hasParent && !hasReplica {
			continue
		}

		states := []fileState{}
		changed := false
		localPath, parentPath, replicaPath := rec.LocalPath, rec.ParentPath, rec.ReplicaPath

		if hasLocal {
			st := checkShared(rec.NodeID, extractSharedBackupRelPath(*rec.LocalPath, rec.ContainerName))
			states = append(states, st)
			if st == fileMissing {
				localPath, changed = nil, true
			}
		}
		if hasParent {
			st := fileUnknown
			if trusted := trustedParentPath(rec); trusted == "" {
				st = fileMissing
			} else if _, err := os.Stat(trusted); err == nil {
				st = filePresent
			} else if os.IsNotExist(err) {
				st = fileMissing
			}
			states = append(states, st)
			if st == fileMissing {
				parentPath, changed = nil, true
			}
		}
		if hasReplica {
			st := checkShared(*rec.ReplicaNodeID, *rec.ReplicaPath)
			states = append(states, st)
			if st == fileMissing {
				replicaPath, changed = nil, true
			}
		}

		allMissing := true
		for _, st := range states {
			if st != fileMissing {
				allMissing = false
				break
			}
		}

		if allMissing {
			reason := "backup archive no longer exists on any storage location"
			if err := h.backupSvc.MarkRecordDeleted(rec.ID, reason); err != nil {
				log.Printf("[BACKUP] reconcile: failed to mark record %s as deleted: %v", rec.ID, err)
				continue
			}
			rec.Status = "deleted"
			rec.ErrorMessage = &reason
			log.Printf("[BACKUP] reconcile: record %s marked deleted (archive missing)", rec.ID)
			continue
		}

		if changed {
			if err := h.backupSvc.UpdateRecordLocations(rec.ID, localPath, parentPath, replicaPath); err != nil {
				log.Printf("[BACKUP] reconcile: failed to update locations of record %s: %v", rec.ID, err)
				continue
			}
			rec.LocalPath, rec.ParentPath, rec.ReplicaPath = localPath, parentPath, replicaPath
		}
	}
}

// DeleteRecord removes a backup archive from every storage location it lives on and marks the record
// as deleted (the row is kept for the audit log).
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
		if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, rec.NodeID, identity.PermBackupsManage); err != nil || !ok {
			writeJSONError(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if rec.UserID != claims.UserID {
		writeJSONError(w, "forbidden", http.StatusForbidden)
		return
	}

	if rec.Status == "running" {
		writeJSONError(w, "cannot delete a backup that is still running", http.StatusConflict)
		return
	}
	if rec.S3Key != nil && *rec.S3Key != "" {
		writeJSONError(w, "deleting backups stored in object storage is not supported yet", http.StatusNotImplemented)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var failures []string
	localPath, parentPath, replicaPath := rec.LocalPath, rec.ParentPath, rec.ReplicaPath

	if rec.LocalPath != nil && *rec.LocalPath != "" {
		relPath := extractSharedBackupRelPath(*rec.LocalPath, rec.ContainerName)
		srcNode, err := h.nodeSvc.GetByID(rec.NodeID)
		if err == nil && srcNode != nil {
			err = h.nodeFileHandler.DeleteNodeSharedFileIfExists(ctx, srcNode, relPath)
		} else if err == nil {
			err = errors.New("source node not found")
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("source node: %v", err))
		} else {
			localPath = nil
		}
	}

	if rec.ParentPath != nil && *rec.ParentPath != "" {
		trusted := trustedParentPath(rec)
		if trusted == "" {
			// Untrusted path outside the backups dir: drop the reference without touching the disk.
			parentPath = nil
		} else if err := os.Remove(trusted); err != nil && !os.IsNotExist(err) {
			failures = append(failures, fmt.Sprintf("parent server: %v", err))
		} else {
			parentPath = nil
		}
	}

	if rec.ReplicaPath != nil && *rec.ReplicaPath != "" && rec.ReplicaNodeID != nil && *rec.ReplicaNodeID != "" {
		replicaNode, err := h.nodeSvc.GetByID(*rec.ReplicaNodeID)
		if err == nil && replicaNode != nil {
			err = h.nodeFileHandler.DeleteNodeSharedFileIfExists(ctx, replicaNode, *rec.ReplicaPath)
		} else if err == nil {
			err = errors.New("replica node not found")
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("replica node: %v", err))
		} else {
			replicaPath = nil
		}
	}

	if len(failures) > 0 {
		// Persist what was actually removed so the record keeps pointing only at remaining copies.
		_ = h.backupSvc.UpdateRecordLocations(rec.ID, localPath, parentPath, replicaPath)
		writeJSONError(w, "failed to delete backup archive: "+strings.Join(failures, "; "), http.StatusBadGateway)
		return
	}

	if err := h.backupSvc.MarkRecordDeleted(rec.ID, "deleted manually by "+deletedByLabel(claims)); err != nil {
		writeJSONError(w, "archive removed but failed to update record", http.StatusInternalServerError)
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

	if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, nodeID, identity.PermBackupsCreate); err != nil || !ok {
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

	if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, rec.NodeID, identity.PermBackupsRestore); err != nil || !ok {
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

	// Replicate to target replica node if configured
	if rec.ReplicaNodeID != nil && *rec.ReplicaNodeID != "" && h.nodeFileHandler != nil && h.nodeSvc != nil {
		replicaNodeID := *rec.ReplicaNodeID
		replicaRelPath := fmt.Sprintf("/backup/%s/%s", rec.ContainerName, filename)
		apiURL := resolveAPIURL(r)
		if h.nodeFileHandler != nil {
			apiURL = h.nodeFileHandler.ResolveMasterBaseURL(r)
		}

		if replicaNode, err := h.nodeSvc.GetByID(replicaNodeID); err == nil && replicaNode != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			go func() {
				defer cancel()
				if err := h.nodeFileHandler.PushLocalFileToNodeShared(ctx, apiURL, replicaNode, targetPath, replicaRelPath); err != nil {
					log.Printf("[ERROR] replicate backup %s to node %s failed: %v", recordID, replicaNodeID, err)
					return
				}
				_ = h.backupSvc.UpdateRecordReplica(recordID, replicaNodeID, replicaRelPath)
				log.Printf("[BACKUP] replicated backup %s to node %s at %s", recordID, replicaNodeID, replicaRelPath)
			}()
		}
	}

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
	// Restores are dispatched to the record's own node; no other node may fetch its archive.
	if rec.NodeID != nodeID {
		writeJSONError(w, "forbidden: backup record belongs to another node", http.StatusForbidden)
		return
	}

	if parentPath := trustedParentPath(rec); parentPath != "" {
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
		if ok, err := h.rbacSvc.HasNodePermission(claims.UserID, rec.NodeID, identity.PermBackupsCreate); err != nil || !ok {
			writeJSONError(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if rec.UserID != claims.UserID {
		writeJSONError(w, "forbidden", http.StatusForbidden)
		return
	}

	// 1. If stored on parent node disk, serve directly
	if parentPath := trustedParentPath(rec); parentPath != "" {
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
			relPath := extractSharedBackupRelPath(*rec.LocalPath, rec.ContainerName)
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

// trustedParentPath returns the record's parent_path only if it lives inside the API backups
// directory. Any other value is treated as absent so it is never served, copied or deleted.
func trustedParentPath(rec *apps.BackupRecord) string {
	if rec == nil || rec.ParentPath == nil || *rec.ParentPath == "" {
		return ""
	}
	if !platform.IsWithinDir(platform.BackupsDir(), *rec.ParentPath) {
		log.Printf("[BACKUP] ignoring parent_path %q of record %s: outside backups dir", *rec.ParentPath, rec.ID)
		return ""
	}
	return *rec.ParentPath
}

func extractSharedBackupRelPath(localPath, containerName string) string {
	cleaned := filepath.ToSlash(filepath.Clean(localPath))
	if idx := strings.Index(cleaned, "/backup/"); idx != -1 {
		return cleaned[idx:]
	}
	if strings.HasPrefix(cleaned, "backup/") {
		return "/" + cleaned
	}
	return fmt.Sprintf("/backup/%s/%s", containerName, filepath.Base(localPath))
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

// nodePerm returns the route-level node authorization middleware (see requireNodePerm).
func (h *BackupHandler) nodePerm(perm string) func(http.Handler) http.Handler {
	return requireNodePerm(func() *identity.RBACService { return h.rbacSvc }, perm)
}
