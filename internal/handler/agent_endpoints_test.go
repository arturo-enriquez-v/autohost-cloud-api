// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
)

const testAgentToken = platform.TokenApiPrefix + "test"

// agentTokenRepo accepts testAgentToken as the token of node "node-1".
type agentTokenRepo struct{ MockNodeTokenRepo }

func (agentTokenRepo) FindNodeTokenByHash(hash string) (*node.NodeToken, error) {
	if hash != platform.HashTokenApi(testAgentToken) {
		return nil, errors.New("not found")
	}
	return &node.NodeToken{ID: "tok-1", NodeID: "node-1"}, nil
}

func agentRequest(t *testing.T, h http.Handler, method, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testAgentToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func agentAuth() func(http.Handler) http.Handler {
	return middleware.NodeAuth(node.NewNodeTokenService(&agentTokenRepo{}))
}

// commandRepo stores upserted commands in memory.
type commandRepo struct{ saved []*jobs.NodeCommand }

func (r *commandRepo) Upsert(c *jobs.NodeCommand) (*jobs.NodeCommand, error) {
	c.ID = "cmd-1"
	c.CreatedAt = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	r.saved = append(r.saved, c)
	return c, nil
}
func (r *commandRepo) FindByNodeID(string) ([]*jobs.NodeCommand, error) { return r.saved, nil }
func (r *commandRepo) FindByID(string) (*jobs.NodeCommand, error) {
	return nil, jobs.ErrCommandNotFound
}
func (r *commandRepo) Delete(string) error { return nil }

func commandRouter() http.Handler {
	h := handler.NewNodeCommandHandler(jobs.NewNodeCommandService(&commandRepo{}))
	r := chi.NewRouter()
	r.Mount("/v1/node-commands", h.Routes(agentAuth()))
	return r
}

func TestNodeCommands_RegisterRequiresName(t *testing.T) {
	rec := agentRequest(t, commandRouter(), http.MethodPost, "/v1/node-commands", `{"type":"custom"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// Commands are serialized with snake_case field names, like the rest of the API.
func TestNodeCommands_ResponseIsSnakeCase(t *testing.T) {
	rec := agentRequest(t, commandRouter(), http.MethodPost, "/v1/node-commands",
		`{"name":"backup","type":"custom","description":"Run backup","script_path":"/opt/backup.sh"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	want := map[string]any{
		"id": "cmd-1", "node_id": "node-1", "name": "backup", "description": "Run backup",
		"type": "custom", "script_path": "/opt/backup.sh", "created_at": "2026-11-21T10:00:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected fields in %v", got)
	}
}

// failingNodeRepo fails every write with an error that must not reach the client.
type failingNodeRepo struct{ MockNodeRepo }

var errInternalDetail = errors.New(`pq: relation "nodes" does not exist`)

func (failingNodeRepo) UpdateLastSeen(string) error             { return errInternalDetail }
func (failingNodeRepo) UpdateVersionAgent(string, string) error { return errInternalDetail }

func TestHeartbeats_DoNotLeakInternalErrors(t *testing.T) {
	h := handler.NewHeartbeatsHandler(node.NewNodeService(&failingNodeRepo{}))
	r := chi.NewRouter()
	r.Mount("/v1/heartbeats", h.Routes(agentAuth()))

	for _, tc := range []struct{ url, body string }{
		{"/v1/heartbeats/heartbeat", ""},
		{"/v1/heartbeats/version", `{"version":"v1.0.0"}`},
	} {
		rec := agentRequest(t, r, http.MethodPost, tc.url, tc.body)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: status = %d, want 500", tc.url, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "pq:") {
			t.Errorf("%s: response leaks the internal error: %q", tc.url, rec.Body.String())
		}
	}
}
