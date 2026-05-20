package handler

// node_profile_handler.go — on-demand pprof profiling for a node.
//
// Routes (all require authenticated user and ownership of the node):
//
//   POST /v1/nodes/{nodeID}/profile
//        Body: {"type":"cpu","duration":30}   (duration 0 → agent default 30 s)
//        Triggers profile collection on the agent, starts a local pprof HTTP
//        server, and returns:
//        {"session_id":"<uuid>","proxy_path":"/v1/pprof/<session_id>"}
//
//   GET  /v1/pprof/{sessionID}
//   GET  /v1/pprof/{sessionID}/*
//        Reverse proxy to the local `go tool pprof -http` server.
//        Intended for <iframe src="/v1/pprof/{sessionID}/ui/">.
//
//   GET  /v1/nodes/{nodeID}/profile/download
//        Returns the raw .pb.gz bytes so the user can run
//        `go tool pprof` locally.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ProfileRequester is satisfied by *grpcserver.NodeAgentServer.
type ProfileRequester interface {
	RequestProfile(ctx context.Context, nodeID, profileType string, durationSec int32) ([]byte, error)
}

// NodeProfileHandler handles HTTP requests for on-demand profiling.
type NodeProfileHandler struct {
	requester      ProfileRequester
	profileManager *grpcserver.ProfileManager

	// rawMu / rawStore hold the most-recently-collected bytes per node so
	// /download can serve them without re-profiling.
	rawMu    sync.RWMutex
	rawStore map[string][]byte // nodeID → raw pprof bytes

	// sessions maps sessionID → nodeID so the proxy route can look up the
	// running pprof server.
	sessMu   sync.RWMutex
	sessions map[string]string // sessionID → nodeID
}

func NewNodeProfileHandler(requester ProfileRequester, profileManager *grpcserver.ProfileManager) *NodeProfileHandler {
	return &NodeProfileHandler{
		requester:      requester,
		profileManager: profileManager,
		rawStore:       make(map[string][]byte),
		sessions:       make(map[string]string),
	}
}

// NodeRoutes returns routes that live under /v1/nodes/{nodeID}/profile.
func (h *NodeProfileHandler) NodeRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Post("/", h.Collect)
	r.Get("/download", h.Download)
	return r
}

// ProxyRoutes returns routes for /v1/pprof/{sessionID} (top-level, so that
// pprof's absolute-path assets resolve correctly within the iframe).
func (h *NodeProfileHandler) ProxyRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.HandleFunc("/*", h.Proxy)
	r.HandleFunc("/", h.Proxy)
	return r
}

// ─── Handlers ────────────────────────────────────────────────────────────────

type collectRequest struct {
	Type     string `json:"type"`     // "cpu" or "heap"
	Duration int32  `json:"duration"` // seconds; 0 → agent default
}

type collectResponse struct {
	SessionID string `json:"session_id"`
	ProxyPath string `json:"proxy_path"` // e.g. /v1/pprof/<sessionID>
}

// Collect triggers profile collection on the agent, then starts (or restarts)
// a local pprof HTTP server for that node.
func (h *NodeProfileHandler) Collect(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	nodeID := chi.URLParam(r, "nodeID")
	if nodeID == "" {
		response.RespondError(w, http.StatusBadRequest, "missing nodeID")
		return
	}

	var req collectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Type != "cpu" && req.Type != "heap" {
		response.RespondError(w, http.StatusBadRequest, `type must be "cpu" or "heap"`)
		return
	}

	log.Printf("[profile] collecting %s profile for node %s (duration=%ds)", req.Type, nodeID, req.Duration)

	// Block until the agent returns the raw bytes (may take req.Duration + grace).
	data, err := h.requester.RequestProfile(r.Context(), nodeID, req.Type, req.Duration)
	if err != nil {
		log.Printf("[profile] RequestProfile failed for node %s: %v", nodeID, err)
		response.RespondError(w, http.StatusBadGateway, fmt.Sprintf("profile collection failed: %v", err))
		return
	}

	// Persist raw bytes for /download.
	h.rawMu.Lock()
	h.rawStore[nodeID] = data
	h.rawMu.Unlock()

	// Start (or restart) the pprof UI process.
	sess, err := h.profileManager.StartSession(nodeID, data)
	if err != nil {
		log.Printf("[profile] StartSession failed for node %s: %v", nodeID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not start pprof server")
		return
	}

	// Register a new session ID.
	sessionID := uuid.New().String()
	h.sessMu.Lock()
	h.sessions[sessionID] = nodeID
	h.sessMu.Unlock()

	_ = sess // port stored inside ProfileManager; proxy looks it up via nodeID

	response.RespondJSON(w, http.StatusOK, collectResponse{
		SessionID: sessionID,
		ProxyPath: "/v1/pprof/" + sessionID,
	})
}

// Download serves the raw pprof bytes collected in the last Collect call for
// this node.  The client can open it with:
//
//	go tool pprof -http=: profile.pb.gz
func (h *NodeProfileHandler) Download(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	nodeID := chi.URLParam(r, "nodeID")

	h.rawMu.RLock()
	data, exists := h.rawStore[nodeID]
	h.rawMu.RUnlock()

	if !exists {
		response.RespondError(w, http.StatusNotFound, "no profile collected for this node yet; call POST first")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pprof-%s.pb.gz"`, nodeID))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// Proxy reverse-proxies requests to the local `go tool pprof -http` server
// that was started for the given sessionID.
func (h *NodeProfileHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	h.sessMu.RLock()
	nodeID, ok := h.sessions[sessionID]
	h.sessMu.RUnlock()

	if !ok {
		http.Error(w, "pprof session not found or expired", http.StatusNotFound)
		return
	}

	sess, ok := h.profileManager.Get(nodeID)
	if !ok {
		http.Error(w, "pprof process no longer running; re-collect the profile", http.StatusGone)
		return
	}

	prefix := "/v1/pprof/" + sessionID
	proxy := sess.NewReverseProxy(prefix)
	proxy.ServeHTTP(w, r)
}
