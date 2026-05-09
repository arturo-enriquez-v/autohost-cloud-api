package server

import (
	"net/http"
	"os"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"

	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	handlerMiddleware "github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

type RouterParams struct {
	fx.In

	AuthHandler           *handler.AuthHandler
	NodeHandler           *handler.NodeHandler
	NodeMetricHandler     *handler.NodeMetricHandler
	EnrollmentHandler     *handler.EnrollmentHandler
	HeartbeatsHandler     *handler.HeartbeatsHandler
	NodeCommandHandler    *handler.NodeCommandHandler
	JobsHandler           *handler.JobHandler
	WSHandler             *handler.WSHandler
	NodeLogHandler        *handler.NodeLogHandler
	NodeAlertHandler      *handler.NodeAlertHandler
	ScheduledTaskHandler  *handler.ScheduledTaskHandler
	AlertEventHandler     *handler.AlertEventHandler
	ProjectHandler        *handler.ProjectHandler
	ServiceMonitorHandler *handler.ServiceMonitorHandler
	CaddyRouteHandler     *handler.CaddyRouteHandler
	MarketplaceHandler    *handler.MarketplaceHandler
	WebhookHandler        *handler.WebhookHandler

	NodeTokenService *nodetoken.Service
}

// NewRouter construye el mux de Chi
func NewRouter(p RouterParams) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(corsMiddleware)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","service":"autohost-cloud-api"}`))
	})

	nodeAuthMiddleware := handlerMiddleware.NodeAuth(p.NodeTokenService)

	r.Route("/v1", func(r chi.Router) {
		r.Mount("/auth", p.AuthHandler.Routes())
		r.Mount("/nodes", p.NodeHandler.Routes())
		r.Mount("/node-metrics", p.NodeMetricHandler.Routes(nodeAuthMiddleware))
		r.Mount("/enrollments", p.EnrollmentHandler.Routes())
		r.Mount("/heartbeats", p.HeartbeatsHandler.Routes(nodeAuthMiddleware))
		r.Mount("/node-commands", p.NodeCommandHandler.Routes(nodeAuthMiddleware))
		r.Mount("/jobs", p.JobsHandler.Routes())
		r.Mount("/ws", p.WSHandler.Routes(nodeAuthMiddleware))
		r.Mount("/node-logs", p.NodeLogHandler.Routes())
		r.Mount("/node-alerts", p.NodeAlertHandler.Routes())
		r.Mount("/scheduled-tasks", p.ScheduledTaskHandler.Routes())
		r.Mount("/alert-events", p.AlertEventHandler.Routes())
		r.Mount("/projects", p.ProjectHandler.Routes())
		r.Mount("/service-monitors", p.ServiceMonitorHandler.Routes())
		r.Mount("/caddy-routes", p.CaddyRouteHandler.Routes())
		r.Mount("/marketplace", p.MarketplaceHandler.Routes())
		r.Mount("/webhooks", p.WebhookHandler.Routes())
	})

	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	frontendURL := os.Getenv("FRONTEND_URL")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", frontendURL)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
