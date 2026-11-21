// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
)

type fakeTokenRepo struct{ active map[string]bool }

func (f *fakeTokenRepo) CreateNodeToken(string, string) error { return nil }
func (f *fakeTokenRepo) FindNodeTokenByHash(string) (*node.NodeToken, error) {
	return nil, node.ErrNodeTokenNotFound
}
func (f *fakeTokenRepo) UpdateLastSeen(string, time.Time) error { return nil }
func (f *fakeTokenRepo) RevokeByNodeID(id string) error         { f.active[id] = false; return nil }
func (f *fakeTokenRepo) HasActiveToken(id string) (bool, error) { return f.active[id], nil }

// certOnlyCtx simulates an mTLS connection with a verified client certificate and no bearer token.
func certOnlyCtx(nodeID string) context.Context {
	leaf := &x509.Certificate{Subject: pkix.Name{CommonName: nodeID}}
	info := credentials.TLSInfo{State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf}}}}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: info})
}

func TestNodeIDFromCtx_CertOnlyRequiresActiveEnrollment(t *testing.T) {
	repo := &fakeTokenRepo{active: map[string]bool{"node-active": true, "node-revoked": false}}
	s := &NodeAgentServer{tokenSvc: node.NewNodeTokenService(repo)}
	enrollmentCache.Delete("node-active")
	enrollmentCache.Delete("node-revoked")
	enrollmentCache.Delete("node-deleted")

	if id, err := s.nodeIDFromCtx(certOnlyCtx("node-active")); err != nil || id != "node-active" {
		t.Fatalf("active node should authenticate, got %q, %v", id, err)
	}
	if _, err := s.nodeIDFromCtx(certOnlyCtx("node-revoked")); err == nil {
		t.Fatal("certificate of a node with revoked tokens must be rejected")
	}
	if _, err := s.nodeIDFromCtx(certOnlyCtx("node-deleted")); err == nil {
		t.Fatal("certificate of a deleted node (no tokens) must be rejected")
	}
}

func TestNodeEnrollmentActive_RevocationTakesEffectAfterCacheExpiry(t *testing.T) {
	repo := &fakeTokenRepo{active: map[string]bool{"node-x": true}}
	s := &NodeAgentServer{tokenSvc: node.NewNodeTokenService(repo)}
	enrollmentCache.Delete("node-x")

	if !s.nodeEnrollmentActive("node-x") {
		t.Fatal("expected active")
	}
	_ = repo.RevokeByNodeID("node-x")
	// Simulate the cache entry expiring.
	enrollmentCache.Store("node-x", enrollmentCacheEntry{active: true, expiresAt: time.Now().Add(-time.Second)})
	if s.nodeEnrollmentActive("node-x") {
		t.Fatal("revocation must be honored once the cache entry expires")
	}
	if s.checkEnrollmentUncached("node-x") {
		t.Fatal("uncached check must see the revocation immediately")
	}
}

func TestStreamRegistry_StaleUnregisterKeepsNewStream(t *testing.T) {
	r := newStreamRegistry()
	oldStream := &nodeStream{}
	newStream := &nodeStream{}
	r.register("n1", oldStream)
	r.register("n1", newStream) // node reconnected
	r.unregister("n1", oldStream)
	if got, ok := r.get("n1"); !ok || got != newStream {
		t.Fatal("cleanup of the old stream removed the new registration")
	}
	r.unregister("n1", newStream)
	if r.isConnected("n1") {
		t.Fatal("expected node to be unregistered")
	}
}
