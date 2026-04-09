package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
)

// alertCooldown is the minimum time between repeated alerts for the same
// node+metric combination.
const alertCooldown = 10 * time.Minute

type NodeMetricHandler struct {
	service      *nodemetric.Service
	alertService *nodealert.Service
	nodeService  *node.Service

	cooldownMu sync.Mutex
	cooldowns  map[string]time.Time // key: "nodeID:metricName"
}

func NewNodeMetricHandler(service *nodemetric.Service, alertService *nodealert.Service, nodeService *node.Service) *NodeMetricHandler {
	return &NodeMetricHandler{
		service:      service,
		alertService: alertService,
		nodeService:  nodeService,
		cooldowns:    make(map[string]time.Time),
	}
}

func (h *NodeMetricHandler) Routes(nodeMetricMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	r.Group(func(protected chi.Router) {
		protected.Use(nodeMetricMiddleware)
		protected.Post("/metrics", h.PostMetrics)
	})

	// User-authenticated history endpoint
	r.With(middleware.Auth).Get("/{nodeId}/history", h.GetHistory)

	return r
}

func (h *NodeMetricHandler) PostMetrics(w http.ResponseWriter, r *http.Request) {

	// El node_id viene del token validado, no del body
	nodeToken := middleware.GetNodeToken(r.Context())

	if nodeToken == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var nodeMetrics nodemetric.CreateNodeMetricRequest
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
func (h *NodeMetricHandler) checkThresholdAlerts(nodeID string, m *nodemetric.CreateNodeMetricRequest) {
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

	check := func(name string, value float64, threshold *float32) {
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
	}

	check("CPU", m.CPUUsagePercent, cfg.CPUThreshold)
	check("RAM", m.MemoryUsagePercent, cfg.RAMThreshold)
	check("Disk", m.DiskUsagePercent, cfg.DiskThreshold)
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

	points, err := h.service.GetHistory(nodemetric.HistoryParams{
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

// func (h *NodeMetricHandler) ListMetrics(w http.ResponseWriter, r *http.Request) {
// 	claims := middleware.GetClaims(r.Context())
// 	if claims == nil || claims.UserID == "" {
// 		http.Error(w, "unauthorized", http.StatusUnauthorized)
// 		return
// 	}

// 	// Usar el servicio para obtener métricas
// 	metrics, err := h.service.GetMetricsByNodeID()
// 	if err != nil {
// 		http.Error(w, "could not list metrics", http.StatusInternalServerError)
// 		return
// 	}

// 	w.Header().Set("Content-Type", "application/json")
// 	json.NewEncoder(w).Encode(metrics)
// }
