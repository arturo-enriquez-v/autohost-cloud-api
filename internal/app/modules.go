// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package app

import (
	"log"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/network"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
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
	fx.Annotate(postgres.NewNodeRepository, fx.As(new(node.NodeRepository))),
	fx.Annotate(postgres.NewNodeMetricRepository, fx.As(new(node.NodeMetricRepository))),
	fx.Annotate(postgres.NewContainerMetricRepository, fx.As(new(node.ContainerMetricRepository))),
	fx.Annotate(postgres.NewEnrollmentRepository, fx.As(new(node.EnrollmentRepository))),
	fx.Annotate(postgres.NewNodeTokenRepository, fx.As(new(node.NodeTokenRepository))),
	fx.Annotate(postgres.NewNodeCommandRepository, fx.As(new(jobs.NodeCommandRepository))),
	fx.Annotate(postgres.NewJobRepository, fx.As(new(jobs.JobRepository))),
	fx.Annotate(postgres.NewNodeAlertRepository, fx.As(new(node.NodeAlertRepository))),
	fx.Annotate(postgres.NewScheduledTaskRepository, fx.As(new(jobs.ScheduledTaskRepository))),
	fx.Annotate(postgres.NewMarketplaceRepository, fx.As(new(apps.MarketplaceRepository))),
	fx.Annotate(postgres.NewAlertEventRepository, fx.As(new(node.AlertEventRepository))),
	fx.Annotate(postgres.NewPlanRepository, fx.As(new(identity.PlanRepository))),
	fx.Annotate(postgres.NewProjectRepository, fx.As(new(apps.ProjectRepository))),
	fx.Annotate(postgres.NewServiceMonitorRepository, fx.As(new(apps.ServiceMonitorRepository))),
	fx.Annotate(postgres.NewCaddyRouteRepository, fx.As(new(apps.CaddyRouteRepository))),
	fx.Annotate(postgres.NewReportSettingsRepository, fx.As(new(identity.ReportSettingsRepository))),
	fx.Annotate(postgres.NewTelegramBotRepository, fx.As(new(identity.TelegramBotRepository))),
	fx.Annotate(postgres.NewNetworkStatRepository, fx.As(new(network.NetworkStatRepository))),
	fx.Annotate(postgres.NewExternalLogRepository, fx.As(new(node.ExternalLogRepository))),
	fx.Annotate(postgres.NewBackupRepository, fx.As(new(apps.BackupRepository))),
	fx.Annotate(postgres.NewLogDiagnosticRepository, fx.As(new(node.LogDiagnosticRepository))),
	fx.Annotate(postgres.NewClusterRepository, fx.As(new(infra.ClusterRepository))),
	fx.Annotate(postgres.NewContainerSettingRepository, fx.As(new(node.ContainerSettingRepository))),
	fx.Annotate(postgres.NewNotificationMarkerRepository, fx.As(new(identity.NotificationMarkerRepository))),
	fx.Annotate(postgres.NewVMRepository, fx.As(new(infra.VMRepository))),
)

var ServiceModule = fx.Provide(
	identity.NewAuthService,
	node.NewNodeService,
	node.NewNodeMetricService,
	node.NewContainerMetricService,
	node.NewContainerSettingService,
	node.NewEnrollmentService,
	node.NewNodeTokenService,
	node.NewLocalNodeBootstrapper,
	jobs.NewNodeCommandService,
	jobs.NewJobService,
	node.NewNodeAlertService,
	jobs.NewScheduledTaskService,
	node.NewAlertEventService,
	identity.NewPlanService,
	apps.NewProjectService,
	apps.NewServiceMonitorService,
	apps.NewCaddyRouteService,
	identity.NewReportSettingsService,
	identity.NewTelegramBotService,
	node.NewExternalLogService,
	network.NewNetworkStatService,
	network.NewVPNService,
	identity.NewNotificationService,
	apps.NewBackupService,
	node.NewLogDiagnosticService,
	fx.Annotate(
		func(repo infra.ClusterRepository, nodeRepo node.NodeRepository, jobService *jobs.JobService, grpc *grpcserver.NodeAgentServer) *infra.ClusterManager {
			return infra.NewClusterManager(repo, nodeRepo, jobService, grpc)
		},
	),
	fx.Annotate(
		func(repo infra.VMRepository, nodeRepo node.NodeRepository, enrollSvc *node.EnrollmentService, jobService *jobs.JobService, grpc *grpcserver.NodeAgentServer) *infra.VMService {
			return infra.NewVMService(repo, nodeRepo, enrollSvc, jobService, grpc)
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
	handler.NewCloudflareTunnelHandler,
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
	handler.NewNodeTerminalHandler,
	handler.NewNodeFileHandler,
)

var InfraModule = fx.Provide(
	platform.NewPKIManager,
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
		func(repo infra.ClusterRepository, jobSvc *jobs.JobService, grpc *grpcserver.NodeAgentServer) *scheduler.AutoscalerScheduler {
			return scheduler.NewAutoscalerScheduler(repo, jobSvc, grpc)
		},
	),
	// Wire NodeReportHandler as ReportTrigger for the ReportScheduler.
	fx.Annotate(
		func(h *handler.NodeReportHandler) scheduler.ReportTrigger { return h },
	),
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
