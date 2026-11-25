// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arturo/autohost-cloud-api/api"
)

// seen records what the fake API received.
type seen struct {
	method, path, rawQuery, auth, remote, contentType, body string
}

// newTestAPI is a stand-in for the root router with a few real paths of the spec.
func newTestAPI(got *seen) http.Handler {
	r := chi.NewRouter()
	record := func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		*got = seen{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, req.Header.Get("Authorization"), req.RemoteAddr, req.Header.Get("Content-Type"), string(b)}
	}
	r.Get("/v1/connectors/{id}", func(w http.ResponseWriter, req *http.Request) {
		record(w, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + chi.URLParam(req, "id") + `","name":"x"}`))
	})
	// Same path as listConnectors with another method: a call must reach the handler of its own method.
	r.Get("/v1/connectors", func(w http.ResponseWriter, req *http.Request) {
		record(w, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	r.Post("/v1/connectors", func(w http.ResponseWriter, req *http.Request) {
		record(w, req)
		w.WriteHeader(http.StatusTeapot)
	})
	r.Post("/v1/connectors/{id}/run", func(w http.ResponseWriter, req *http.Request) {
		record(w, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden: requires connectors:run"}`))
	})
	r.Get("/v1/connectors/{id}/runs", func(w http.ResponseWriter, req *http.Request) {
		record(w, req)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", maxResultBytes+10)))
	})
	return r
}

// connect starts the MCP handler behind an HTTP server and returns a connected client session. The
// wrapper sets RemoteAddr as TrustedRealIP would.
func connect(t *testing.T, got *seen) *mcp.ClientSession {
	t.Helper()
	h, err := NewHandler(api.OpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	// Mounted in a chi router as in production, so the /mcp request carries chi's routing context.
	root := chi.NewRouter()
	root.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.RemoteAddr = "203.0.113.7:5555" // as TrustedRealIP leaves it
			next.ServeHTTP(w, r)
		})
	})
	root.Post("/mcp", h.ServeHTTP)
	h.SetAPI(newTestAPI(got))
	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)

	httpClient := &http.Client{Transport: headerTransport{"Bearer ahp_test-token"}}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

type headerTransport struct{ auth string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", h.auth)
	return http.DefaultTransport.RoundTrip(r)
}

func call(t *testing.T, sess *mcp.ClientSession, tool string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: result is not JSON: %s", tool, text)
	}
	return out, res.IsError
}

func TestMCPListsThreeTools(t *testing.T) {
	sess := connect(t, &seen{})
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "execute_endpoint,get_endpoint_schema,search_endpoints" && len(names) != 3 {
		t.Fatalf("tools = %v", names)
	}
}

func TestMCPSearchAndSchema(t *testing.T) {
	sess := connect(t, &seen{})
	out, isErr := call(t, sess, "search_endpoints", map[string]any{"query": "delete connector"})
	if isErr || out["total"].(float64) == 0 {
		t.Fatalf("search: %v", out)
	}
	first := out["results"].([]any)[0].(map[string]any)
	if first["operation_id"] != "deleteConnector" || first["effect"] != "destructive" {
		t.Fatalf("first result = %v", first)
	}
	out, _ = call(t, sess, "search_endpoints", map[string]any{})
	if len(out["tags"].([]any)) < 5 {
		t.Fatalf("empty search should list tags: %v", out["tags"])
	}

	out, isErr = call(t, sess, "get_endpoint_schema", map[string]any{"operation_id": "updateConnector"})
	if isErr || out["request_body"] == nil || len(out["parameters"].([]any)) != 1 {
		t.Fatalf("schema: %v", out)
	}
	if _, isErr = call(t, sess, "get_endpoint_schema", map[string]any{"operation_id": "nope"}); !isErr {
		t.Fatal("unknown operation should be an error")
	}
}

func TestMCPExecuteForwardsCallerAndEscapes(t *testing.T) {
	got := &seen{}
	sess := connect(t, got)
	out, isErr := call(t, sess, "execute_endpoint", map[string]any{
		"operation_id": "getConnector",
		"params":       map[string]any{"id": "a/../b?x=1"},
	})
	if isErr || out["status"].(float64) != 200 {
		t.Fatalf("execute: %v", out)
	}
	if got.method != "GET" || got.path != "/v1/connectors/a%2F..%2Fb%3Fx=1" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.auth != "Bearer ahp_test-token" || got.remote != "203.0.113.7:5555" {
		t.Errorf("caller not forwarded: auth=%q remote=%q", got.auth, got.remote)
	}
	// Still one path segment for the router (chi hands it over escaped, as it does over HTTP).
	body := out["body"].(map[string]any)
	if body["id"] != "a%2F..%2Fb%3Fx=1" {
		t.Errorf("body = %v", body)
	}
}

func TestMCPExecuteBodyQueryAndErrors(t *testing.T) {
	got := &seen{}
	sess := connect(t, got)

	// A 4xx answer is returned as a tool error, with the API's message.
	out, isErr := call(t, sess, "execute_endpoint", map[string]any{
		"operation_id": "runConnector",
		"params":       map[string]any{"id": "c1"},
		"body":         map[string]any{"params": map[string]any{"n": 1}},
	})
	if !isErr || out["status"].(float64) != 403 || got.body != `{"params":{"n":1}}` || got.contentType != "application/json" {
		t.Fatalf("run: isErr=%v out=%v body=%q ct=%q", isErr, out, got.body, got.contentType)
	}

	// Query parameters and truncation of large answers.
	out, _ = call(t, sess, "execute_endpoint", map[string]any{
		"operation_id": "listConnectorRuns",
		"params":       map[string]any{"id": "c1", "limit": 5},
	})
	if got.rawQuery != "limit=5" || out["truncated"] != true || len(out["body"].(string)) != maxResultBytes {
		t.Fatalf("runs: query=%q truncated=%v len=%d", got.rawQuery, out["truncated"], len(out["body"].(string)))
	}

	for name, args := range map[string]map[string]any{
		"blocked":          {"operation_id": "login"},
		"blocked terminal": {"operation_id": findID(t, "GET", "/v1/nodes/{nodeId}/terminal"), "params": map[string]any{"nodeId": "n1"}},
		"unknown param":    {"operation_id": "getConnector", "params": map[string]any{"id": "c1", "idd": "x"}},
		"missing path":     {"operation_id": "getConnector"},
		"body not allowed": {"operation_id": "getConnector", "params": map[string]any{"id": "c1"}, "body": map[string]any{"a": 1}},
		"unknown op":       {"operation_id": "doesNotExist"},
	} {
		*got = seen{}
		out, isErr := call(t, sess, "execute_endpoint", args)
		if !isErr || out["error"] == nil {
			t.Errorf("%s: want a tool error, got %v", name, out)
		}
		if got.method != "" {
			t.Errorf("%s: the API was called (%s %s)", name, got.method, got.path)
		}
	}
}

func findID(t *testing.T, method, path string) string {
	t.Helper()
	idx, _ := LoadIndex(api.OpenAPISpec)
	for _, op := range idx.Operations() {
		if op.Method == method && op.Path == path {
			return op.ID
		}
	}
	t.Fatalf("%s %s not found", method, path)
	return ""
}

// Regression: the call used to inherit the /mcp request's context, and chi reused its routing state
// (POST), so GET /v1/connectors ran the POST handler.
func TestMCPExecuteUsesItsOwnMethod(t *testing.T) {
	got := &seen{}
	sess := connect(t, got)
	out, isErr := call(t, sess, "execute_endpoint", map[string]any{"operation_id": "listConnectors"})
	if isErr || got.method != http.MethodGet || out["status"].(float64) != 200 {
		t.Fatalf("listConnectors reached %s (status %v)", got.method, out["status"])
	}
}
