// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// BackupNodeLookup resolves nodes by ID (implemented by node.NodeService).
type BackupNodeLookup interface {
	GetByID(id string) (*node.Node, error)
}

// SharedFileOps performs file operations on a node's shared folder (local disk for the master node,
// gRPC/HTTP transfers for remote nodes). Implemented by the node file transport.
type SharedFileOps interface {
	MasterBaseURL() string
	ListNodeSharedFileNames(ctx context.Context, n *node.Node, relDir string) (map[string]struct{}, error)
	DeleteNodeSharedFile(ctx context.Context, n *node.Node, relPath string) error
	DeleteNodeSharedFileIfExists(ctx context.Context, n *node.Node, relPath string) error
	PullNodeSharedFileToLocal(ctx context.Context, baseURL string, n *node.Node, relPath, destLocalPath string) (int64, error)
	PushLocalFileToNodeShared(ctx context.Context, baseURL string, n *node.Node, localSrcPath, relPath string) error
	TransferSharedFile(ctx context.Context, baseURL string, src *node.Node, srcRelPath string, dst *node.Node, dstRelPath string) (int64, error)
}

var (
	// ErrBackupStillRunning is returned when deleting a backup whose job has not finished.
	ErrBackupStillRunning = errors.New("cannot delete a backup that is still running")
	// ErrBackupInObjectStorage is returned when deleting an archive stored in S3/R2 (not supported yet).
	ErrBackupInObjectStorage = errors.New("deleting backups stored in object storage is not supported yet")
)

// PartialDeleteError reports archive copies that could not be removed. The record keeps pointing only at
// the copies that still exist.
type PartialDeleteError struct {
	Failures []string
}

func (e *PartialDeleteError) Error() string {
	return "failed to delete backup archive: " + strings.Join(e.Failures, "; ")
}

// BackupFileManager owns the lifecycle of backup archives across storage locations: the source node's shared
// folder, the parent (API) server disk and an optional replica node. HTTP handlers only translate requests
// and responses; every file-level decision lives here.
type BackupFileManager struct {
	svc   *BackupService
	nodes BackupNodeLookup
	files SharedFileOps
	// backupsDir returns the parent-server directory that may hold archives (platform.BackupsDir).
	backupsDir func() string
}

func NewBackupFileManager(svc *BackupService, nodes BackupNodeLookup, files SharedFileOps) *BackupFileManager {
	return &BackupFileManager{svc: svc, nodes: nodes, files: files, backupsDir: platform.BackupsDir}
}

func (m *BackupFileManager) ready() bool {
	return m != nil && m.svc != nil && m.nodes != nil && m.files != nil
}

// SharedBackupRelPath maps an agent-reported local path to the archive's path relative to the node's shared
// folder ("/backup/<container>/<file>").
func SharedBackupRelPath(localPath, containerName string) string {
	cleaned := filepath.ToSlash(filepath.Clean(localPath))
	if idx := strings.Index(cleaned, "/backup/"); idx != -1 {
		return cleaned[idx:]
	}
	if strings.HasPrefix(cleaned, "backup/") {
		return "/" + cleaned
	}
	return fmt.Sprintf("/backup/%s/%s", containerName, filepath.Base(localPath))
}

// TrustedParentPath returns the record's parent_path only if it lives inside the backups directory. Any
// other value (it can originate from agent job output) is treated as absent: never served, copied or deleted.
func (m *BackupFileManager) TrustedParentPath(rec *BackupRecord) string {
	if rec == nil || rec.ParentPath == nil || *rec.ParentPath == "" {
		return ""
	}
	if !platform.IsWithinDir(m.backupsDir(), *rec.ParentPath) {
		log.Printf("[BACKUP] ignoring parent_path %q of record %s: outside backups dir", *rec.ParentPath, rec.ID)
		return ""
	}
	return *rec.ParentPath
}

// ParentArchiveLocation returns the directory and file path on the parent server for an archive of a node's
// container backup.
func (m *BackupFileManager) ParentArchiveLocation(nodeID, containerName, filename string) (dir, path string) {
	dir = filepath.Join(m.backupsDir(), "nodes", nodeID, containerName)
	return dir, filepath.Join(dir, filepath.Base(filename))
}

// ─── Post-job replication ────────────────────────────────────────────────────

// ReplicateAfterJob runs when a backup job completes on an agent. Depending on the configured destination it
// pulls the archive to the parent server, copies it to the replica node, and (remote_node mode) removes the
// source copy once the replica holds it.
func (m *BackupFileManager) ReplicateAfterJob(jobID string) {
	if !m.ready() {
		return
	}
	rec, err := m.svc.GetRecordByJobID(jobID)
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

	srcNode, err := m.nodes.GetByID(rec.NodeID)
	if err != nil || srcNode == nil {
		log.Printf("[BACKUP] post-backup replication: source node %s not found: %v", rec.NodeID, err)
		return
	}

	filename := filepath.Base(*rec.LocalPath)
	srcRelPath := SharedBackupRelPath(*rec.LocalPath, rec.ContainerName)
	baseURL := m.files.MasterBaseURL()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// 1. Pull to the parent disk (local_and_parent).
	if needsParentCopy {
		targetDir, targetPath := m.ParentArchiveLocation(rec.NodeID, rec.ContainerName, filename)
		sizeBytes, err := m.files.PullNodeSharedFileToLocal(ctx, baseURL, srcNode, srcRelPath, targetPath)
		if err != nil {
			log.Printf("[ERROR] post-backup pull to parent for record %s failed: %v", rec.ID, err)
		} else {
			_ = m.svc.UpdateRecordParentPath(rec.ID, targetPath, sizeBytes)
			rec.ParentPath = &targetPath
			m.applyParentRetention(rec.BackupConfigID, targetDir)
			log.Printf("[BACKUP] pulled backup %s to parent path %s", rec.ID, targetPath)
		}
	}

	// 2. Copy to the replica node.
	replicaSucceeded := rec.ReplicaPath != nil && *rec.ReplicaPath != ""
	if needsReplicaCopy {
		replicaNodeID := strings.TrimSpace(*rec.ReplicaNodeID)
		replicaNode, err := m.nodes.GetByID(replicaNodeID)
		if err != nil || replicaNode == nil {
			log.Printf("[ERROR] post-backup replica node %s not found: %v", replicaNodeID, err)
			return
		}
		replicaRelPath := fmt.Sprintf("/backup/%s/%s", rec.ContainerName, filename)

		var repErr error
		parentPath := m.TrustedParentPath(rec)
		if parentPath != "" {
			if _, statErr := os.Stat(parentPath); statErr == nil {
				repErr = m.files.PushLocalFileToNodeShared(ctx, baseURL, replicaNode, parentPath, replicaRelPath)
			}
		}
		if repErr != nil || parentPath == "" {
			_, repErr = m.files.TransferSharedFile(ctx, baseURL, srcNode, srcRelPath, replicaNode, replicaRelPath)
		}
		if repErr != nil {
			log.Printf("[ERROR] post-backup transfer of %s to replica node %s failed: %v", rec.ID, replicaNodeID, repErr)
			return
		}
		_ = m.svc.UpdateRecordReplica(rec.ID, replicaNodeID, replicaRelPath)
		replicaSucceeded = true
		log.Printf("[BACKUP] transferred backup %s to replica node %s (%s)", rec.ID, replicaNodeID, replicaRelPath)
	}

	// 3. remote_node: the replica is the only copy, remove the temporary source copy.
	if dest == "remote_node" && replicaSucceeded {
		if err := m.files.DeleteNodeSharedFile(ctx, srcNode, srcRelPath); err != nil {
			log.Printf("[WARN] failed to delete temporary source backup %s on node %s: %v", srcRelPath, rec.NodeID, err)
		} else {
			_ = m.svc.ClearRecordLocalPath(rec.ID)
			log.Printf("[BACKUP] removed source copy %s on node %s (remote_node mode)", srcRelPath, rec.NodeID)
		}
	}
}

// AfterParentUpload runs after an agent uploaded an archive to the parent server (targetPath): it records the
// location, starts the replica copy in the background when configured, and applies retention.
func (m *BackupFileManager) AfterParentUpload(rec *BackupRecord, targetDir, targetPath string, size int64, baseURL string) {
	if m == nil || m.svc == nil {
		return
	}
	_ = m.svc.UpdateRecordParentPath(rec.ID, targetPath, size)

	if rec.ReplicaNodeID != nil && *rec.ReplicaNodeID != "" && m.ready() {
		replicaNodeID := *rec.ReplicaNodeID
		replicaRelPath := fmt.Sprintf("/backup/%s/%s", rec.ContainerName, filepath.Base(targetPath))
		if replicaNode, err := m.nodes.GetByID(replicaNodeID); err == nil && replicaNode != nil {
			go func(recordID string) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()
				if err := m.files.PushLocalFileToNodeShared(ctx, baseURL, replicaNode, targetPath, replicaRelPath); err != nil {
					log.Printf("[ERROR] replicate backup %s to node %s failed: %v", recordID, replicaNodeID, err)
					return
				}
				_ = m.svc.UpdateRecordReplica(recordID, replicaNodeID, replicaRelPath)
				log.Printf("[BACKUP] replicated backup %s to node %s at %s", recordID, replicaNodeID, replicaRelPath)
			}(rec.ID)
		}
	}

	m.applyParentRetention(rec.BackupConfigID, targetDir)
}

// applyParentRetention keeps only the newest RetentionCount archives in a parent-server directory.
func (m *BackupFileManager) applyParentRetention(configID, dir string) {
	cfg, err := m.svc.GetConfigByID(configID)
	if err != nil || cfg == nil || cfg.RetentionCount <= 0 {
		return
	}
	pruneOldestFiles(dir, cfg.RetentionCount)
}

// pruneOldestFiles removes all but the maxCount most recently modified files in dir (ignoring .part files).
func pruneOldestFiles(dir string, maxCount int) {
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
		if info, err := e.Info(); err == nil {
			files = append(files, fileInfo{path: filepath.Join(dir, e.Name()), modTime: info.ModTime()})
		}
	}
	if len(files) <= maxCount {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	for _, f := range files[maxCount:] {
		_ = os.Remove(f.path)
	}
}

// ─── Reconciliation ──────────────────────────────────────────────────────────

// fileState is the result of checking a single storage location of a backup archive.
type fileState int

const (
	fileUnknown fileState = iota // could not verify (node offline, timeout, ...)
	filePresent
	fileMissing
)

// ReconcileRecords checks every completed backup record against the storage locations it points to. Locations
// whose archive was removed (manually, or by retention) are cleared, and records left without any archive are
// marked "deleted" (they disappear from restorable snapshots but stay in the audit log). Locations that cannot
// be verified (e.g. node offline) are left untouched. recs are updated in place.
func (m *BackupFileManager) ReconcileRecords(ctx context.Context, recs []*BackupRecord) {
	if !m.ready() {
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
		n, err := m.nodes.GetByID(id)
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
			names, err := m.files.ListNodeSharedFileNames(ctx, n, dir)
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

		var states []fileState
		changed := false
		localPath, parentPath, replicaPath := rec.LocalPath, rec.ParentPath, rec.ReplicaPath

		if hasLocal {
			st := checkShared(rec.NodeID, SharedBackupRelPath(*rec.LocalPath, rec.ContainerName))
			states = append(states, st)
			if st == fileMissing {
				localPath, changed = nil, true
			}
		}
		if hasParent {
			st := fileUnknown
			if trusted := m.TrustedParentPath(rec); trusted == "" {
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
			if err := m.svc.MarkRecordDeleted(rec.ID, reason); err != nil {
				log.Printf("[BACKUP] reconcile: failed to mark record %s as deleted: %v", rec.ID, err)
				continue
			}
			rec.Status = "deleted"
			rec.ErrorMessage = &reason
			log.Printf("[BACKUP] reconcile: record %s marked deleted (archive missing)", rec.ID)
			continue
		}
		if changed {
			if err := m.svc.UpdateRecordLocations(rec.ID, localPath, parentPath, replicaPath); err != nil {
				log.Printf("[BACKUP] reconcile: failed to update locations of record %s: %v", rec.ID, err)
				continue
			}
			rec.LocalPath, rec.ParentPath, rec.ReplicaPath = localPath, parentPath, replicaPath
		}
	}
}

// ─── Deletion ────────────────────────────────────────────────────────────────

// DeleteBackup removes the archive from every location it lives on and marks the record deleted (kept for the
// audit log). It returns ErrBackupStillRunning, ErrBackupInObjectStorage, or a *PartialDeleteError (after
// persisting which copies remain).
func (m *BackupFileManager) DeleteBackup(ctx context.Context, rec *BackupRecord, deletedBy string) error {
	if !m.ready() {
		return errors.New("backup file manager is not configured")
	}
	if rec.Status == "running" {
		return ErrBackupStillRunning
	}
	if rec.S3Key != nil && *rec.S3Key != "" {
		return ErrBackupInObjectStorage
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var failures []string
	localPath, parentPath, replicaPath := rec.LocalPath, rec.ParentPath, rec.ReplicaPath

	if rec.LocalPath != nil && *rec.LocalPath != "" {
		relPath := SharedBackupRelPath(*rec.LocalPath, rec.ContainerName)
		srcNode, err := m.nodes.GetByID(rec.NodeID)
		if err == nil && srcNode != nil {
			err = m.files.DeleteNodeSharedFileIfExists(ctx, srcNode, relPath)
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
		trusted := m.TrustedParentPath(rec)
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
		replicaNode, err := m.nodes.GetByID(*rec.ReplicaNodeID)
		if err == nil && replicaNode != nil {
			err = m.files.DeleteNodeSharedFileIfExists(ctx, replicaNode, *rec.ReplicaPath)
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
		_ = m.svc.UpdateRecordLocations(rec.ID, localPath, parentPath, replicaPath)
		return &PartialDeleteError{Failures: failures}
	}
	return m.svc.MarkRecordDeleted(rec.ID, "deleted manually by "+deletedBy)
}
