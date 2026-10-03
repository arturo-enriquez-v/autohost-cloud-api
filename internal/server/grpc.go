package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

func StartGRPCServer(lc fx.Lifecycle, agentServer *grpcserver.NodeAgentServer) {
	grpcPort := os.Getenv("GRPC_PORT")
	if grpcPort == "" {
		mode := strings.ToLower(strings.TrimSpace(os.Getenv("SERVICE_MODE")))
		if (mode == "grpc" || mode == "agent") && os.Getenv("PORT") != "" {
			grpcPort = os.Getenv("PORT")
		} else {
			grpcPort = "9090"
		}
	}

	recoveryInterceptor := func(p any) error {
		log.Printf("[gRPC] panic recovered: %v\n%s", p, debug.Stack())
		return status.Errorf(codes.Internal, "internal server error: %v", fmt.Sprintf("%v", p))
	}

	grpcSrv := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    20 * time.Second, // send PING to client if idle for 20s
			Timeout: 5 * time.Second,  // close connection if no PONG within 5s
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // minimum PING interval accepted from clients
			PermitWithoutStream: true,             // allow pings with no active streams
		}),
		grpc.ChainStreamInterceptor(streamRecovery(recoveryInterceptor)),
		grpc.ChainUnaryInterceptor(unaryRecovery(recoveryInterceptor)),
	)

	nodepb.RegisterNodeAgentServiceServer(grpcSrv, agentServer)

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			lis, err := net.Listen("tcp", "0.0.0.0:"+grpcPort)
			if err != nil {
				return fmt.Errorf("failed to listen on gRPC port %s: %w", grpcPort, err)
			}

			log.Printf("gRPC server listening on :%s", grpcPort)
			go grpcSrv.Serve(lis)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Println("Deteniendo servidor gRPC (Graceful Stop)...")
			grpcSrv.GracefulStop()
			return nil
		},
	})
}

// streamRecovery returns a gRPC stream interceptor that recovers from panics.
func streamRecovery(handle func(any) error) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = handle(p)
			}
		}()
		return handler(srv, ss)
	}
}

// unaryRecovery returns a gRPC unary interceptor that recovers from panics.
func unaryRecovery(handle func(any) error) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if p := recover(); p != nil {
				err = handle(p)
			}
		}()
		return handler(ctx, req)
	}
}
