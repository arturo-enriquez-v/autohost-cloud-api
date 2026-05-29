package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/auth"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

const (
	nodeStatusReportKey = "reports:enqueue"
	activeThreshold     = 5 * time.Minute
	// Thresholds for alert detection
	cpuAlertThreshold  = 85.0
	ramAlertThreshold  = 85.0
	diskAlertThreshold = 90.0
)

type NodeReportHandler struct {
	nodeService *node.Service
	authService *auth.Service
	redis       *redis.Client
}

func NewNodeReportHandler(nodeService *node.Service, authService *auth.Service, redis *redis.Client) *NodeReportHandler {
	return &NodeReportHandler{nodeService: nodeService, authService: authService, redis: redis}
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

	nodes, err := h.nodeService.GetByOwnerWithMetrics(userID)
	if err != nil {
		log.Printf("[ERROR] node report: fetch nodes: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not fetch node metrics")
		return
	}

	user, err := h.authService.GetByID(userID)
	if err != nil || user == nil {
		log.Printf("[ERROR] node report: fetch user: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not resolve user")
		return
	}

	now := time.Now()
	var activeCount, alertCount int
	var metricas []nodeReportMetric

	for _, n := range nodes {
		isActive := n.LastSeenAt != nil && now.Sub(*n.LastSeenAt) < activeThreshold
		if isActive {
			activeCount++
		}

		var cpuPct, ramPct, diskUsagePct float64
		if n.LastMetric != nil {
			cpuPct = n.LastMetric.CPUUsagePercent
			ramPct = n.LastMetric.MemoryUsagePercent
			diskUsagePct = n.LastMetric.DiskUsagePercent

			if isActive && (cpuPct > cpuAlertThreshold || ramPct > ramAlertThreshold || diskUsagePct > diskAlertThreshold) {
				alertCount++
			}
		}

		// disk_disponible is reported as free % (100 - usage)
		diskFreePct := 100.0 - diskUsagePct

		metricas = append(metricas, nodeReportMetric{
			Nodo:                     n.Hostname,
			CPUPromedio:              cpuPct,
			CPUPico:                  cpuPct,
			RAMPromedio:              ramPct,
			AlmacenamientoDisponible: diskFreePct,
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
