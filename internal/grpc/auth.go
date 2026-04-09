package grpcserver

import (
	"context"
	"log"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// nodeIDFromCtx validates the "authorization" metadata and returns the node ID.
func (s *NodeAgentServer) nodeIDFromCtx(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		log.Printf("[gRPC] auth: missing metadata")
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		log.Printf("[gRPC] auth: missing authorization header")
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}

	raw := values[0]
	if strings.HasPrefix(raw, "Bearer ") {
		raw = raw[7:]
	}

	if !strings.HasPrefix(raw, platform.TokenApiPrefix) {
		log.Printf("[gRPC] auth: invalid token format (prefix mismatch)")
		return "", status.Error(codes.Unauthenticated, "invalid node token format")
	}

	tokenHash := platform.HashTokenApi(raw)
	tok, err := s.tokenSvc.FindNodeTokenByHash(tokenHash)
	if err != nil {
		log.Printf("[gRPC] auth: token not found in DB (hash prefix: %.8s...): %v", tokenHash, err)
		return "", status.Error(codes.Unauthenticated, "invalid node token")
	}
	if tok.RevokedAt != nil {
		log.Printf("[gRPC] auth: token revoked for node %s", tok.NodeID)
		return "", status.Error(codes.Unauthenticated, "node token revoked")
	}
	return tok.NodeID, nil
}
