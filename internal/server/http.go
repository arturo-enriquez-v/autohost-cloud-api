package server

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
)

func StartHTTPServer(lc fx.Lifecycle, router *chi.Mux) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
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
