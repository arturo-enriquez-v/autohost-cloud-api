// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
)

// fakeBackupRepo records the mutations the file manager performs. Unused methods panic via the embedded nil
// interface.
type fakeBackupRepo struct {
	BackupRepository
	deleted   map[string]string // record ID -> reason
	locations map[string][3]*string
	configs   map[string]*BackupConfig
}

func newFakeBackupRepo() *fakeBackupRepo {
	return &fakeBackupRepo{deleted: map[string]string{}, locations: map[string][3]*string{}, configs: map[string]*BackupConfig{}}
}
func (f *fakeBackupRepo) MarkRecordDeleted(id, reason string) error {
	f.deleted[id] = reason
	return nil
}
func (f *fakeBackupRepo) UpdateRecordLocations(id string, l, p, r *string) error {
	f.locations[id] = [3]*string{l, p, r}
	return nil
}
func (f *fakeBackupRepo) GetConfigByID(id string) (*BackupConfig, error) {
	if c, ok := f.configs[id]; ok {
		return c, nil
	}
	return nil, ErrBackupNotFound
}

type fakeNodes map[string]*node.Node

func (f fakeNodes) GetByID(id string) (*node.Node, error) {
	if n, ok := f[id]; ok {
		return n, nil
	}
	return nil, node.ErrNodeNotFound
}

// fakeSharedFiles simulates node shared folders: files[nodeID][relPath] = present.
type fakeSharedFiles struct {
	files     map[string]map[string]bool
	offline   map[string]bool // listing fails (node unreachable)
	deleteErr map[string]bool // deleting on this node fails
}

func (f *fakeSharedFiles) MasterBaseURL() string { return "http://master:8080" }
func (f *fakeSharedFiles) ListNodeSharedFileNames(_ context.Context, n *node.Node, dir string) (map[string]struct{}, error) {
	if f.offline[n.ID] {
		return nil, errors.New("node offline")
	}
	out := map[string]struct{}{}
	for p := range f.files[n.ID] {
		if filepath.Dir(p) == dir {
			out[filepath.Base(p)] = struct{}{}
		}
	}
	return out, nil
}
func (f *fakeSharedFiles) DeleteNodeSharedFile(ctx context.Context, n *node.Node, p string) error {
	return f.DeleteNodeSharedFileIfExists(ctx, n, p)
}
func (f *fakeSharedFiles) DeleteNodeSharedFileIfExists(_ context.Context, n *node.Node, p string) error {
	if f.deleteErr[n.ID] {
		return errors.New("agent unreachable")
	}
	delete(f.files[n.ID], p)
	return nil
}
func (f *fakeSharedFiles) PullNodeSharedFileToLocal(context.Context, string, *node.Node, string, string) (int64, error) {
	return 0, errors.New("not used")
}
func (f *fakeSharedFiles) PushLocalFileToNodeShared(context.Context, string, *node.Node, string, string) error {
	return errors.New("not used")
}
func (f *fakeSharedFiles) TransferSharedFile(context.Context, string, *node.Node, string, *node.Node, string) (int64, error) {
	return 0, errors.New("not used")
}

func ptr(s string) *string { return &s }

func newTestManager(t *testing.T, files *fakeSharedFiles) (*BackupFileManager, *fakeBackupRepo, string) {
	t.Helper()
	repo := newFakeBackupRepo()
	backupsDir := t.TempDir()
	m := NewBackupFileManager(NewBackupService(repo), fakeNodes{"src": {ID: "src"}, "rep": {ID: "rep"}}, files)
	m.backupsDir = func() string { return backupsDir }
	return m, repo, backupsDir
}

func TestReconcileRecords(t *testing.T) {
	files := &fakeSharedFiles{
		files: map[string]map[string]bool{
			"src": {"/backup/db/kept.back": true},
			"rep": {},
		},
		offline: map[string]bool{},
	}
	m, repo, backupsDir := newTestManager(t, files)

	parentFile := filepath.Join(backupsDir, "nodes", "src", "db", "kept.back")
	_ = os.MkdirAll(filepath.Dir(parentFile), 0755)
	_ = os.WriteFile(parentFile, []byte("x"), 0600)

	recs := []*BackupRecord{
		// Source copy still there, replica copy gone: only the replica location is cleared.
		{ID: "partial", NodeID: "src", ContainerName: "db", Status: "completed",
			LocalPath: ptr("/var/lib/autohost/shared/backup/db/kept.back"), ReplicaNodeID: ptr("rep"), ReplicaPath: ptr("/backup/db/kept.back")},
		// Every copy gone (source deleted, parent path outside the backups dir is untrusted): marked deleted.
		{ID: "gone", NodeID: "src", ContainerName: "db", Status: "completed",
			LocalPath: ptr("/backup/db/old.back"), ParentPath: ptr("/etc/passwd")},
		// Parent copy present: untouched.
		{ID: "parent-ok", NodeID: "src", ContainerName: "db", Status: "completed", ParentPath: &parentFile},
		// Running / S3 records are never reconciled.
		{ID: "running", NodeID: "src", ContainerName: "db", Status: "running", LocalPath: ptr("/backup/db/x.back")},
		{ID: "s3", NodeID: "src", ContainerName: "db", Status: "completed", S3Key: ptr("k"), LocalPath: ptr("/backup/db/x.back")},
	}
	m.ReconcileRecords(context.Background(), recs)

	if loc, ok := repo.locations["partial"]; !ok || loc[0] == nil || loc[2] != nil {
		t.Errorf("partial: expected only the replica location cleared, got %v (updated=%v)", loc, ok)
	}
	if recs[0].Status != "completed" {
		t.Errorf("partial: record should stay completed")
	}
	if _, ok := repo.deleted["gone"]; !ok || recs[1].Status != "deleted" {
		t.Errorf("gone: expected record marked deleted")
	}
	for _, id := range []string{"parent-ok", "running", "s3"} {
		if _, ok := repo.deleted[id]; ok {
			t.Errorf("%s: must not be marked deleted", id)
		}
		if _, ok := repo.locations[id]; ok {
			t.Errorf("%s: locations must not change", id)
		}
	}
}

func TestReconcileRecords_OfflineNodeIsLeftAlone(t *testing.T) {
	files := &fakeSharedFiles{files: map[string]map[string]bool{"src": {}}, offline: map[string]bool{"src": true}}
	m, repo, _ := newTestManager(t, files)
	recs := []*BackupRecord{{ID: "r", NodeID: "src", ContainerName: "db", Status: "completed", LocalPath: ptr("/backup/db/a.back")}}
	m.ReconcileRecords(context.Background(), recs)
	if len(repo.deleted) != 0 || len(repo.locations) != 0 || recs[0].Status != "completed" {
		t.Fatal("a record whose node cannot be listed must not be changed")
	}
}

func TestDeleteBackup(t *testing.T) {
	t.Run("removes every copy and marks the record deleted", func(t *testing.T) {
		files := &fakeSharedFiles{files: map[string]map[string]bool{"src": {"/backup/db/a.back": true}, "rep": {"/backup/db/a.back": true}}}
		m, repo, backupsDir := newTestManager(t, files)
		parent := filepath.Join(backupsDir, "a.back")
		_ = os.WriteFile(parent, []byte("x"), 0600)
		rec := &BackupRecord{ID: "r", NodeID: "src", ContainerName: "db", Status: "completed",
			LocalPath: ptr("/backup/db/a.back"), ParentPath: &parent, ReplicaNodeID: ptr("rep"), ReplicaPath: ptr("/backup/db/a.back")}

		if err := m.DeleteBackup(context.Background(), rec, "alice"); err != nil {
			t.Fatal(err)
		}
		if files.files["src"]["/backup/db/a.back"] || files.files["rep"]["/backup/db/a.back"] {
			t.Error("node copies should be deleted")
		}
		if _, err := os.Stat(parent); !os.IsNotExist(err) {
			t.Error("parent copy should be deleted")
		}
		if repo.deleted["r"] != "deleted manually by alice" {
			t.Errorf("unexpected delete reason %q", repo.deleted["r"])
		}
	})

	t.Run("partial failure keeps the remaining location and does not mark deleted", func(t *testing.T) {
		files := &fakeSharedFiles{
			files:     map[string]map[string]bool{"src": {"/backup/db/a.back": true}, "rep": {"/backup/db/a.back": true}},
			deleteErr: map[string]bool{"rep": true},
		}
		m, repo, _ := newTestManager(t, files)
		rec := &BackupRecord{ID: "r", NodeID: "src", ContainerName: "db", Status: "completed",
			LocalPath: ptr("/backup/db/a.back"), ReplicaNodeID: ptr("rep"), ReplicaPath: ptr("/backup/db/a.back")}

		var partial *PartialDeleteError
		if err := m.DeleteBackup(context.Background(), rec, "alice"); !errors.As(err, &partial) {
			t.Fatalf("expected PartialDeleteError, got %v", err)
		}
		loc := repo.locations["r"]
		if loc[0] != nil || loc[2] == nil {
			t.Errorf("expected source cleared and replica kept, got %v", loc)
		}
		if _, ok := repo.deleted["r"]; ok {
			t.Error("record must not be marked deleted after a partial failure")
		}
	})

	t.Run("never touches an untrusted parent path", func(t *testing.T) {
		files := &fakeSharedFiles{files: map[string]map[string]bool{}}
		m, _, _ := newTestManager(t, files)
		outside := filepath.Join(t.TempDir(), "important.txt")
		_ = os.WriteFile(outside, []byte("keep"), 0600)
		rec := &BackupRecord{ID: "r", NodeID: "src", Status: "completed", ParentPath: &outside}
		if err := m.DeleteBackup(context.Background(), rec, "alice"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatal("a parent_path outside the backups dir must never be deleted")
		}
	})

	t.Run("rejects running and object-storage backups", func(t *testing.T) {
		m, _, _ := newTestManager(t, &fakeSharedFiles{})
		if err := m.DeleteBackup(context.Background(), &BackupRecord{Status: "running"}, "x"); !errors.Is(err, ErrBackupStillRunning) {
			t.Errorf("running: got %v", err)
		}
		if err := m.DeleteBackup(context.Background(), &BackupRecord{Status: "completed", S3Key: ptr("k")}, "x"); !errors.Is(err, ErrBackupInObjectStorage) {
			t.Errorf("s3: got %v", err)
		}
	})
}

func TestPruneOldestFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i, name := range []string{"a.back", "b.back", "c.back", "d.back"} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte("x"), 0600)
		_ = os.Chtimes(p, now.Add(time.Duration(i)*time.Hour), now.Add(time.Duration(i)*time.Hour))
	}
	_ = os.WriteFile(filepath.Join(dir, "e.back.part"), []byte("x"), 0600)

	pruneOldestFiles(dir, 2)

	for name, want := range map[string]bool{"a.back": false, "b.back": false, "c.back": true, "d.back": true, "e.back.part": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if got := err == nil; got != want {
			t.Errorf("%s: exists=%v, want %v", name, got, want)
		}
	}
}

func TestSharedBackupRelPath(t *testing.T) {
	cases := map[string]string{
		"/var/lib/autohost/shared/backup/db/x.back": "/backup/db/x.back",
		"backup/db/x.back":                          "/backup/db/x.back",
		"/somewhere/else/x.back":                    "/backup/db/x.back",
	}
	for in, want := range cases {
		if got := SharedBackupRelPath(in, "db"); got != want {
			t.Errorf("SharedBackupRelPath(%q) = %q, want %q", in, got, want)
		}
	}
}
