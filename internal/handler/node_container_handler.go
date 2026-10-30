// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// NodeContainerHandler streams real-time Docker container list snapshots from a
// node to the browser via Server-Sent Events (SSE) and serves metric history.
type NodeContainerHandler struct {
	grpc               *grpcserver.NodeAgentServer
	nodeSvc            *node.NodeService
	containerMetricSvc *node.ContainerMetricService
	settingSvc         *node.ContainerSettingService
	redis              *redis.Client
}

func NewNodeContainerHandler(
	grpc *grpcserver.NodeAgentServer,
	nodeSvc *node.NodeService,
	containerMetricSvc *node.ContainerMetricService,
	settingSvc *node.ContainerSettingService,
	redis *redis.Client,
) *NodeContainerHandler {
	return &NodeContainerHandler{
		grpc:               grpc,
		nodeSvc:            nodeSvc,
		containerMetricSvc: containerMetricSvc,
		settingSvc:         settingSvc,
		redis:              redis,
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

	log.Printf("[SSE] container stream started for node %s", nodeID)

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	// 1. Decoupled streaming via Redis Pub/Sub (supports distributed Cloud Run + VM gRPC)
	if h.redis != nil {
		isOnline := false
		if val, err := h.redis.Exists(r.Context(), fmt.Sprintf("nodes:%s:online", nodeID)).Result(); err == nil && val > 0 {
			isOnline = true
		} else if h.grpc != nil && h.grpc.IsNodeConnected(nodeID) {
			isOnline = true
		}

		if !isOnline {
			fmt.Fprintf(w, "event: server-error\ndata: {\"message\":\"node not connected via gRPC — is the agent running?\"}\n\n")
			flusher.Flush()
		}

		// Send cached snapshot immediately if available so UI doesn't flicker empty
		if cached, err := h.redis.Get(r.Context(), fmt.Sprintf("nodes:%s:containers:latest", nodeID)).Result(); err == nil && cached != "" {
			fmt.Fprintf(w, "event: containers\ndata: %s\n\n", cached)
			flusher.Flush()
		}

		// Subscribe to container updates for this node
		pubsub := h.redis.Subscribe(r.Context(), fmt.Sprintf("nodes:%s:containers", nodeID))
		defer pubsub.Close()
		redisCh := pubsub.Channel()

		// Request gRPC server to start streaming containers for this node
		_ = h.redis.Publish(r.Context(), fmt.Sprintf("nodes:%s:containers:cmd", nodeID), "start").Err()
		defer func() {
			_ = h.redis.Publish(context.Background(), fmt.Sprintf("nodes:%s:containers:cmd", nodeID), "stop").Err()
		}()

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

			case msg, ok := <-redisCh:
				if !ok {
					return
				}
				if _, err := fmt.Fprintf(w, "event: containers\ndata: %s\n\n", msg.Payload); err != nil {
					log.Printf("[SSE] container stream write error for node %s: %v", nodeID, err)
					return
				}
				flusher.Flush()
			}
		}
	}

	// 2. In-memory broker fallback (local dev / monolith mode)
	if h.grpc != nil {
		ch, cancel := h.grpc.SubscribeToContainers(nodeID)
		defer cancel()

		if !h.grpc.IsNodeConnected(nodeID) {
			fmt.Fprintf(w, "event: server-error\ndata: {\"message\":\"node not connected via gRPC — is the agent running?\"}\n\n")
			flusher.Flush()
		}

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

	points, err := h.containerMetricSvc.GetHistory(node.ContainerHistoryParams{
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
		points = []*node.ContainerMetricHistoryPoint{}
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
