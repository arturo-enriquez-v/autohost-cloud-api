package handler

import (
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/domain/auth"
	"github.com/arturo/autohost-cloud-api/internal/domain/enrollment"
	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	scheduledtask "github.com/arturo/autohost-cloud-api/internal/domain/scheduled_task"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	handlerMiddleware "github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/arturo/autohost-cloud-api/internal/repository/postgres"
	"github.com/arturo/autohost-cloud-api/internal/scheduler"
)

type Config struct {
	DB *sqlx.DB
}

// Application bundles the HTTP handler together with the gRPC server so that
// main.go can start both.
type Application struct {
	HTTP       http.Handler
	GRPCServer *grpcserver.NodeAgentServer
	Scheduler  *scheduler.Scheduler
}

// NewRouter builds all repositories, services, and handlers and returns the
// Application containing both the HTTP mux and the gRPC server.
func NewRouter(cfg *Config) *Application {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(corsMiddleware)

	r.Get("/health", healthCheckHandler)

	// Repositories
	authRepo := postgres.NewAuthRepository(cfg.DB)
	nodeRepo := postgres.NewNodeRepository(cfg.DB)
	nodeMetricRepo := postgres.NewNodeMetricRepository(cfg.DB)
	enrollmentRepo := postgres.NewEnrollmentRepository(cfg.DB)
	nodeTokenRepo := postgres.NewNodeTokenRepository(cfg.DB)
	nodeCommandRepo := postgres.NewNodeCommandRepository(cfg.DB)
	jobRepo := postgres.NewJobRepository(cfg.DB)
	nodeAlertRepo := postgres.NewNodeAlertRepository(cfg.DB)
	scheduledTaskRepo := postgres.NewScheduledTaskRepository(cfg.DB)
	marketplaceRepo := postgres.NewMarketplaceRepository(cfg.DB)

	// Services
	authService := auth.NewService(authRepo)
	nodeService := node.NewService(nodeRepo)
	nodeMetricService := nodemetric.NewService(nodeMetricRepo)
	enrollmentService := enrollment.NewService(enrollmentRepo)
	nodeTokenService := nodetoken.NewService(nodeTokenRepo)
	nodeCommandService := nodecommand.NewService(nodeCommandRepo)
	jobService := job.NewService(jobRepo)
	nodeAlertService := nodealert.NewService(nodeAlertRepo)
	scheduledTaskService := scheduledtask.NewService(scheduledTaskRepo)

	nodeAuthMiddleware := handlerMiddleware.NodeAuth(nodeTokenService)

	// gRPC server — also a NodeDispatcher over gRPC transport
	grpcSrv := grpcserver.NewNodeAgentServer(nodeCommandService, jobService, nodeTokenService, nodeAlertService, nodeService, nodeMetricService)

	// HTTP handlers
	authHandler := NewAuthHandler(authService, authRepo)
	nodeHandler := NewNodeHandler(nodeService, nodeTokenService)
	nodeMetricHandler := NewNodeMetricHandler(nodeMetricService, nodeAlertService, nodeService, grpcSrv)
	enrollmentHandler := NewEnrollmentHandler(enrollmentService, nodeService, nodeTokenService)
	heartbeatsHandler := NewHeartbeatsHandler(nodeService)
	wsHandler := NewWSHandler(jobService, nodeCommandService)
	nodeCommandHandler := NewNodeCommandHandler(nodeCommandService)
	nodeLogHandler := NewNodeLogHandler(grpcSrv)
	nodeAlertHandler := NewNodeAlertHandler(nodeAlertService)
	scheduledTaskHandler := NewScheduledTaskHandler(scheduledTaskService)

	// MultiDispatcher: tries gRPC first, falls back to WebSocket
	dispatcher := NewMultiDispatcher(grpcSrv, wsHandler)
	jobHandler := NewJobHandler(jobService, dispatcher)

	// Marketplace: R2 client is optional — marketplace routes are only mounted
	// when R2 credentials are present in the environment.
	var marketplaceHandler *MarketplaceHandler
	if r2, err := platform.NewR2Client(); err == nil {
		marketplaceHandler = NewMarketplaceHandler(jobService, marketplaceRepo, grpcSrv, r2)
	} else {
		log.Printf("[WARN] marketplace: R2 not configured (%v) — /marketplace routes disabled", err)
	}

	r.Route("/v1", func(r chi.Router) {
		r.Mount("/auth", authHandler.Routes())
		r.Mount("/nodes", nodeHandler.Routes())
		r.Mount("/node-metrics", nodeMetricHandler.Routes(nodeAuthMiddleware))
		r.Mount("/enrollments", enrollmentHandler.Routes())
		r.Mount("/heartbeats", heartbeatsHandler.Routes(nodeAuthMiddleware))
		r.Mount("/node-commands", nodeCommandHandler.Routes(nodeAuthMiddleware))
		r.Mount("/jobs", jobHandler.Routes())
		r.Mount("/ws", wsHandler.Routes(nodeAuthMiddleware))
		r.Mount("/node-logs", nodeLogHandler.Routes())
		r.Mount("/node-alerts", nodeAlertHandler.Routes())
		r.Mount("/scheduled-tasks", scheduledTaskHandler.Routes())
		if marketplaceHandler != nil {
			r.Mount("/marketplace", marketplaceHandler.Routes())
		}
	})

	sched := scheduler.NewScheduler(scheduledTaskService, jobService, dispatcher)

	return &Application{HTTP: r, GRPCServer: grpcSrv, Scheduler: sched}
}

func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok","service":"autohost-cloud-api"}`))
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
