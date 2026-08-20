package server

import (
	"context"
	"log"

	"github.com/arturo/autohost-cloud-api/internal/scheduler"
	"go.uber.org/fx"
)

func StartScheduler(lc fx.Lifecycle, sched *scheduler.Scheduler) {
	var cancel context.CancelFunc

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var schedCtx context.Context
			schedCtx, cancel = context.WithCancel(context.Background())

			log.Println("Iniciando Scheduler en segundo plano...")
			go sched.Start(schedCtx)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Println("Deteniendo Scheduler...")
			if cancel != nil {
				cancel()
			}
			return nil
		},
	})
}

func StartReportScheduler(lc fx.Lifecycle, sched *scheduler.ReportScheduler) {
	var cancel context.CancelFunc

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var schedCtx context.Context
			schedCtx, cancel = context.WithCancel(context.Background())

			log.Println("Iniciando ReportScheduler en segundo plano...")
			go sched.Start(schedCtx)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Println("Deteniendo ReportScheduler...")
			if cancel != nil {
				cancel()
			}
			return nil
		},
	})
}

func StartDiagnosticCleanupScheduler(lc fx.Lifecycle, sched *scheduler.DiagnosticCleanupScheduler) {
	var cancel context.CancelFunc

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var schedCtx context.Context
			schedCtx, cancel = context.WithCancel(context.Background())

			log.Println("Iniciando DiagnosticCleanupScheduler en segundo plano...")
			go sched.Start(schedCtx)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Println("Deteniendo DiagnosticCleanupScheduler...")
			if cancel != nil {
				cancel()
			}
			return nil
		},
	})
}
