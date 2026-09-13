package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	containermetric "github.com/arturo/autohost-cloud-api/internal/domain/container_metric"
	containersetting "github.com/arturo/autohost-cloud-api/internal/domain/container_setting"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// NodeContainerHandler streams real-time Docker container list snapshots from a
// node to the browser via Server-Sent Events (SSE) and serves metric history.
type NodeContainerHandler struct {
	grpc               *grpcserver.NodeAgentServer
	nodeSvc            *node.Service
	containerMetricSvc *containermetric.Service
	settingSvc         *containersetting.Service
}

func NewNodeContainerHandler(
	grpc *grpcserver.NodeAgentServer,
	nodeSvc *node.Service,
	containerMetricSvc *containermetric.Service,
	settingSvc *containersetting.Service,
) *NodeContainerHandler {
	return &NodeContainerHandler{
		grpc:               grpc,
		nodeSvc:            nodeSvc,
		containerMetricSvc: containerMetricSvc,
		settingSvc:         settingSvc,
	}
}

func (h *NodeContainerHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Get("/{nodeId}/stream", h.streamContainers)
		protected.Get("/{nodeId}/metrics", h.getContainerMetrics)
		protected.Get("/{nodeId}/settings", h.getContainerSettings)
		protected.Put("/{nodeId}/settings", h.updateContainerSettings)
	})
	return r
}

// sseContainerInfo is the JSON shape of one container in the SSE event.
type sseContainerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	CPU     string `json:"cpu,omitempty"`
	Mem     string `json:"mem,omitempty"`
	MemPerc string `json:"memPerc,omitempty"`
}

// sseContainerList is the top-level SSE data payload.
type sseContainerList struct {
	Containers []sseContainerInfo `json:"containers"`
	Error      string             `json:"error,omitempty"`
}

// streamContainers opens an SSE stream that fans out Docker container list
// snapshots pushed by the node agent whenever the container state changes.
func (h *NodeContainerHandler) streamContainers(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")

	// Verify the node belongs to the authenticated user.
	n, err := h.nodeSvc.GetByID(nodeID)
	if err != nil || n == nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if n.OwnerID == nil || *n.OwnerID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// SSE headers — disable proxy buffering.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Remove the server-wide WriteTimeout for this long-lived SSE connection.
	// Without this, net/http kills the stream after WriteTimeout (30 s).
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	// Subscribe — first subscriber causes StreamContainers to be sent to agent.
	ch, cancel := h.grpc.SubscribeToContainers(nodeID)
	defer cancel()

	log.Printf("[SSE] container stream started for node %s", nodeID)

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	if !h.grpc.IsNodeConnected(nodeID) {
		fmt.Fprintf(w, "event: server-error\ndata: {\"message\":\"node not connected via gRPC — is the agent running?\"}\n\n")
		flusher.Flush()
	}

	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			log.Printf("[SSE] container stream closed for node %s", nodeID)
			return

		case <-keepalive.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				log.Printf("[SSE] container stream client disconnected for node %s", nodeID)
				return
			}
			flusher.Flush()

		case snapshot, ok := <-ch:
			if !ok {
				return
			}
			list := sseContainerList{
				Error:      snapshot.GetError(),
				Containers: make([]sseContainerInfo, 0, len(snapshot.GetContainers())),
			}
			for _, c := range snapshot.GetContainers() {
				list.Containers = append(list.Containers, sseContainerInfo{
					ID:      c.GetId(),
					Name:    c.GetName(),
					Image:   c.GetImage(),
					State:   c.GetState(),
					Status:  c.GetStatus(),
					CPU:     c.GetCpu(),
					Mem:     c.GetMem(),
					MemPerc: c.GetMemPerc(),
				})
			}
			data, _ := json.Marshal(list)
			if _, err := fmt.Fprintf(w, "event: containers\ndata: %s\n\n", data); err != nil {
				log.Printf("[SSE] container stream write error for node %s: %v", nodeID, err)
				return
			}
			flusher.Flush()
		}
	}
}

// getContainerMetrics returns aggregated historical metrics for a specific container on a node.
func (h *NodeContainerHandler) getContainerMetrics(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	n, err := h.nodeSvc.GetByID(nodeID)
	if err != nil || n == nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if n.OwnerID == nil || *n.OwnerID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	containerName := r.URL.Query().Get("container_name")
	if containerName == "" {
		containerName = r.URL.Query().Get("name")
	}
	containerID := r.URL.Query().Get("container_id")
	if containerID == "" {
		containerID = r.URL.Query().Get("containerId")
	}

	if containerName == "" && containerID == "" {
		http.Error(w, "container_name or container_id parameter is required", http.StatusBadRequest)
		return
	}

	fromTime := time.Now().Add(-7 * 24 * time.Hour)
	toTime := time.Now()

	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			fromTime = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			toTime = t
		}
	}

	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "hour"
	}

	points, err := h.containerMetricSvc.GetHistory(containermetric.ContainerHistoryParams{
		NodeID:        nodeID,
		ContainerName: containerName,
		ContainerID:   containerID,
		From:          fromTime,
		To:            toTime,
		Bucket:        bucket,
	})
	if err != nil {
		log.Printf("[ERROR] fetch container metrics history: %v", err)
		http.Error(w, "could not fetch container metrics history", http.StatusInternalServerError)
		return
	}

	if points == nil {
		points = []*containermetric.ContainerMetricHistoryPoint{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(points)
}

type updateContainerSettingsRequest struct {
	SortOrder        []string        `json:"sort_order"`
	LockedContainers map[string]bool `json:"locked_containers"`
}

func (h *NodeContainerHandler) getContainerSettings(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	if h.nodeSvc != nil {
		n, err := h.nodeSvc.GetByID(nodeID)
		if err != nil || n == nil {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		if n.OwnerID == nil || *n.OwnerID != claims.UserID {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	if h.settingSvc == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	settings, err := h.settingSvc.GetByNodeID(r.Context(), nodeID)
	if err != nil {
		log.Printf("[ERROR] get container settings for node %s: %v", nodeID, err)
		http.Error(w, "could not fetch container settings", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

func (h *NodeContainerHandler) updateContainerSettings(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	if h.nodeSvc != nil {
		n, err := h.nodeSvc.GetByID(nodeID)
		if err != nil || n == nil {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		if n.OwnerID == nil || *n.OwnerID != claims.UserID {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	if h.settingSvc == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	var req updateContainerSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	saved, err := h.settingSvc.Save(r.Context(), nodeID, req.SortOrder, req.LockedContainers)
	if err != nil {
		log.Printf("[ERROR] update container settings for node %s: %v", nodeID, err)
		http.Error(w, "could not save container settings", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(saved)
}

