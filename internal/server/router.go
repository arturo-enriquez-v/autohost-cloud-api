// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package server

import (
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	handlerMiddleware "github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

type RouterParams struct {
	fx.In

	AuthHandler             *handler.AuthHandler
	UserHandler             *handler.UserHandler `optional:"true"`
	RBACService             *identity.RBACService `optional:"true"`
	NodeService             *node.NodeService `optional:"true"`
	NodeHandler             *handler.NodeHandler
	NodeMetricHandler     *handler.NodeMetricHandler
	EnrollmentHandler     *handler.EnrollmentHandler
	HeartbeatsHandler     *handler.HeartbeatsHandler
	NodeCommandHandler    *handler.NodeCommandHandler
	JobsHandler           *handler.JobHandler
	WSHandler             *handler.WSHandler
	NodeLogHandler        *handler.NodeLogHandler
	NodeContainerHandler  *handler.NodeContainerHandler
	NodeAlertHandler      *handler.NodeAlertHandler
	ScheduledTaskHandler  *handler.ScheduledTaskHandler
	AlertEventHandler     *handler.AlertEventHandler
	ProjectHandler        *handler.ProjectHandler
	PlanHandler           *handler.PlanHandler
	ServiceMonitorHandler   *handler.ServiceMonitorHandler
	CaddyRouteHandler       *handler.CaddyRouteHandler
	CloudflareTunnelHandler *handler.CloudflareTunnelHandler
	MarketplaceHandler      *handler.MarketplaceHandler
	WebhookHandler        *handler.WebhookHandler
	NodeReportHandler     *handler.NodeReportHandler
	TelegramBotHandler    *handler.TelegramBotHandler
	ExternalLogHandler    *handler.ExternalLogHandler
	VPNHandler            *handler.VPNHandler
	NodeNetworkHandler    *handler.NodeNetworkHandler
	BackupHandler         *handler.BackupHandler
	LogDiagnosticHandler  *handler.LogDiagnosticHandler
	ClusterHandler        *handler.ClusterHandler
	NotificationHandler   *handler.NotificationHandler
	HypervisorHandler     *handler.HypervisorHandler
	VMHandler             *handler.VMHandler
	NodeTerminalHandler     *handler.NodeTerminalHandler
	NodeFileHandler         *handler.NodeFileHandler
	PublicShareHandler      *handler.PublicShareHandler
	NodeTokenService        *node.NodeTokenService
}

// NewRouter construye el mux de Chi
func NewRouter(p RouterParams) *chi.Mux {
	r := chi.NewRouter()

	if p.RBACService != nil {
		if p.AuthHandler != nil {
			p.AuthHandler.SetRBACService(p.RBACService)
		}
		if p.NodeHandler != nil {
			p.NodeHandler.SetRBACService(p.RBACService)
		}
		if p.ProjectHandler != nil {
			p.ProjectHandler.SetRBACService(p.RBACService)
		}
		if p.NodeMetricHandler != nil {
			p.NodeMetricHandler.SetRBACService(p.RBACService)
		}
		if p.NodeCommandHandler != nil {
			p.NodeCommandHandler.SetRBACService(p.RBACService)
		}
		if p.JobsHandler != nil {
			p.JobsHandler.SetRBACService(p.RBACService)
		}
		if p.NodeLogHandler != nil {
			p.NodeLogHandler.SetRBACService(p.RBACService)
		}
		if p.NodeContainerHandler != nil {
			p.NodeContainerHandler.SetRBACService(p.RBACService)
		}
		if p.NodeTerminalHandler != nil {
			p.NodeTerminalHandler.SetRBACService(p.RBACService)
		}
		if p.NodeFileHandler != nil {
			p.NodeFileHandler.SetRBACService(p.RBACService)
		}
		if p.PublicShareHandler != nil {
			p.PublicShareHandler.SetRBACService(p.RBACService)
		}
		if p.NodeAlertHandler != nil {
			p.NodeAlertHandler.SetRBACService(p.RBACService)
		}
		if p.ScheduledTaskHandler != nil {
			p.ScheduledTaskHandler.SetRBACService(p.RBACService, p.NodeService)
		}
		if p.ServiceMonitorHandler != nil {
			p.ServiceMonitorHandler.SetRBACService(p.RBACService, p.NodeService)
		}
		if p.CaddyRouteHandler != nil {
			p.CaddyRouteHandler.SetRBACService(p.RBACService)
		}
		if p.MarketplaceHandler != nil {
			p.MarketplaceHandler.SetRBACService(p.RBACService)
		}
		if p.NodeNetworkHandler != nil {
			p.NodeNetworkHandler.SetRBACService(p.RBACService)
		}
		if p.BackupHandler != nil {
			p.BackupHandler.SetRBACService(p.RBACService)
		}
		if p.LogDiagnosticHandler != nil {
			p.LogDiagnosticHandler.SetRBACService(p.RBACService)
		}
		if p.HypervisorHandler != nil {
			p.HypervisorHandler.SetRBACService(p.RBACService)
		}
		if p.VMHandler != nil {
			p.VMHandler.SetRBACService(p.RBACService)
		}
		if p.ClusterHandler != nil {
			p.ClusterHandler.SetRBACService(p.RBACService)
		}
		if p.CloudflareTunnelHandler != nil {
			p.CloudflareTunnelHandler.SetRBACService(p.RBACService)
		}
	}

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(corsMiddleware)
	r.Use(securityHeaders)
	r.Use(limitRequestBody)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","service":"autohost-cloud-api"}`))
	})

	nodeAuthMiddleware := handlerMiddleware.NodeAuth(p.NodeTokenService)

	r.Route("/v1", func(r chi.Router) {
		r.Mount("/auth", p.AuthHandler.Routes())
		if p.UserHandler != nil {
			r.Mount("/users", p.UserHandler.Routes())
		}
		r.Mount("/nodes", p.NodeHandler.Routes())
		r.Mount("/node-metrics", p.NodeMetricHandler.Routes(nodeAuthMiddleware))
		r.Mount("/enrollments", p.EnrollmentHandler.Routes())
		r.Mount("/heartbeats", p.HeartbeatsHandler.Routes(nodeAuthMiddleware))
		r.Mount("/node-commands", p.NodeCommandHandler.Routes(nodeAuthMiddleware))
		r.Mount("/jobs", p.JobsHandler.Routes())
		r.Mount("/ws", p.WSHandler.Routes(nodeAuthMiddleware))
		r.Mount("/node-logs", p.NodeLogHandler.Routes())
		r.Mount("/node-containers", p.NodeContainerHandler.Routes())
		r.Mount("/node-terminal", p.NodeTerminalHandler.Routes())
		r.Mount("/nodes/{nodeId}/terminal", p.NodeTerminalHandler.NodeScopedRoutes())
		r.Mount("/node-files", p.NodeFileHandler.Routes())
		r.Mount("/nodes/{nodeId}/files", p.NodeFileHandler.NodeScopedRoutes())
		r.Mount("/file-shares", p.PublicShareHandler.OwnerShareRoutes())
		r.Mount("/public-shares", p.PublicShareHandler.PublicPortalRoutes())
		r.Mount("/tailscale-funnel", p.PublicShareHandler.FunnelRoutes())
		r.Mount("/node-alerts", p.NodeAlertHandler.Routes())
		r.Mount("/scheduled-tasks", p.ScheduledTaskHandler.Routes())
		r.Mount("/alert-events", p.AlertEventHandler.Routes())
		r.Mount("/projects", p.ProjectHandler.Routes())
		r.Mount("/usage", p.PlanHandler.Routes())
		r.Mount("/plans", p.PlanHandler.Routes())
		r.Mount("/service-monitors", p.ServiceMonitorHandler.Routes())
		r.Mount("/caddy-routes", p.CaddyRouteHandler.Routes())
		r.Mount("/cloudflare-tunnel", p.CloudflareTunnelHandler.Routes())
		r.Mount("/marketplace", p.MarketplaceHandler.Routes())
		r.Mount("/webhooks", p.WebhookHandler.Routes())
		r.Mount("/reports", p.NodeReportHandler.Routes())
		r.Mount("/telegram-bots", p.TelegramBotHandler.Routes())
		r.Mount("/external-logs", p.ExternalLogHandler.UserLogRoutes())
		r.Mount("/external-logs/ingest", p.ExternalLogHandler.IngestRoutes())
		r.Mount("/external-log-tokens", p.ExternalLogHandler.UserTokenRoutes())
		r.Mount("/vpn", p.VPNHandler.Routes(nodeAuthMiddleware))
		r.Mount("/backups", p.BackupHandler.Routes(nodeAuthMiddleware))
		r.Mount("/log-diagnostics", p.LogDiagnosticHandler.Routes())
		r.Mount("/diagnostics", p.LogDiagnosticHandler.Routes())
		r.Mount("/clusters", p.ClusterHandler.Routes())
		r.Mount("/notifications", p.NotificationHandler.Routes())
		r.Mount("/hypervisors", p.HypervisorHandler.Routes())
		r.Mount("/vms", p.VMHandler.VMRoutes())
		r.Mount("/node-vms", p.VMHandler.NodeVMRoutes())
		r.Mount("/nodes/{nodeId}/vms", p.VMHandler.NodeVMRoutes())
		// Agent → POST /v1/agent/network-stats
		r.Mount("/agent/network-stats", p.NodeNetworkHandler.Routes(nodeAuthMiddleware))
		// Frontend → GET /v1/node-network/{nodeId}
		r.Mount("/node-network", p.NodeNetworkHandler.NodeRoutes())
	})

	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	frontendURL := os.Getenv("FRONTEND_URL")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if frontendURL != "" {
			w.Header().Set("Access-Control-Allow-Origin", frontendURL)
		} else if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Upgrade, Connection, X-Share-Token")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders adds hardening headers to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// limitRequestBody caps incoming request bodies at 2 MB to prevent memory
// exhaustion attacks, exempting large streaming uploads like backups.
func limitRequestBody(next http.Handler) http.Handler {
	const maxBytes = 2 << 20 // 2 MB
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/backups/upload/") ||
			strings.Contains(r.URL.Path, "/node-files/") ||
			strings.Contains(r.URL.Path, "/public-shares/") ||
			strings.Contains(r.URL.Path, "/files/upload") {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}
