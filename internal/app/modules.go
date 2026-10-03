package app

import (
	"log"

	alertevent "github.com/arturo/autohost-cloud-api/internal/domain/alert_event"
	"github.com/arturo/autohost-cloud-api/internal/domain/auth"
	"github.com/arturo/autohost-cloud-api/internal/domain/backup"
	caddyroute "github.com/arturo/autohost-cloud-api/internal/domain/caddy_route"
	clusterdomain "github.com/arturo/autohost-cloud-api/internal/domain/cluster"
	containermetric "github.com/arturo/autohost-cloud-api/internal/domain/container_metric"
	containersetting "github.com/arturo/autohost-cloud-api/internal/domain/container_setting"
	"github.com/arturo/autohost-cloud-api/internal/domain/enrollment"
	externallog "github.com/arturo/autohost-cloud-api/internal/domain/external_log"
	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	logdiagnostic "github.com/arturo/autohost-cloud-api/internal/domain/log_diagnostic"
	"github.com/arturo/autohost-cloud-api/internal/domain/marketplace"
	networkstat "github.com/arturo/autohost-cloud-api/internal/domain/network_stat"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	"github.com/arturo/autohost-cloud-api/internal/domain/notification"
	"github.com/arturo/autohost-cloud-api/internal/domain/plan"
	projectdomain "github.com/arturo/autohost-cloud-api/internal/domain/project"
	reportsettings "github.com/arturo/autohost-cloud-api/internal/domain/report_settings"
	scheduledtask "github.com/arturo/autohost-cloud-api/internal/domain/scheduled_task"
	servicemonitor "github.com/arturo/autohost-cloud-api/internal/domain/service_monitor"
	telegrambot "github.com/arturo/autohost-cloud-api/internal/domain/telegram_bot"
	"github.com/arturo/autohost-cloud-api/internal/domain/vpn"
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
	fx.Annotate(postgres.NewAuthRepository, fx.As(new(auth.Repository))),
	fx.Annotate(postgres.NewNodeRepository, fx.As(new(node.Repository))),
	fx.Annotate(postgres.NewNodeMetricRepository, fx.As(new(nodemetric.Repository))),
	fx.Annotate(postgres.NewContainerMetricRepository, fx.As(new(containermetric.Repository))),
	fx.Annotate(postgres.NewEnrollmentRepository, fx.As(new(enrollment.Repository))),
	fx.Annotate(postgres.NewNodeTokenRepository, fx.As(new(nodetoken.Repository))),
	fx.Annotate(postgres.NewNodeCommandRepository, fx.As(new(nodecommand.Repository))),
	fx.Annotate(postgres.NewJobRepository, fx.As(new(job.Repository))),
	fx.Annotate(postgres.NewNodeAlertRepository, fx.As(new(nodealert.Repository))),
	fx.Annotate(postgres.NewScheduledTaskRepository, fx.As(new(scheduledtask.Repository))),
	fx.Annotate(postgres.NewMarketplaceRepository, fx.As(new(marketplace.Repository))),
	fx.Annotate(postgres.NewAlertEventRepository, fx.As(new(alertevent.Repository))),
	fx.Annotate(postgres.NewPlanRepository, fx.As(new(plan.Repository))),
	fx.Annotate(postgres.NewProjectRepository, fx.As(new(projectdomain.Repository))),
	fx.Annotate(postgres.NewServiceMonitorRepository, fx.As(new(servicemonitor.Repository))),
	fx.Annotate(postgres.NewCaddyRouteRepository, fx.As(new(caddyroute.Repository))),
	fx.Annotate(postgres.NewReportSettingsRepository, fx.As(new(reportsettings.Repository))),
	fx.Annotate(postgres.NewTelegramBotRepository, fx.As(new(telegrambot.Repository))),
	fx.Annotate(postgres.NewNetworkStatRepository, fx.As(new(networkstat.Repository))),
	fx.Annotate(postgres.NewExternalLogRepository, fx.As(new(externallog.Repository))),
	fx.Annotate(postgres.NewBackupRepository, fx.As(new(backup.Repository))),
	fx.Annotate(postgres.NewLogDiagnosticRepository, fx.As(new(logdiagnostic.Repository))),
	fx.Annotate(postgres.NewClusterRepository, fx.As(new(clusterdomain.Repository))),
	fx.Annotate(postgres.NewContainerSettingRepository, fx.As(new(containersetting.Repository))),
	fx.Annotate(postgres.NewNotificationMarkerRepository, fx.As(new(notification.MarkerRepository))),
)

var ServiceModule = fx.Provide(
	auth.NewService,
	node.NewService,
	nodemetric.NewService,
	containermetric.NewService,
	containersetting.NewService,
	enrollment.NewService,
	nodetoken.NewService,
	nodecommand.NewService,
	job.NewService,
	nodealert.NewService,
	scheduledtask.NewService,
	alertevent.NewService,
	plan.NewService,
	projectdomain.NewService,
	servicemonitor.NewService,
	caddyroute.NewService,
	reportsettings.NewService,
	telegrambot.NewService,
	externallog.NewService,
	networkstat.NewService,
	vpn.NewService,
	notification.NewService,
	backup.NewService,
	logdiagnostic.NewService,
	fx.Annotate(
		func(repo clusterdomain.Repository, nodeRepo node.Repository, jobService *job.Service, grpc *grpcserver.NodeAgentServer) *clusterdomain.Service {
			return clusterdomain.NewService(repo, nodeRepo, jobService, grpc)
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
)

var InfraModule = fx.Provide(
	grpcserver.NewNodeAgentServer,
	provideOptionalRedisClient,
	platform.NewGeminiClient,
	headscale.NewClient,
	// NodeAgentServer also satisfies ParamDispatcher (used by CaddyRoute and Marketplace handlers).
	fx.Annotate(
		func(s *grpcserver.NodeAgentServer) handler.ParamDispatcher { return s },
	),
	// Non-variadic wrapper: wire grpc + ws into MultiDispatcher.
	// Exposed as handler.NodeDispatcher (JobHandler) and scheduler.Dispatcher (Scheduler).
	fx.Annotate(
		func(grpc *grpcserver.NodeAgentServer, ws *handler.WSHandler) *handler.MultiDispatcher {
			return handler.NewMultiDispatcher(grpc, ws)
		},
		fx.As(new(handler.NodeDispatcher)),
		fx.As(new(scheduler.Dispatcher)),
	),
	handler.NewJobHandler,
	scheduler.NewScheduler,
	scheduler.NewReportScheduler,
	scheduler.NewDiagnosticCleanupScheduler,
	fx.Annotate(
		func(repo clusterdomain.Repository, jobSvc *job.Service, grpc *grpcserver.NodeAgentServer) *scheduler.AutoscalerScheduler {
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
