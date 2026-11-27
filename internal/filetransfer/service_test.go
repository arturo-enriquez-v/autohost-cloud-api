// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package filetransfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// fakeAgent emulates a remote agent with an in-memory shared folder. For transfers it connects back to the
// service through the ticket in the transfer URL, exactly like the real agent does over HTTP.
type fakeAgent struct {
	svc      *Service
	mu       sync.Mutex
	files    map[string][]byte
	silent   bool  // never connect back (simulates an agent that cannot reach the master)
	failWith error // fail every operation
}

func (a *fakeAgent) ticketFor(transferURL string) *Ticket {
	u, _ := url.Parse(transferURL)
	id := path.Base(u.Path)
	t, _ := a.svc.LookupTicket(id, u.Query().Get("token"))
	return t
}

func (a *fakeAgent) ExecuteFileOperation(ctx context.Context, _ string, op pb.FileOpType, p, transferURL, _ string) (*pb.FileOperationResponsePayload, error) {
	if a.failWith != nil {
		return &pb.FileOperationResponsePayload{}, a.failWith
	}
	switch op {
	case pb.FileOpType_FILE_OP_LIST:
		a.mu.Lock()
		defer a.mu.Unlock()
		var entries []*pb.FileEntryInfo
		for name, data := range a.files {
			if path.Dir(name) == path.Clean("/"+p) {
				entries = append(entries, &pb.FileEntryInfo{Name: path.Base(name), Path: name, SizeBytes: int64(len(data))})
			}
		}
		return &pb.FileOperationResponsePayload{Success: true, Entries: entries}, nil
	case pb.FileOpType_FILE_OP_PULL_FROM_URL: // agent downloads from the master
		if a.silent {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		t := a.ticketFor(transferURL)
		if t == nil {
			return nil, errors.New("invalid ticket")
		}
		var data []byte
		if t.IsPipe() {
			t.MarkStarted()
			data, _ = io.ReadAll(t.PipeReader)
		} else {
			data, _ = os.ReadFile(t.FilePath)
		}
		a.mu.Lock()
		a.files[path.Clean("/"+p)] = data
		a.mu.Unlock()
		return &pb.FileOperationResponsePayload{Success: true}, nil
	case pb.FileOpType_FILE_OP_PUSH_TO_URL: // agent uploads to the master
		if a.silent {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		t := a.ticketFor(transferURL)
		if t == nil {
			return nil, errors.New("invalid ticket")
		}
		a.mu.Lock()
		data, ok := a.files[path.Clean("/"+p)]
		a.mu.Unlock()
		if !ok {
			return &pb.FileOperationResponsePayload{}, errors.New("open: no such file or directory")
		}
		u, _ := url.Parse(transferURL)
		offset, _ := strconv.ParseInt(u.Query().Get("offset"), 10, 64)
		if t.IsPipe() {
			t.SetSizes(int64(len(data))-offset, int64(len(data)))
			t.MarkStarted()
			_, _ = t.PipeWriter.Write(data[offset:])
			_ = t.PipeWriter.Close()
		} else if err := t.ReceiveFile(bytes.NewReader(data)); err != nil {
			return nil, err
		}
		return &pb.FileOperationResponsePayload{Success: true}, nil
	case pb.FileOpType_FILE_OP_DELETE:
		a.mu.Lock()
		delete(a.files, path.Clean("/"+p))
		a.mu.Unlock()
		return &pb.FileOperationResponsePayload{Success: true}, nil
	}
	return &pb.FileOperationResponsePayload{Success: true}, nil
}

func (a *fakeAgent) ExecuteFileRename(context.Context, string, string, string) (*pb.FileOperationResponsePayload, error) {
	return &pb.FileOperationResponsePayload{Success: true}, a.failWith
}

func newTestService(t *testing.T) (*Service, *fakeAgent, string) {
	t.Helper()
	shared := t.TempDir()
	t.Setenv("AUTOHOST_STORAGE_DIR", t.TempDir()) // staging dir
	agent := &fakeAgent{files: map[string][]byte{}}
	svc := newService(agent, nil, shared)
	agent.svc = svc
	return svc, agent, shared
}

var (
	master = &node.Node{ID: "master", IsLocal: true}
	remote = &node.Node{ID: "remote"}
)

func TestTicketLookup(t *testing.T) {
	r := newTicketRegistry()
	tk := r.newFileTicket("/tmp/x")
	if _, ok := r.lookup(tk.ID, tk.Token); !ok {
		t.Fatal("valid ticket rejected")
	}
	for _, bad := range []string{"", "wrong", tk.Token + "x", tk.Token[:len(tk.Token)-1]} {
		if _, ok := r.lookup(tk.ID, bad); ok {
			t.Errorf("token %q accepted", bad)
		}
	}
	tk.ExpiresAt = time.Now().Add(-time.Second)
	if _, ok := r.lookup(tk.ID, tk.Token); ok {
		t.Error("expired ticket accepted")
	}
	r.remove(tk.ID)
	if _, ok := r.lookup(tk.ID, tk.Token); ok {
		t.Error("removed ticket accepted")
	}
}

func TestLocalOperations(t *testing.T) {
	svc, _, shared := newTestService(t)
	ctx := context.Background()

	if p, err := svc.Mkdir(ctx, master, "/docs/sub"); err != nil || p != "/docs/sub" {
		t.Fatalf("mkdir: %q, %v", p, err)
	}
	rel, n, err := svc.Upload(ctx, master, "/docs", "../../evil/a.txt", strings.NewReader("hello"), "")
	if err != nil || rel != "/docs/a.txt" || n != 5 {
		t.Fatalf("upload: %q %d %v (the file name must not escape the directory)", rel, n, err)
	}
	listing, err := svc.List(ctx, master, "/docs")
	if err != nil || len(listing.Entries) != 2 || !listing.Entries[0].IsDir || listing.Entries[1].Name != "a.txt" {
		t.Fatalf("list: %+v, %v (directories first)", listing, err)
	}
	if _, err := svc.Rename(ctx, master, "/docs/a.txt", "/docs/sub"); KindOf(err) != KindConflict {
		t.Errorf("rename onto existing path: expected conflict, got %v", err)
	}
	// Moving to another folder.
	if p, err := svc.Rename(ctx, master, "/docs/a.txt", "/docs/sub/a.txt"); err != nil || p != "/docs/sub/a.txt" {
		t.Fatalf("move into a subfolder: %q, %v", p, err)
	}
	if _, err := os.Stat(filepath.Join(shared, "docs", "sub", "a.txt")); err != nil {
		t.Fatalf("moved file not found: %v", err)
	}
	if _, err := svc.Rename(ctx, master, "/docs/sub/a.txt", "/docs/a.txt"); err != nil {
		t.Fatalf("move back: %v", err)
	}
	for _, tc := range [][2]string{{"/docs", "/docs/sub/docs"}, {"/docs", "/docs/"}, {"/docs/", "/docs"}} {
		if _, err := svc.Rename(ctx, master, tc[0], tc[1]); KindOf(err) != KindInvalid {
			t.Errorf("rename %s -> %s: expected invalid, got %v", tc[0], tc[1], err)
		}
	}
	if _, err := svc.Rename(ctx, master, "/", "/x"); KindOf(err) != KindInvalid {
		t.Errorf("renaming the root must be rejected, got %v", err)
	}
	if _, err := svc.DeletePath(ctx, master, "/"); KindOf(err) != KindInvalid {
		t.Errorf("deleting the root must be rejected, got %v", err)
	}
	if _, err := svc.LocalFile("/docs/sub"); KindOf(err) != KindNotFound {
		t.Errorf("a directory is not a file, got %v", err)
	}
	if _, err := svc.List(ctx, master, "/../.."); err != nil {
		t.Errorf("traversal is clamped to the shared root, got %v", err)
	}
	if _, err := svc.DeletePath(ctx, master, "/docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shared, "docs")); !os.IsNotExist(err) {
		t.Fatal("directory should be deleted")
	}
}

func TestRemoteUploadAndDownloadThroughTickets(t *testing.T) {
	svc, agent, _ := newTestService(t)
	ctx := context.Background()

	payload := strings.Repeat("autohost-", 1000)
	rel, n, err := svc.Upload(ctx, remote, "/media", "video.mp4", strings.NewReader(payload), "http://master:8080")
	if err != nil || rel != "/media/video.mp4" || n != int64(len(payload)) {
		t.Fatalf("remote upload: %q %d %v", rel, n, err)
	}
	if string(agent.files["/media/video.mp4"]) != payload {
		t.Fatal("agent did not receive the uploaded bytes")
	}

	stream, err := svc.OpenRemote(ctx, remote, "/media/video.mp4", "http://master:8080", 9, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(stream.Body)
	stream.Close()
	if string(got) != payload[9:] || stream.TotalSize != int64(len(payload)) || stream.ContentLen != int64(len(payload)-9) {
		t.Fatalf("ranged download mismatch: len=%d total=%d contentLen=%d", len(got), stream.TotalSize, stream.ContentLen)
	}
	if len(svc.tickets.tickets) != 0 {
		t.Fatalf("tickets must be removed after use, %d left", len(svc.tickets.tickets))
	}

	listing, err := svc.List(ctx, remote, "/media")
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "video.mp4" {
		t.Fatalf("remote list: %+v, %v", listing, err)
	}
}

func TestTransferBetweenNodes(t *testing.T) {
	svc, agent, shared := newTestService(t)
	ctx := context.Background()
	agent.files["/src/report.pdf"] = []byte("pdf-bytes")

	size, dst, err := svc.Transfer(ctx, "http://master:8080", remote, "/src/report.pdf", master, "/inbox/report.pdf")
	if err != nil || size != 9 || dst != "/inbox/report.pdf" {
		t.Fatalf("remote -> master: %d %q %v", size, dst, err)
	}
	if b, _ := os.ReadFile(filepath.Join(shared, "inbox", "report.pdf")); string(b) != "pdf-bytes" {
		t.Fatal("file not delivered to the master")
	}

	if _, _, err := svc.Transfer(ctx, "http://master:8080", master, "/inbox/report.pdf", remote, "/copy.pdf"); err != nil {
		t.Fatal(err)
	}
	if string(agent.files["/copy.pdf"]) != "pdf-bytes" {
		t.Fatal("file not delivered to the remote node")
	}

	if _, _, err := svc.Transfer(ctx, "http://master:8080", remote, "/missing", master, "/x"); KindOf(err) != KindAgent ||
		!strings.HasPrefix(err.Error(), "pull from source node failed") {
		t.Fatalf("missing remote source: expected agent error, got %v", err)
	}
	staged, _ := os.ReadDir(filepath.Join(os.Getenv("AUTOHOST_STORAGE_DIR"), "transfers"))
	if len(staged) != 0 {
		t.Fatalf("staging files must be cleaned up, found %d", len(staged))
	}
}

func TestAgentFailuresAndTimeouts(t *testing.T) {
	svc, agent, _ := newTestService(t)
	ctx := context.Background()

	agent.failWith = errors.New("node is not connected via gRPC")
	if _, err := svc.List(ctx, remote, "/"); KindOf(err) != KindAgent || err.Error() != "node is not connected via gRPC" {
		t.Errorf("agent failure: %v (kind %v)", err, KindOf(err))
	}
	if _, err := svc.OpenRemote(ctx, remote, "/x", "http://m", 0, time.Minute); KindOf(err) != KindAgent {
		t.Errorf("agent failure on download: %v", err)
	}

	agent.failWith = nil
	agent.silent = true
	old := agentConnectTimeout
	agentConnectTimeout = 50 * time.Millisecond
	defer func() { agentConnectTimeout = old }()
	if _, err := svc.OpenRemote(ctx, remote, "/x", "http://m", 0, time.Minute); KindOf(err) != KindAgentTimeout {
		t.Errorf("agent never connecting back: expected timeout, got %v", err)
	}
	if _, _, err := svc.Upload(ctx, remote, "/", "a.txt", strings.NewReader("x"), "http://m"); KindOf(err) != KindAgentTimeout {
		t.Errorf("upload with silent agent: expected timeout, got %v", err)
	}
}

func TestListNodeSharedFileNamesAndDelete(t *testing.T) {
	svc, agent, shared := newTestService(t)
	ctx := context.Background()
	_ = os.MkdirAll(filepath.Join(shared, "backup", "db"), 0755)
	_ = os.WriteFile(filepath.Join(shared, "backup", "db", "a.back"), []byte("x"), 0600)

	names, err := svc.ListNodeSharedFileNames(ctx, master, "/backup/db")
	if _, ok := names["a.back"]; err != nil || !ok {
		t.Fatalf("local names: %v %v", names, err)
	}
	if names, err := svc.ListNodeSharedFileNames(ctx, master, "/backup/none"); err != nil || len(names) != 0 {
		t.Fatalf("missing directory should be empty, got %v %v", names, err)
	}
	if err := svc.DeleteNodeSharedFileIfExists(ctx, master, "/backup/db/missing.back"); err != nil {
		t.Fatalf("missing file must not be an error: %v", err)
	}

	agent.files["/backup/db/r.back"] = []byte("x")
	if err := svc.DeleteNodeSharedFileIfExists(ctx, remote, "/backup/db/r.back"); err != nil {
		t.Fatal(err)
	}
	if _, ok := agent.files["/backup/db/r.back"]; ok {
		t.Fatal("remote file should be deleted")
	}
}

func TestCopyLocalFile(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.txt"), filepath.Join(dir, "dst.txt")
	_ = os.WriteFile(src, []byte("payload"), 0644)
	if err := copyLocalFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "payload" {
		t.Fatalf("got %q", b)
	}
}
