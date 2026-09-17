package main

import (
	"log"

	"github.com/joho/godotenv"
	"go.uber.org/fx"

	"github.com/arturo/autohost-cloud-api/internal/app"
	"github.com/arturo/autohost-cloud-api/internal/server"
)

func main() {
	// 1. Cargar entorno básico antes de iniciar Fx
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment")
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

		// -- Invocadores (Los motores que queremos arrancar) --
		fx.Invoke(
			server.StartHTTPServer,
			server.StartGRPCServer,
			server.StartScheduler,
			server.StartReportScheduler,
			server.StartDiagnosticCleanupScheduler,
			server.StartAutoscalerScheduler,
		),
	).Run()
}
