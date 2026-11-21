// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"crypto/tls"
	"log"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// nodeIDFromCtx validates the peer's mTLS client certificate and/or the
// "authorization" bearer token metadata, returning the authenticated node ID.
func (s *NodeAgentServer) nodeIDFromCtx(ctx context.Context) (string, error) {
	var certNodeID string
	var hasTLSInfo bool

	if p, ok := peer.FromContext(ctx); ok && p.AuthInfo != nil {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			hasTLSInfo = true
			if len(tlsInfo.State.VerifiedChains) > 0 && len(tlsInfo.State.VerifiedChains[0]) > 0 {
				leaf := tlsInfo.State.VerifiedChains[0][0]
				certNodeID = strings.TrimSpace(leaf.Subject.CommonName)
			}
		}
	}

	// If connection arrived over TLS and strict mTLS is configured, require a valid client certificate.
	if hasTLSInfo && certNodeID == "" && platform.ResolveClientAuthMode() == tls.RequireAndVerifyClientCert {
		log.Printf("[gRPC] auth: missing verified mTLS client certificate")
		return "", status.Error(codes.Unauthenticated, "missing valid client certificate")
	}

	md, hasMD := metadata.FromIncomingContext(ctx)
	var authValues []string
	if hasMD {
		authValues = md.Get("authorization")
	}

	// When bearer token is provided, validate it and cross-check against the mTLS certificate CN.
	if len(authValues) > 0 {
		raw := strings.TrimSpace(authValues[0])
		if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
			raw = strings.TrimSpace(raw[7:])
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

		if certNodeID != "" && certNodeID != tok.NodeID {
			log.Printf("[gRPC] auth: mTLS cert node_id (%s) does not match token node_id (%s)", certNodeID, tok.NodeID)
			return "", status.Error(codes.PermissionDenied, "client certificate identity mismatch")
		}

		return tok.NodeID, nil
	}

	// If no bearer token was sent, allow pure mTLS authentication when certNodeID is verified,
	// but only while the node is still enrolled: client certificates live for a year and cannot be
	// revoked at the TLS layer, so deleting the node or revoking its token must cut access here.
	if certNodeID != "" {
		if !s.nodeEnrollmentActive(certNodeID) {
			log.Printf("[gRPC] auth: rejecting certificate for node %s: node deleted or tokens revoked", certNodeID)
			return "", status.Error(codes.Unauthenticated, "node enrollment revoked")
		}
		return certNodeID, nil
	}

	if !hasMD {
		log.Printf("[gRPC] auth: missing metadata")
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	log.Printf("[gRPC] auth: missing authorization header")
	return "", status.Error(codes.Unauthenticated, "missing authorization header")
}

// enrollmentCacheTTL bounds how long a positive/negative enrollment check is reused for unary calls.
const enrollmentCacheTTL = 30 * time.Second

type enrollmentCacheEntry struct {
	active    bool
	expiresAt time.Time
}

// enrollmentCache memoizes HasActiveToken lookups so high-frequency unary RPCs (metrics) do not hit the DB
// on every call. Entries are short-lived so revocations take effect within enrollmentCacheTTL.
var enrollmentCache sync.Map // nodeID -> enrollmentCacheEntry

// nodeEnrollmentActive reports whether nodeID still has a non-revoked token (cached briefly).
// Lookup errors fail closed.
func (s *NodeAgentServer) nodeEnrollmentActive(nodeID string) bool {
	if v, ok := enrollmentCache.Load(nodeID); ok {
		if e := v.(enrollmentCacheEntry); time.Now().Before(e.expiresAt) {
			return e.active
		}
	}
	active := s.checkEnrollmentUncached(nodeID)
	enrollmentCache.Store(nodeID, enrollmentCacheEntry{active: active, expiresAt: time.Now().Add(enrollmentCacheTTL)})
	return active
}

// checkEnrollmentUncached queries the token store directly (no cache). Errors fail closed.
func (s *NodeAgentServer) checkEnrollmentUncached(nodeID string) bool {
	if s.tokenSvc == nil {
		return false
	}
	ok, err := s.tokenSvc.HasActiveToken(nodeID)
	if err != nil {
		log.Printf("[gRPC] auth: enrollment check for node %s failed: %v", nodeID, err)
		return false
	}
	return ok
}
