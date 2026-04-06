package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"

	"fmt"
	"runtime/debug"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

	lis, err := net.Listen("tcp", "0.0.0.0:"+grpcPort)
	if err != nil {
		log.Fatalf("Failed to listen on gRPC port %s: %v", grpcPort, err)
	}

	recoveryInterceptor := func(p any) error {
		log.Printf("[gRPC] panic recovered: %v\n%s", p, debug.Stack())
		return status.Errorf(codes.Internal, "internal server error: %v", fmt.Sprintf("%v", p))
	}
	grpcSrv := grpc.NewServer(
		grpc.ChainStreamInterceptor(streamRecovery(recoveryInterceptor)),
		grpc.ChainUnaryInterceptor(unaryRecovery(recoveryInterceptor)),
	)
	nodepb.RegisterNodeAgentServiceServer(grpcSrv, app.GRPCServer)

	go func() {
		log.Printf("gRPC server listening on :%s", grpcPort)
		if err := grpcSrv.Serve(lis); err != nil {
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

// streamRecovery returns a gRPC stream interceptor that recovers from panics.
func streamRecovery(handle func(any) error) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = handle(r)
			}
		}()
		return handler(srv, ss)
	}
}

// unaryRecovery returns a gRPC unary interceptor that recovers from panics.
func unaryRecovery(handle func(any) error) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = handle(r)
			}
		}()
		return handler(ctx, req)
	}
}
