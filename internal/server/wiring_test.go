// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package server_test

import (
	"testing"

	"go.uber.org/fx"

	"github.com/arturo/autohost-cloud-api/internal/app"
	"github.com/arturo/autohost-cloud-api/internal/server"
)

// TestDependencyGraph validates the full fx graph used by cmd/api (SERVICE_MODE=all) without invoking any
// constructor, so a missing or mis-wired dependency fails here instead of at startup.
func TestDependencyGraph(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(app.ProvideDB),
		app.RepositoryModule,
		app.ServiceModule,
		app.HandlerModule,
		app.InfraModule,
		fx.Provide(server.NewRouter),
		fx.Invoke(
			server.StartHTTPServer,
			server.StartGRPCServer,
			server.StartScheduler,
			server.StartReportScheduler,
			server.StartDiagnosticCleanupScheduler,
			server.StartAutoscalerScheduler,
		),
	)
	if err != nil {
		t.Fatalf("invalid dependency graph: %v", err)
	}
}
