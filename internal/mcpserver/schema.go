// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package mcpserver

import (
	"sort"
	"strings"
)

// maxInlineDepth bounds how many nested $refs are inlined in a schema; deeper ones (and cycles) are left
// as {"$ref": ...} so a client can ask for them by name.
const maxInlineDepth = 6

// resolve returns a copy of v with local $refs inlined, up to maxInlineDepth and without cycles.
func (idx *Index) resolve(v any, depth int, seen map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok {
			name := ref[strings.LastIndex(ref, "/")+1:]
			if depth >= maxInlineDepth || seen[ref] {
				return map[string]any{"$ref": ref, "x-schema-name": name}
			}
			target := idx.lookup(ref)
			if target == nil {
				return map[string]any{"$ref": ref}
			}
			next := make(map[string]bool, len(seen)+1)
			for k := range seen {
				next[k] = true
			}
			next[ref] = true
			out := idx.resolve(target, depth+1, next)
			if m, ok := out.(map[string]any); ok {
				if _, has := m["title"]; !has && strings.Contains(ref, "/schemas/") {
					m["title"] = name
				}
			}
			return out
		}
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = idx.resolve(val, depth, seen)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = idx.resolve(val, depth, seen)
		}
		return out
	default:
		return v
	}
}

// EndpointSchema is what get_endpoint_schema returns.
type EndpointSchema struct {
	Operation
	Description string          `json:"description,omitempty"`
	Parameters  []Param         `json:"parameters"`
	RequestBody *RequestBody    `json:"request_body,omitempty"`
	Responses   []ResponseBrief `json:"responses"`
}

// RequestBody describes the JSON body an operation takes.
type RequestBody struct {
	Required    bool   `json:"required"`
	ContentType string `json:"content_type"`
	Schema      any    `json:"schema,omitempty"`
}

// ResponseBrief summarizes one response; only the success responses carry their schema.
type ResponseBrief struct {
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Schema      any    `json:"schema,omitempty"`
}

// Schema returns an operation's parameters, body and responses with $refs inlined.
func (idx *Index) Schema(op *Operation) *EndpointSchema {
	out := &EndpointSchema{Operation: *op, Description: op.Description, Parameters: []Param{}}
	for _, p := range op.params {
		p.Schema = idx.resolve(p.Schema, 0, map[string]bool{})
		out.Parameters = append(out.Parameters, p)
	}
	if op.bodyContentType != "" {
		out.RequestBody = &RequestBody{
			Required:    op.bodyRequired,
			ContentType: op.bodyContentType,
			Schema:      idx.resolve(op.bodySchema, 0, map[string]bool{}),
		}
	}
	codes := make([]string, 0, len(op.responses))
	for code := range op.responses {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		resp, _ := idx.deref(op.responses[code]).(map[string]any)
		brief := ResponseBrief{Status: code, Description: firstLine(str(resp["description"]))}
		content, _ := resp["content"].(map[string]any)
		brief.ContentType = firstKey(content)
		if strings.HasPrefix(code, "2") {
			if media, ok := content[brief.ContentType].(map[string]any); ok {
				brief.Schema = idx.resolve(media["schema"], 0, map[string]bool{})
			}
		}
		out.Responses = append(out.Responses, brief)
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// SearchResult is one match of search_endpoints.
type SearchResult struct {
	Operation
	Score int `json:"-"`
}

// Search ranks operations by how many query words appear in their ID, path, summary, tags and
// description (the first four weigh more). An empty query lists everything (optionally by tag).
func (idx *Index) Search(query, tag string, limit int) (results []Operation, total int) {
	words := strings.Fields(strings.ToLower(query))
	tag = strings.ToLower(strings.TrimSpace(tag))
	var matches []SearchResult
	for _, op := range idx.ops {
		if tag != "" && !hasTag(op, tag) {
			continue
		}
		score := 0
		if len(words) > 0 {
			id := strings.ToLower(op.ID)
			strong := strings.ToLower(op.ID + " " + op.Path + " " + op.Summary + " " + strings.Join(op.Tags, " "))
			weak := strings.ToLower(op.Description)
			for _, w := range words {
				switch {
				case strings.Contains(strong, w):
					score += 3
					if strings.Contains(id, w) {
						score++ // the operation ID names what it does: break ties toward it
					}
				case strings.Contains(weak, w):
					score++
				}
			}
			if score == 0 {
				continue
			}
		}
		matches = append(matches, SearchResult{Operation: *op, Score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	total = len(matches)
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	results = make([]Operation, 0, len(matches))
	for _, m := range matches {
		results = append(results, m.Operation)
	}
	return results, total
}

func hasTag(op *Operation, tag string) bool {
	for _, t := range op.Tags {
		if strings.EqualFold(t, tag) || strings.Contains(strings.ToLower(t), tag) {
			return true
		}
	}
	return false
}

// Tags returns every tag with its number of operations.
func (idx *Index) Tags() map[string]int {
	out := map[string]int{}
	for _, op := range idx.ops {
		for _, t := range op.Tags {
			out[t]++
		}
	}
	return out
}
