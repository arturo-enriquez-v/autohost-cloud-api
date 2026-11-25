// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

// Package mcpserver serves the REST API to MCP clients (Claude Code, Claude Desktop, IDEs) at /mcp. It
// is built from the embedded OpenAPI spec and exposes three meta-tools instead of one tool per endpoint,
// so a client discovers what it needs without loading ~190 tool definitions: search_endpoints,
// get_endpoint_schema and execute_endpoint. Calls run in process through the API's own router, with the
// caller's credentials, so authentication, permissions, token scopes and rate limits apply exactly as
// over HTTP.
package mcpserver

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Effects of an operation, from its method.
const (
	EffectRead        = "read"
	EffectWrite       = "write"
	EffectDestructive = "destructive"
)

// Param is one parameter of an operation.
type Param struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
	Schema      any    `json:"schema,omitempty"`
}

// Operation is one documented endpoint.
type Operation struct {
	ID          string   `json:"operation_id"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"-"`
	Tags        []string `json:"tags,omitempty"`
	Effect      string   `json:"effect"`
	// Blocked says why the operation cannot be executed over MCP (empty when it can).
	Blocked string `json:"not_executable,omitempty"`

	params          []Param
	bodyRequired    bool
	bodyContentType string
	bodySchema      any
	responses       map[string]any
}

// Index holds the operations of the spec.
type Index struct {
	root map[string]any
	ops  []*Operation
	byID map[string]*Operation
}

var methods = []string{"get", "post", "put", "patch", "delete", "head"}

// LoadIndex parses the OpenAPI document.
func LoadIndex(spec []byte) (*Index, error) {
	var root map[string]any
	if err := yaml.Unmarshal(spec, &root); err != nil {
		return nil, fmt.Errorf("parse openapi spec: %w", err)
	}
	paths, _ := root["paths"].(map[string]any)
	if len(paths) == 0 {
		return nil, fmt.Errorf("openapi spec has no paths")
	}
	idx := &Index{root: root, byID: map[string]*Operation{}}
	defaultSecurity := root["security"]

	for path, rawItem := range paths {
		item, _ := rawItem.(map[string]any)
		shared := idx.params(item["parameters"])
		for _, m := range methods {
			rawOp, ok := item[m].(map[string]any)
			if !ok {
				continue
			}
			op := &Operation{
				ID:          str(rawOp["operationId"]),
				Method:      strings.ToUpper(m),
				Path:        path,
				Summary:     str(rawOp["summary"]),
				Description: str(rawOp["description"]),
				Effect:      effectOf(strings.ToUpper(m)),
			}
			if op.ID == "" {
				return nil, fmt.Errorf("%s %s has no operationId", op.Method, path)
			}
			if _, dup := idx.byID[op.ID]; dup {
				return nil, fmt.Errorf("duplicate operationId %s", op.ID)
			}
			for _, t := range list(rawOp["tags"]) {
				op.Tags = append(op.Tags, str(t))
			}
			op.params = mergeParams(shared, idx.params(rawOp["parameters"]))
			if rb, ok := idx.deref(rawOp["requestBody"]).(map[string]any); ok {
				op.bodyRequired, _ = rb["required"].(bool)
				content, _ := rb["content"].(map[string]any)
				op.bodyContentType = firstKey(content)
				if media, ok := content[op.bodyContentType].(map[string]any); ok {
					op.bodySchema = media["schema"]
				}
			}
			op.responses, _ = rawOp["responses"].(map[string]any)
			security, hasSecurity := rawOp["security"]
			if !hasSecurity {
				security = defaultSecurity
			}
			op.Blocked = idx.blockedReason(op, security)
			idx.ops = append(idx.ops, op)
			idx.byID[op.ID] = op
		}
	}
	sort.Slice(idx.ops, func(i, j int) bool {
		if idx.ops[i].Path != idx.ops[j].Path {
			return idx.ops[i].Path < idx.ops[j].Path
		}
		return idx.ops[i].Method < idx.ops[j].Method
	})
	return idx, nil
}

// Operation returns an operation by ID.
func (idx *Index) Operation(id string) (*Operation, bool) {
	op, ok := idx.byID[id]
	return op, ok
}

// Version is the spec's info.version.
func (idx *Index) Version() string {
	info, _ := idx.root["info"].(map[string]any)
	if v := str(info["version"]); v != "" {
		return v
	}
	return "dev"
}

// Operations returns every operation, sorted by path and method.
func (idx *Index) Operations() []*Operation { return idx.ops }

func effectOf(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead:
		return EffectRead
	case http.MethodDelete:
		return EffectDestructive
	default:
		return EffectWrite
	}
}

// blockedPrefixes are paths that are never executed over MCP, whatever the spec says about them.
var blockedPrefixes = []struct{ prefix, reason string }{
	{"/mcp", "the MCP endpoint itself"},
	{"/v1/auth/login", "session endpoints (login, refresh, logout, OAuth) are not available over MCP"},
	{"/v1/auth/logout", "session endpoints (login, refresh, logout, OAuth) are not available over MCP"},
	{"/v1/auth/refresh", "session endpoints (login, refresh, logout, OAuth) are not available over MCP"},
	{"/v1/auth/register", "session endpoints (login, refresh, logout, OAuth) are not available over MCP"},
	{"/v1/auth/oauth/", "session endpoints (login, refresh, logout, OAuth) are not available over MCP"},
	{"/v1/api-tokens", "API tokens can only be managed from a browser session"},
}

// blockedReason says why an operation cannot run over MCP: it does not accept user bearer tokens
// (agent, share or public routes), streams, upgrades to WebSocket, or sends or returns binary data.
func (idx *Index) blockedReason(op *Operation, security any) string {
	for _, b := range blockedPrefixes {
		if op.Path == b.prefix || strings.HasPrefix(op.Path, strings.TrimSuffix(b.prefix, "/")+"/") {
			return b.reason
		}
	}
	if !acceptsBearer(security) {
		return "not a user endpoint (public, agent or share-token route)"
	}
	if op.bodyContentType != "" && op.bodyContentType != "application/json" {
		return "the request body is " + op.bodyContentType + " (uploads are not supported over MCP)"
	}
	for code, raw := range op.responses {
		if code == "101" {
			return "upgrades to a WebSocket"
		}
		resp, _ := idx.deref(raw).(map[string]any)
		content, _ := resp["content"].(map[string]any)
		for ct := range content {
			switch {
			case ct == "text/event-stream":
				return "streams server-sent events"
			case ct == "application/octet-stream", !(ct == "application/json" || strings.HasPrefix(ct, "text/") || strings.HasSuffix(ct, "+json")):
				if strings.HasPrefix(code, "2") {
					return "returns binary data (" + ct + ")"
				}
			}
		}
	}
	return ""
}

func acceptsBearer(security any) bool {
	for _, req := range list(security) {
		if m, ok := req.(map[string]any); ok {
			if _, ok := m["bearerAuth"]; ok {
				return true
			}
		}
	}
	return false
}

// params resolves a list of parameters ($ref to components/parameters included).
func (idx *Index) params(raw any) []Param {
	var out []Param
	for _, r := range list(raw) {
		p, ok := idx.deref(r).(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Param{
			Name:        str(p["name"]),
			In:          str(p["in"]),
			Required:    p["required"] == true,
			Description: str(p["description"]),
			Schema:      p["schema"],
		})
	}
	return out
}

// mergeParams lets operation parameters override path-level ones with the same name and location.
func mergeParams(shared, own []Param) []Param {
	out := append([]Param{}, own...)
	for _, s := range shared {
		dup := false
		for _, o := range own {
			if o.Name == s.Name && o.In == s.In {
				dup = true
			}
		}
		if !dup {
			out = append(out, s)
		}
	}
	return out
}

// deref follows a top-level $ref, if any.
func (idx *Index) deref(v any) any {
	for i := 0; i < 10; i++ {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return v
		}
		v = idx.lookup(ref)
	}
	return v
}

// lookup resolves a local JSON pointer like "#/components/schemas/Node".
func (idx *Index) lookup(ref string) any {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var cur any = idx.root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func firstKey(m map[string]any) string {
	if _, ok := m["application/json"]; ok {
		return "application/json"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}
