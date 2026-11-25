// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package connectors

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxMappedRows bounds the rows a mapping returns.
const maxMappedRows = 500

// step is one element of a path: a key, an index, or [] (every item).
type step struct {
	key   string
	index int
	kind  byte // 'k' key, 'i' index, 'a' all
}

// parsePath parses paths like "data.items[].name", "results[0].id", "[0]" or "meta.total".
func parsePath(p string) ([]step, error) {
	if p == "" {
		return nil, errors.New("empty path")
	}
	if len(p) > maxPathLen {
		return nil, fmt.Errorf("path too long (max %d characters)", maxPathLen)
	}
	var steps []step
	i := 0
	for i < len(p) {
		switch c := p[i]; {
		case c == '.':
			if i == 0 || i == len(p)-1 || p[i+1] == '.' || p[i+1] == '[' {
				return nil, fmt.Errorf("unexpected '.' in %q", p)
			}
			i++
		case c == '[':
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("missing ']' in %q", p)
			}
			inner := p[i+1 : i+end]
			if inner == "" {
				steps = append(steps, step{kind: 'a'})
			} else {
				n, err := strconv.Atoi(inner)
				if err != nil {
					return nil, fmt.Errorf("index %q is not a number", inner)
				}
				steps = append(steps, step{kind: 'i', index: n})
			}
			i += end + 1
			if i < len(p) && p[i] != '.' && p[i] != '[' {
				return nil, fmt.Errorf("unexpected %q after ']' in %q", p[i], p)
			}
		default:
			j := i
			for j < len(p) && p[j] != '.' && p[j] != '[' {
				if p[j] == ']' {
					return nil, fmt.Errorf("unexpected ']' in %q", p)
				}
				j++
			}
			steps = append(steps, step{kind: 'k', key: p[i:j]})
			i = j
		}
	}
	return steps, nil
}

// extract applies a path to a decoded JSON value. After a [] step the rest of the path applies to every
// item and the result is a list. A missing key or index yields nil.
func extract(v any, steps []step) any {
	for n, s := range steps {
		switch s.kind {
		case 'k':
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[s.key]
		case 'i':
			arr, ok := v.([]any)
			if !ok {
				return nil
			}
			idx := s.index
			if idx < 0 {
				idx += len(arr)
			}
			if idx < 0 || idx >= len(arr) {
				return nil
			}
			v = arr[idx]
		case 'a':
			arr, ok := v.([]any)
			if !ok {
				return nil
			}
			out := make([]any, 0, len(arr))
			for _, item := range arr {
				out = append(out, extract(item, steps[n+1:]))
			}
			return out
		}
	}
	return v
}

// Mapped is the response turned into rows for widgets.
type Mapped struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated,omitempty"`
}

// applyMapping turns a decoded JSON body into rows. It returns nil when the connector has no mapping.
// If Root selects a list, each item is a row; otherwise the selected value is the only row. Without
// fields there is a single "value" column holding the item itself.
func applyMapping(body any, m ResponseMapping) (*Mapped, error) {
	if m.Root == "" && len(m.Fields) == 0 {
		return nil, nil
	}
	root := body
	if m.Root != "" {
		steps, err := parsePath(m.Root)
		if err != nil {
			return nil, err
		}
		root = extract(body, steps)
	}
	items, isList := root.([]any)
	if !isList {
		items = []any{root}
	}
	out := &Mapped{Rows: [][]any{}}
	if len(items) > maxMappedRows {
		items, out.Truncated = items[:maxMappedRows], true
	}
	if len(m.Fields) == 0 {
		out.Columns = []string{"value"}
		for _, it := range items {
			out.Rows = append(out.Rows, []any{it})
		}
		return out, nil
	}
	paths := make([][]step, len(m.Fields))
	for i, f := range m.Fields {
		out.Columns = append(out.Columns, f.Label)
		steps, err := parsePath(f.Path)
		if err != nil {
			return nil, err
		}
		paths[i] = steps
	}
	for _, it := range items {
		row := make([]any, len(paths))
		for i, steps := range paths {
			row[i] = extract(it, steps)
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
