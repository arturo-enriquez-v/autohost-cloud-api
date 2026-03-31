package grpcserver

import (
	"context"
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
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}

	raw := values[0]
	if strings.HasPrefix(raw, "Bearer ") {
		raw = raw[7:]
	}

	if !strings.HasPrefix(raw, platform.TokenApiPrefix) {
		return "", status.Error(codes.Unauthenticated, "invalid node token format")
	}

	tokenHash := platform.HashTokenApi(raw)
	tok, err := s.tokenSvc.FindNodeTokenByHash(tokenHash)
	if err != nil {
		return "", status.Error(codes.Unauthenticated, "invalid node token")
	}
	if tok.RevokedAt != nil {
		return "", status.Error(codes.Unauthenticated, "node token revoked")
	}
	return tok.NodeID, nil
}
