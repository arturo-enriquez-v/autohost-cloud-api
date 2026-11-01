// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const DefaultStorageDir = "/var/lib/autohost"

// StorageDir returns the root storage path for AutoHost persistent data
// (backups, certificates, VM images, etc.). Configurable via AUTOHOST_STORAGE_DIR.
func StorageDir() string {
	if dir := strings.TrimSpace(os.Getenv("AUTOHOST_STORAGE_DIR")); dir != "" {
		return dir
	}
	return DefaultStorageDir
}

// BackupsDir returns the directory where replicated node backups are stored.
// Reads BACKUP_STORAGE_DIR first for backwards compatibility, then falls back
// to <AUTOHOST_STORAGE_DIR>/backups.
func BackupsDir() string {
	if dir := strings.TrimSpace(os.Getenv("BACKUP_STORAGE_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(StorageDir(), "backups")
}

// PKICertsDir returns the directory for mTLS Root CA and server certificates.
// Reads GRPC_CERTS_DIR first, then <AUTOHOST_STORAGE_DIR>/certs if AUTOHOST_STORAGE_DIR
// is explicitly configured, otherwise "./certs".
func PKICertsDir() string {
	if dir := strings.TrimSpace(os.Getenv("GRPC_CERTS_DIR")); dir != "" {
		return dir
	}
	if storage := strings.TrimSpace(os.Getenv("AUTOHOST_STORAGE_DIR")); storage != "" {
		return filepath.Join(storage, "certs")
	}
	return "certs"
}

// LocalAgentConfigDir returns the directory where auto-enrolled local node
// configuration (agent.yaml and client certificates) is written.
func LocalAgentConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("AUTOHOST_LOCAL_AGENT_DIR")); dir != "" {
		return dir
	}
	if storage := strings.TrimSpace(os.Getenv("AUTOHOST_STORAGE_DIR")); storage != "" {
		return filepath.Join(storage, "agent")
	}
	return "/etc/autohost"
}

// StorageUsage holds disk usage metrics for the configured storage mount.
type StorageUsage struct {
	Path           string  `json:"path"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsagePercent   float64 `json:"usage_percent"`
	WarningHigh    bool    `json:"warning_high"` // true when usage >= 85%
}

// GetStorageUsage inspects the filesystem backing the given directory (or its parent).
func GetStorageUsage(dir string) (*StorageUsage, error) {
	if dir == "" {
		dir = StorageDir()
	}
	_ = os.MkdirAll(dir, 0755)
	checkPath := dir
	if _, err := os.Stat(checkPath); err != nil {
		checkPath = filepath.Dir(dir)
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(checkPath, &stat); err != nil {
		return nil, err
	}

	total := stat.Blocks * uint64(stat.Bsize)
	avail := stat.Bavail * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	var used uint64
	if total > free {
		used = total - free
	}
	var pct float64
	if total > 0 {
		pct = (float64(used) / float64(total)) * 100.0
	}

	return &StorageUsage{
		Path:           dir,
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: avail,
		UsagePercent:   pct,
		WarningHigh:    pct >= 85.0,
	}, nil
}

