package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// NodeContainerHandler streams real-time Docker container list snapshots from a
// node to the browser via Server-Sent Events (SSE).
type NodeContainerHandler struct {
	grpc    *grpcserver.NodeAgentServer
	nodeSvc *node.Service
}

func NewNodeContainerHandler(grpc *grpcserver.NodeAgentServer, nodeSvc *node.Service) *NodeContainerHandler {
	return &NodeContainerHandler{grpc: grpc, nodeSvc: nodeSvc}
}

func (h *NodeContainerHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Get("/{nodeId}/stream", h.streamContainers)
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
