// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package server_test

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/arturo/autohost-cloud-api/api"
)

// pendingDocPrefixes lists route prefixes whose module is not documented in api/openapi.yaml yet. Routes
// under them are allowed to be missing from the spec. Remove a prefix once its module is documented; the
// test flags prefixes that are already fully covered so this list only shrinks.
var pendingDocPrefixes = []string{
	"/v1/plans",
	"/v1/caddy-routes",
	"/v1/cloudflare-tunnel",
	"/v1/marketplace",
	"/v1/webhooks",
	"/v1/telegram-bots",
	"/v1/external-logs",
	"/v1/external-log-tokens",
	"/v1/vpn",
	"/v1/log-diagnostics",
	"/v1/notifications",
	"/v1/agent/network-stats",
	"/v1/node-network",
}

var pathParam = regexp.MustCompile(`\{[^}]*\}`)

// normalizeRoute makes chi patterns and OpenAPI paths comparable: no trailing slash, and every path
// parameter collapsed to {} so a chi regexp ({id:[0-9]+}) or a renamed parameter does not cause a mismatch.
func normalizeRoute(path string) string {
	if path != "/" {
		path = strings.TrimSuffix(path, "/")
	}
	return pathParam.ReplaceAllString(path, "{}")
}

func underPrefix(route, prefix string) bool {
	return route == prefix || strings.HasPrefix(route, prefix+"/")
}

func loadSpecOperations(t *testing.T) map[string]bool {
	t.Helper()
	var doc struct {
		OpenAPI string                          `yaml:"openapi"`
		Paths   map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(api.OpenAPISpec, &doc); err != nil {
		t.Fatalf("api/openapi.yaml is not valid YAML: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		t.Fatalf("api/openapi.yaml: unexpected openapi version %q", doc.OpenAPI)
	}
	methods := map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true, "head": true, "options": true}
	ops := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			if methods[method] {
				ops[strings.ToUpper(method)+" "+normalizeRoute(path)] = true
			}
		}
	}
	return ops
}

// TestOpenAPICoversRouter fails when a registered route is missing from api/openapi.yaml (outside the
// pending prefixes), when the spec documents a route the router does not serve, or when a pending prefix
// is already fully documented.
func TestOpenAPICoversRouter(t *testing.T) {
	specOps := loadSpecOperations(t)
	r := newRouterForInspection()

	routerOps := map[string]bool{}
	pendingHasUndocumented := map[string]bool{}
	var undocumented []string

	err := chi.Walk(r.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + normalizeRoute(route)
		routerOps[key] = true
		if specOps[key] {
			return nil
		}
		for _, prefix := range pendingDocPrefixes {
			if underPrefix(normalizeRoute(route), normalizeRoute(prefix)) {
				pendingHasUndocumented[prefix] = true
				return nil
			}
		}
		undocumented = append(undocumented, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(undocumented)
	for _, op := range undocumented {
		t.Errorf("route %s is not documented in api/openapi.yaml", op)
	}

	var stale []string
	for op := range specOps {
		if !routerOps[op] {
			stale = append(stale, op)
		}
	}
	sort.Strings(stale)
	for _, op := range stale {
		t.Errorf("api/openapi.yaml documents %s, but the router does not serve it", op)
	}

	for _, prefix := range pendingDocPrefixes {
		if !pendingHasUndocumented[prefix] {
			t.Errorf("every route under %s is documented: remove it from pendingDocPrefixes", prefix)
		}
	}
}

// TestOpenAPIRefsResolve fails when a local $ref in api/openapi.yaml points to a component that does not exist.
func TestOpenAPIRefsResolve(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal(api.OpenAPISpec, &root); err != nil {
		t.Fatalf("api/openapi.yaml is not valid YAML: %v", err)
	}

	lookup := func(ref string) bool {
		node := root.Content[0]
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			if node.Kind != yaml.MappingNode {
				return false
			}
			var next *yaml.Node
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == part {
					next = node.Content[i+1]
					break
				}
			}
			if next == nil {
				return false
			}
			node = next
		}
		return true
	}

	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "$ref" {
					ref := n.Content[i+1].Value
					if !strings.HasPrefix(ref, "#/") {
						t.Errorf("$ref %q (line %d): only local references are supported", ref, n.Content[i+1].Line)
					} else if !lookup(ref) {
						t.Errorf("$ref %q (line %d) does not resolve", ref, n.Content[i+1].Line)
					}
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&root)
}
