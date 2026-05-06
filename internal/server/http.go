package server

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
)

func StartHTTPServer(lc fx.Lifecycle, router *chi.Mux) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			log.Printf("Iniciando servidor HTTP en %s", srv.Addr)
			go srv.ListenAndServe()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Println("Apagando servidor HTTP...")
			return srv.Shutdown(ctx)
		},
	})
}
