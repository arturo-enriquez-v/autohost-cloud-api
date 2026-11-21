// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

type NodeFileEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at"`
	Mode       string `json:"mode"`
}

type transferTicket struct {
	ID         string
	Token      string
	FilePath   string // Staging or local file path on Master
	ExpiresAt  time.Time
	PipeReader *io.PipeReader
	PipeWriter *io.PipeWriter
	ContentLen int64
	TotalSize  int64
	StartedCh  chan struct{}
	ReadyCh    chan error // Signaled when a remote node finishes uploading to FilePath
}

type NodeFileHandler struct {
	grpc    *grpcserver.NodeAgentServer
	nodeSvc *node.NodeService
	rbacSvc *identity.RBACService

	ticketsMu sync.RWMutex
	tickets   map[string]*transferTicket
}

func (h *NodeFileHandler) SetRBACService(rbac *identity.RBACService) {
	h.rbacSvc = rbac
}

func NewNodeFileHandler(grpc *grpcserver.NodeAgentServer, nodeSvc *node.NodeService) *NodeFileHandler {
	_ = os.MkdirAll(platform.SharedFilesDir(), 0755)
	_ = os.MkdirAll(platform.TransfersStagingDir(), 0750)
	return &NodeFileHandler{
		grpc:    grpc,
		nodeSvc: nodeSvc,
		tickets: make(map[string]*transferTicket),
	}
}

func (h *NodeFileHandler) Routes() http.Handler {
	r := chi.NewRouter()

	// Internal agent-to-master transfer endpoints (secured with one-time ticket token)
	r.Get("/internal-transfer/{transferId}", h.AgentPullTransfer)
	r.Post("/internal-transfer/{transferId}", h.AgentPushTransfer)

	// User-authenticated endpoints
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.With(h.nodePerm(identity.PermFilesRead)).Get("/{nodeId}", h.ListFiles)
		protected.With(h.nodePerm(identity.PermFilesWrite)).Post("/{nodeId}/mkdir", h.MakeDirectory)
		protected.With(h.nodePerm(identity.PermFilesWrite)).Post("/{nodeId}/rename", h.RenameFile)
		protected.With(h.nodePerm(identity.PermFilesDelete)).Delete("/{nodeId}", h.DeleteFile)
		protected.With(h.nodePerm(identity.PermFilesWrite)).Post("/{nodeId}/upload", h.UploadFile)
		protected.With(h.nodePerm(identity.PermFilesRead)).Get("/{nodeId}/download", h.DownloadFile)
		protected.With(h.nodePerm(identity.PermFilesRead)).Get("/{nodeId}/stream", h.StreamFile)
		protected.With(h.nodePerm(identity.PermFilesRead)).Post("/{nodeId}/transfer", h.TransferBetweenNodes)
	})

	return r
}

func (h *NodeFileHandler) NodeScopedRoutes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.With(h.nodePerm(identity.PermFilesRead)).Get("/", h.ListFiles)
		protected.With(h.nodePerm(identity.PermFilesWrite)).Post("/mkdir", h.MakeDirectory)
		protected.With(h.nodePerm(identity.PermFilesWrite)).Post("/rename", h.RenameFile)
		protected.With(h.nodePerm(identity.PermFilesDelete)).Delete("/", h.DeleteFile)
		protected.With(h.nodePerm(identity.PermFilesWrite)).Post("/upload", h.UploadFile)
		protected.With(h.nodePerm(identity.PermFilesRead)).Get("/download", h.DownloadFile)
		protected.With(h.nodePerm(identity.PermFilesRead)).Get("/stream", h.StreamFile)
		protected.With(h.nodePerm(identity.PermFilesRead)).Post("/transfer", h.TransferBetweenNodes)
	})
	return r
}

type ctxKeyAuthorizedNode struct{}

// WithAuthorizedNode injects a pre-authorized target node into the request context
// (used by PublicShareHandler after validating the scoped Share Token and path jail).
func WithAuthorizedNode(ctx context.Context, n *node.Node) context.Context {
	return context.WithValue(ctx, ctxKeyAuthorizedNode{}, n)
}

func (h *NodeFileHandler) authorizeNode(w http.ResponseWriter, r *http.Request, nodeID string, requiredPerm ...string) (*node.Node, bool) {
	if preAuth, ok := r.Context().Value(ctxKeyAuthorizedNode{}).(*node.Node); ok && preAuth != nil {
		return preAuth, true
	}
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return nil, false
	}

	n, err := h.nodeSvc.GetByID(nodeID)
	if err != nil || n == nil {
		http.Error(w, `{"error":"node not found"}`, http.StatusNotFound)
		return nil, false
	}
	perm := identity.PermFilesRead
	if len(requiredPerm) > 0 && requiredPerm[0] != "" {
		perm = requiredPerm[0]
	}
	if !checkNodeAccess(r.Context(), h.rbacSvc, n, claims.UserID, perm) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return nil, false
	}
	return n, true
}

func (h *NodeFileHandler) resolveMasterBaseURL(r *http.Request) string {
	if h.nodeSvc != nil {
		if localNode, err := h.nodeSvc.FindLocalNode(); err == nil && localNode != nil {
			if localNode.IPVPN != nil && *localNode.IPVPN != "" {
				ip := strings.Split(strings.TrimSpace(*localNode.IPVPN), "/")[0]
				if ip != "" {
					return fmt.Sprintf("http://%s:8080", ip)
				}
			}
			if localNode.IPLocal != "" {
				ip := strings.Split(strings.TrimSpace(localNode.IPLocal), "/")[0]
				if ip != "" {
					return fmt.Sprintf("http://%s:8080", ip)
				}
			}
		}
	}
	if extURL := os.Getenv("NEXT_PUBLIC_API_URL"); extURL != "" {
		return strings.TrimRight(extURL, "/")
	}
	if r != nil && r.Host != "" {
		proto := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			proto = "https"
		}
		return fmt.Sprintf("%s://%s", proto, r.Host)
	}
	return "http://127.0.0.1:8080"
}

func (h *NodeFileHandler) createTicket(filePath string) *transferTicket {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	t := &transferTicket{
		ID:        uuid.NewString(),
		Token:     hex.EncodeToString(raw),
		FilePath:  filePath,
		ExpiresAt: time.Now().Add(60 * time.Minute),
		ReadyCh:   make(chan error, 1),
		StartedCh: make(chan struct{}),
	}
	h.ticketsMu.Lock()
	h.tickets[t.ID] = t
	h.ticketsMu.Unlock()
	return t
}

func (h *NodeFileHandler) createPipeTicket(pr *io.PipeReader, pw *io.PipeWriter) *transferTicket {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	t := &transferTicket{
		ID:         uuid.NewString(),
		Token:      hex.EncodeToString(raw),
		ExpiresAt:  time.Now().Add(60 * time.Minute),
		PipeReader: pr,
		PipeWriter: pw,
		ReadyCh:    make(chan error, 1),
		StartedCh:  make(chan struct{}),
	}
	h.ticketsMu.Lock()
	h.tickets[t.ID] = t
	h.ticketsMu.Unlock()
	return t
}

func (h *NodeFileHandler) removeTicket(id string) {
	h.ticketsMu.Lock()
	delete(h.tickets, id)
	h.ticketsMu.Unlock()
}

func (h *NodeFileHandler) getValidTicket(r *http.Request) (*transferTicket, bool) {
	id := chi.URLParam(r, "transferId")
	token := r.URL.Query().Get("token")
	if token == "" {
		authz := r.Header.Get("Authorization")
		token = strings.TrimPrefix(authz, "Bearer ")
	}

	h.ticketsMu.RLock()
	t, ok := h.tickets[id]
	h.ticketsMu.RUnlock()
	if !ok || t == nil || time.Now().After(t.ExpiresAt) || t.Token != token {
		return nil, false
	}
	return t, true
}

func stageFilePath(safeFilename string) string {
	stagingDir := platform.TransfersStagingDir()
	_ = os.MkdirAll(stagingDir, 0755)
	return filepath.Join(stagingDir, uuid.NewString()+"_"+safeFilename)
}

func createStagedFile(safeFilename string) (*os.File, string, error) {
	tempFile := stageFilePath(safeFilename)
	out, err := os.Create(tempFile)
	if err != nil {
		fallbackDir := filepath.Join(os.TempDir(), "autohost", "transfers")
		_ = os.MkdirAll(fallbackDir, 0755)
		tempFile = filepath.Join(fallbackDir, uuid.NewString()+"_"+safeFilename)
		out, err = os.Create(tempFile)
	}
	return out, tempFile, err
}

// ─── 1. List Files ──────────────────────────────────────────────────────────

func (h *NodeFileHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	targetNode, ok := h.authorizeNode(w, r, nodeID)
	if !ok {
		return
	}

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		relPath = "/"
	}

	if targetNode.IsLocal {
		baseDir := platform.SharedFilesDir()
		_ = os.MkdirAll(baseDir, 0755)
		absPath, cleanedRel, err := platform.SafeSharedPath(baseDir, relPath)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}

		dirEntries, err := os.ReadDir(absPath)
		if err != nil {
			if os.IsNotExist(err) && cleanedRel == "/" {
				_ = os.MkdirAll(absPath, 0755)
				dirEntries = nil
			} else {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
				return
			}
		}

		entries := make([]NodeFileEntry, 0, len(dirEntries))
		for _, de := range dirEntries {
			info, err := de.Info()
			if err != nil {
				continue
			}
			entryRel := filepath.Join(cleanedRel, de.Name())
			entries = append(entries, NodeFileEntry{
				Name:       de.Name(),
				Path:       entryRel,
				IsDir:      de.IsDir(),
				SizeBytes:  info.Size(),
				ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
				Mode:       info.Mode().String(),
			})
		}

		sortFileEntries(entries)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node_id":      nodeID,
			"base_dir":     baseDir,
			"current_path": cleanedRel,
			"entries":      entries,
		})
		return
	}

	// Remote node via gRPC
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	res, err := h.grpc.ExecuteFileOperation(ctx, nodeID, pb.FileOpType_FILE_OP_LIST, relPath, "", "")
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}

	entries := make([]NodeFileEntry, 0, len(res.GetEntries()))
	for _, e := range res.GetEntries() {
		entries = append(entries, NodeFileEntry{
			Name:       e.GetName(),
			Path:       e.GetPath(),
			IsDir:      e.GetIsDir(),
			SizeBytes:  e.GetSizeBytes(),
			ModifiedAt: time.Unix(e.GetModifiedAt(), 0).UTC().Format(time.RFC3339),
			Mode:       e.GetMode(),
		})
	}
	sortFileEntries(entries)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"node_id":      nodeID,
		"current_path": filepath.Clean("/" + relPath),
		"entries":      entries,
	})
}

func sortFileEntries(entries []NodeFileEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir // Directories first
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

// ─── 2. Make Directory ──────────────────────────────────────────────────────

func (h *NodeFileHandler) MakeDirectory(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	targetNode, ok := h.authorizeNode(w, r, nodeID, identity.PermFilesWrite)
	if !ok {
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		http.Error(w, `{"error":"path is required"}`, http.StatusBadRequest)
		return
	}

	if targetNode.IsLocal {
		baseDir := platform.SharedFilesDir()
		absPath, cleanedRel, err := platform.SafeSharedPath(baseDir, req.Path)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(absPath, 0755); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"path":    cleanedRel,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if _, err := h.grpc.ExecuteFileOperation(ctx, nodeID, pb.FileOpType_FILE_OP_MKDIR, req.Path, "", ""); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"path":    filepath.Clean("/" + req.Path),
	})
}

// ─── 2b. Rename File or Directory ───────────────────────────────────────────

func (h *NodeFileHandler) RenameFile(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	targetNode, ok := h.authorizeNode(w, r, nodeID, identity.PermFilesWrite)
	if !ok {
		return
	}

	var req struct {
		Path    string `json:"path"`
		NewPath string `json:"new_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" || strings.TrimSpace(req.NewPath) == "" {
		http.Error(w, `{"error":"path and new_path are required"}`, http.StatusBadRequest)
		return
	}

	if targetNode.IsLocal {
		baseDir := platform.SharedFilesDir()
		srcAbs, _, err := platform.SafeSharedPath(baseDir, req.Path)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		destAbs, cleanedDest, err := platform.SafeSharedPath(baseDir, req.NewPath)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		if srcAbs == filepath.Clean(baseDir) || destAbs == filepath.Clean(baseDir) {
			http.Error(w, `{"error":"cannot rename root shared directory"}`, http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(destAbs); err == nil {
			http.Error(w, `{"error":"destination already exists"}`, http.StatusConflict)
			return
		}
		if err := os.Rename(srcAbs, destAbs); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":  true,
			"new_path": cleanedDest,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if _, err := h.grpc.ExecuteFileRename(ctx, nodeID, req.Path, req.NewPath); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":  true,
		"new_path": filepath.Clean("/" + req.NewPath),
	})
}

// ─── 3. Delete File or Directory ────────────────────────────────────────────

func (h *NodeFileHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	targetNode, ok := h.authorizeNode(w, r, nodeID, identity.PermFilesDelete)
	if !ok {
		return
	}

	relPath := r.URL.Query().Get("path")
	if strings.TrimSpace(relPath) == "" || filepath.Clean("/"+relPath) == "/" {
		http.Error(w, `{"error":"cannot delete root shared directory"}`, http.StatusBadRequest)
		return
	}

	if targetNode.IsLocal {
		baseDir := platform.SharedFilesDir()
		absPath, cleanedRel, err := platform.SafeSharedPath(baseDir, relPath)
		if err != nil || cleanedRel == "/" {
			http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
			return
		}
		if err := os.RemoveAll(absPath); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"deleted": cleanedRel,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if _, err := h.grpc.ExecuteFileOperation(ctx, nodeID, pb.FileOpType_FILE_OP_DELETE, relPath, "", ""); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"deleted": filepath.Clean("/" + relPath),
	})
}

// ─── 4. Upload File from Browser to Node ────────────────────────────────────

func (h *NodeFileHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	targetNode, ok := h.authorizeNode(w, r, nodeID, identity.PermFilesWrite)
	if !ok {
		return
	}

	// Allow large uploads (up to 10+ GB)
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	dirPath := r.URL.Query().Get("path")

	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid multipart upload: %v"}`, err), http.StatusBadRequest)
		return
	}

	var filePart *multipart.Part
	for {
		part, partErr := mr.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed reading multipart stream: %v"}`, partErr), http.StatusBadRequest)
			return
		}
		formName := part.FormName()
		if formName == "path" && dirPath == "" {
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			dirPath = string(b)
			_ = part.Close()
			continue
		}
		if formName == "file" {
			filePart = part
			break
		}
		_ = part.Close()
	}

	if filePart == nil {
		http.Error(w, `{"error":"missing 'file' field in form data"}`, http.StatusBadRequest)
		return
	}
	defer filePart.Close()

	safeFilename := filepath.Base(filePart.FileName())
	relDestPath := filepath.Join("/", dirPath, safeFilename)

	if targetNode.IsLocal {
		baseDir := platform.SharedFilesDir()
		absDest, cleanedRel, err := platform.SafeSharedPath(baseDir, relDestPath)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(absDest), 0755); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		out, err := os.Create(absDest)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		written, copyErr := io.Copy(out, filePart)
		_ = out.Close()
		if copyErr != nil {
			_ = os.Remove(absDest)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, copyErr.Error()), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":    true,
			"path":       cleanedRel,
			"size_bytes": written,
		})
		return
	}

	// Remote node: Stream directly via pipe from browser -> master -> remote agent
	pr, pw := io.Pipe()
	ticket := h.createPipeTicket(pr, pw)
	defer h.removeTicket(ticket.ID)

	transferURL := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s", h.resolveMasterBaseURL(r), ticket.ID, ticket.Token)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := h.grpc.ExecuteFileOperation(ctx, nodeID, pb.FileOpType_FILE_OP_PULL_FROM_URL, relDestPath, transferURL, ticket.Token)
		if err != nil {
			_ = pr.CloseWithError(err)
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	// Wait for remote agent to connect and start pulling (timeout after 25s if agent never connects)
	select {
	case <-ticket.StartedCh:
		// Remote agent connected and is pulling!
	case err := <-errCh:
		if err != nil {
			_ = pw.CloseWithError(err)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
			return
		}
	case <-time.After(25 * time.Second):
		_ = pw.CloseWithError(errors.New("timeout waiting for remote agent to initiate pull"))
		_ = pr.Close()
		http.Error(w, `{"error":"timeout waiting for remote agent to initiate pull"}`, http.StatusGatewayTimeout)
		return
	case <-ctx.Done():
		_ = pw.CloseWithError(ctx.Err())
		_ = pr.Close()
		http.Error(w, `{"error":"upload cancelled"}`, http.StatusRequestTimeout)
		return
	}

	written, copyErr := io.Copy(pw, filePart)
	if copyErr != nil {
		_ = pw.CloseWithError(copyErr)
		http.Error(w, fmt.Sprintf(`{"error":%q}`, copyErr.Error()), http.StatusInternalServerError)
		return
	}
	_ = pw.Close()

	// Wait for remote agent to finish flushing/renaming the file and acknowledge via gRPC
	select {
	case err := <-errCh:
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
			return
		}
	case <-ctx.Done():
		http.Error(w, `{"error":"upload cancelled"}`, http.StatusRequestTimeout)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":    true,
		"path":       relDestPath,
		"size_bytes": written,
	})
}

// ─── 5. Download File from Node to Browser ──────────────────────────────────

func (h *NodeFileHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	targetNode, ok := h.authorizeNode(w, r, nodeID)
	if !ok {
		return
	}

	relPath := r.URL.Query().Get("path")
	if strings.TrimSpace(relPath) == "" {
		http.Error(w, `{"error":"path query param is required"}`, http.StatusBadRequest)
		return
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	if targetNode.IsLocal {
		baseDir := platform.SharedFilesDir()
		absPath, _, err := platform.SafeSharedPath(baseDir, relPath)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		info, err := os.Stat(absPath)
		if err != nil || info.IsDir() {
			http.Error(w, `{"error":"file not found or is a directory"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(absPath)))
		http.ServeFile(w, r, absPath)
		return
	}

	// Remote node: Stream directly via pipe from remote agent -> master -> browser
	pr, pw := io.Pipe()
	ticket := h.createPipeTicket(pr, pw)
	defer h.removeTicket(ticket.ID)

	transferURL := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s", h.resolveMasterBaseURL(r), ticket.ID, ticket.Token)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := h.grpc.ExecuteFileOperation(ctx, nodeID, pb.FileOpType_FILE_OP_PUSH_TO_URL, relPath, transferURL, ticket.Token)
		if err != nil {
			_ = pw.CloseWithError(err)
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	// Wait for agent to connect and provide headers (timeout after 25s if agent never connects)
	select {
	case <-ticket.StartedCh:
		// Agent connected and started pushing!
	case err := <-errCh:
		if err != nil {
			_ = pr.Close()
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
			return
		}
	case <-time.After(25 * time.Second):
		_ = pw.CloseWithError(errors.New("timeout waiting for remote agent to initiate transfer"))
		_ = pr.Close()
		http.Error(w, `{"error":"timeout waiting for remote agent to initiate transfer"}`, http.StatusGatewayTimeout)
		return
	case <-ctx.Done():
		_ = pw.CloseWithError(ctx.Err())
		_ = pr.Close()
		http.Error(w, `{"error":"request cancelled"}`, http.StatusRequestTimeout)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(relPath)))
	if ticket.ContentLen > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(ticket.ContentLen, 10))
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	_, _ = io.Copy(w, pr)
	_ = pr.Close()
}

// ─── 5b. Stream File (Media Playback with HTTP Range Support) ───────────────

func detectMediaType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".ogv":
		return "video/ogg"
	case ".flac":
		return "audio/flac"
	case ".aac":
		return "audio/aac"
	default:
		if m := mime.TypeByExtension(ext); m != "" {
			return m
		}
		return "application/octet-stream"
	}
}

func (h *NodeFileHandler) StreamFile(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	targetNode, ok := h.authorizeNode(w, r, nodeID)
	if !ok {
		return
	}

	relPath := r.URL.Query().Get("path")
	if strings.TrimSpace(relPath) == "" {
		http.Error(w, `{"error":"path query param is required"}`, http.StatusBadRequest)
		return
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	if targetNode.IsLocal {
		baseDir := platform.SharedFilesDir()
		absPath, _, err := platform.SafeSharedPath(baseDir, relPath)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		info, err := os.Stat(absPath)
		if err != nil || info.IsDir() {
			http.Error(w, `{"error":"file not found or is a directory"}`, http.StatusNotFound)
			return
		}

		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", detectMediaType(absPath))
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(absPath)))
		http.ServeFile(w, r, absPath)
		return
	}

	// Remote node: Stream directly via pipe from remote agent -> master -> browser
	rangeHeader := r.Header.Get("Range")
	var startOffset int64
	if strings.HasPrefix(rangeHeader, "bytes=") {
		spec := strings.TrimPrefix(rangeHeader, "bytes=")
		parts := strings.Split(spec, "-")
		if len(parts) > 0 && parts[0] != "" {
			if s, err := strconv.ParseInt(parts[0], 10, 64); err == nil && s >= 0 {
				startOffset = s
			}
		}
	}

	pr, pw := io.Pipe()
	ticket := h.createPipeTicket(pr, pw)
	defer h.removeTicket(ticket.ID)

	transferURL := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s&offset=%d",
		h.resolveMasterBaseURL(r), ticket.ID, ticket.Token, startOffset)

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Minute)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := h.grpc.ExecuteFileOperation(ctx, nodeID, pb.FileOpType_FILE_OP_PUSH_TO_URL, relPath, transferURL, ticket.Token)
		if err != nil {
			_ = pw.CloseWithError(err)
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	// Wait for agent to connect and provide headers (timeout after 25s if agent never connects)
	select {
	case <-ticket.StartedCh:
		// Agent connected!
	case err := <-errCh:
		if err != nil {
			_ = pr.Close()
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
			return
		}
	case <-time.After(25 * time.Second):
		_ = pw.CloseWithError(errors.New("timeout waiting for remote agent to initiate stream"))
		_ = pr.Close()
		http.Error(w, `{"error":"timeout waiting for remote agent to initiate stream"}`, http.StatusGatewayTimeout)
		return
	case <-ctx.Done():
		_ = pw.CloseWithError(ctx.Err())
		_ = pr.Close()
		http.Error(w, `{"error":"request cancelled"}`, http.StatusRequestTimeout)
		return
	}

	mimeType := detectMediaType(relPath)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(relPath)))

	totalSize := ticket.TotalSize
	if totalSize <= 0 && ticket.ContentLen > 0 {
		totalSize = ticket.ContentLen + startOffset
	}

	if rangeHeader != "" && totalSize > 0 {
		endByte := totalSize - 1
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", startOffset, endByte, totalSize))
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize-startOffset, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else if ticket.ContentLen > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(ticket.ContentLen, 10))
		w.WriteHeader(http.StatusOK)
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	_, _ = io.Copy(w, pr)
	_ = pr.Close()
}

// ─── 6. Transfer File Between Nodes (Node A -> Node B over Tailscale) ───────

func (h *NodeFileHandler) TransferBetweenNodes(w http.ResponseWriter, r *http.Request) {
	sourceNodeID := chi.URLParam(r, "nodeId")
	sourceNode, ok := h.authorizeNode(w, r, sourceNodeID)
	if !ok {
		return
	}

	var req struct {
		SourcePath   string `json:"source_path"`
		TargetNodeID string `json:"target_node_id"`
		TargetPath   string `json:"target_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SourcePath) == "" || strings.TrimSpace(req.TargetNodeID) == "" {
		http.Error(w, `{"error":"source_path and target_node_id are required"}`, http.StatusBadRequest)
		return
	}

	targetNode, ok := h.authorizeNode(w, r, req.TargetNodeID, identity.PermFilesWrite)
	if !ok {
		return
	}

	targetRelPath := req.TargetPath
	if strings.TrimSpace(targetRelPath) == "" {
		targetRelPath = "/" + filepath.Base(req.SourcePath)
	} else if strings.HasSuffix(targetRelPath, "/") {
		targetRelPath = filepath.Join(targetRelPath, filepath.Base(req.SourcePath))
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()

	baseDir := platform.SharedFilesDir()
	var sourceLocalFile string
	var cleanupStaging bool

	// Step A: Obtain file from Source Node
	if sourceNode.IsLocal {
		absSrc, _, err := platform.SafeSharedPath(baseDir, req.SourcePath)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		info, err := os.Stat(absSrc)
		if err != nil || info.IsDir() {
			http.Error(w, `{"error":"source file not found or is a directory"}`, http.StatusNotFound)
			return
		}
		sourceLocalFile = absSrc
	} else {
		sourceLocalFile = stageFilePath(filepath.Base(req.SourcePath))
		cleanupStaging = true
		defer func() {
			if cleanupStaging {
				_ = os.Remove(sourceLocalFile)
			}
		}()

		pushTicket := h.createTicket(sourceLocalFile)
		defer h.removeTicket(pushTicket.ID)

		pushURL := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s", h.resolveMasterBaseURL(r), pushTicket.ID, pushTicket.Token)
		if _, err := h.grpc.ExecuteFileOperation(ctx, sourceNodeID, pb.FileOpType_FILE_OP_PUSH_TO_URL, req.SourcePath, pushURL, pushTicket.Token); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"pull from source node failed: %v"}`, err), http.StatusBadGateway)
			return
		}
	}

	// Step B: Deliver file to Target Node
	var sizeBytes int64
	if info, err := os.Stat(sourceLocalFile); err == nil {
		sizeBytes = info.Size()
	}

	if targetNode.IsLocal {
		absDst, cleanedDst, err := platform.SafeSharedPath(baseDir, targetRelPath)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(absDst), 0755); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		if sourceLocalFile != absDst {
			if err := copyLocalFile(sourceLocalFile, absDst); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
				return
			}
		}
		targetRelPath = cleanedDst
	} else {
		pullTicket := h.createTicket(sourceLocalFile)
		defer h.removeTicket(pullTicket.ID)

		pullURL := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s", h.resolveMasterBaseURL(r), pullTicket.ID, pullTicket.Token)
		if _, err := h.grpc.ExecuteFileOperation(ctx, req.TargetNodeID, pb.FileOpType_FILE_OP_PULL_FROM_URL, targetRelPath, pullURL, pullTicket.Token); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"push to target node failed: %v"}`, err), http.StatusBadGateway)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":        true,
		"source_node_id": sourceNodeID,
		"target_node_id": req.TargetNodeID,
		"source_path":    filepath.Clean("/" + req.SourcePath),
		"target_path":    filepath.Clean("/" + targetRelPath),
		"size_bytes":     sizeBytes,
	})
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

// ─── Internal Agent Transfer Endpoints (Tailscale HTTP Stream) ──────────────

func (h *NodeFileHandler) AgentPullTransfer(w http.ResponseWriter, r *http.Request) {
	ticket, ok := h.getValidTicket(r)
	if !ok {
		http.Error(w, `{"error":"invalid or expired transfer ticket"}`, http.StatusUnauthorized)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	if ticket.PipeReader != nil {
		select {
		case <-ticket.StartedCh:
		default:
			close(ticket.StartedCh)
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		_, copyErr := io.Copy(w, ticket.PipeReader)
		if copyErr != nil {
			_ = ticket.PipeReader.CloseWithError(copyErr)
			return
		}
		_ = ticket.PipeReader.Close()
		return
	}

	http.ServeFile(w, r, ticket.FilePath)
}

func (h *NodeFileHandler) AgentPushTransfer(w http.ResponseWriter, r *http.Request) {
	ticket, ok := h.getValidTicket(r)
	if !ok {
		http.Error(w, `{"error":"invalid or expired transfer ticket"}`, http.StatusUnauthorized)
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})

	if ticket.PipeWriter != nil {
		ticket.ContentLen = r.ContentLength
		if fileSizeHeader := r.Header.Get("X-File-Size"); fileSizeHeader != "" {
			if s, err := strconv.ParseInt(fileSizeHeader, 10, 64); err == nil && s > 0 {
				ticket.TotalSize = s
			}
		}
		select {
		case <-ticket.StartedCh:
		default:
			close(ticket.StartedCh)
		}

		_, copyErr := io.Copy(ticket.PipeWriter, r.Body)
		if copyErr != nil {
			_ = ticket.PipeWriter.CloseWithError(copyErr)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, copyErr.Error()), http.StatusInternalServerError)
			return
		}
		_ = ticket.PipeWriter.Close()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}

	out, err := os.Create(ticket.FilePath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	_, copyErr := io.Copy(out, r.Body)
	_ = out.Close()
	if copyErr != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, copyErr.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// MasterBaseURL returns the base HTTP URL agents use to reach this server, without request context
// (implements apps.SharedFileOps).
func (h *NodeFileHandler) MasterBaseURL() string {
	return h.resolveMasterBaseURL(nil)
}

// ResolveMasterBaseURL returns the base HTTP URL that connected node agents can use to reach this server.
func (h *NodeFileHandler) ResolveMasterBaseURL(r *http.Request) string {
	return h.resolveMasterBaseURL(r)
}

// PullNodeSharedFileToLocal pulls a file from a node's shared directory into a destination path on the parent server disk.
func (h *NodeFileHandler) PullNodeSharedFileToLocal(ctx context.Context, baseURL string, sourceNode *node.Node, sourceRelPath, destLocalPath string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(destLocalPath), 0755); err != nil {
		return 0, err
	}
	if sourceNode.IsLocal {
		absSrc, _, err := platform.SafeSharedPath(platform.SharedFilesDir(), sourceRelPath)
		if err != nil {
			return 0, err
		}
		info, err := os.Stat(absSrc)
		if err != nil || info.IsDir() {
			return 0, fmt.Errorf("source file not found or is a directory")
		}
		if absSrc != destLocalPath {
			if err := copyLocalFile(absSrc, destLocalPath); err != nil {
				return 0, err
			}
		}
		return info.Size(), nil
	}

	pushTicket := h.createTicket(destLocalPath)
	defer h.removeTicket(pushTicket.ID)

	pushURL := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s", strings.TrimRight(baseURL, "/"), pushTicket.ID, pushTicket.Token)
	if _, err := h.grpc.ExecuteFileOperation(ctx, sourceNode.ID, pb.FileOpType_FILE_OP_PUSH_TO_URL, sourceRelPath, pushURL, pushTicket.Token); err != nil {
		return 0, fmt.Errorf("pull from source node failed: %w", err)
	}
	var sizeBytes int64
	if info, err := os.Stat(destLocalPath); err == nil {
		sizeBytes = info.Size()
	}
	return sizeBytes, nil
}

// DeleteNodeSharedFile deletes a file from a node's shared directory.
func (h *NodeFileHandler) DeleteNodeSharedFile(ctx context.Context, targetNode *node.Node, relPath string) error {
	if targetNode.IsLocal {
		absPath, _, err := platform.SafeSharedPath(platform.SharedFilesDir(), relPath)
		if err != nil {
			return err
		}
		return os.Remove(absPath)
	}
	_, err := h.grpc.ExecuteFileOperation(ctx, targetNode.ID, pb.FileOpType_FILE_OP_DELETE, relPath, "", "")
	return err
}

// isSharedNotExistErr reports whether an error from a (local or remote) shared folder operation means
// the path does not exist, as opposed to the node being unreachable or another failure.
func isSharedNotExistErr(err error) bool {
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

// ListNodeSharedFileNames returns the set of regular file names inside relDir of a node's shared folder.
// A directory that does not exist yields an empty set and no error. Any other failure (node offline,
// timeout, permission error) is returned so callers can treat the state as unknown.
func (h *NodeFileHandler) ListNodeSharedFileNames(ctx context.Context, targetNode *node.Node, relDir string) (map[string]struct{}, error) {
	names := make(map[string]struct{})
	if targetNode.IsLocal {
		absPath, _, err := platform.SafeSharedPath(platform.SharedFilesDir(), relDir)
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(absPath)
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

	res, err := h.grpc.ExecuteFileOperation(ctx, targetNode.ID, pb.FileOpType_FILE_OP_LIST, relDir, "", "")
	if err != nil {
		if res != nil && isSharedNotExistErr(err) {
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

// DeleteNodeSharedFileIfExists deletes a file from a node's shared directory, treating a missing file as success.
func (h *NodeFileHandler) DeleteNodeSharedFileIfExists(ctx context.Context, targetNode *node.Node, relPath string) error {
	if err := h.DeleteNodeSharedFile(ctx, targetNode, relPath); err != nil && !isSharedNotExistErr(err) {
		return err
	}
	return nil
}

// StreamSharedFileAttachment streams a file from a node's shared directory directly to an HTTP response as an attachment.
func (h *NodeFileHandler) StreamSharedFileAttachment(w http.ResponseWriter, r *http.Request, n *node.Node, relPath, filename string) error {
	if filename == "" {
		filename = filepath.Base(relPath)
	}

	if n.IsLocal {
		absPath, _, err := platform.SafeSharedPath(platform.SharedFilesDir(), relPath)
		if err != nil {
			return err
		}
		info, err := os.Stat(absPath)
		if err != nil || info.IsDir() {
			return fmt.Errorf("file not found on local node shared folder")
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		http.ServeFile(w, r, absPath)
		return nil
	}

	pr, pw := io.Pipe()
	ticket := h.createPipeTicket(pr, pw)
	defer h.removeTicket(ticket.ID)

	transferURL := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s",
		h.resolveMasterBaseURL(r), ticket.ID, ticket.Token)

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Minute)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := h.grpc.ExecuteFileOperation(ctx, n.ID, pb.FileOpType_FILE_OP_PUSH_TO_URL, relPath, transferURL, ticket.Token)
		if err != nil {
			_ = pw.CloseWithError(err)
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	select {
	case <-ticket.StartedCh:
	case err := <-errCh:
		if err != nil {
			_ = pr.Close()
			return err
		}
	case <-time.After(25 * time.Second):
		_ = pw.CloseWithError(errors.New("timeout waiting for remote agent to initiate stream"))
		_ = pr.Close()
		return errors.New("timeout waiting for remote agent to initiate stream")
	case <-ctx.Done():
		_ = pw.CloseWithError(ctx.Err())
		_ = pr.Close()
		return ctx.Err()
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if ticket.ContentLen > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(ticket.ContentLen, 10))
	}
	w.WriteHeader(http.StatusOK)

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	_, _ = io.Copy(w, pr)
	_ = pr.Close()
	return nil
}

// PushLocalFileToNodeShared delivers a file from the parent server disk to a node's shared directory.
func (h *NodeFileHandler) PushLocalFileToNodeShared(ctx context.Context, baseURL string, targetNode *node.Node, localSrcPath, targetRelPath string) error {
	if targetNode.IsLocal {
		absDst, _, err := platform.SafeSharedPath(platform.SharedFilesDir(), targetRelPath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(absDst), 0755); err != nil {
			return err
		}
		if localSrcPath == absDst {
			return nil
		}
		return copyLocalFile(localSrcPath, absDst)
	}

	pullTicket := h.createTicket(localSrcPath)
	defer h.removeTicket(pullTicket.ID)

	pullURL := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s", strings.TrimRight(baseURL, "/"), pullTicket.ID, pullTicket.Token)
	_, err := h.grpc.ExecuteFileOperation(ctx, targetNode.ID, pb.FileOpType_FILE_OP_PULL_FROM_URL, targetRelPath, pullURL, pullTicket.Token)
	return err
}

// TransferSharedFile transfers a file from sourceNode's shared directory to targetNode's shared directory.
func (h *NodeFileHandler) TransferSharedFile(ctx context.Context, baseURL string, sourceNode *node.Node, sourceRelPath string, targetNode *node.Node, targetRelPath string) (int64, error) {
	baseDir := platform.SharedFilesDir()
	var sourceLocalFile string
	var cleanupStaging bool

	if sourceNode.IsLocal {
		absSrc, _, err := platform.SafeSharedPath(baseDir, sourceRelPath)
		if err != nil {
			return 0, err
		}
		info, err := os.Stat(absSrc)
		if err != nil || info.IsDir() {
			return 0, fmt.Errorf("source file not found or is a directory")
		}
		sourceLocalFile = absSrc
	} else {
		sourceLocalFile = stageFilePath(filepath.Base(sourceRelPath))
		cleanupStaging = true
		defer func() {
			if cleanupStaging {
				_ = os.Remove(sourceLocalFile)
			}
		}()

		pushTicket := h.createTicket(sourceLocalFile)
		defer h.removeTicket(pushTicket.ID)

		pushURL := fmt.Sprintf("%s/v1/node-files/internal-transfer/%s?token=%s", strings.TrimRight(baseURL, "/"), pushTicket.ID, pushTicket.Token)
		if _, err := h.grpc.ExecuteFileOperation(ctx, sourceNode.ID, pb.FileOpType_FILE_OP_PUSH_TO_URL, sourceRelPath, pushURL, pushTicket.Token); err != nil {
			return 0, fmt.Errorf("pull from source node failed: %w", err)
		}
	}

	var sizeBytes int64
	if info, err := os.Stat(sourceLocalFile); err == nil {
		sizeBytes = info.Size()
	}

	if err := h.PushLocalFileToNodeShared(ctx, baseURL, targetNode, sourceLocalFile, targetRelPath); err != nil {
		return sizeBytes, fmt.Errorf("push to target node failed: %w", err)
	}
	return sizeBytes, nil
}

// nodePerm returns the route-level node authorization middleware (see requireNodePerm).
func (h *NodeFileHandler) nodePerm(perm string) func(http.Handler) http.Handler {
	return requireNodePerm(func() *identity.RBACService { return h.rbacSvc }, perm)
}
