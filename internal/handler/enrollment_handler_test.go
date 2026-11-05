// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

type mockEnrollRepo struct {
	tokens map[string]*node.EnrollToken
	used   map[string]bool
}

func (m *mockEnrollRepo) CreateEnrollToken(token string, userID string, expiresAt time.Time) error {
	t := &node.EnrollToken{
		ID:        "tok-1",
		Token:     token,
		UserID:    userID,
		ExpiresAt: expiresAt,
	}
	m.tokens[token] = t
	return nil
}

func (m *mockEnrollRepo) FindEnrollTokenByHash(hash string) (*node.EnrollToken, error) {
	for _, t := range m.tokens {
		if platform.HashEnrollToken(t.Token) == hash {
			return t, nil
		}
	}
	return nil, errors.New("token not found")
}

func (m *mockEnrollRepo) MarkTokenAsUsed(token string, usedAt time.Time) error {
	m.used[token] = true
	if t, ok := m.tokens[token]; ok {
		t.ConsumedAt = &usedAt
	}
	return nil
}

type mockNodeTokenRepo struct {
	tokens map[string]string
}

func (m *mockNodeTokenRepo) CreateNodeToken(nodeID, token string) error {
	m.tokens[nodeID] = token
	return nil
}

func (m *mockNodeTokenRepo) FindNodeTokenByHash(tokenHash string) (*node.NodeToken, error) {
	return nil, nil
}

func (m *mockNodeTokenRepo) UpdateLastSeen(tokenID string, lastSeenAt time.Time) error {
	return nil
}

func (m *mockNodeTokenRepo) RevokeByNodeID(nodeID string) error {
	return nil
}

func TestEnrollNode_SanitizesUnknownIPAndSucceeds(t *testing.T) {
	enrollRepo := &mockEnrollRepo{
		tokens: make(map[string]*node.EnrollToken),
		used:   make(map[string]bool),
	}
	rawToken := "autohost-enr_test12345"
	enrollRepo.tokens[rawToken] = &node.EnrollToken{
		ID:        "tok-1",
		Token:     rawToken,
		UserID:    "user-uuid-1",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}

	nodeRepo := &MockNodeRepo{nodes: make(map[string]*node.Node)}
	nodeSvc := node.NewNodeService(nodeRepo)
	nodeTokenRepo := &mockNodeTokenRepo{tokens: make(map[string]string)}
	nodeTokenSvc := node.NewNodeTokenService(nodeTokenRepo)
	enrollSvc := node.NewEnrollmentService(enrollRepo)

	h := handler.NewEnrollmentHandler(enrollSvc, nodeSvc, nodeTokenSvc, nil, nil)

	// Send request with IPLocal: "unknown" (as autohost-cli does when offline or behind VPN)
	reqBody := map[string]string{
		"enroll_token":  rawToken,
		"hostname":      "mazapan",
		"ip_local":      "unknown",
		"os":            "linux",
		"arch":          "x86_64",
		"version_agent": "0.1.0",
	}
	b, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/enroll", bytes.NewReader(b))
	req.RemoteAddr = "100.83.116.52:54321"
	w := httptest.NewRecorder()

	h.EnrollNode(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["node_id"] == "" {
		t.Errorf("expected node_id in response, got empty")
	}
	if resp["api_token"] == "" {
		t.Errorf("expected api_token in response, got empty")
	}

	// Verify token was marked as used
	if !enrollRepo.used[rawToken] {
		t.Errorf("expected token to be marked as used on success")
	}
}
