package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	alertevent "github.com/arturo/autohost-cloud-api/internal/domain/alert_event"
	"github.com/arturo/autohost-cloud-api/internal/domain/auth"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

const (
	nodeStatusReportKey = "reports:enqueue"
	activeThreshold     = 5 * time.Minute
	reportWindow        = 24 * time.Hour
)

type NodeReportHandler struct {
	nodeService       *node.Service
	authService       *auth.Service
	metricService     *nodemetric.Service
	alertEventService *alertevent.Service
	redis             *redis.Client
}

func NewNodeReportHandler(
	nodeService *node.Service,
	authService *auth.Service,
	metricService *nodemetric.Service,
	alertEventService *alertevent.Service,
	redis *redis.Client,
) *NodeReportHandler {
	return &NodeReportHandler{
		nodeService:       nodeService,
		authService:       authService,
		metricService:     metricService,
		alertEventService: alertEventService,
		redis:             redis,
	}
}

func (h *NodeReportHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Post("/node-status/trigger", h.TriggerNodeStatusReport)
	return r
}

type nodeReportMetric struct {
	Nodo                     string  `json:"nodo"`
	CPUPromedio              float64 `json:"cpu_promedio"`
	CPUPico                  float64 `json:"cpu_pico"`
	RAMPromedio              float64 `json:"ram_promedio"`
	AlmacenamientoDisponible float64 `json:"almacenamiento_disponible"`
}

type nodeReportInfra struct {
	NodosActivos      int     `json:"nodos_activos"`
	NodosAlertas      int     `json:"nodos_alertas"`
	UptimePromedioRed float64 `json:"uptime_promedio_red"`
}

type nodeStatusReport struct {
	FechaReporte    string             `json:"fecha_reporte"`
	AdminEmail      string             `json:"admin_email"`
	Infraestructura nodeReportInfra    `json:"infraestructura"`
	MetricasResumen []nodeReportMetric `json:"metricas_resumen"`
}

func (h *NodeReportHandler) TriggerNodeStatusReport(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}

	now := time.Now()
	since := now.Add(-reportWindow)

	// Fetch user for email
	user, err := h.authService.GetByID(userID)
	if err != nil || user == nil {
		log.Printf("[ERROR] node report: fetch user: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not resolve user")
		return
	}

	// Active status from last heartbeat
	nodes, err := h.nodeService.GetByOwnerWithMetrics(userID)
	if err != nil {
		log.Printf("[ERROR] node report: fetch nodes: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not fetch nodes")
		return
	}

	// Avg + max metrics over the last 24h
	periodStats, err := h.metricService.GetNodePeriodStats(userID, since)
	if err != nil {
		log.Printf("[ERROR] node report: fetch period stats: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not fetch metric stats")
		return
	}

	// Index period stats by node ID for O(1) lookup
	statsByID := make(map[string]*nodemetric.NodePeriodStats, len(periodStats))
	for _, s := range periodStats {
		statsByID[s.NodeID] = s
	}

	// Real alert count from DB (distinct nodes with alerts in last 24h)
	alertCount, err := h.alertEventService.CountDistinctNodesWithRecentAlerts(userID, since)
	if err != nil {
		log.Printf("[WARN] node report: alert count: %v", err)
		// non-fatal: continue with 0
	}

	var activeCount int
	var metricas []nodeReportMetric

	for _, n := range nodes {
		isActive := n.LastSeenAt != nil && now.Sub(*n.LastSeenAt) < activeThreshold
		if isActive {
			activeCount++
		}

		stats := statsByID[n.ID]
		var cpuAvg, cpuMax, ramAvg, diskFree float64
		if stats != nil {
			cpuAvg = stats.CPUAvg
			cpuMax = stats.CPUMax
			ramAvg = stats.RAMAvg
			diskFree = stats.DiskFreeAvg
		}

		metricas = append(metricas, nodeReportMetric{
			Nodo:                     n.Hostname,
			CPUPromedio:              cpuAvg,
			CPUPico:                  cpuMax,
			RAMPromedio:              ramAvg,
			AlmacenamientoDisponible: diskFree,
		})
	}

	uptimePct := 0.0
	if len(nodes) > 0 {
		uptimePct = float64(activeCount) / float64(len(nodes)) * 100
	}

	payload := nodeStatusReport{
		FechaReporte: now.Format("2006-01-02"),
		AdminEmail:   user.Email,
		Infraestructura: nodeReportInfra{
			NodosActivos:      activeCount,
			NodosAlertas:      alertCount,
			UptimePromedioRed: uptimePct,
		},
		MetricasResumen: metricas,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[ERROR] node report: marshal payload: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not build report")
		return
	}

	ctx := context.Background()
	if err := h.redis.LPush(ctx, nodeStatusReportKey, data).Err(); err != nil {
		log.Printf("[ERROR] node report: redis push: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not queue report")
		return
	}

	log.Printf("[INFO] node status report queued for user %s: active=%d alerts=%d nodes=%d",
		userID, activeCount, alertCount, len(nodes))

	response.RespondJSON(w, http.StatusOK, map[string]any{
		"status":        "queued",
		"nodes_total":   len(nodes),
		"nodes_active":  activeCount,
		"nodes_alerts":  alertCount,
		"fecha_reporte": payload.FechaReporte,
	})
}
