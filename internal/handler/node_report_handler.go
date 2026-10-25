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

	alertevent "github.com/arturo/autohost-cloud-api/internal/domain/alert_event"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	"github.com/arturo/autohost-cloud-api/internal/domain/project"
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
	authService           *identity.AuthService
	metricService         *nodemetric.Service
	alertEventService     *alertevent.Service
	nodeAlertService      *nodealert.Service
	reportSettingsService *identity.ReportSettingsService
	projectService        *project.Service
	redis                 *redis.Client
}

func NewNodeReportHandler(
	nodeService *node.Service,
	authService *identity.AuthService,
	metricService *nodemetric.Service,
	alertEventService *alertevent.Service,
	nodeAlertService *nodealert.Service,
	reportSettingsService *identity.ReportSettingsService,
	projectService *project.Service,
	redis *redis.Client,
) *NodeReportHandler {
	return &NodeReportHandler{
		nodeService:           nodeService,
		authService:           authService,
		metricService:         metricService,
		alertEventService:     alertEventService,
		nodeAlertService:      nodeAlertService,
		reportSettingsService: reportSettingsService,
		projectService:        projectService,
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
	excluded := settings.ExcludedNodeIDs
	if excluded == nil {
		excluded = []string{}
	}
	response.RespondJSON(w, http.StatusOK, map[string]any{
		"enabled":           settings.Enabled,
		"hour_utc":          settings.HourUTC,
		"excluded_node_ids": excluded,
	})
}

func (h *NodeReportHandler) UpdateReportSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled         bool     `json:"enabled"`
		HourUTC         int      `json:"hour_utc"`
		ExcludedNodeIDs []string `json:"excluded_node_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.RespondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.ExcludedNodeIDs == nil {
		body.ExcludedNodeIDs = []string{}
	}
	if err := h.reportSettingsService.Upsert(userID, body.Enabled, body.HourUTC, body.ExcludedNodeIDs); err != nil {
		log.Printf("[ERROR] update report settings: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not update report settings")
		return
	}
	response.RespondJSON(w, http.StatusOK, map[string]any{
		"enabled":           body.Enabled,
		"hour_utc":          body.HourUTC,
		"excluded_node_ids": body.ExcludedNodeIDs,
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
	Grupo                    string              `json:"grupo,omitempty"`
	IsActive                 bool                `json:"is_active"`
	Uptime                   float64             `json:"uptime"`
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

	result, err := h.BuildAndPushForUser(context.Background(), userID)
	if err != nil {
		log.Printf("[ERROR] node report trigger: %v", err)
		response.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.RespondJSON(w, http.StatusOK, result)
}

// BuildAndPushForUser builds the node status report for the given user and pushes it to Redis.
// Returns a summary map suitable for API responses.
func (h *NodeReportHandler) BuildAndPushForUser(ctx context.Context, userID string) (map[string]any, error) {
	now := time.Now()
	since := now.Add(-reportWindow)

	user, err := h.authService.GetByID(userID)
	if err != nil || user == nil {
		return nil, fmt.Errorf("could not resolve user")
	}

	// Fetch report settings to get excluded nodes
	settings, err := h.reportSettingsService.Get(userID)
	excludedSet := make(map[string]bool)
	if err == nil && settings != nil {
		for _, id := range settings.ExcludedNodeIDs {
			excludedSet[id] = true
		}
	}

	// Fetch user's projects/groups
	projects, err := h.projectService.GetByUser(userID)
	if err != nil {
		log.Printf("[WARN] node report: fetch projects: %v", err)
	}
	projectNameByID := make(map[string]string)
	for _, p := range projects {
		projectNameByID[p.ID] = p.Name
	}

	nodes, err := h.nodeService.GetByOwnerWithMetrics(userID)
	if err != nil {
		return nil, fmt.Errorf("could not fetch nodes")
	}

	periodStats, err := h.metricService.GetNodePeriodStats(userID, since)
	if err != nil {
		return nil, fmt.Errorf("could not fetch metric stats")
	}

	alertSummaries, err := h.alertEventService.ListNodeAlertSummaryByUserID(userID, since)
	if err != nil {
		log.Printf("[WARN] node report: alert summaries: %v", err)
	}

	alertConfigs, err := h.nodeAlertService.FindAllByOwnerID(userID)
	if err != nil {
		log.Printf("[WARN] node report: alert configs: %v", err)
	}

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
	var nodesWithAlerts int
	var metricas []nodeReportMetric

	for _, n := range nodes {
		// Skip nodes excluded from report
		if excludedSet[n.ID] {
			continue
		}

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
		if alertCount24h > 0 {
			nodesWithAlerts++
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

		grupoName := "Sin grupo"
		if n.ProjectID != nil {
			if name, exists := projectNameByID[*n.ProjectID]; exists && name != "" {
				grupoName = name
			}
		}

		nodeUptime := 0.0
		if isActive {
			nodeUptime = 100.0
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
			Grupo:                    grupoName,
			IsActive:                 isActive,
			Uptime:                   nodeUptime,
		})
	}

	uptimePct := 0.0
	if len(metricas) > 0 {
		uptimePct = float64(activeCount) / float64(len(metricas)) * 100
	}

	payload := nodeStatusReport{
		FechaReporte: now.Format("2006-01-02"),
		AdminEmail:   user.Email,
		Infraestructura: nodeReportInfra{
			NodosActivos:      activeCount,
			NodosAlertas:      nodesWithAlerts,
			UptimePromedioRed: uptimePct,
		},
		MetricasResumen: metricas,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("could not build report")
	}

	if h.redis != nil {
		if err := h.redis.LPush(ctx, nodeStatusReportKey, data).Err(); err != nil {
			return nil, fmt.Errorf("could not queue report")
		}
		log.Printf("[INFO] node status report queued for user %s: active=%d alerts=%d nodes=%d",
			userID, activeCount, nodesWithAlerts, len(metricas))
	} else {
		log.Printf("[WARN] node status report not queued (redis unavailable) for user %s: active=%d alerts=%d nodes=%d",
			userID, activeCount, nodesWithAlerts, len(metricas))
	}

	return map[string]any{
		"status":        "queued",
		"nodes_total":   len(metricas),
		"nodes_active":  activeCount,
		"nodes_alerts":  nodesWithAlerts,
		"fecha_reporte": payload.FechaReporte,
	}, nil
}
