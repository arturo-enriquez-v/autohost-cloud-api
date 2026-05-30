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
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	reportsettings "github.com/arturo/autohost-cloud-api/internal/domain/report_settings"
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
	nodeService           *node.Service
	authService           *auth.Service
	metricService         *nodemetric.Service
	alertEventService     *alertevent.Service
	nodeAlertService      *nodealert.Service
	reportSettingsService *reportsettings.Service
	redis                 *redis.Client
}

func NewNodeReportHandler(
	nodeService *node.Service,
	authService *auth.Service,
	metricService *nodemetric.Service,
	alertEventService *alertevent.Service,
	nodeAlertService *nodealert.Service,
	reportSettingsService *reportsettings.Service,
	redis *redis.Client,
) *NodeReportHandler {
	return &NodeReportHandler{
		nodeService:           nodeService,
		authService:           authService,
		metricService:         metricService,
		alertEventService:     alertEventService,
		nodeAlertService:      nodeAlertService,
		reportSettingsService: reportSettingsService,
		redis:                 redis,
	}
}

func (h *NodeReportHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Post("/node-status/trigger", h.TriggerNodeStatusReport)
	r.Get("/settings", h.GetReportSettings)
	r.Put("/settings", h.UpdateReportSettings)
	return r
}

func (h *NodeReportHandler) GetReportSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	settings, err := h.reportSettingsService.Get(userID)
	if err != nil {
		log.Printf("[ERROR] get report settings: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not get report settings")
		return
	}
	response.RespondJSON(w, http.StatusOK, map[string]any{
		"enabled":  settings.Enabled,
		"hour_utc": settings.HourUTC,
	})
}

func (h *NodeReportHandler) UpdateReportSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
		HourUTC int  `json:"hour_utc"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.reportSettingsService.Upsert(userID, body.Enabled, body.HourUTC); err != nil {
		log.Printf("[ERROR] update report settings: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not update report settings")
		return
	}
	response.RespondJSON(w, http.StatusOK, map[string]any{
		"enabled":  body.Enabled,
		"hour_utc": body.HourUTC,
	})
}

type nodeReportUmbrales struct {
	CPU     *float32 `json:"cpu,omitempty"`
	RAM     *float32 `json:"ram,omitempty"`
	Disk    *float32 `json:"disk,omitempty"`
	Offline bool     `json:"offline"`
}

type nodeReportMetric struct {
	Nodo                     string              `json:"nodo"`
	CPUPromedio              float64             `json:"cpu_promedio"`
	CPUPico                  float64             `json:"cpu_pico"`
	RAMPromedio              float64             `json:"ram_promedio"`
	AlmacenamientoDisponible float64             `json:"almacenamiento_disponible"`
	Alertas24h               int                 `json:"alertas_24h"`
	TiposAlerta              []string            `json:"tipos_alerta"`
	Umbrales                 *nodeReportUmbrales `json:"umbrales,omitempty"`
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

	user, err := h.authService.GetByID(userID)
	if err != nil || user == nil {
		log.Printf("[ERROR] node report: fetch user: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not resolve user")
		return
	}

	nodes, err := h.nodeService.GetByOwnerWithMetrics(userID)
	if err != nil {
		log.Printf("[ERROR] node report: fetch nodes: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not fetch nodes")
		return
	}

	periodStats, err := h.metricService.GetNodePeriodStats(userID, since)
	if err != nil {
		log.Printf("[ERROR] node report: fetch period stats: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not fetch metric stats")
		return
	}

	alertSummaries, err := h.alertEventService.ListNodeAlertSummaryByUserID(userID, since)
	if err != nil {
		log.Printf("[WARN] node report: alert summaries: %v", err)
	}

	alertConfigs, err := h.nodeAlertService.FindAllByOwnerID(userID)
	if err != nil {
		log.Printf("[WARN] node report: alert configs: %v", err)
	}

	// Index all lookups by node ID
	statsByID := make(map[string]*nodemetric.NodePeriodStats, len(periodStats))
	for _, s := range periodStats {
		statsByID[s.NodeID] = s
	}

	alertSummaryByID := make(map[string]*alertevent.NodeAlertSummary, len(alertSummaries))
	for _, a := range alertSummaries {
		alertSummaryByID[a.NodeID] = a
	}

	alertConfigByID := make(map[string]*nodealert.NodeAlert, len(alertConfigs))
	for _, c := range alertConfigs {
		alertConfigByID[c.NodeID] = c
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

		alertCount24h := 0
		var tiposAlerta []string
		if summary := alertSummaryByID[n.ID]; summary != nil {
			alertCount24h = summary.Count
			for _, t := range summary.Types {
				tiposAlerta = append(tiposAlerta, string(t))
			}
		}
		if tiposAlerta == nil {
			tiposAlerta = []string{}
		}

		var umbrales *nodeReportUmbrales
		if cfg := alertConfigByID[n.ID]; cfg != nil {
			umbrales = &nodeReportUmbrales{
				CPU:     cfg.CPUThreshold,
				RAM:     cfg.RAMThreshold,
				Disk:    cfg.DiskThreshold,
				Offline: cfg.AlertOnOffline,
			}
		}

		metricas = append(metricas, nodeReportMetric{
			Nodo:                     n.Hostname,
			CPUPromedio:              cpuAvg,
			CPUPico:                  cpuMax,
			RAMPromedio:              ramAvg,
			AlmacenamientoDisponible: diskFree,
			Alertas24h:               alertCount24h,
			TiposAlerta:              tiposAlerta,
			Umbrales:                 umbrales,
		})
	}

	uptimePct := 0.0
	if len(nodes) > 0 {
		uptimePct = float64(activeCount) / float64(len(nodes)) * 100
	}

	alertCount, err := h.alertEventService.CountDistinctNodesWithRecentAlerts(userID, since)
	if err != nil {
		log.Printf("[WARN] node report: alert count: %v", err)
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
