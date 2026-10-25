// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package app

import (
	"log"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	alertevent "github.com/arturo/autohost-cloud-api/internal/domain/alert_event"
	"github.com/arturo/autohost-cloud-api/internal/domain/backup"
	caddyroute "github.com/arturo/autohost-cloud-api/internal/domain/caddy_route"
	clusterdomain "github.com/arturo/autohost-cloud-api/internal/domain/cluster"
	containermetric "github.com/arturo/autohost-cloud-api/internal/domain/container_metric"
	containersetting "github.com/arturo/autohost-cloud-api/internal/domain/container_setting"
	"github.com/arturo/autohost-cloud-api/internal/domain/enrollment"
	externallog "github.com/arturo/autohost-cloud-api/internal/domain/external_log"
	logdiagnostic "github.com/arturo/autohost-cloud-api/internal/domain/log_diagnostic"
	"github.com/arturo/autohost-cloud-api/internal/domain/marketplace"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	projectdomain "github.com/arturo/autohost-cloud-api/internal/domain/project"
	servicemonitor "github.com/arturo/autohost-cloud-api/internal/domain/service_monitor"
	"github.com/arturo/autohost-cloud-api/internal/domain/vm"
	"github.com/arturo/autohost-cloud-api/internal/core/network"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/arturo/autohost-cloud-api/internal/platform/headscale"
	"github.com/arturo/autohost-cloud-api/internal/repository/postgres"
	"github.com/arturo/autohost-cloud-api/internal/scheduler"
	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
)

var RepositoryModule = fx.Provide(
	fx.Annotate(postgres.NewAuthRepository, fx.As(new(identity.AuthRepository))),
	fx.Annotate(postgres.NewNodeRepository, fx.As(new(node.Repository))),
	fx.Annotate(postgres.NewNodeMetricRepository, fx.As(new(nodemetric.Repository))),
	fx.Annotate(postgres.NewContainerMetricRepository, fx.As(new(containermetric.Repository))),
	fx.Annotate(postgres.NewEnrollmentRepository, fx.As(new(enrollment.Repository))),
	fx.Annotate(postgres.NewNodeTokenRepository, fx.As(new(nodetoken.Repository))),
	fx.Annotate(postgres.NewNodeCommandRepository, fx.As(new(jobs.NodeCommandRepository))),
	fx.Annotate(postgres.NewJobRepository, fx.As(new(jobs.JobRepository))),
	fx.Annotate(postgres.NewNodeAlertRepository, fx.As(new(nodealert.Repository))),
	fx.Annotate(postgres.NewScheduledTaskRepository, fx.As(new(jobs.ScheduledTaskRepository))),
	fx.Annotate(postgres.NewMarketplaceRepository, fx.As(new(marketplace.Repository))),
	fx.Annotate(postgres.NewAlertEventRepository, fx.As(new(alertevent.Repository))),
	fx.Annotate(postgres.NewPlanRepository, fx.As(new(identity.PlanRepository))),
	fx.Annotate(postgres.NewProjectRepository, fx.As(new(projectdomain.Repository))),
	fx.Annotate(postgres.NewServiceMonitorRepository, fx.As(new(servicemonitor.Repository))),
	fx.Annotate(postgres.NewCaddyRouteRepository, fx.As(new(caddyroute.Repository))),
	fx.Annotate(postgres.NewReportSettingsRepository, fx.As(new(identity.ReportSettingsRepository))),
	fx.Annotate(postgres.NewTelegramBotRepository, fx.As(new(identity.TelegramBotRepository))),
	fx.Annotate(postgres.NewNetworkStatRepository, fx.As(new(network.NetworkStatRepository))),
	fx.Annotate(postgres.NewExternalLogRepository, fx.As(new(externallog.Repository))),
	fx.Annotate(postgres.NewBackupRepository, fx.As(new(backup.Repository))),
	fx.Annotate(postgres.NewLogDiagnosticRepository, fx.As(new(logdiagnostic.Repository))),
	fx.Annotate(postgres.NewClusterRepository, fx.As(new(clusterdomain.Repository))),
	fx.Annotate(postgres.NewContainerSettingRepository, fx.As(new(containersetting.Repository))),
	fx.Annotate(postgres.NewNotificationMarkerRepository, fx.As(new(identity.NotificationMarkerRepository))),
	fx.Annotate(postgres.NewVMRepository, fx.As(new(vm.Repository))),
)

var ServiceModule = fx.Provide(
	identity.NewAuthService,
	node.NewService,
	nodemetric.NewService,
	containermetric.NewService,
	containersetting.NewService,
	enrollment.NewService,
	nodetoken.NewService,
	jobs.NewNodeCommandService,
	jobs.NewJobService,
	nodealert.NewService,
	jobs.NewScheduledTaskService,
	alertevent.NewService,
	identity.NewPlanService,
	projectdomain.NewService,
	servicemonitor.NewService,
	caddyroute.NewService,
	identity.NewReportSettingsService,
	identity.NewTelegramBotService,
	externallog.NewService,
	network.NewNetworkStatService,
	network.NewVPNService,
	identity.NewNotificationService,
	backup.NewService,
	logdiagnostic.NewService,
	fx.Annotate(
		func(repo clusterdomain.Repository, nodeRepo node.Repository, jobService *jobs.JobService, grpc *grpcserver.NodeAgentServer) *clusterdomain.Service {
			return clusterdomain.NewService(repo, nodeRepo, jobService, grpc)
		},
	),
	fx.Annotate(
		func(repo vm.Repository, nodeRepo node.Repository, enrollSvc *enrollment.Service, jobService *jobs.JobService, grpc *grpcserver.NodeAgentServer) *vm.Service {
			return vm.NewService(repo, nodeRepo, enrollSvc, jobService, grpc)
		},
	),
)

var HandlerModule = fx.Provide(
	handler.NewAuthHandler,
	handler.NewNodeHandler,
	handler.NewNodeMetricHandler,
	handler.NewEnrollmentHandler,
	handler.NewHeartbeatsHandler,
	handler.NewWSHandler,
	handler.NewNodeCommandHandler,
	handler.NewNodeLogHandler,
	handler.NewNodeContainerHandler,
	handler.NewNodeAlertHandler,
	handler.NewScheduledTaskHandler,
	handler.NewAlertEventHandler,
	handler.NewPlanHandler,
	handler.NewProjectHandler,
	handler.NewServiceMonitorHandler,
	handler.NewCaddyRouteHandler,
	handler.NewMarketplaceHandler,
	handler.NewWebhookHandler,
	handler.NewNodeReportHandler,
	handler.NewTelegramBotHandler,
	handler.NewExternalLogHandler,
	handler.NewVPNHandler,
	handler.NewNodeNetworkHandler,
	handler.NewBackupHandler,
	handler.NewLogDiagnosticHandler,
	handler.NewClusterHandler,
	handler.NewNotificationHandler,
	handler.NewHypervisorHandler,
	handler.NewVMHandler,
)

var InfraModule = fx.Provide(
	grpcserver.NewNodeAgentServer,
	provideOptionalRedisClient,
	platform.NewGeminiClient,
	headscale.NewClient,
	// NodeAgentServer also satisfies ParamDispatcher (used by CaddyRoute and Marketplace handlers).
	fx.Annotate(
		func(s *grpcserver.NodeAgentServer) dispatcher.ParamDispatcher { return s },
	),
	// Non-variadic wrapper: wire grpc + ws into MultiDispatcher.
	// Exposed as dispatcher.NodeDispatcher (JobHandler) and scheduler.Dispatcher (Scheduler).
	fx.Annotate(
		func(grpc *grpcserver.NodeAgentServer, ws *handler.WSHandler) *dispatcher.MultiDispatcher {
			return dispatcher.NewMultiDispatcher(grpc, ws)
		},
		fx.As(new(dispatcher.NodeDispatcher)),
		fx.As(new(scheduler.Dispatcher)),
	),
	handler.NewJobHandler,
	scheduler.NewScheduler,
	scheduler.NewReportScheduler,
	scheduler.NewDiagnosticCleanupScheduler,
	fx.Annotate(
		func(repo clusterdomain.Repository, jobSvc *jobs.JobService, grpc *grpcserver.NodeAgentServer) *scheduler.AutoscalerScheduler {
			return scheduler.NewAutoscalerScheduler(repo, jobSvc, grpc)
		},
	),
	// Wire NodeReportHandler as ReportTrigger for the ReportScheduler.
	fx.Annotate(
		func(h *handler.NodeReportHandler) scheduler.ReportTrigger { return h },
	),
	provideOptionalR2Client,
)

// provideOptionalRedisClient returns a nil *redis.Client (without error) when
// Redis connection fails or is unavailable, so fx does not fail on startup.
// Handlers and services will fall back to in-memory/direct dispatching.
func provideOptionalRedisClient() *redis.Client {
	client, err := platform.NewRedisClient()
	if err != nil {
		log.Printf("[WARN] redis: no disponible (%v) — funcionando en modo fallback local", err)
		return nil
	}
	return client
}

// provideOptionalR2Client returns a nil *platform.R2Client (without error) when
// the R2 environment variables are absent, so fx does not fail on startup.
// Marketplace routes that require R2 will respond with 501 if the client is nil.
func provideOptionalR2Client() *platform.R2Client {
	r2, err := platform.NewR2Client()
	if err != nil {
		log.Printf("[WARN] marketplace: R2 not configured (%v) — marketplace routes will be unavailable", err)
		return nil
	}
	return r2
}
