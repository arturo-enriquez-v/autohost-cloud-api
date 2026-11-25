// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = `Tools for the AutoHost API (self-hosting control plane: nodes, containers, apps, backups, monitors,
networking, projects, connectors). Work in three steps:
1. search_endpoints to find the operation (by keyword or tag).
2. get_endpoint_schema to see its parameters, body and response.
3. execute_endpoint to call it.
Calls run with the permissions of the token this client was configured with. Operations with effect
"write" or "destructive" change the user's servers: confirm with the user before running them.`

// Handler serves MCP over streamable HTTP. It is stateless (no session state between requests), so it
// works behind load balancers and on Cloud Run.
type Handler struct {
	index  *Index
	api    atomic.Pointer[http.Handler]
	server *mcp.Server
	http   http.Handler
}

type callerKey struct{}

// NewHandler builds the MCP server from the OpenAPI spec. Calls fail until SetAPI is called with the
// router they run through.
func NewHandler(spec []byte) (*Handler, error) {
	idx, err := LoadIndex(spec)
	if err != nil {
		return nil, err
	}
	h := &Handler{index: idx}
	version := idx.Version()
	h.server = mcp.NewServer(&mcp.Implementation{Name: "autohost", Title: "AutoHost API", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	h.addTools()
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return h.server }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Requests must carry an Authorization header (AuthIdentity runs before this handler), which a
		// DNS-rebound browser page cannot add, so the localhost Host check is not needed. It would also
		// reject every request behind a reverse proxy on the same host.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        2 << 20,
	})
	h.http = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller := Caller{
			Authorization: r.Header.Get("Authorization"),
			RemoteAddr:    r.RemoteAddr,
			RequestID:     middleware.GetReqID(r.Context()),
		}
		streamable.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, caller)))
	})
	return h, nil
}

// SetAPI sets the handler API calls run through: the root router, so every middleware applies.
func (h *Handler) SetAPI(api http.Handler) { h.api.Store(&api) }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.http.ServeHTTP(w, r) }

// Index returns the operations index.
func (h *Handler) Index() *Index { return h.index }

type searchInput struct {
	Query string `json:"query,omitempty" jsonschema:"words to look for in the operation ID, path, summary, tags and description, e.g. 'restart container' or 'backup'. Empty lists every operation (or every operation of tag)"`
	Tag   string `json:"tag,omitempty" jsonschema:"only operations with this tag, e.g. 'Nodes', 'Containers', 'Backups'"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum results (default 20, max 100)"`
}

type schemaInput struct {
	OperationID string `json:"operation_id" jsonschema:"operation_id returned by search_endpoints"`
}

type executeInput struct {
	OperationID string         `json:"operation_id" jsonschema:"operation_id returned by search_endpoints"`
	Params      map[string]any `json:"params,omitempty" jsonschema:"path, query and header parameters by name, as listed by get_endpoint_schema"`
	Body        any            `json:"body,omitempty" jsonschema:"JSON request body, for operations that take one"`
}

func boolPtr(b bool) *bool { return &b }

func (h *Handler) addTools() {
	mcp.AddTool(h.server, &mcp.Tool{
		Name:        "search_endpoints",
		Title:       "Search AutoHost API endpoints",
		Description: "Find AutoHost API operations by keyword and/or tag. Returns operation_id, method, path, summary, effect (read, write or destructive) and, for operations that cannot run over MCP, why. Without query and tag it also lists the tags.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false)},
	}, h.search)

	mcp.AddTool(h.server, &mcp.Tool{
		Name:        "get_endpoint_schema",
		Title:       "Get an AutoHost endpoint's schema",
		Description: "Parameters, request body and response schema of one AutoHost API operation, with the permission it needs in the description.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false)},
	}, h.schema)

	mcp.AddTool(h.server, &mcp.Tool{
		Name:  "execute_endpoint",
		Title: "Call an AutoHost API endpoint",
		Description: "Call one AutoHost API operation with the configured credentials and return its HTTP status and body. " +
			"Operations whose effect is write or destructive change the user's servers: confirm with the user first.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(false)},
	}, h.execute)
}

func jsonResult(v any, isError bool) *mcp.CallToolResult {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		raw = []byte(fmt.Sprintf(`{"error": %q}`, err.Error()))
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, IsError: isError}
}

func errorResult(err error) *mcp.CallToolResult {
	return jsonResult(map[string]string{"error": err.Error()}, true)
}

func (h *Handler) search(_ context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	results, total := h.index.Search(in.Query, in.Tag, limit)
	out := map[string]any{"total": total, "results": results}
	if total > len(results) {
		out["note"] = fmt.Sprintf("showing %d of %d; refine the query or raise limit", len(results), total)
	}
	if strings.TrimSpace(in.Query) == "" && strings.TrimSpace(in.Tag) == "" {
		tags := h.index.Tags()
		names := make([]string, 0, len(tags))
		for t := range tags {
			names = append(names, t)
		}
		sort.Strings(names)
		out["tags"] = names
	}
	return jsonResult(out, false), nil, nil
}

func (h *Handler) schema(_ context.Context, _ *mcp.CallToolRequest, in schemaInput) (*mcp.CallToolResult, any, error) {
	op, ok := h.index.Operation(strings.TrimSpace(in.OperationID))
	if !ok {
		return errorResult(fmt.Errorf("%w %q: use search_endpoints", ErrUnknownOperation, in.OperationID)), nil, nil
	}
	return jsonResult(h.index.Schema(op), false), nil, nil
}

func (h *Handler) execute(ctx context.Context, req *mcp.CallToolRequest, in executeInput) (*mcp.CallToolResult, any, error) {
	op, ok := h.index.Operation(strings.TrimSpace(in.OperationID))
	if !ok {
		return errorResult(fmt.Errorf("%w %q: use search_endpoints", ErrUnknownOperation, in.OperationID)), nil, nil
	}
	api := h.api.Load()
	if api == nil {
		return errorResult(errors.New("the API is not ready")), nil, nil
	}
	caller, _ := ctx.Value(callerKey{}).(Caller)
	if caller.Authorization == "" && req != nil && req.Extra != nil && req.Extra.Header != nil {
		caller.Authorization = req.Extra.Header.Get("Authorization")
	}
	if caller.Authorization == "" {
		return errorResult(errors.New("missing credentials")), nil, nil
	}
	res, err := Execute(ctx, *api, op, in.Params, in.Body, caller)
	if err != nil {
		return errorResult(err), nil, nil
	}
	log.Printf("[MCP] %s %s %s -> %d", op.ID, op.Method, op.Path, res.Status)
	return jsonResult(res, res.Status >= 400), nil, nil
}
