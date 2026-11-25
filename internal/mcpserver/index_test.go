// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arturo/autohost-cloud-api/api"
)

func loadSpec(t *testing.T) *Index {
	t.Helper()
	idx, err := LoadIndex(api.OpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

// Every operation of the real spec is indexed and its schema renders.
func TestIndexRealSpec(t *testing.T) {
	idx := loadSpec(t)
	if len(idx.Operations()) < 150 {
		t.Fatalf("only %d operations indexed", len(idx.Operations()))
	}
	var blocked, open int
	for _, op := range idx.Operations() {
		if op.Blocked != "" {
			blocked++
		} else {
			open++
		}
		s := idx.Schema(op)
		if _, err := json.Marshal(s); err != nil {
			t.Errorf("%s: schema does not marshal: %v", op.ID, err)
		}
	}
	t.Logf("%d executable, %d blocked", open, blocked)
}

func TestBlockedOperations(t *testing.T) {
	idx := loadSpec(t)
	byPath := func(method, path string) *Operation {
		for _, op := range idx.Operations() {
			if op.Method == method && op.Path == path {
				return op
			}
		}
		t.Fatalf("%s %s not in spec", method, path)
		return nil
	}
	blocked := map[string]string{
		"POST /v1/auth/login":             "session",
		"POST /v1/api-tokens":             "API tokens",
		"GET /v1/nodes/{nodeId}/terminal": "WebSocket",
		"POST /v1/heartbeats/heartbeat":   "not a user endpoint",
		"GET /health":                     "not a user endpoint",
	}
	for key, want := range blocked {
		m, p, _ := strings.Cut(key, " ")
		op := byPath(m, p)
		if !strings.Contains(op.Blocked, want) {
			t.Errorf("%s: blocked = %q, want it to mention %q", key, op.Blocked, want)
		}
	}
	for _, key := range []string{"GET /v1/auth/me", "GET /v1/nodes", "POST /v1/connectors", "DELETE /v1/connectors/{id}", "GET /v1/service-monitors"} {
		m, p, _ := strings.Cut(key, " ")
		if op := byPath(m, p); op.Blocked != "" {
			t.Errorf("%s should be executable, blocked: %s", key, op.Blocked)
		}
	}
	// Streaming and binary operations.
	for _, op := range idx.Operations() {
		s := idx.Schema(op)
		for _, r := range s.Responses {
			if strings.HasPrefix(r.Status, "2") && (r.ContentType == "text/event-stream" || r.ContentType == "application/octet-stream") && op.Blocked == "" {
				t.Errorf("%s returns %s but is executable", op.ID, r.ContentType)
			}
		}
		if s.RequestBody != nil && s.RequestBody.ContentType != "application/json" && op.Blocked == "" {
			t.Errorf("%s takes %s but is executable", op.ID, s.RequestBody.ContentType)
		}
	}
}

func TestSearch(t *testing.T) {
	idx := loadSpec(t)
	res, total := idx.Search("connector run", "", 5)
	if total == 0 || res[0].ID != "runConnector" {
		t.Fatalf("search 'connector run' = %+v (total %d)", res, total)
	}
	res, _ = idx.Search("", "Connectors", 100)
	for _, op := range res {
		if !hasTag(&op, "connectors") {
			t.Errorf("tag filter returned %s %v", op.ID, op.Tags)
		}
	}
	if len(res) < 6 {
		t.Errorf("tag Connectors: %d operations", len(res))
	}
}

func TestSchemaInlinesRefs(t *testing.T) {
	idx := loadSpec(t)
	op, _ := idx.Operation("createConnector")
	s := idx.Schema(op)
	raw, _ := json.Marshal(s)
	text := string(raw)
	if s.RequestBody == nil || !strings.Contains(text, `"project_id"`) || !strings.Contains(text, `"ConnectorRequest"`) {
		t.Fatalf("request body not inlined: %s", text[:min(len(text), 500)])
	}
	if strings.Contains(text, `"$ref":"#/components/schemas/ConnectorRequest"`) {
		t.Fatal("top-level $ref left unresolved")
	}
	op, _ = idx.Operation("getConnector")
	s = idx.Schema(op)
	if len(s.Parameters) != 1 || s.Parameters[0].Name != "id" || s.Parameters[0].In != "path" {
		t.Fatalf("path-level parameter not merged: %+v", s.Parameters)
	}
}
