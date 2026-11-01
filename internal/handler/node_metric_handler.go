// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// alertCooldown is the minimum time between repeated alerts for the same
// node+metric combination.
const alertCooldown = 10 * time.Minute

type NodeMetricHandler struct {
	service       *node.NodeMetricService
	alertService  *node.NodeAlertService
	nodeService   *node.NodeService
	grpc          *grpcserver.NodeAgentServer
	alertEventSvc *node.AlertEventService
	redis         *redis.Client

	cooldownMu sync.Mutex
	cooldowns  map[string]time.Time // key: "nodeID:metricName"
}

func NewNodeMetricHandler(
	service *node.NodeMetricService,
	alertService *node.NodeAlertService,
	nodeService *node.NodeService,
	grpc *grpcserver.NodeAgentServer,
	alertEventSvc *node.AlertEventService,
	redis *redis.Client,
) *NodeMetricHandler {
	return &NodeMetricHandler{
		service:       service,
		alertService:  alertService,
		nodeService:   nodeService,
		grpc:          grpc,
		alertEventSvc: alertEventSvc,
		redis:         redis,
		cooldowns:     make(map[string]time.Time),
	}
}

func (h *NodeMetricHandler) Routes(nodeMetricMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	r.Group(func(protected chi.Router) {
		protected.Use(nodeMetricMiddleware)
		protected.Post("/metrics", h.PostMetrics)
	})

	// User-authenticated endpoints
	r.With(middleware.Auth).Get("/{nodeId}/history", h.GetHistory)
	r.With(middleware.Auth).Get("/{nodeId}/live", h.StreamLiveMetrics)

	return r
}

func (h *NodeMetricHandler) PostMetrics(w http.ResponseWriter, r *http.Request) {

	// El node_id viene del token validado, no del body
	nodeToken := middleware.GetNodeToken(r.Context())

	if nodeToken == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var nodeMetrics node.CreateNodeMetricRequest
	if err := json.NewDecoder(r.Body).Decode(&nodeMetrics); err != nil {
		fmt.Println("Error decoding JSON:", &nodeMetrics)
		http.Error(w, "bad json", http.StatusBadRequest)
		fmt.Println(err)
		return
	}

	nodeMetrics.NodeID = nodeToken.NodeID
	nodeMetrics.CollectedAt = time.Now()

	// Usar el servicio para guardar las métricas
	if _, err := h.service.StoreNodeMetric(&nodeMetrics); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Fire threshold alerts asynchronously - never block the metric ingestion path.
	go h.checkThresholdAlerts(nodeToken.NodeID, &nodeMetrics)

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// checkThresholdAlerts looks up the alert config for the node and sends a
// Telegram notification for each metric that exceeds its configured threshold.
// Repeated alerts for the same metric are suppressed for alertCooldown duration.
func (h *NodeMetricHandler) checkThresholdAlerts(nodeID string, m *node.CreateNodeMetricRequest) {
	cfg, err := h.alertService.FindByNodeID(nodeID)
	if err != nil {
		return // no config or DB error — silently skip
	}
	if cfg.TelegramBotToken == "" || cfg.TelegramChatID == "" {
		return // Telegram not configured
	}

	// Resolve the human-readable hostname; fall back to the UUID if unavailable.
	nodeName := nodeID
	if h.nodeService != nil {
		if n, err := h.nodeService.GetByID(nodeID); err == nil {
			nodeName = n.Hostname
		}
	}

	check := func(name string, value float64, threshold *float32, evType node.AlertEventType) {
		if threshold == nil {
			return
		}
		if value < float64(*threshold) {
			return
		}

		key := nodeID + ":" + name
		now := time.Now()
		h.cooldownMu.Lock()
		last, exists := h.cooldowns[key]
		if exists && now.Before(last.Add(alertCooldown)) {
			h.cooldownMu.Unlock()
			return // still within cooldown window
		}
		h.cooldowns[key] = now
		h.cooldownMu.Unlock()

		msg := fmt.Sprintf("⚠️ <b>AutoHost Alert</b>\nNode: <b>%s</b> <code>%s</code>\n%s usage: <b>%.1f%%</b> (threshold: %.1f%%)", nodeName, nodeID, name, value, *threshold)
		if err := platform.SendTelegramMessage(cfg.TelegramBotToken, cfg.TelegramChatID, msg); err != nil {
			log.Printf("[alert] telegram send for node %s (%s): %v", nodeID, name, err)
		}

		// Persist so the event appears in the cloud UI.
		if h.alertEventSvc != nil {
			var userID *string
			if n, err := h.nodeService.GetByID(nodeID); err == nil && n.OwnerID != nil {
				userID = n.OwnerID
			}
			ev := &node.AlertEvent{
				UserID:    userID,
				NodeID:    nodeID,
				AlertType: evType,
				Message:   msg,
			}
			if _, err := h.alertEventSvc.Create(ev); err != nil {
				log.Printf("[alert] persist threshold alert event for node %s (%s): %v", nodeID, name, err)
			}
		}
	}

	check("CPU", m.CPUUsagePercent, cfg.CPUThreshold, node.AlertEventTypeCPU)
	check("RAM", m.MemoryUsagePercent, cfg.RAMThreshold, node.AlertEventTypeRAM)
	check("Disk", m.DiskUsagePercent, cfg.DiskThreshold, node.AlertEventTypeDisk)
}

// GetHistory returns time-bucketed metric history for a node.
// Query params: from, to (RFC3339), bucket (minute|hour|day, default hour).
// Defaults: last 7 days, hourly buckets.
func (h *NodeMetricHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")

	// Verify the node belongs to the authenticated user.
	n, err := h.nodeService.GetByID(nodeID)
	if err != nil || n == nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if n.OwnerID == nil || *n.OwnerID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	now := time.Now().UTC()
	toTime := now
	fromTime := now.AddDate(0, 0, -7)

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

	points, err := h.service.GetHistory(node.HistoryParams{
		NodeID: nodeID,
		From:   fromTime,
		To:     toTime,
		Bucket: bucket,
	})
	if err != nil {
		http.Error(w, "could not fetch metrics history", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(points)
}

// StreamLiveMetrics streams real-time metric updates from the node to the
// browser via Server-Sent Events.  Requires a valid user JWT.
func (h *NodeMetricHandler) StreamLiveMetrics(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")

	// Verify the node belongs to the authenticated user.
	n, err := h.nodeService.GetByID(nodeID)
	if err != nil || n == nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	if n.OwnerID == nil || *n.OwnerID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// SSE headers.
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

	log.Printf("[SSE] live metrics stream started for node %s", nodeID)

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Keepalive ticker so the browser knows the connection is alive.
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	// 1. If Redis is available, subscribe to Redis Pub/Sub (supports distributed Cloud Run + VM gRPC).
	if h.redis != nil {
		channel := fmt.Sprintf("nodes:%s:metrics", nodeID)
		pubsub := h.redis.Subscribe(r.Context(), channel)
		defer pubsub.Close()
		redisCh := pubsub.Channel()

		for {
			select {
			case <-r.Context().Done():
				log.Printf("[SSE] live metrics stream closed for node %s", nodeID)
				return

			case <-keepalive.C:
				fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()

			case msg, ok := <-redisCh:
				if !ok {
					return
				}
				fmt.Fprintf(w, "event: metric\ndata: %s\n\n", msg.Payload)
				flusher.Flush()
			}
		}
	}

	// 2. Fallback to in-memory broker (monolith local mode without Redis).
	if h.grpc != nil {
		ch, cancel := h.grpc.SubscribeToNodeMetrics(nodeID)
		defer cancel()

		type liveMetric struct {
			CPUUsagePercent    float32 `json:"cpu_usage_percent"`
			RAMTotalBytes      uint64  `json:"ram_total_bytes"`
			RAMUsedBytes       uint64  `json:"ram_used_bytes"`
			RAMAvailableBytes  uint64  `json:"ram_available_bytes"`
			RAMUsagePercent    float32 `json:"ram_usage_percent"`
			DiskTotalBytes     uint64  `json:"disk_total_bytes"`
			DiskUsedBytes      uint64  `json:"disk_used_bytes"`
			DiskAvailableBytes uint64  `json:"disk_available_bytes"`
			DiskUsagePercent   float32 `json:"disk_usage_percent"`
			UptimeSeconds      int64   `json:"uptime_seconds"`
			CPUTempCelsius     float32 `json:"cpu_temp_celsius,omitempty"`
			CollectedAt        string  `json:"collected_at"`
		}

		for {
			select {
			case <-r.Context().Done():
				log.Printf("[SSE] live metrics stream closed for node %s", nodeID)
				return

			case <-keepalive.C:
				fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()

			case m, ok := <-ch:
				if !ok {
					return
				}
				payload := liveMetric{
					CPUUsagePercent:    m.GetCpuUsagePercent(),
					RAMTotalBytes:      m.GetRamTotalBytes(),
					RAMUsedBytes:       m.GetRamUsedBytes(),
					RAMAvailableBytes:  m.GetRamAvailableBytes(),
					RAMUsagePercent:    m.GetRamUsagePercent(),
					DiskTotalBytes:     m.GetDiskTotalBytes(),
					DiskUsedBytes:      m.GetDiskUsedBytes(),
					DiskAvailableBytes: m.GetDiskAvailableBytes(),
					DiskUsagePercent:   m.GetDiskUsagePercent(),
					UptimeSeconds:      m.GetUptimeSeconds(),
					CPUTempCelsius:     m.GetCpuTempCelsius(),
					CollectedAt:        time.Now().UTC().Format(time.RFC3339),
				}
				data, _ := json.Marshal(payload)
				fmt.Fprintf(w, "event: metric\ndata: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}
