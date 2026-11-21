// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/filetransfer"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// NodeFileEntry is a shared-folder listing entry (kept as a handler-level name for the public share portal).
type NodeFileEntry = filetransfer.Entry

// NodeFileHandler exposes the node shared-folder API. It authorizes callers and translates HTTP; the
// filetransfer.Service performs the operations on local disk or through the node agent.
type NodeFileHandler struct {
	files   *filetransfer.Service
	nodeSvc *node.NodeService
	rbacSvc *identity.RBACService
}

func (h *NodeFileHandler) SetRBACService(rbac *identity.RBACService) {
	h.rbacSvc = rbac
}

func NewNodeFileHandler(files *filetransfer.Service, nodeSvc *node.NodeService) *NodeFileHandler {
	return &NodeFileHandler{files: files, nodeSvc: nodeSvc}
}

// SharedFiles returns the file transfer service (used by the backup file manager).
func (h *NodeFileHandler) SharedFiles() *filetransfer.Service { return h.files }

// InternalTransferRoutes serves the agent-to-master transfer endpoints, authenticated with a one-time
// ticket token in the URL. User-facing file routes live under /v1/nodes/{nodeId}/files (NodeScopedRoutes).
func (h *NodeFileHandler) InternalTransferRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/internal-transfer/{transferId}", h.AgentPullTransfer)
	r.Post("/internal-transfer/{transferId}", h.AgentPushTransfer)
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
		writeFileError(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	n, err := h.nodeSvc.GetByID(nodeID)
	if err != nil || n == nil {
		writeFileError(w, "node not found", http.StatusNotFound)
		return nil, false
	}
	perm := identity.PermFilesRead
	if len(requiredPerm) > 0 && requiredPerm[0] != "" {
		perm = requiredPerm[0]
	}
	if !checkNodeAccess(r.Context(), h.rbacSvc, n, claims.UserID, perm) {
		writeFileError(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return n, true
}

func writeFileError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeFileOpError maps a filetransfer error to an HTTP status, keeping its message.
func writeFileOpError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch filetransfer.KindOf(err) {
	case filetransfer.KindInvalid:
		code = http.StatusBadRequest
	case filetransfer.KindNotFound:
		code = http.StatusNotFound
	case filetransfer.KindConflict:
		code = http.StatusConflict
	case filetransfer.KindAgent:
		code = http.StatusBadGateway
	case filetransfer.KindAgentTimeout:
		code = http.StatusGatewayTimeout
	case filetransfer.KindCancelled:
		code = http.StatusRequestTimeout
	}
	writeFileError(w, err.Error(), code)
}

func writeFileJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	_ = json.NewEncoder(w).Encode(v)
}

// requestBaseURL is the URL of this server as seen by the current request (fallback for agent callbacks).
func requestBaseURL(r *http.Request) string {
	if r == nil || r.Host == "" {
		return ""
	}
	proto := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		proto = "https"
	}
	return fmt.Sprintf("%s://%s", proto, r.Host)
}

// ResolveMasterBaseURL returns the base HTTP URL that connected node agents can use to reach this server.
func (h *NodeFileHandler) ResolveMasterBaseURL(r *http.Request) string {
	return h.files.MasterBaseURLOr(requestBaseURL(r))
}

// ─── 1. List Files ──────────────────────────────────────────────────────────

func (h *NodeFileHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")
	targetNode, ok := h.authorizeNode(w, r, nodeID)
	if !ok {
		return
	}
	listing, err := h.files.List(r.Context(), targetNode, r.URL.Query().Get("path"))
	if err != nil {
		writeFileOpError(w, err)
		return
	}
	resp := map[string]any{"node_id": nodeID, "current_path": listing.CurrentPath, "entries": listing.Entries}
	if listing.BaseDir != "" {
		resp["base_dir"] = listing.BaseDir
	}
	writeFileJSON(w, http.StatusOK, resp)
}

// ─── 2. Make Directory ──────────────────────────────────────────────────────

func (h *NodeFileHandler) MakeDirectory(w http.ResponseWriter, r *http.Request) {
	targetNode, ok := h.authorizeNode(w, r, chi.URLParam(r, "nodeId"), identity.PermFilesWrite)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeFileError(w, "path is required", http.StatusBadRequest)
		return
	}
	cleaned, err := h.files.Mkdir(r.Context(), targetNode, req.Path)
	if err != nil {
		writeFileOpError(w, err)
		return
	}
	writeFileJSON(w, http.StatusCreated, map[string]any{"success": true, "path": cleaned})
}

// ─── 2b. Rename File or Directory ───────────────────────────────────────────

func (h *NodeFileHandler) RenameFile(w http.ResponseWriter, r *http.Request) {
	targetNode, ok := h.authorizeNode(w, r, chi.URLParam(r, "nodeId"), identity.PermFilesWrite)
	if !ok {
		return
	}
	var req struct {
		Path    string `json:"path"`
		NewPath string `json:"new_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" || strings.TrimSpace(req.NewPath) == "" {
		writeFileError(w, "path and new_path are required", http.StatusBadRequest)
		return
	}
	newPath, err := h.files.Rename(r.Context(), targetNode, req.Path, req.NewPath)
	if err != nil {
		writeFileOpError(w, err)
		return
	}
	writeFileJSON(w, http.StatusOK, map[string]any{"success": true, "new_path": newPath})
}

// ─── 3. Delete File or Directory ────────────────────────────────────────────

func (h *NodeFileHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	targetNode, ok := h.authorizeNode(w, r, chi.URLParam(r, "nodeId"), identity.PermFilesDelete)
	if !ok {
		return
	}
	deleted, err := h.files.DeletePath(r.Context(), targetNode, r.URL.Query().Get("path"))
	if err != nil {
		writeFileOpError(w, err)
		return
	}
	writeFileJSON(w, http.StatusOK, map[string]any{"success": true, "deleted": deleted})
}

// ─── 4. Upload File from Browser to Node ────────────────────────────────────

func (h *NodeFileHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	targetNode, ok := h.authorizeNode(w, r, chi.URLParam(r, "nodeId"), identity.PermFilesWrite)
	if !ok {
		return
	}

	// Allow large uploads (up to 10+ GB)
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	dirPath := r.URL.Query().Get("path")
	mr, err := r.MultipartReader()
	if err != nil {
		writeFileError(w, fmt.Sprintf("invalid multipart upload: %v", err), http.StatusBadRequest)
		return
	}

	var filePart *multipart.Part
	for {
		part, partErr := mr.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			writeFileError(w, fmt.Sprintf("failed reading multipart stream: %v", partErr), http.StatusBadRequest)
			return
		}
		if part.FormName() == "path" && dirPath == "" {
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			dirPath = string(b)
			_ = part.Close()
			continue
		}
		if part.FormName() == "file" {
			filePart = part
			break
		}
		_ = part.Close()
	}
	if filePart == nil {
		writeFileError(w, "missing 'file' field in form data", http.StatusBadRequest)
		return
	}
	defer filePart.Close()

	relPath, written, err := h.files.Upload(r.Context(), targetNode, dirPath, filePart.FileName(), filePart, h.ResolveMasterBaseURL(r))
	if err != nil {
		writeFileOpError(w, err)
		return
	}
	writeFileJSON(w, http.StatusCreated, map[string]any{"success": true, "path": relPath, "size_bytes": written})
}

// ─── 5. Download File from Node to Browser ──────────────────────────────────

func (h *NodeFileHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	targetNode, ok := h.authorizeNode(w, r, chi.URLParam(r, "nodeId"))
	if !ok {
		return
	}
	relPath := r.URL.Query().Get("path")
	if strings.TrimSpace(relPath) == "" {
		writeFileError(w, "path query param is required", http.StatusBadRequest)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	if targetNode.IsLocal {
		abs, err := h.files.LocalFile(relPath)
		if err != nil {
			writeFileOpError(w, err)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(abs)))
		http.ServeFile(w, r, abs)
		return
	}

	stream, err := h.files.OpenRemote(r.Context(), targetNode, relPath, h.ResolveMasterBaseURL(r), 0, 60*time.Minute)
	if err != nil {
		writeFileOpError(w, err)
		return
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(relPath)))
	if stream.ContentLen > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(stream.ContentLen, 10))
	}
	_, _ = io.Copy(w, stream.Body)
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

// rangeStart parses the start offset of a "bytes=N-" Range header (0 if absent or invalid).
func rangeStart(rangeHeader string) int64 {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0
	}
	parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
	if len(parts) > 0 && parts[0] != "" {
		if s, err := strconv.ParseInt(parts[0], 10, 64); err == nil && s >= 0 {
			return s
		}
	}
	return 0
}

func (h *NodeFileHandler) StreamFile(w http.ResponseWriter, r *http.Request) {
	targetNode, ok := h.authorizeNode(w, r, chi.URLParam(r, "nodeId"))
	if !ok {
		return
	}
	relPath := r.URL.Query().Get("path")
	if strings.TrimSpace(relPath) == "" {
		writeFileError(w, "path query param is required", http.StatusBadRequest)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	if targetNode.IsLocal {
		abs, err := h.files.LocalFile(relPath)
		if err != nil {
			writeFileOpError(w, err)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", detectMediaType(abs))
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(abs)))
		http.ServeFile(w, r, abs)
		return
	}

	rangeHeader := r.Header.Get("Range")
	startOffset := rangeStart(rangeHeader)
	stream, err := h.files.OpenRemote(r.Context(), targetNode, relPath, h.ResolveMasterBaseURL(r), startOffset, 120*time.Minute)
	if err != nil {
		writeFileOpError(w, err)
		return
	}
	defer stream.Close()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", detectMediaType(relPath))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(relPath)))

	totalSize := stream.TotalSize
	if totalSize <= 0 && stream.ContentLen > 0 {
		totalSize = stream.ContentLen + startOffset
	}
	if rangeHeader != "" && totalSize > 0 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", startOffset, totalSize-1, totalSize))
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize-startOffset, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else if stream.ContentLen > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(stream.ContentLen, 10))
		w.WriteHeader(http.StatusOK)
	}
	_, _ = io.Copy(w, stream.Body)
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
		writeFileError(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SourcePath) == "" || strings.TrimSpace(req.TargetNodeID) == "" {
		writeFileError(w, "source_path and target_node_id are required", http.StatusBadRequest)
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

	size, cleanedTarget, err := h.files.Transfer(ctx, h.ResolveMasterBaseURL(r), sourceNode, req.SourcePath, targetNode, targetRelPath)
	if err != nil {
		writeFileOpError(w, err)
		return
	}
	writeFileJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"source_node_id": sourceNodeID,
		"target_node_id": req.TargetNodeID,
		"source_path":    filepath.Clean("/" + req.SourcePath),
		"target_path":    cleanedTarget,
		"size_bytes":     size,
	})
}

// ─── Internal Agent Transfer Endpoints (Tailscale HTTP Stream) ──────────────

func (h *NodeFileHandler) lookupTicket(r *http.Request) (*filetransfer.Ticket, bool) {
	token := r.URL.Query().Get("token")
	if token == "" {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return h.files.LookupTicket(chi.URLParam(r, "transferId"), token)
}

// AgentPullTransfer serves a file (or a browser upload streaming through a pipe) to an agent.
func (h *NodeFileHandler) AgentPullTransfer(w http.ResponseWriter, r *http.Request) {
	ticket, ok := h.lookupTicket(r)
	if !ok {
		writeFileError(w, "invalid or expired transfer ticket", http.StatusUnauthorized)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	if ticket.IsPipe() {
		ticket.MarkStarted()
		w.Header().Set("Content-Type", "application/octet-stream")
		if _, err := io.Copy(w, ticket.PipeReader); err != nil {
			_ = ticket.PipeReader.CloseWithError(err)
			return
		}
		_ = ticket.PipeReader.Close()
		return
	}
	http.ServeFile(w, r, ticket.FilePath)
}

// AgentPushTransfer receives a file from an agent, into a pipe (download/stream to a browser) or a file on disk.
func (h *NodeFileHandler) AgentPushTransfer(w http.ResponseWriter, r *http.Request) {
	ticket, ok := h.lookupTicket(r)
	if !ok {
		writeFileError(w, "invalid or expired transfer ticket", http.StatusUnauthorized)
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})

	if ticket.IsPipe() {
		var totalSize int64
		if v := r.Header.Get("X-File-Size"); v != "" {
			if s, err := strconv.ParseInt(v, 10, 64); err == nil && s > 0 {
				totalSize = s
			}
		}
		// Sizes must be recorded before MarkStarted: the waiting side reads them once started.
		ticket.SetSizes(r.ContentLength, totalSize)
		ticket.MarkStarted()
		if _, err := io.Copy(ticket.PipeWriter, r.Body); err != nil {
			_ = ticket.PipeWriter.CloseWithError(err)
			writeFileError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = ticket.PipeWriter.Close()
		writeFileJSON(w, http.StatusOK, map[string]bool{"success": true})
		return
	}

	if err := ticket.ReceiveFile(r.Body); err != nil {
		writeFileError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeFileJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// StreamSharedFileAttachment streams a file from a node's shared folder to w as an attachment.
func (h *NodeFileHandler) StreamSharedFileAttachment(w http.ResponseWriter, r *http.Request, n *node.Node, relPath, filename string) error {
	if filename == "" {
		filename = filepath.Base(relPath)
	}
	if n.IsLocal {
		abs, err := h.files.LocalFile(relPath)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		http.ServeFile(w, r, abs)
		return nil
	}

	stream, err := h.files.OpenRemote(r.Context(), n, relPath, h.ResolveMasterBaseURL(r), 0, 120*time.Minute)
	if err != nil {
		return err
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if stream.ContentLen > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(stream.ContentLen, 10))
	}
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	_, _ = io.Copy(w, stream.Body)
	return nil
}

// nodePerm returns the route-level node authorization middleware (see requireNodePerm).
func (h *NodeFileHandler) nodePerm(perm string) func(http.Handler) http.Handler {
	return requireNodePerm(func() *identity.RBACService { return h.rbacSvc }, perm)
}
