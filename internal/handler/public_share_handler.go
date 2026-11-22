// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/network"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// Domain types live in core/network; aliases keep this handler's code readable.
type (
	SharePermission      = network.SharePermission
	SharedFolderLink     = network.SharedFolderLink
	TailscaleFunnelRoute = network.TailscaleFunnelRoute
)

const (
	SharePermRead   = network.SharePermRead
	SharePermWrite  = network.SharePermWrite
	SharePermEditor = network.SharePermEditor
)

// PublicShareHandler exposes the folder-share owner API, the public visitor portal and the Tailscale Funnel
// API. It authenticates and authorizes callers; network.ShareGateway owns state and side effects.
type PublicShareHandler struct {
	nodeSvc     *node.NodeService
	fileHandler *NodeFileHandler
	rbacSvc     *identity.RBACService
	gw          *network.ShareGateway
}

func (h *PublicShareHandler) SetRBACService(rbac *identity.RBACService) {
	h.rbacSvc = rbac
}

func NewPublicShareHandler(
	nodeSvc *node.NodeService,
	fileHandler *NodeFileHandler,
	jobService *jobs.JobService,
	disp dispatcher.ParamDispatcher,
	store network.GatewayStateStore,
) *PublicShareHandler {
	sharesDir := filepath.Join(platform.StorageDir(), "shares")
	_ = os.MkdirAll(sharesDir, 0700)

	gw := network.NewShareGateway(network.ShareGatewayConfig{
		Store:           store,
		LegacyStatePath: filepath.Join(sharesDir, "state.json"),
		CommandsDir:     filepath.Join(platform.StorageDir(), "commands"),
		RunOnMaster:     masterScriptRunner(nodeSvc, jobService, disp),
		DetectDNSName:   network.DetectTailscaleDNSName,
		SigningSecret:   os.Getenv("JWT_SECRET"),
	})
	gw.StartFunnelProxies()
	go func() {
		time.Sleep(3 * time.Second)
		gw.EnsureDNSName()
	}()

	return &PublicShareHandler{nodeSvc: nodeSvc, fileHandler: fileHandler, gw: gw}
}

// masterScriptRunner dispatches a script.exec job for scriptPath to the master (local) node's agent.
func masterScriptRunner(nodeSvc *node.NodeService, jobService *jobs.JobService, disp dispatcher.ParamDispatcher) func(string) error {
	return func(scriptPath string) error {
		if nodeSvc == nil || jobService == nil || disp == nil {
			return errors.New("job dispatch is not configured")
		}
		masterNode, err := nodeSvc.FindLocalNode()
		if err != nil || masterNode == nil {
			return fmt.Errorf("master node not found: %v", err)
		}
		params, _ := json.Marshal(map[string]string{"script_path": scriptPath})
		j, err := jobService.DispatchWithParams(masterNode.ID, "script.exec", jobs.CommandTypeDefault, string(params))
		if err != nil {
			return err
		}
		return disp.DispatchJobWithParams(masterNode.ID, j.ID, "script.exec", jobs.CommandTypeDefault, string(params))
	}
}

// OwnerShareRoutes mounts at /v1/file-shares (requires user authentication).
func (h *PublicShareHandler) OwnerShareRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/", h.ListOwnerShares)
	r.Post("/", h.CreateShare)
	r.Put("/{shareId}", h.UpdateShare)
	r.Delete("/{shareId}", h.DeleteShare)
	return r
}

// PublicPortalRoutes mounts at /v1/public-shares (no user JWT required; uses scoped Share Token).
func (h *PublicShareHandler) PublicPortalRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{shareId}/info", h.GetPublicShareInfo)
	r.With(middleware.RateLimitByIP(shareUnlockIPLim)).Post("/{shareId}/unlock", h.UnlockPublicShare)
	r.Get("/{shareId}/files", h.PublicListFiles)
	r.Get("/{shareId}/download", h.PublicDownloadFile)
	r.Get("/{shareId}/stream", h.PublicStreamFile)
	r.Post("/{shareId}/mkdir", h.PublicMakeDirectory)
	r.Post("/{shareId}/upload", h.PublicUploadFile)
	r.Delete("/{shareId}/files", h.PublicDeleteFile)
	return r
}

// FunnelRoutes mounts at /v1/tailscale-funnel (requires user authentication).
func (h *PublicShareHandler) FunnelRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/status", h.GetFunnelStatus)
	r.Post("/config", h.UpdateFunnelConfig)
	r.Post("/routes", h.UpsertFunnelRoute)
	r.Delete("/routes/{routeId}", h.DeleteFunnelRoute)
	return r
}

// shareWithPassword is the create/update response: the plaintext password is included only when it was
// just set or regenerated, since only its bcrypt hash is stored.
type shareWithPassword struct {
	*SharedFolderLink
	Password string `json:"password,omitempty"`
}

func writeShareJSONError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// toVisitorPath converts a path inside the node's shared folder into a path relative to the share root, so
// visitors never learn where the shared folder lives on the node.
func toVisitorPath(shareRoot, nodePath string) string {
	root := filepath.Clean("/" + shareRoot)
	p := filepath.Clean("/" + nodePath)
	if root == "/" {
		return p
	}
	if p == root {
		return "/"
	}
	if strings.HasPrefix(p, root+"/") {
		return strings.TrimPrefix(p, root)
	}
	return p
}

// relayWithVisitorPaths runs a delegated file operation and relays its response, rewriting the given string
// fields of a successful JSON body from node paths to visitor paths (see toVisitorPath).
func relayWithVisitorPaths(w http.ResponseWriter, share *SharedFolderLink, run func(http.ResponseWriter), fields ...string) {
	rec := httptest.NewRecorder()
	run(rec)
	body := rec.Body.Bytes()
	if rec.Code < http.StatusMultipleChoices {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err == nil {
			for _, f := range fields {
				if p, ok := m[f].(string); ok {
					m[f] = toVisitorPath(share.FolderPath, p)
				}
			}
			if b, err := json.Marshal(m); err == nil {
				body = append(b, '\n')
			}
		}
	}
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(rec.Code)
	_, _ = w.Write(body)
}

// canManageShare: the share's creator, or users with files:share on its node. Evaluated outside the
// gateway lock (RBAC hits the database).
func (h *PublicShareHandler) canManageShare(ctx context.Context, userID string, s *SharedFolderLink) bool {
	if s.OwnerID == userID {
		return true
	}
	if h.rbacSvc == nil {
		return false
	}
	ok, err := h.rbacSvc.HasNodePermission(ctx, userID, s.NodeID, identity.PermFilesShare)
	return err == nil && ok
}

// ─── 1. Owner Share Management Endpoints ────────────────────────────────────

// GET /v1/file-shares
func (h *PublicShareHandler) ListOwnerShares(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeShareJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	nodeFilter := strings.TrimSpace(r.URL.Query().Get("node_id"))

	// One permission lookup per node, not per share.
	allowedByNode := map[string]bool{}
	out := make([]*SharedFolderLink, 0)
	for _, s := range h.gw.Shares() {
		if nodeFilter != "" && s.NodeID != nodeFilter {
			continue
		}
		allowed, seen := allowedByNode[s.NodeID]
		if !seen {
			if h.rbacSvc != nil {
				ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, s.NodeID, identity.PermFilesShare)
				allowed = err == nil && ok
			}
			allowedByNode[s.NodeID] = allowed
		}
		if allowed || (h.rbacSvc == nil && s.OwnerID == claims.UserID) {
			out = append(out, s)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

type createShareRequest struct {
	NodeID        string          `json:"node_id"`
	FolderPath    string          `json:"folder_path"`
	Title         string          `json:"title"`
	Permission    SharePermission `json:"permission"`
	Password      string          `json:"password"`
	FunnelEnabled bool            `json:"funnel_enabled"`
}

// POST /v1/file-shares
func (h *PublicShareHandler) CreateShare(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeShareJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req createShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeShareJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	targetNode, err := h.nodeSvc.GetByID(req.NodeID)
	if err != nil || targetNode == nil {
		writeShareJSONError(w, "node not found", http.StatusNotFound)
		return
	}
	if !checkNodeAccess(r.Context(), h.rbacSvc, targetNode, claims.UserID, identity.PermFilesShare) {
		writeShareJSONError(w, "forbidden", http.StatusForbidden)
		return
	}

	share, password, err := h.gw.CreateShare(network.NewShareInput{
		OwnerID: claims.UserID, NodeID: targetNode.ID, NodeName: targetNode.Hostname,
		FolderPath: req.FolderPath, Title: req.Title, Permission: req.Permission,
		Password: req.Password, FunnelEnabled: req.FunnelEnabled,
	})
	if err != nil {
		h.writeGatewayError(w, err, "could not save share")
		return
	}

	// The plaintext password is returned only here (and when it is reset); only its hash is stored.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(shareWithPassword{SharedFolderLink: share, Password: password})
}

// PUT /v1/file-shares/{shareId}
func (h *PublicShareHandler) UpdateShare(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeShareJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Title      *string          `json:"title"`
		Permission *SharePermission `json:"permission"`
		Password   *string          `json:"password"`
		// RegeneratePassword issues a new random password (returned once in the response).
		RegeneratePassword bool  `json:"regenerate_password"`
		FunnelEnabled      *bool `json:"funnel_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeShareJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	share, ok := h.gw.Share(chi.URLParam(r, "shareId"))
	if !ok || !h.canManageShare(r.Context(), claims.UserID, share) {
		writeShareJSONError(w, "share not found", http.StatusNotFound)
		return
	}
	updated, password, err := h.gw.UpdateShare(share.ID, network.ShareUpdate{
		Title: req.Title, Permission: req.Permission, Password: req.Password,
		RegeneratePassword: req.RegeneratePassword, FunnelEnabled: req.FunnelEnabled,
	})
	if err != nil {
		h.writeGatewayError(w, err, "could not save share")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(shareWithPassword{SharedFolderLink: updated, Password: password})
}

// DELETE /v1/file-shares/{shareId}
func (h *PublicShareHandler) DeleteShare(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeShareJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	share, ok := h.gw.Share(chi.URLParam(r, "shareId"))
	if !ok || !h.canManageShare(r.Context(), claims.UserID, share) {
		writeShareJSONError(w, "share not found", http.StatusNotFound)
		return
	}
	if err := h.gw.DeleteShare(share.ID); err != nil {
		h.writeGatewayError(w, err, "could not delete share")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeGatewayError maps gateway errors to HTTP responses.
func (h *PublicShareHandler) writeGatewayError(w http.ResponseWriter, err error, persistMsg string) {
	switch {
	case errors.Is(err, network.ErrShareNotFound):
		writeShareJSONError(w, "share not found", http.StatusNotFound)
	case errors.Is(err, network.ErrFunnelRouteNotFound):
		writeShareJSONError(w, "route not found", http.StatusNotFound)
	case errors.Is(err, network.ErrFunnelPathTaken):
		writeShareJSONError(w, "forbidden: "+err.Error(), http.StatusForbidden)
	case errors.Is(err, network.ErrInvalidSharePasswd):
		writeShareJSONError(w, strings.TrimPrefix(err.Error(), network.ErrInvalidSharePasswd.Error()+": "), http.StatusBadRequest)
	case errors.Is(err, network.ErrGatewayPersist):
		writeShareJSONError(w, persistMsg, http.StatusInternalServerError)
	default:
		writeShareJSONError(w, err.Error(), http.StatusBadRequest)
	}
}

// ─── 2. Public Visitor Endpoints (Password Gate + Scoped File Access) ───────

// GET /v1/public-shares/{shareId}/info
// Returns minimal metadata before password verification. Never exposes file listings or internal paths.
func (h *PublicShareHandler) GetPublicShareInfo(w http.ResponseWriter, r *http.Request) {
	share, ok := h.gw.Share(chi.URLParam(r, "shareId"))
	if !ok {
		writeShareJSONError(w, "Este enlace compartido no existe o fue revocado.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":                share.ID,
		"title":             share.Title,
		"node_name":         share.NodeName,
		"requires_password": true,
	})
}

// POST /v1/public-shares/{shareId}/unlock
func (h *PublicShareHandler) UnlockPublicShare(w http.ResponseWriter, r *http.Request) {
	shareID := chi.URLParam(r, "shareId")
	// Per-share budget: share passwords are short, so cap guesses against one share regardless of IP.
	if ok, retry := shareUnlockShareLim.Allow("share:" + shareID); !ok {
		middleware.WriteRateLimited(w, retry)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeShareJSONError(w, "invalid request", http.StatusBadRequest)
		return
	}

	token, share, err := h.gw.Unlock(shareID, req.Password)
	switch {
	case errors.Is(err, network.ErrShareNotFound):
		writeShareJSONError(w, "Enlace no encontrado", http.StatusNotFound)
		return
	case errors.Is(err, network.ErrWrongSharePassword):
		writeShareJSONError(w, "Contraseña incorrecta", http.StatusUnauthorized)
		return
	case err != nil:
		writeShareJSONError(w, "failed to issue session token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token": token,
		"share": map[string]any{
			"id":         share.ID,
			"title":      share.Title,
			"node_name":  share.NodeName,
			"permission": share.Permission,
		},
	})
}

func (h *PublicShareHandler) authorizePublicShareRequest(
	w http.ResponseWriter,
	r *http.Request,
	requiredPerms ...SharePermission,
) (*SharedFolderLink, *node.Node, bool) {
	shareID := chi.URLParam(r, "shareId")
	token := strings.TrimSpace(r.Header.Get("X-Share-Token"))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("st"))
	}
	if token == "" {
		writeShareJSONError(w, "Se requiere contraseña para acceder a este recurso", http.StatusUnauthorized)
		return nil, nil, false
	}

	share, err := h.gw.VerifyToken(token, shareID)
	if err != nil {
		writeShareJSONError(w, err.Error(), http.StatusUnauthorized)
		return nil, nil, false
	}

	allowed := false
	for _, p := range requiredPerms {
		if share.Permission == p {
			allowed = true
			break
		}
	}
	if !allowed {
		writeShareJSONError(w, "No tienes permisos suficientes en esta carpeta compartida", http.StatusForbidden)
		return nil, nil, false
	}

	targetNode, err := h.nodeSvc.GetByID(share.NodeID)
	if err != nil || targetNode == nil {
		writeShareJSONError(w, "El nodo de almacenamiento no está disponible", http.StatusBadGateway)
		return nil, nil, false
	}
	return share, targetNode, true
}

func injectNodeAndPath(r *http.Request, targetNode *node.Node, realNodePath string) *http.Request {
	ctx := WithAuthorizedNode(r.Context(), targetNode)
	rctx := chi.RouteContext(ctx)
	if rctx != nil {
		rctx.URLParams.Add("nodeId", targetNode.ID)
	}
	cloned := r.Clone(ctx)
	q := cloned.URL.Query()
	q.Set("path", realNodePath)
	cloned.URL.RawQuery = q.Encode()
	return cloned
}

// GET /v1/public-shares/{shareId}/files?path=/...
func (h *PublicShareHandler) PublicListFiles(w http.ResponseWriter, r *http.Request) {
	share, targetNode, ok := h.authorizePublicShareRequest(w, r, SharePermRead, SharePermWrite, SharePermEditor)
	if !ok {
		return
	}
	realNodePath, cleanVisitorPath, err := network.ResolveJailedPath(share.FolderPath, r.URL.Query().Get("path"))
	if err != nil {
		writeShareJSONError(w, err.Error(), http.StatusForbidden)
		return
	}

	rec := httptest.NewRecorder()
	h.fileHandler.ListFiles(rec, injectNodeAndPath(r, targetNode, realNodePath))
	if rec.Code != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}

	var rawResp struct {
		Entries []NodeFileEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawResp); err != nil {
		writeShareJSONError(w, "failed to parse directory listing", http.StatusInternalServerError)
		return
	}

	// Rewrite entry paths relative to share.FolderPath so internal parent paths are never leaked.
	relEntries := make([]NodeFileEntry, 0, len(rawResp.Entries))
	for _, e := range rawResp.Entries {
		e.Path = toVisitorPath(share.FolderPath, e.Path)
		relEntries = append(relEntries, e)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"share_id":     share.ID,
		"title":        share.Title,
		"node_name":    share.NodeName,
		"permission":   share.Permission,
		"current_path": cleanVisitorPath,
		"entries":      relEntries,
	})
}

// GET /v1/public-shares/{shareId}/download?path=/...
func (h *PublicShareHandler) PublicDownloadFile(w http.ResponseWriter, r *http.Request) {
	share, targetNode, ok := h.authorizePublicShareRequest(w, r, SharePermRead, SharePermWrite, SharePermEditor)
	if !ok {
		return
	}
	realNodePath, _, err := network.ResolveJailedPath(share.FolderPath, r.URL.Query().Get("path"))
	if err != nil {
		writeShareJSONError(w, err.Error(), http.StatusForbidden)
		return
	}
	h.fileHandler.DownloadFile(w, injectNodeAndPath(r, targetNode, realNodePath))
}

// GET /v1/public-shares/{shareId}/stream?path=/...
func (h *PublicShareHandler) PublicStreamFile(w http.ResponseWriter, r *http.Request) {
	share, targetNode, ok := h.authorizePublicShareRequest(w, r, SharePermRead, SharePermWrite, SharePermEditor)
	if !ok {
		return
	}
	realNodePath, _, err := network.ResolveJailedPath(share.FolderPath, r.URL.Query().Get("path"))
	if err != nil {
		writeShareJSONError(w, err.Error(), http.StatusForbidden)
		return
	}
	h.fileHandler.StreamFile(w, injectNodeAndPath(r, targetNode, realNodePath))
}

// POST /v1/public-shares/{shareId}/mkdir (Allowed for "write" and "editor")
func (h *PublicShareHandler) PublicMakeDirectory(w http.ResponseWriter, r *http.Request) {
	share, targetNode, ok := h.authorizePublicShareRequest(w, r, SharePermWrite, SharePermEditor)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeShareJSONError(w, "path is required", http.StatusBadRequest)
		return
	}
	realNodePath, _, err := network.ResolveJailedPath(share.FolderPath, req.Path)
	if err != nil {
		writeShareJSONError(w, err.Error(), http.StatusForbidden)
		return
	}
	newBody, _ := json.Marshal(map[string]string{"path": realNodePath})
	delegatedReq := injectNodeAndPath(r, targetNode, realNodePath)
	delegatedReq.Body = ioNopCloser(newBody)
	relayWithVisitorPaths(w, share, func(rw http.ResponseWriter) { h.fileHandler.MakeDirectory(rw, delegatedReq) }, "path")
}

// POST /v1/public-shares/{shareId}/upload (Allowed for "write" and "editor")
func (h *PublicShareHandler) PublicUploadFile(w http.ResponseWriter, r *http.Request) {
	share, targetNode, ok := h.authorizePublicShareRequest(w, r, SharePermWrite, SharePermEditor)
	if !ok {
		return
	}
	realNodePath, _, err := network.ResolveJailedPath(share.FolderPath, r.URL.Query().Get("path"))
	if err != nil {
		writeShareJSONError(w, err.Error(), http.StatusForbidden)
		return
	}
	// The upload is relayed through a recorder, so lift the connection's deadlines here (UploadFile cannot).
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	delegatedReq := injectNodeAndPath(r, targetNode, realNodePath)
	relayWithVisitorPaths(w, share, func(rw http.ResponseWriter) { h.fileHandler.UploadFile(rw, delegatedReq) }, "path")
}

// DELETE /v1/public-shares/{shareId}/files?path=/... (Allowed ONLY for "editor")
func (h *PublicShareHandler) PublicDeleteFile(w http.ResponseWriter, r *http.Request) {
	share, targetNode, ok := h.authorizePublicShareRequest(w, r, SharePermEditor)
	if !ok {
		return
	}
	reqSubPath := r.URL.Query().Get("path")
	if strings.TrimSpace(reqSubPath) == "" || filepath.Clean("/"+reqSubPath) == "/" {
		writeShareJSONError(w, "No se puede eliminar la carpeta raíz compartida", http.StatusForbidden)
		return
	}
	realNodePath, _, err := network.ResolveJailedPath(share.FolderPath, reqSubPath)
	if err != nil || realNodePath == filepath.Clean("/"+share.FolderPath) {
		writeShareJSONError(w, "Acceso denegado", http.StatusForbidden)
		return
	}
	delegatedReq := injectNodeAndPath(r, targetNode, realNodePath)
	relayWithVisitorPaths(w, share, func(rw http.ResponseWriter) { h.fileHandler.DeleteFile(rw, delegatedReq) }, "deleted")
}

func ioNopCloser(b []byte) *readCloserBuffer {
	return &readCloserBuffer{Reader: bytes.NewReader(b)}
}

type readCloserBuffer struct {
	*bytes.Reader
}

func (r *readCloserBuffer) Close() error { return nil }

// ─── 3. Tailscale Funnel API ─────────────────────────────────────────────────

// canManageFunnelRoute reports whether userID may modify a funnel route: global admins, or users with
// network:manage on the route's target node.
func (h *PublicShareHandler) canManageFunnelRoute(ctx context.Context, userID string, rt *TailscaleFunnelRoute) bool {
	if checkAdminAccess(ctx, h.rbacSvc, userID) {
		return true
	}
	if h.rbacSvc == nil || rt == nil || rt.TargetNodeID == "" {
		return false
	}
	ok, err := h.rbacSvc.HasNodePermission(ctx, userID, rt.TargetNodeID, identity.PermNetworkManage)
	return err == nil && ok
}

// GET /v1/tailscale-funnel/status
func (h *PublicShareHandler) GetFunnelStatus(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeShareJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	dnsName, allRoutes := h.gw.FunnelStatus()

	// Only expose routes targeting nodes the caller can see (admins see everything).
	isAdmin := checkAdminAccess(r.Context(), h.rbacSvc, claims.UserID)
	routes := make([]*TailscaleFunnelRoute, 0, len(allRoutes))
	for _, rt := range allRoutes {
		if isAdmin {
			routes = append(routes, rt)
			continue
		}
		if h.rbacSvc != nil {
			if ok, err := h.rbacSvc.HasNodePermission(r.Context(), claims.UserID, rt.TargetNodeID, identity.PermNetworkRead); err == nil && ok {
				routes = append(routes, rt)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"funnel_dns_name": dnsName,
		"allowed_ports":   []int{443, 8443, 10000},
		"routes":          routes,
	})
}

// POST /v1/tailscale-funnel/config
func (h *PublicShareHandler) UpdateFunnelConfig(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeShareJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// The funnel DNS name is a platform-wide setting of the master host.
	if !checkAdminAccess(r.Context(), h.rbacSvc, claims.UserID) {
		writeShareJSONError(w, "forbidden: admin only", http.StatusForbidden)
		return
	}
	var req struct {
		FunnelDNSName string `json:"funnel_dns_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeShareJSONError(w, "invalid request", http.StatusBadRequest)
		return
	}
	clean, err := h.gw.SetFunnelDNSName(req.FunnelDNSName)
	if err != nil {
		h.writeGatewayError(w, err, "could not save funnel configuration")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"funnel_dns_name": clean})
}

// POST /v1/tailscale-funnel/routes
func (h *PublicShareHandler) UpsertFunnelRoute(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeShareJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		FunnelPort     int    `json:"funnel_port"`
		PathPrefix     string `json:"path_prefix"`
		TargetNodeID   string `json:"target_node_id"`
		ContainerPort  int    `json:"container_port"`
		ContainerID    string `json:"container_id"`
		ContainerName  string `json:"container_name"`
		ContainerImage string `json:"container_image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeShareJSONError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := network.FunnelPortBindings[req.FunnelPort]; !ok {
		req.FunnelPort = 443
	}
	if req.ContainerPort <= 0 || req.ContainerPort > 65535 {
		writeShareJSONError(w, "invalid container_port", http.StatusBadRequest)
		return
	}

	targetNode, err := h.nodeSvc.GetByID(req.TargetNodeID)
	if err != nil || targetNode == nil {
		writeShareJSONError(w, "target node not found", http.StatusNotFound)
		return
	}
	if !checkNodeAccess(r.Context(), h.rbacSvc, targetNode, claims.UserID, identity.PermNetworkManage) {
		writeShareJSONError(w, "forbidden", http.StatusForbidden)
		return
	}

	targetIP := ""
	if targetNode.IPVPN != nil && strings.TrimSpace(*targetNode.IPVPN) != "" {
		targetIP = hostIPOnly(*targetNode.IPVPN)
	} else if targetNode.IsLocal {
		targetIP = "127.0.0.1"
	} else if strings.TrimSpace(targetNode.IPLocal) != "" {
		targetIP = hostIPOnly(targetNode.IPLocal)
	}
	if targetIP == "" {
		writeShareJSONError(w, "target node has no valid IP address", http.StatusBadRequest)
		return
	}
	prefix, err := normalizeFunnelPathPrefix(req.PathPrefix)
	if err != nil {
		writeShareJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Replacing a route that already serves this public path requires managing that route's node too.
	replaceID := ""
	if existing, ok := h.gw.FunnelRouteAt(req.FunnelPort, prefix); ok {
		if !h.canManageFunnelRoute(r.Context(), claims.UserID, existing) {
			writeShareJSONError(w, "forbidden: this public path is already routed to a node you cannot manage", http.StatusForbidden)
			return
		}
		replaceID = existing.ID
	}

	route, err := h.gw.UpsertFunnelRoute(network.FunnelRouteInput{
		FunnelPort: req.FunnelPort, PathPrefix: prefix, TargetNodeID: targetNode.ID,
		TargetNodeName: targetNode.Hostname, TargetIP: targetIP, ContainerPort: req.ContainerPort,
		ContainerID: req.ContainerID, ContainerName: req.ContainerName, ContainerImage: req.ContainerImage,
	}, replaceID)
	if err != nil {
		h.writeGatewayError(w, err, "could not save funnel route")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(route)
}

// DELETE /v1/tailscale-funnel/routes/{routeId}
func (h *PublicShareHandler) DeleteFunnelRoute(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeShareJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rt, ok := h.gw.FunnelRoute(chi.URLParam(r, "routeId"))
	if !ok {
		writeShareJSONError(w, "route not found", http.StatusNotFound)
		return
	}
	if !h.canManageFunnelRoute(r.Context(), claims.UserID, rt) {
		writeShareJSONError(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := h.gw.DeleteFunnelRoute(rt.ID); err != nil {
		h.writeGatewayError(w, err, "could not delete funnel route")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
