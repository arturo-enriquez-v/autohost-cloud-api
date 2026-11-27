// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

// Package filetransfer moves files in and out of node shared folders: the master node's folder is accessed
// on local disk, remote nodes through gRPC file operations plus HTTP transfers authorized by short-lived
// tickets. HTTP handlers and services (backups, shares) use it instead of talking to the agent directly.
package filetransfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// AgentFileOps sends file operations to a connected agent (implemented by the gRPC server).
type AgentFileOps interface {
	ExecuteFileOperation(ctx context.Context, nodeID string, op pb.FileOpType, path, transferURL, token string) (*pb.FileOperationResponsePayload, error)
	ExecuteFileRename(ctx context.Context, nodeID, srcPath, destPath string) (*pb.FileOperationResponsePayload, error)
}

// LocalNodeFinder returns the master (local) node, used to compute the URL agents reach the master on.
type LocalNodeFinder interface {
	FindLocalNode() (*node.Node, error)
}

// Kind classifies an operation error so handlers can map it to a status code.
type Kind int

const (
	KindInternal Kind = iota
	KindInvalid
	KindNotFound
	KindConflict
	KindAgent        // the agent failed or is unreachable
	KindAgentTimeout // the agent never connected back
	KindCancelled
)

// OpError is returned by Service operations. Error() keeps the original user-facing message.
type OpError struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *OpError) Error() string { return e.Msg }
func (e *OpError) Unwrap() error { return e.Err }

func opErr(kind Kind, err error) error  { return &OpError{Kind: kind, Msg: err.Error(), Err: err} }
func opMsg(kind Kind, msg string) error { return &OpError{Kind: kind, Msg: msg} }
func agentErr(prefix string, err error) error {
	if prefix == "" {
		return opErr(KindAgent, err)
	}
	return &OpError{Kind: KindAgent, Msg: prefix + ": " + err.Error(), Err: err}
}

// KindOf returns the error's Kind (KindInternal for foreign errors).
func KindOf(err error) Kind {
	var oe *OpError
	if errors.As(err, &oe) {
		return oe.Kind
	}
	return KindInternal
}

// agentConnectTimeout is how long to wait for an agent to connect back to a transfer URL (var for tests).
var agentConnectTimeout = 25 * time.Second

// Entry is a file or directory in a shared folder listing.
type Entry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at"`
	Mode       string `json:"mode"`
}

// Service implements shared-folder file operations for local and remote nodes.
type Service struct {
	agent   AgentFileOps
	nodes   LocalNodeFinder
	tickets *ticketRegistry
	// sharedDir is the master's shared folder (platform.SharedFilesDir).
	sharedDir string
}

func NewService(agent *grpcserver.NodeAgentServer, nodes *node.NodeService) *Service {
	_ = os.MkdirAll(platform.SharedFilesDir(), 0755)
	_ = os.MkdirAll(platform.TransfersStagingDir(), 0750)
	var finder LocalNodeFinder
	if nodes != nil {
		finder = nodes
	}
	var ops AgentFileOps
	if agent != nil {
		ops = agent
	}
	return newService(ops, finder, platform.SharedFilesDir())
}

func newService(agent AgentFileOps, nodes LocalNodeFinder, sharedDir string) *Service {
	return &Service{agent: agent, nodes: nodes, tickets: newTicketRegistry(), sharedDir: sharedDir}
}

// LookupTicket returns a live ticket for the internal-transfer endpoints.
func (s *Service) LookupTicket(id, token string) (*Ticket, bool) { return s.tickets.lookup(id, token) }

// ─── Master URL ──────────────────────────────────────────────────────────────

// MasterBaseURL returns the URL agents use to reach this server (implements apps.SharedFileOps).
func (s *Service) MasterBaseURL() string { return s.MasterBaseURLOr("") }

// MasterBaseURLOr is MasterBaseURL with a fallback (e.g. derived from the current request) used when neither
// the local node's address nor NEXT_PUBLIC_API_URL is known.
func (s *Service) MasterBaseURLOr(fallback string) string {
	var localIPs []string
	if s.nodes != nil {
		if local, err := s.nodes.FindLocalNode(); err == nil && local != nil {
			localIPs = []string{derefStr(local.IPVPN), local.IPLocal}
		}
	}
	if u := platform.AgentAPIURL(localIPs, fallback); u != "" {
		return u
	}
	return "http://127.0.0.1:" + platform.AgentAPIPort()
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func transferURL(baseURL string, t *Ticket, offset int64) string {
	u := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s", strings.TrimRight(baseURL, "/"), t.ID, t.Token)
	if offset > 0 {
		u += fmt.Sprintf("&offset=%d", offset)
	}
	return u
}

func (s *Service) agentOp(ctx context.Context, nodeID string, op pb.FileOpType, path, url, token string) (*pb.FileOperationResponsePayload, error) {
	if s.agent == nil {
		return nil, errors.New("agent transport is not configured")
	}
	return s.agent.ExecuteFileOperation(ctx, nodeID, op, path, url, token)
}

// localPath resolves relPath inside the master's shared folder.
func (s *Service) localPath(relPath string) (abs, cleaned string, err error) {
	abs, cleaned, err = platform.SafeSharedPath(s.sharedDir, relPath)
	if err != nil {
		return "", "", opErr(KindInvalid, err)
	}
	return abs, cleaned, nil
}

// ─── Listing and metadata operations ─────────────────────────────────────────

// Listing is the result of List.
type Listing struct {
	CurrentPath string
	BaseDir     string // only for the master node
	Entries     []Entry
}

// List returns the entries of relPath (directories first, then by name).
func (s *Service) List(ctx context.Context, n *node.Node, relPath string) (*Listing, error) {
	if relPath == "" {
		relPath = "/"
	}
	if n.IsLocal {
		_ = os.MkdirAll(s.sharedDir, 0755)
		abs, cleaned, err := s.localPath(relPath)
		if err != nil {
			return nil, err
		}
		dirEntries, err := os.ReadDir(abs)
		if err != nil {
			if !(os.IsNotExist(err) && cleaned == "/") {
				return nil, opErr(KindInvalid, err)
			}
			_ = os.MkdirAll(abs, 0755)
		}
		entries := make([]Entry, 0, len(dirEntries))
		for _, de := range dirEntries {
			info, err := de.Info()
			if err != nil {
				continue
			}
			entries = append(entries, Entry{
				Name: de.Name(), Path: filepath.Join(cleaned, de.Name()), IsDir: de.IsDir(), SizeBytes: info.Size(),
				ModifiedAt: info.ModTime().UTC().Format(time.RFC3339), Mode: info.Mode().String(),
			})
		}
		sortEntries(entries)
		return &Listing{CurrentPath: cleaned, BaseDir: s.sharedDir, Entries: entries}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := s.agentOp(ctx, n.ID, pb.FileOpType_FILE_OP_LIST, relPath, "", "")
	if err != nil {
		return nil, agentErr("", err)
	}
	entries := make([]Entry, 0, len(res.GetEntries()))
	for _, e := range res.GetEntries() {
		entries = append(entries, Entry{
			Name: e.GetName(), Path: e.GetPath(), IsDir: e.GetIsDir(), SizeBytes: e.GetSizeBytes(),
			ModifiedAt: time.Unix(e.GetModifiedAt(), 0).UTC().Format(time.RFC3339), Mode: e.GetMode(),
		})
	}
	sortEntries(entries)
	return &Listing{CurrentPath: filepath.Clean("/" + relPath), Entries: entries}, nil
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir // directories first
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

// Mkdir creates relPath (and parents) and returns the cleaned path.
func (s *Service) Mkdir(ctx context.Context, n *node.Node, relPath string) (string, error) {
	if n.IsLocal {
		abs, cleaned, err := s.localPath(relPath)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(abs, 0755); err != nil {
			return "", opErr(KindInternal, err)
		}
		return cleaned, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := s.agentOp(ctx, n.ID, pb.FileOpType_FILE_OP_MKDIR, relPath, "", ""); err != nil {
		return "", agentErr("", err)
	}
	return filepath.Clean("/" + relPath), nil
}

// Rename moves fromPath to toPath (another name, another folder or both) and returns the cleaned
// destination.
func (s *Service) Rename(ctx context.Context, n *node.Node, fromPath, toPath string) (string, error) {
	src, dst := filepath.Clean("/"+fromPath), filepath.Clean("/"+toPath)
	if src == dst {
		return "", opMsg(KindInvalid, "the source and the destination are the same")
	}
	// The OS refuses it with a bare "invalid argument"; say what is wrong instead.
	if strings.HasPrefix(dst, src+"/") {
		return "", opMsg(KindInvalid, "cannot move a folder inside itself")
	}
	if n.IsLocal {
		src, _, err := s.localPath(fromPath)
		if err != nil {
			return "", err
		}
		dst, cleanedDst, err := s.localPath(toPath)
		if err != nil {
			return "", err
		}
		root := filepath.Clean(s.sharedDir)
		if src == root || dst == root {
			return "", opMsg(KindInvalid, "cannot rename root shared directory")
		}
		if _, err := os.Stat(dst); err == nil {
			return "", opMsg(KindConflict, "destination already exists")
		}
		if err := os.Rename(src, dst); err != nil {
			return "", opErr(KindInternal, err)
		}
		return cleanedDst, nil
	}
	if s.agent == nil {
		return "", opMsg(KindAgent, "agent transport is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := s.agent.ExecuteFileRename(ctx, n.ID, fromPath, toPath); err != nil {
		return "", agentErr("", err)
	}
	return filepath.Clean("/" + toPath), nil
}

// DeletePath recursively deletes relPath (never the shared root) and returns the cleaned path.
func (s *Service) DeletePath(ctx context.Context, n *node.Node, relPath string) (string, error) {
	if strings.TrimSpace(relPath) == "" || filepath.Clean("/"+relPath) == "/" {
		return "", opMsg(KindInvalid, "cannot delete root shared directory")
	}
	if n.IsLocal {
		abs, cleaned, err := s.localPath(relPath)
		if err != nil || cleaned == "/" {
			return "", opMsg(KindInvalid, "invalid path")
		}
		if err := os.RemoveAll(abs); err != nil {
			return "", opErr(KindInternal, err)
		}
		return cleaned, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := s.agentOp(ctx, n.ID, pb.FileOpType_FILE_OP_DELETE, relPath, "", ""); err != nil {
		return "", agentErr("", err)
	}
	return filepath.Clean("/" + relPath), nil
}

// LocalFile resolves a regular file inside the master's shared folder (for serving it directly).
func (s *Service) LocalFile(relPath string) (string, error) {
	abs, _, err := s.localPath(relPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return "", opMsg(KindNotFound, "file not found or is a directory")
	}
	return abs, nil
}

// ─── Streaming between browser, master and agent ─────────────────────────────

// Upload writes body as dirPath/filename on the node and returns the relative path and bytes written.
// For remote nodes the body is streamed through the master to the agent without touching the master's disk.
func (s *Service) Upload(ctx context.Context, n *node.Node, dirPath, filename string, body io.Reader, baseURL string) (string, int64, error) {
	relDest := filepath.Join("/", dirPath, filepath.Base(filename))
	if n.IsLocal {
		abs, cleaned, err := s.localPath(relDest)
		if err != nil {
			return "", 0, err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			return "", 0, opErr(KindInternal, err)
		}
		out, err := os.Create(abs)
		if err != nil {
			return "", 0, opErr(KindInternal, err)
		}
		written, copyErr := io.Copy(out, body)
		_ = out.Close()
		if copyErr != nil {
			_ = os.Remove(abs)
			return "", 0, opErr(KindInternal, copyErr)
		}
		return cleaned, written, nil
	}

	ticket := s.tickets.newPipeTicket()
	defer s.tickets.remove(ticket.ID)
	pr, pw := ticket.PipeReader, ticket.PipeWriter

	ctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := s.agentOp(ctx, n.ID, pb.FileOpType_FILE_OP_PULL_FROM_URL, relDest, transferURL(baseURL, ticket, 0), ticket.Token)
		if err != nil {
			_ = pr.CloseWithError(err)
		}
		errCh <- err
	}()

	select {
	case <-ticket.Started():
	case err := <-errCh:
		if err != nil {
			_ = pw.CloseWithError(err)
			return "", 0, agentErr("", err)
		}
	case <-time.After(agentConnectTimeout):
		_ = pw.CloseWithError(errors.New("timeout waiting for remote agent to initiate pull"))
		_ = pr.Close()
		return "", 0, opMsg(KindAgentTimeout, "timeout waiting for remote agent to initiate pull")
	case <-ctx.Done():
		_ = pw.CloseWithError(ctx.Err())
		_ = pr.Close()
		return "", 0, opMsg(KindCancelled, "upload cancelled")
	}

	written, copyErr := io.Copy(pw, body)
	if copyErr != nil {
		_ = pw.CloseWithError(copyErr)
		return "", 0, opErr(KindInternal, copyErr)
	}
	_ = pw.Close()

	// Wait for the agent to finish writing the file and acknowledge over gRPC.
	select {
	case err := <-errCh:
		if err != nil {
			return "", 0, agentErr("", err)
		}
	case <-ctx.Done():
		return "", 0, opMsg(KindCancelled, "upload cancelled")
	}
	return relDest, written, nil
}

// RemoteStream is a file being streamed from an agent. Close it once the body has been consumed.
type RemoteStream struct {
	Body       io.Reader
	ContentLen int64 // bytes the agent sends (0 if unknown)
	TotalSize  int64 // full file size, for ranged requests (0 if unknown)
	close      func()
}

func (rs *RemoteStream) Close() { rs.close() }

// OpenRemote asks the agent to push relPath (from offset) to the master and returns the stream once the agent
// connected. timeout bounds the whole transfer.
func (s *Service) OpenRemote(ctx context.Context, n *node.Node, relPath, baseURL string, offset int64, timeout time.Duration) (*RemoteStream, error) {
	ticket := s.tickets.newPipeTicket()
	pr, pw := ticket.PipeReader, ticket.PipeWriter
	ctx, cancel := context.WithTimeout(ctx, timeout)
	closeAll := func() {
		_ = pr.Close()
		cancel()
		s.tickets.remove(ticket.ID)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := s.agentOp(ctx, n.ID, pb.FileOpType_FILE_OP_PUSH_TO_URL, relPath, transferURL(baseURL, ticket, offset), ticket.Token)
		if err != nil {
			_ = pw.CloseWithError(err)
		}
		errCh <- err
	}()

	select {
	case <-ticket.Started():
	case err := <-errCh:
		if err != nil {
			closeAll()
			return nil, agentErr("", err)
		}
	case <-time.After(agentConnectTimeout):
		_ = pw.CloseWithError(errors.New("timeout waiting for remote agent to initiate transfer"))
		closeAll()
		return nil, opMsg(KindAgentTimeout, "timeout waiting for remote agent to initiate transfer")
	case <-ctx.Done():
		_ = pw.CloseWithError(ctx.Err())
		closeAll()
		return nil, opMsg(KindCancelled, "request cancelled")
	}

	contentLen, totalSize := ticket.Sizes()
	return &RemoteStream{Body: pr, ContentLen: contentLen, TotalSize: totalSize, close: closeAll}, nil
}

// ─── Master disk <-> node transfers (backups, replicas) ──────────────────────

func stagingPath(filename string) string {
	dir := platform.TransfersStagingDir()
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, uuid.NewString()+"_"+filepath.Base(filename))
}

// PullNodeSharedFileToLocal copies a file from a node's shared folder to destLocalPath on the master.
func (s *Service) PullNodeSharedFileToLocal(ctx context.Context, baseURL string, src *node.Node, srcRelPath, destLocalPath string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(destLocalPath), 0755); err != nil {
		return 0, err
	}
	if src.IsLocal {
		abs, _, err := s.localPath(srcRelPath)
		if err != nil {
			return 0, err
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			return 0, opMsg(KindNotFound, "source file not found or is a directory")
		}
		if abs != destLocalPath {
			if err := copyLocalFile(abs, destLocalPath); err != nil {
				return 0, opErr(KindInternal, err)
			}
		}
		return info.Size(), nil
	}

	ticket := s.tickets.newFileTicket(destLocalPath)
	defer s.tickets.remove(ticket.ID)
	if _, err := s.agentOp(ctx, src.ID, pb.FileOpType_FILE_OP_PUSH_TO_URL, srcRelPath, transferURL(baseURL, ticket, 0), ticket.Token); err != nil {
		return 0, agentErr("pull from source node failed", err)
	}
	var size int64
	if info, err := os.Stat(destLocalPath); err == nil {
		size = info.Size()
	}
	return size, nil
}

// PushLocalFileToNodeShared copies localSrcPath on the master into a node's shared folder.
func (s *Service) PushLocalFileToNodeShared(ctx context.Context, baseURL string, dst *node.Node, localSrcPath, dstRelPath string) error {
	if dst.IsLocal {
		abs, _, err := s.localPath(dstRelPath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			return opErr(KindInternal, err)
		}
		if localSrcPath == abs {
			return nil
		}
		if err := copyLocalFile(localSrcPath, abs); err != nil {
			return opErr(KindInternal, err)
		}
		return nil
	}
	ticket := s.tickets.newFileTicket(localSrcPath)
	defer s.tickets.remove(ticket.ID)
	if _, err := s.agentOp(ctx, dst.ID, pb.FileOpType_FILE_OP_PULL_FROM_URL, dstRelPath, transferURL(baseURL, ticket, 0), ticket.Token); err != nil {
		return agentErr("", err)
	}
	return nil
}

// Transfer copies a file between two nodes' shared folders (staging it on the master when the source is
// remote) and returns the size and the cleaned destination path.
func (s *Service) Transfer(ctx context.Context, baseURL string, src *node.Node, srcRelPath string, dst *node.Node, dstRelPath string) (int64, string, error) {
	var sourceFile string
	if src.IsLocal {
		abs, _, err := s.localPath(srcRelPath)
		if err != nil {
			return 0, "", err
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			return 0, "", opMsg(KindNotFound, "source file not found or is a directory")
		}
		sourceFile = abs
	} else {
		sourceFile = stagingPath(srcRelPath)
		defer os.Remove(sourceFile)
		ticket := s.tickets.newFileTicket(sourceFile)
		defer s.tickets.remove(ticket.ID)
		if _, err := s.agentOp(ctx, src.ID, pb.FileOpType_FILE_OP_PUSH_TO_URL, srcRelPath, transferURL(baseURL, ticket, 0), ticket.Token); err != nil {
			return 0, "", agentErr("pull from source node failed", err)
		}
	}

	var size int64
	if info, err := os.Stat(sourceFile); err == nil {
		size = info.Size()
	}
	cleanedDst := filepath.Clean("/" + dstRelPath)
	if dst.IsLocal {
		if _, c, err := s.localPath(dstRelPath); err == nil {
			cleanedDst = c
		}
	}
	if err := s.PushLocalFileToNodeShared(ctx, baseURL, dst, sourceFile, dstRelPath); err != nil {
		if KindOf(err) == KindAgent {
			return size, "", agentErr("push to target node failed", errors.Unwrap(err))
		}
		return size, "", err
	}
	return size, cleanedDst, nil
}

// TransferSharedFile implements apps.SharedFileOps.
func (s *Service) TransferSharedFile(ctx context.Context, baseURL string, src *node.Node, srcRelPath string, dst *node.Node, dstRelPath string) (int64, error) {
	size, _, err := s.Transfer(ctx, baseURL, src, srcRelPath, dst, dstRelPath)
	return size, err
}

// DeleteNodeSharedFile deletes a single file from a node's shared folder.
func (s *Service) DeleteNodeSharedFile(ctx context.Context, n *node.Node, relPath string) error {
	if n.IsLocal {
		abs, _, err := s.localPath(relPath)
		if err != nil {
			return err
		}
		return os.Remove(abs)
	}
	_, err := s.agentOp(ctx, n.ID, pb.FileOpType_FILE_OP_DELETE, relPath, "", "")
	return err
}

// DeleteNodeSharedFileIfExists is DeleteNodeSharedFile treating a missing file as success.
func (s *Service) DeleteNodeSharedFileIfExists(ctx context.Context, n *node.Node, relPath string) error {
	if err := s.DeleteNodeSharedFile(ctx, n, relPath); err != nil && !isNotExist(err) {
		return err
	}
	return nil
}

// ListNodeSharedFileNames returns the regular file names in relDir. A missing directory yields an empty set;
// any other failure (node offline, timeout) is returned so callers can treat the state as unknown.
func (s *Service) ListNodeSharedFileNames(ctx context.Context, n *node.Node, relDir string) (map[string]struct{}, error) {
	names := make(map[string]struct{})
	if n.IsLocal {
		abs, _, err := s.localPath(relDir)
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return names, nil
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				names[e.Name()] = struct{}{}
			}
		}
		return names, nil
	}
	res, err := s.agentOp(ctx, n.ID, pb.FileOpType_FILE_OP_LIST, relDir, "", "")
	if err != nil {
		if res != nil && isNotExist(err) {
			return names, nil
		}
		return nil, err
	}
	for _, e := range res.GetEntries() {
		if !e.GetIsDir() {
			names[e.GetName()] = struct{}{}
		}
	}
	return names, nil
}

// isNotExist reports whether a local or agent error means the path does not exist (as opposed to the node
// being unreachable).
func isNotExist(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "cannot find the file") ||
		strings.Contains(msg, "cannot find the path")
}

func copyLocalFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
