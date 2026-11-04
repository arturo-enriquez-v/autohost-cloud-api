// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package platform

import (
	"errors"
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

// TransfersStagingDir returns the directory used to stage node-to-node file transfers.
// If the primary storage directory is not writable, it falls back to a user/temp directory.
func TransfersStagingDir() string {
	primary := filepath.Join(StorageDir(), "transfers")
	if err := os.MkdirAll(primary, 0755); err == nil {
		testFile := filepath.Join(primary, ".write_check")
		if err := os.WriteFile(testFile, []byte("ok"), 0600); err == nil {
			_ = os.Remove(testFile)
			return primary
		}
	}

	home, _ := os.UserHomeDir()
	var fallback string
	if home != "" {
		fallback = filepath.Join(home, ".autohost", "transfers")
	} else {
		fallback = filepath.Join(os.TempDir(), "autohost", "transfers")
	}
	_ = os.MkdirAll(fallback, 0755)
	return fallback
}

// SharedFilesDir returns the default node shared files folder (<AUTOHOST_STORAGE_DIR>/shared).
func SharedFilesDir() string {
	if dir := strings.TrimSpace(os.Getenv("AUTOHOST_SHARED_DIR")); dir != "" {
		_ = os.MkdirAll(dir, 0755)
		return dir
	}
	primary := filepath.Join(StorageDir(), "shared")
	if err := os.MkdirAll(primary, 0755); err == nil {
		testFile := filepath.Join(primary, ".write_check")
		if err := os.WriteFile(testFile, []byte("ok"), 0600); err == nil {
			_ = os.Remove(testFile)
			return primary
		}
	}

	home, _ := os.UserHomeDir()
	var fallback string
	if home != "" {
		fallback = filepath.Join(home, ".autohost", "shared")
	} else {
		fallback = filepath.Join(os.TempDir(), "autohost", "shared")
	}
	_ = os.MkdirAll(fallback, 0755)
	return fallback
}

// SafeSharedPath resolves relPath inside baseDir and prevents path traversal outside baseDir.
func SafeSharedPath(baseDir, relPath string) (string, string, error) {
	cleanedRel := filepath.Clean("/" + strings.TrimSpace(relPath))
	fullPath := filepath.Join(baseDir, cleanedRel)
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", "", err
	}
	absFull, err := filepath.Abs(fullPath)
	if err != nil {
		return "", "", err
	}
	if absFull != absBase && !strings.HasPrefix(absFull, absBase+string(filepath.Separator)) {
		return "", "", errors.New("invalid path: access outside shared directory is forbidden")
	}
	return absFull, cleanedRel, nil
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

