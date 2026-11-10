// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
)

// SharePermission defines the access level for a shared folder link:
//   - "read":   View folder contents, stream media, and download files.
//   - "write":  "read" + upload new files and create subfolders.
//   - "editor": "write" + delete files and folders inside the shared scope.
type SharePermission string

const (
	SharePermRead   SharePermission = "read"
	SharePermWrite  SharePermission = "write"
	SharePermEditor SharePermission = "editor"
)

func isValidSharePerm(p SharePermission) bool {
	return p == SharePermRead || p == SharePermWrite || p == SharePermEditor
}

// SharedFolderLink represents a Google-Drive-style password-protected folder share.
type SharedFolderLink struct {
	ID            string          `json:"id"`
	OwnerID       string          `json:"owner_id"`
	NodeID        string          `json:"node_id"`
	NodeName      string          `json:"node_name"`
	FolderPath    string          `json:"folder_path"` // "/" for all folders, or "/specific/folder"
	Title         string          `json:"title"`
	Permission    SharePermission `json:"permission"` // "read" | "write" | "editor"
	Password      string          `json:"password"`   // visible only to authenticated owner
	FunnelEnabled bool            `json:"funnel_enabled"`
	FunnelURL     string          `json:"funnel_url,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// TailscaleFunnelRoute represents a Docker container exposed via Tailscale Funnel.
type TailscaleFunnelRoute struct {
	ID             string    `json:"id"`
	FunnelPort     int       `json:"funnel_port"` // 443, 8443, or 10000
	PathPrefix     string    `json:"path_prefix"` // "/" or "/app"
	PublicURL      string    `json:"public_url"`
	TargetNodeID   string    `json:"target_node_id"`
	TargetNodeName string    `json:"target_node_name"`
	TargetIP       string    `json:"target_ip"`
	ContainerPort  int       `json:"container_port"`
	Upstream       string    `json:"upstream"`
	ContainerID    string    `json:"container_id,omitempty"`
	ContainerName  string    `json:"container_name,omitempty"`
	ContainerImage string    `json:"container_image,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type shareAndFunnelState struct {
	FunnelDNSName string                  `json:"funnel_dns_name"`
	Shares        []*SharedFolderLink     `json:"shares"`
	FunnelRoutes  []*TailscaleFunnelRoute `json:"funnel_routes"`
	UpdatedAt     time.Time               `json:"updated_at"`
}

type shareTokenClaims struct {
	ShareID    string          `json:"sid"`
	NodeID     string          `json:"nid"`
	FolderPath string          `json:"fp"`
	Permission SharePermission `json:"perm"`
	ExpiresAt  int64           `json:"exp"`
}

type PublicShareHandler struct {
	nodeSvc     *node.NodeService
	fileHandler *NodeFileHandler
	jobService  *jobs.JobService
	dispatcher  dispatcher.ParamDispatcher
	baseDir     string
	statePath   string
	mu          sync.RWMutex
	state       *shareAndFunnelState
	funnelLogs  []string
}

func NewPublicShareHandler(
	nodeSvc *node.NodeService,
	fileHandler *NodeFileHandler,
	jobService *jobs.JobService,
	disp dispatcher.ParamDispatcher,
) *PublicShareHandler {
	baseDir := filepath.Join(platform.StorageDir(), "shares")
	_ = os.MkdirAll(baseDir, 0700)

	h := &PublicShareHandler{
		nodeSvc:     nodeSvc,
		fileHandler: fileHandler,
		jobService:  jobService,
		dispatcher:  disp,
		baseDir:     baseDir,
		statePath:   filepath.Join(baseDir, "state.json"),
		state: &shareAndFunnelState{
			Shares:       []*SharedFolderLink{},
			FunnelRoutes: []*TailscaleFunnelRoute{},
		},
	}

	h.loadState()
	h.startFunnelReverseProxies()

	go func() {
		time.Sleep(3 * time.Second)
		if dns := h.detectTailscaleDNSName(); dns != "" {
			h.mu.Lock()
			if h.state.FunnelDNSName == "" {
				h.state.FunnelDNSName = dns
				h.saveStateLocked()
			}
			h.mu.Unlock()
		}
	}()

	return h
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
	r.Post("/{shareId}/unlock", h.UnlockPublicShare)
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

func (h *PublicShareHandler) loadState() {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := os.ReadFile(h.statePath)
	if err != nil {
		return
	}
	var st shareAndFunnelState
	if err := json.Unmarshal(data, &st); err == nil {
		if st.Shares == nil {
			st.Shares = []*SharedFolderLink{}
		}
		if st.FunnelRoutes == nil {
			st.FunnelRoutes = []*TailscaleFunnelRoute{}
		}
		h.state = &st
	}
}

func (h *PublicShareHandler) saveStateLocked() {
	h.state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(h.state, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(h.baseDir, 0700)
	_ = os.WriteFile(h.statePath, data, 0600)
}

func generateDefaultSharePassword() string {
	const chars = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	var sb strings.Builder
	sb.WriteString("AH-")
	for i := 0; i < 8; i++ {
		if i == 4 {
			sb.WriteByte('-')
		}
		sb.WriteByte(chars[int(b[i])%len(chars)])
	}
	return sb.String()
}

func generateShortID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (h *PublicShareHandler) shareSigningKey() []byte {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = "autohost-default-share-secret-key"
	}
	// Separate domain key so a share token is cryptographically distinct from user JWTs
	sum := sha256.Sum256([]byte("autohost-share-scope-v1:" + secret))
	return sum[:]
}

func (h *PublicShareHandler) issueShareToken(share *SharedFolderLink) (string, error) {
	claims := shareTokenClaims{
		ShareID:    share.ID,
		NodeID:     share.NodeID,
		FolderPath: share.FolderPath,
		Permission: share.Permission,
		ExpiresAt:  time.Now().Add(12 * time.Hour).Unix(),
	}
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)
	mac := hmac.New(sha256.New, h.shareSigningKey())
	mac.Write([]byte(payloadB64))
	sigB64 := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payloadB64 + "." + sigB64, nil
}

func (h *PublicShareHandler) verifyShareToken(tokenStr string, expectedShareID string) (*SharedFolderLink, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid share token format")
	}
	mac := hmac.New(sha256.New, h.shareSigningKey())
	mac.Write([]byte(parts[0]))
	expectedSig := mac.Sum(nil)
	actualSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || subtle.ConstantTimeCompare(expectedSig, actualSig) != 1 {
		return nil, fmt.Errorf("invalid share token signature")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid share token payload")
	}
	var claims shareTokenClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("malformed share token claims")
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, fmt.Errorf("share session expired, please re-enter password")
	}
	if claims.ShareID != expectedShareID {
		return nil, fmt.Errorf("share token mismatch")
	}

	// Verify the share still exists in state (in case owner revoked it or downgraded permissions)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.state.Shares {
		if s.ID == expectedShareID {
			return s, nil
		}
	}
	return nil, fmt.Errorf("share link has been revoked")
}

// resolveJailedPath maps a visitor's relative path inside the shared folder to the real node path,
// guaranteeing that the visitor can NEVER escape above share.FolderPath.
func resolveJailedPath(shareRoot string, requestedSubPath string) (string, string, error) {
	cleanRoot := filepath.Clean("/" + strings.TrimSpace(shareRoot))
	cleanSub := filepath.Clean("/" + strings.TrimSpace(requestedSubPath))

	if cleanRoot == "/" {
		return cleanSub, cleanSub, nil
	}

	fullNodePath := filepath.Clean(filepath.Join(cleanRoot, cleanSub))
	if fullNodePath != cleanRoot && !strings.HasPrefix(fullNodePath, cleanRoot+"/") {
		return "", "", fmt.Errorf("access denied: path outside shared folder")
	}
	return fullNodePath, cleanSub, nil
}

// ─── 1. Owner Share Management Endpoints ────────────────────────────────────

// GET /v1/file-shares
func (h *PublicShareHandler) ListOwnerShares(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	nodeFilter := strings.TrimSpace(r.URL.Query().Get("node_id"))

	h.mu.RLock()
	out := make([]*SharedFolderLink, 0)
	for _, s := range h.state.Shares {
		if s.OwnerID != claims.UserID {
			continue
		}
		if nodeFilter != "" && s.NodeID != nodeFilter {
			continue
		}
		out = append(out, s)
	}
	h.mu.RUnlock()

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
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req createShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	targetNode, err := h.nodeSvc.GetByID(req.NodeID)
	if err != nil || targetNode == nil {
		http.Error(w, `{"error":"node not found"}`, http.StatusNotFound)
		return
	}
	if targetNode.OwnerID != nil && *targetNode.OwnerID != claims.UserID {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	cleanPath := filepath.Clean("/" + strings.TrimSpace(req.FolderPath))
	perm := req.Permission
	if !isValidSharePerm(perm) {
		perm = SharePermRead
	}

	pwd := strings.TrimSpace(req.Password)
	if pwd == "" {
		pwd = generateDefaultSharePassword()
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		if cleanPath == "/" {
			title = fmt.Sprintf("Archivos de %s", targetNode.Hostname)
		} else {
			title = filepath.Base(cleanPath)
		}
	}

	now := time.Now().UTC()
	shareID := generateShortID("sh_")

	h.mu.Lock()
	dnsName := h.state.FunnelDNSName
	var funnelURL string
	if req.FunnelEnabled && dnsName != "" {
		funnelURL = fmt.Sprintf("https://%s/share/%s", dnsName, shareID)
	}

	share := &SharedFolderLink{
		ID:            shareID,
		OwnerID:       claims.UserID,
		NodeID:        targetNode.ID,
		NodeName:      targetNode.Hostname,
		FolderPath:    cleanPath,
		Title:         title,
		Permission:    perm,
		Password:      pwd,
		FunnelEnabled: req.FunnelEnabled,
		FunnelURL:     funnelURL,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	h.state.Shares = append(h.state.Shares, share)
	h.saveStateLocked()
	h.mu.Unlock()

	if req.FunnelEnabled {
		go h.syncHostTailscaleFunnel()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(share)
}

// PUT /v1/file-shares/{shareId}
func (h *PublicShareHandler) UpdateShare(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	shareID := chi.URLParam(r, "shareId")
	var req struct {
		Title         *string          `json:"title"`
		Permission    *SharePermission `json:"permission"`
		Password      *string          `json:"password"`
		FunnelEnabled *bool            `json:"funnel_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	var updated *SharedFolderLink
	for _, s := range h.state.Shares {
		if s.ID == shareID && s.OwnerID == claims.UserID {
			if req.Title != nil && strings.TrimSpace(*req.Title) != "" {
				s.Title = strings.TrimSpace(*req.Title)
			}
			if req.Permission != nil && isValidSharePerm(*req.Permission) {
				s.Permission = *req.Permission
			}
			if req.Password != nil && strings.TrimSpace(*req.Password) != "" {
				s.Password = strings.TrimSpace(*req.Password)
			}
			if req.FunnelEnabled != nil {
				s.FunnelEnabled = *req.FunnelEnabled
				if s.FunnelEnabled && h.state.FunnelDNSName != "" {
					s.FunnelURL = fmt.Sprintf("https://%s/share/%s", h.state.FunnelDNSName, s.ID)
				} else {
					s.FunnelURL = ""
				}
			}
			s.UpdatedAt = time.Now().UTC()
			updated = s
			break
		}
	}
	if updated != nil {
		h.saveStateLocked()
	}
	h.mu.Unlock()

	if updated == nil {
		http.Error(w, `{"error":"share not found"}`, http.StatusNotFound)
		return
	}

	go h.syncHostTailscaleFunnel()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

// DELETE /v1/file-shares/{shareId}
func (h *PublicShareHandler) DeleteShare(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	shareID := chi.URLParam(r, "shareId")
	h.mu.Lock()
	filtered := make([]*SharedFolderLink, 0, len(h.state.Shares))
	found := false
	for _, s := range h.state.Shares {
		if s.ID == shareID && s.OwnerID == claims.UserID {
			found = true
			continue
		}
		filtered = append(filtered, s)
	}
	if found {
		h.state.Shares = filtered
		h.saveStateLocked()
	}
	h.mu.Unlock()

	if !found {
		http.Error(w, `{"error":"share not found"}`, http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─── 2. Public Visitor Endpoints (Password Gate + Scoped File Access) ───────

// GET /v1/public-shares/{shareId}/info
// Returns minimal metadata before password verification. Never exposes file listings or internal paths.
func (h *PublicShareHandler) GetPublicShareInfo(w http.ResponseWriter, r *http.Request) {
	shareID := chi.URLParam(r, "shareId")

	h.mu.RLock()
	var found *SharedFolderLink
	for _, s := range h.state.Shares {
		if s.ID == shareID {
			found = s
			break
		}
	}
	h.mu.RUnlock()

	if found == nil {
		http.Error(w, `{"error":"Este enlace compartido no existe o fue revocado."}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":                found.ID,
		"title":             found.Title,
		"node_name":         found.NodeName,
		"requires_password": true,
	})
}

// POST /v1/public-shares/{shareId}/unlock
func (h *PublicShareHandler) UnlockPublicShare(w http.ResponseWriter, r *http.Request) {
	shareID := chi.URLParam(r, "shareId")

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	h.mu.RLock()
	var found *SharedFolderLink
	for _, s := range h.state.Shares {
		if s.ID == shareID {
			found = s
			break
		}
	}
	h.mu.RUnlock()

	if found == nil {
		http.Error(w, `{"error":"Enlace no encontrado"}`, http.StatusNotFound)
		return
	}

	inputPwd := strings.TrimSpace(req.Password)
	if subtle.ConstantTimeCompare([]byte(inputPwd), []byte(found.Password)) != 1 {
		http.Error(w, `{"error":"Contraseña incorrecta"}`, http.StatusUnauthorized)
		return
	}

	token, err := h.issueShareToken(found)
	if err != nil {
		http.Error(w, `{"error":"failed to issue session token"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token": token,
		"share": map[string]any{
			"id":         found.ID,
			"title":      found.Title,
			"node_name":  found.NodeName,
			"permission": found.Permission,
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
		http.Error(w, `{"error":"Se requiere contraseña para acceder a este recurso"}`, http.StatusUnauthorized)
		return nil, nil, false
	}

	share, err := h.verifyShareToken(token, shareID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusUnauthorized)
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
		http.Error(w, `{"error":"No tienes permisos suficientes en esta carpeta compartida"}`, http.StatusForbidden)
		return nil, nil, false
	}

	targetNode, err := h.nodeSvc.GetByID(share.NodeID)
	if err != nil || targetNode == nil {
		http.Error(w, `{"error":"El nodo de almacenamiento no está disponible"}`, http.StatusBadGateway)
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

	reqSubPath := r.URL.Query().Get("path")
	realNodePath, cleanVisitorPath, err := resolveJailedPath(share.FolderPath, reqSubPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusForbidden)
		return
	}

	rec := httptest.NewRecorder()
	delegatedReq := injectNodeAndPath(r, targetNode, realNodePath)
	h.fileHandler.ListFiles(rec, delegatedReq)

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
		http.Error(w, `{"error":"failed to parse directory listing"}`, http.StatusInternalServerError)
		return
	}

	// Rewrite entry paths relative to share.FolderPath so internal parent paths are never leaked
	cleanRoot := filepath.Clean("/" + share.FolderPath)
	relEntries := make([]NodeFileEntry, 0, len(rawResp.Entries))
	for _, e := range rawResp.Entries {
		eClean := filepath.Clean("/" + e.Path)
		if cleanRoot != "/" && strings.HasPrefix(eClean, cleanRoot) {
			eClean = strings.TrimPrefix(eClean, cleanRoot)
			if eClean == "" {
				eClean = "/"
			}
		}
		e.Path = filepath.Clean("/" + eClean)
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

	reqSubPath := r.URL.Query().Get("path")
	realNodePath, _, err := resolveJailedPath(share.FolderPath, reqSubPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusForbidden)
		return
	}

	delegatedReq := injectNodeAndPath(r, targetNode, realNodePath)
	h.fileHandler.DownloadFile(w, delegatedReq)
}

// GET /v1/public-shares/{shareId}/stream?path=/...
func (h *PublicShareHandler) PublicStreamFile(w http.ResponseWriter, r *http.Request) {
	share, targetNode, ok := h.authorizePublicShareRequest(w, r, SharePermRead, SharePermWrite, SharePermEditor)
	if !ok {
		return
	}

	reqSubPath := r.URL.Query().Get("path")
	realNodePath, _, err := resolveJailedPath(share.FolderPath, reqSubPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusForbidden)
		return
	}

	delegatedReq := injectNodeAndPath(r, targetNode, realNodePath)
	h.fileHandler.StreamFile(w, delegatedReq)
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
		http.Error(w, `{"error":"path is required"}`, http.StatusBadRequest)
		return
	}

	realNodePath, _, err := resolveJailedPath(share.FolderPath, req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusForbidden)
		return
	}

	newBody, _ := json.Marshal(map[string]string{"path": realNodePath})
	delegatedReq := injectNodeAndPath(r, targetNode, realNodePath)
	delegatedReq.Body = ioNopCloser(newBody)
	h.fileHandler.MakeDirectory(w, delegatedReq)
}

// POST /v1/public-shares/{shareId}/upload (Allowed for "write" and "editor")
func (h *PublicShareHandler) PublicUploadFile(w http.ResponseWriter, r *http.Request) {
	share, targetNode, ok := h.authorizePublicShareRequest(w, r, SharePermWrite, SharePermEditor)
	if !ok {
		return
	}

	reqSubPath := r.URL.Query().Get("path")
	realNodePath, _, err := resolveJailedPath(share.FolderPath, reqSubPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusForbidden)
		return
	}

	delegatedReq := injectNodeAndPath(r, targetNode, realNodePath)
	h.fileHandler.UploadFile(w, delegatedReq)
}

// DELETE /v1/public-shares/{shareId}/files?path=/... (Allowed ONLY for "editor")
func (h *PublicShareHandler) PublicDeleteFile(w http.ResponseWriter, r *http.Request) {
	share, targetNode, ok := h.authorizePublicShareRequest(w, r, SharePermEditor)
	if !ok {
		return
	}

	reqSubPath := r.URL.Query().Get("path")
	if strings.TrimSpace(reqSubPath) == "" || filepath.Clean("/"+reqSubPath) == "/" {
		http.Error(w, `{"error":"No se puede eliminar la carpeta raíz compartida"}`, http.StatusForbidden)
		return
	}

	realNodePath, _, err := resolveJailedPath(share.FolderPath, reqSubPath)
	if err != nil || realNodePath == filepath.Clean("/"+share.FolderPath) {
		http.Error(w, `{"error":"Acceso denegado"}`, http.StatusForbidden)
		return
	}

	delegatedReq := injectNodeAndPath(r, targetNode, realNodePath)
	h.fileHandler.DeleteFile(w, delegatedReq)
}

func ioNopCloser(b []byte) *readCloserBuffer {
	return &readCloserBuffer{Reader: bytes.NewReader(b)}
}

type readCloserBuffer struct {
	*bytes.Reader
}

func (r *readCloserBuffer) Close() error { return nil }

// ─── 3. Tailscale Funnel Multi-Node Container & Share Gateway ───────────────

var funnelPortToInternalBind = map[int]string{
	443:   "127.0.0.1:18443",
	8443:  "127.0.0.1:18444",
	10000: "127.0.0.1:18445",
}

func (h *PublicShareHandler) startFunnelReverseProxies() {
	for funnelPort, bindAddr := range funnelPortToInternalBind {
		fp := funnelPort
		addr := bindAddr
		rp := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				upstream := h.resolveFunnelUpstream(fp, req.URL.Path)
				if upstream == "" {
					req.URL.Scheme = "http"
					req.URL.Host = "127.0.0.1:9"
					return
				}
				if !strings.HasPrefix(upstream, "http://") && !strings.HasPrefix(upstream, "https://") {
					upstream = "http://" + upstream
				}
				u, err := url.Parse(upstream)
				if err != nil {
					return
				}
				req.URL.Scheme = u.Scheme
				req.URL.Host = u.Host
				req.Header.Set("X-Forwarded-Proto", "https")
			},
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 0,
			},
			FlushInterval: -1,
		}

		srv := &http.Server{Addr: addr, Handler: rp}
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[WARN] funnel proxy on %s exited: %v", addr, err)
			}
		}()
	}
}

func (h *PublicShareHandler) resolveFunnelUpstream(funnelPort int, reqPath string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Check specific path prefix first, then "/" catch-all for that funnelPort
	var rootMatch string
	for _, rt := range h.state.FunnelRoutes {
		if rt.FunnelPort != funnelPort {
			continue
		}
		if rt.PathPrefix != "" && rt.PathPrefix != "/" && strings.HasPrefix(reqPath, rt.PathPrefix) {
			return rt.Upstream
		}
		if rt.PathPrefix == "" || rt.PathPrefix == "/" {
			rootMatch = rt.Upstream
		}
	}
	return rootMatch
}

func (h *PublicShareHandler) detectTailscaleDNSName() string {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err == nil {
		var st struct {
			Self struct {
				DNSName string `json:"DNSName"`
			} `json:"Self"`
		}
		if json.Unmarshal(out, &st) == nil && st.Self.DNSName != "" {
			return strings.TrimSuffix(st.Self.DNSName, ".")
		}
	}
	return ""
}

// syncHostTailscaleFunnel writes a root-owned custom command script to /var/lib/autohost/commands/funnel_sync.sh
// and dispatches it to the local Master agent so `tailscale funnel` runs natively on the host OS.
func (h *PublicShareHandler) syncHostTailscaleFunnel() {
	h.mu.RLock()
	routes := append([]*TailscaleFunnelRoute{}, h.state.FunnelRoutes...)
	shares := append([]*SharedFolderLink{}, h.state.Shares...)
	h.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("#!/usr/bin/env bash\nset -e\n")
	sb.WriteString("TS_BIN=$(command -v tailscale || echo /usr/bin/tailscale)\n")
	sb.WriteString("if [ ! -x \"$TS_BIN\" ]; then exit 0; fi\n")

	// Expose /share/ path on port 443 to the web frontend (port 3000) if any share has funnel_enabled
	hasFunnelShare := false
	for _, s := range shares {
		if s.FunnelEnabled {
			hasFunnelShare = true
			break
		}
	}
	if hasFunnelShare {
		sb.WriteString("$TS_BIN funnel --bg --https=443 --set-path=/share http://127.0.0.1:3000/share || true\n")
		sb.WriteString("$TS_BIN funnel --bg --https=443 --set-path=/api/public-shares http://127.0.0.1:3000/api/public-shares || true\n")
	}

	// Expose each container route over Tailscale Funnel
	for _, rt := range routes {
		pathArg := ""
		if rt.PathPrefix != "" && rt.PathPrefix != "/" {
			pathArg = fmt.Sprintf("--set-path=%s ", rt.PathPrefix)
		}
		sb.WriteString(fmt.Sprintf(
			"$TS_BIN funnel --bg --https=%d %shttp://%s || true\n",
			rt.FunnelPort,
			pathArg,
			rt.Upstream,
		))
	}

	commandsDir := filepath.Join(platform.StorageDir(), "commands")
	_ = os.MkdirAll(commandsDir, 0755)
	scriptPath := filepath.Join(commandsDir, "funnel_sync.sh")
	if err := os.WriteFile(scriptPath, []byte(sb.String()), 0700); err != nil {
		log.Printf("[WARN] write funnel_sync.sh: %v", err)
		return
	}

	if h.nodeSvc != nil && h.jobService != nil && h.dispatcher != nil {
		if masterNode, err := h.nodeSvc.FindLocalNode(); err == nil && masterNode != nil {
			params, _ := json.Marshal(map[string]string{"script_path": scriptPath})
			if j, err := h.jobService.DispatchWithParams(masterNode.ID, "script.exec", jobs.CommandTypeDefault, string(params)); err == nil {
				_ = h.dispatcher.DispatchJobWithParams(masterNode.ID, j.ID, "script.exec", jobs.CommandTypeDefault, string(params))
			}
		}
	}
}

// GET /v1/tailscale-funnel/status
func (h *PublicShareHandler) GetFunnelStatus(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	h.mu.RLock()
	dnsName := h.state.FunnelDNSName
	routes := append([]*TailscaleFunnelRoute{}, h.state.FunnelRoutes...)
	h.mu.RUnlock()

	if dnsName == "" {
		if detected := h.detectTailscaleDNSName(); detected != "" {
			dnsName = detected
			h.mu.Lock()
			h.state.FunnelDNSName = detected
			h.saveStateLocked()
			h.mu.Unlock()
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
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req struct {
		FunnelDNSName string `json:"funnel_dns_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	cleanDNS := strings.ToLower(strings.TrimSpace(req.FunnelDNSName))
	cleanDNS = strings.TrimPrefix(cleanDNS, "https://")
	cleanDNS = strings.TrimPrefix(cleanDNS, "http://")
	cleanDNS = strings.TrimSuffix(cleanDNS, "/")
	cleanDNS = strings.TrimSuffix(cleanDNS, ".")

	h.mu.Lock()
	h.state.FunnelDNSName = cleanDNS
	// Update FunnelURL on existing funnel shares and routes
	for _, s := range h.state.Shares {
		if s.FunnelEnabled && cleanDNS != "" {
			s.FunnelURL = fmt.Sprintf("https://%s/share/%s", cleanDNS, s.ID)
		}
	}
	for _, rt := range h.state.FunnelRoutes {
		rt.PublicURL = buildFunnelPublicURL(cleanDNS, rt.FunnelPort, rt.PathPrefix)
	}
	h.saveStateLocked()
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"funnel_dns_name": cleanDNS})
}

func buildFunnelPublicURL(dnsName string, funnelPort int, pathPrefix string) string {
	host := dnsName
	if host == "" {
		host = "tu-nodo.tailnet.ts.net"
	}
	portSuffix := ""
	if funnelPort != 443 {
		portSuffix = fmt.Sprintf(":%d", funnelPort)
	}
	cleanPrefix := "/" + strings.TrimPrefix(strings.TrimSpace(pathPrefix), "/")
	if cleanPrefix == "/" {
		cleanPrefix = ""
	}
	return fmt.Sprintf("https://%s%s%s", host, portSuffix, cleanPrefix)
}

// POST /v1/tailscale-funnel/routes
func (h *PublicShareHandler) UpsertFunnelRoute(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
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
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.FunnelPort != 443 && req.FunnelPort != 8443 && req.FunnelPort != 10000 {
		req.FunnelPort = 443
	}
	if req.ContainerPort <= 0 || req.ContainerPort > 65535 {
		http.Error(w, `{"error":"invalid container_port"}`, http.StatusBadRequest)
		return
	}

	targetNode, err := h.nodeSvc.GetByID(req.TargetNodeID)
	if err != nil || targetNode == nil {
		http.Error(w, `{"error":"target node not found"}`, http.StatusNotFound)
		return
	}
	if targetNode.OwnerID != nil && *targetNode.OwnerID != claims.UserID {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}

	targetIP := ""
	if targetNode.IPVPN != nil && strings.TrimSpace(*targetNode.IPVPN) != "" {
		targetIP = strings.TrimSpace(*targetNode.IPVPN)
	} else if targetNode.IsLocal {
		targetIP = "127.0.0.1"
	} else if strings.TrimSpace(targetNode.IPLocal) != "" {
		targetIP = strings.TrimSpace(targetNode.IPLocal)
	}
	if idx := strings.IndexByte(targetIP, '/'); idx != -1 {
		targetIP = targetIP[:idx]
	}

	cleanPrefix := "/" + strings.TrimPrefix(strings.TrimSpace(req.PathPrefix), "/")
	upstream := fmt.Sprintf("%s:%d", targetIP, req.ContainerPort)

	h.mu.Lock()
	dnsName := h.state.FunnelDNSName
	route := &TailscaleFunnelRoute{
		ID:             generateShortID("fn_"),
		FunnelPort:     req.FunnelPort,
		PathPrefix:     cleanPrefix,
		PublicURL:      buildFunnelPublicURL(dnsName, req.FunnelPort, cleanPrefix),
		TargetNodeID:   targetNode.ID,
		TargetNodeName: targetNode.Hostname,
		TargetIP:       targetIP,
		ContainerPort:  req.ContainerPort,
		Upstream:       upstream,
		ContainerID:    strings.TrimSpace(req.ContainerID),
		ContainerName:  strings.TrimSpace(req.ContainerName),
		ContainerImage: strings.TrimSpace(req.ContainerImage),
		CreatedAt:      time.Now().UTC(),
	}

	// Replace existing route on same FunnelPort + PathPrefix
	replaced := false
	for i, existing := range h.state.FunnelRoutes {
		if existing.FunnelPort == req.FunnelPort && existing.PathPrefix == cleanPrefix {
			route.ID = existing.ID
			h.state.FunnelRoutes[i] = route
			replaced = true
			break
		}
	}
	if !replaced {
		h.state.FunnelRoutes = append(h.state.FunnelRoutes, route)
	}
	h.saveStateLocked()
	h.mu.Unlock()

	go h.syncHostTailscaleFunnel()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(route)
}

// DELETE /v1/tailscale-funnel/routes/{routeId}
func (h *PublicShareHandler) DeleteFunnelRoute(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	routeID := chi.URLParam(r, "routeId")
	h.mu.Lock()
	filtered := make([]*TailscaleFunnelRoute, 0, len(h.state.FunnelRoutes))
	for _, rt := range h.state.FunnelRoutes {
		if rt.ID != routeID {
			filtered = append(filtered, rt)
		}
	}
	h.state.FunnelRoutes = filtered
	h.saveStateLocked()
	h.mu.Unlock()

	go h.syncHostTailscaleFunnel()

	w.WriteHeader(http.StatusNoContent)
}
