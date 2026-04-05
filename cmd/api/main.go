package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"

	"github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"github.com/arturo/autohost-cloud-api/internal/handler"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL not set")
	}

	db := sqlx.MustConnect("postgres", dbURL)
	defer db.Close()

	app := handler.NewRouter(&handler.Config{DB: db})

	// ── gRPC server ───────────────────────────────────────────────────────────
	grpcPort := os.Getenv("GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "9090"
	}

	grpcSrv := grpc.NewServer()
	nodepb.RegisterNodeAgentServiceServer(grpcSrv, app.GRPCServer)

	grpcHandler := h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grpcSrv.ServeHTTP(w, r)
	}), &http2.Server{})

	go func() {
		log.Printf("gRPC server (h2c) listening on :%s", grpcPort)
		if err := http.ListenAndServe("0.0.0.0:"+grpcPort, grpcHandler); err != nil {
			log.Fatalf("gRPC server error: %v", err)
		}
	}()

	// ── Scheduler ─────────────────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.Scheduler.Start(ctx)

	// ── HTTP server ───────────────────────────────────────────────────────────
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := "0.0.0.0:" + port
	log.Printf("HTTP server listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, app.HTTP))
}
