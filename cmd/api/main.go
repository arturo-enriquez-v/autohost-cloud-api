// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package main

import (
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"go.uber.org/fx"

	"github.com/arturo/autohost-cloud-api/internal/app"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/server"
)

func main() {
	// 1. Cargar entorno básico antes de iniciar Fx
	if err := godotenv.Overload(); err != nil {
		log.Println("No .env file found, using system environment")
	}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv("SERVICE_MODE")))
	if mode == "" {
		mode = "all"
	}

	var invocations []any
	invocations = append(invocations, func(_ *node.LocalNodeBootstrapper) {})

	switch mode {
	case "http", "api":
		log.Println("[BOOT] Iniciando servicio en modo HTTP...")
		invocations = append(invocations, server.StartHTTPServer)
		if strings.ToLower(strings.TrimSpace(os.Getenv("RUN_SCHEDULERS"))) == "true" {
			log.Println("[BOOT] RUN_SCHEDULERS=true detectado: iniciando schedulers...")
			invocations = append(invocations,
				server.StartScheduler,
				server.StartReportScheduler,
				server.StartDiagnosticCleanupScheduler,
				server.StartAutoscalerScheduler,
			)
		}

	case "grpc", "agent":
		log.Println("[BOOT] Iniciando servicio en modo gRPC...")
		invocations = append(invocations, server.StartGRPCServer)

	case "worker", "scheduler", "schedulers":
		log.Println("[BOOT] Iniciando servicio en modo Worker (Schedulers)...")
		invocations = append(invocations,
			server.StartScheduler,
			server.StartReportScheduler,
			server.StartDiagnosticCleanupScheduler,
			server.StartAutoscalerScheduler,
		)

	default: // "all"
		log.Printf("[BOOT] Iniciando servicio en modo '%s' (HTTP + gRPC + Schedulers)...", mode)
		invocations = append(invocations,
			server.StartHTTPServer,
			server.StartGRPCServer,
			server.StartScheduler,
			server.StartReportScheduler,
			server.StartDiagnosticCleanupScheduler,
			server.StartAutoscalerScheduler,
		)
	}

	// 2. Iniciar la aplicación gestionada por Fx
	fx.New(
		// -- Proveedores Base --
		fx.Provide(
			app.ProvideDB, // Extraemos la conexión de DB aquí
		),

		// --Módulos (Definidos en internal/app/modules.go) --
		app.RepositoryModule,
		app.ServiceModule,
		app.HandlerModule,
		app.InfraModule,

		// -- Proveedor del Router HTTP --
		fx.Provide(server.NewRouter),

		// -- Invocadores (Los motores que queremos arrancar según SERVICE_MODE) --
		fx.Invoke(invocations...),
	).Run()
}
