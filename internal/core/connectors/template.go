// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package connectors

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ResolveParams checks the caller's values against the declared params (unknown names, types, required
// values) and returns the canonical text of every param, defaults included.
func ResolveParams(params []Param, in map[string]any) (map[string]string, error) {
	byName := make(map[string]Param, len(params))
	for _, p := range params {
		byName[p.Name] = p
	}
	for name := range in {
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("%w: unknown param %q", ErrInvalidParams, name)
		}
	}
	out := make(map[string]string, len(params))
	for _, p := range params {
		raw, given := in[p.Name]
		var text string
		switch v := raw.(type) {
		case nil:
			given = false
		case string:
			text = v
		case bool:
			text = strconv.FormatBool(v)
		case float64:
			text = strconv.FormatFloat(v, 'f', -1, 64)
		case json.Number:
			text = v.String()
		default:
			return nil, fmt.Errorf("%w: param %q must be a string, number or boolean", ErrInvalidParams, p.Name)
		}
		if !given || (text == "" && p.Type != ParamString) {
			text, given = p.Default, p.Default != ""
		}
		if !given || text == "" {
			if p.Required {
				return nil, fmt.Errorf("%w: param %q is required", ErrInvalidParams, p.Name)
			}
			out[p.Name] = ""
			continue
		}
		norm, err := normalizeValue(p, text)
		if err != nil {
			return nil, fmt.Errorf("%w: param %q: %v", ErrInvalidParams, p.Name, err)
		}
		out[p.Name] = norm
	}
	return out, nil
}

// fill replaces every {{name}} in s with escape(name, value).
func fill(s string, values map[string]string, escape func(name, v string) string) string {
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		return escape(name, values[name])
	})
}

// renderURL fills the URL: values are path-escaped before the "?" and query-escaped after it, so a value
// can never add path segments, query parameters or a fragment.
func renderURL(tmpl string, values map[string]string) (string, error) {
	path, query, hasQuery := strings.Cut(tmpl, "?")
	out := fill(path, values, func(_, v string) string { return url.PathEscape(v) })
	if hasQuery {
		out += "?" + fill(query, values, func(_, v string) string { return url.QueryEscape(v) })
	}
	u, err := url.Parse(out)
	if err != nil {
		return "", fmt.Errorf("%w: url is not valid after filling params", ErrInvalidParams)
	}
	u.Fragment = ""
	return u.String(), nil
}

// renderHeader fills a header value; values with line breaks are rejected (header injection).
func renderHeader(tmpl string, values map[string]string) (string, error) {
	var bad bool
	out := fill(tmpl, values, func(_, v string) string {
		if strings.ContainsAny(v, "\r\n\x00") {
			bad = true
		}
		return v
	})
	if bad {
		return "", fmt.Errorf("%w: header values cannot contain line breaks", ErrInvalidParams)
	}
	return out, nil
}

// renderBody fills the body according to its type. In JSON, string params are inserted JSON-escaped
// (write them between quotes in the template) and numbers and booleans as they are; the result must be
// valid JSON.
func renderBody(d *Definition, values map[string]string) (string, error) {
	types := make(map[string]string, len(d.Params))
	for _, p := range d.Params {
		types[p.Name] = p.Type
	}
	switch d.BodyType {
	case BodyNone:
		return "", nil
	case BodyJSON:
		out := fill(d.Body, values, func(name, v string) string {
			// Variables are text, like string params.
			if types[name] == ParamString || strings.HasPrefix(name, varPrefix) {
				b, _ := json.Marshal(v)
				return string(b[1 : len(b)-1])
			}
			if v == "" {
				return "null"
			}
			return v
		})
		if strings.TrimSpace(out) != "" && !json.Valid([]byte(out)) {
			return "", fmt.Errorf("%w: body is not valid JSON after filling params", ErrInvalidParams)
		}
		return out, nil
	case BodyForm:
		return fill(d.Body, values, func(_, v string) string { return url.QueryEscape(v) }), nil
	default:
		return fill(d.Body, values, func(_, v string) string { return v }), nil
	}
}

// loggableParams returns the values to record in the run history, with sensitive params masked.
func loggableParams(params []Param, values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for _, p := range params {
		v, ok := values[p.Name]
		if !ok || v == "" {
			continue
		}
		if p.Sensitive {
			v = "***"
		}
		out[p.Name] = v
	}
	return out
}

// cacheKey identifies a query result: same connector version, same parameter values and same variable
// values (hashed, so secret ones are not kept in clear in the key).
func cacheKey(c *Connector, values map[string]string, vars map[string]resolvedVar) string {
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(c.ID)
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(c.UpdatedAt.UnixNano(), 10))
	for _, k := range names {
		b.WriteByte('|')
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(values[k]))
	}
	if len(vars) > 0 {
		vnames := make([]string, 0, len(vars))
		for k := range vars {
			vnames = append(vnames, k)
		}
		sort.Strings(vnames)
		h := sha256.New()
		for _, k := range vnames {
			h.Write([]byte(url.QueryEscape(k) + "=" + url.QueryEscape(vars[k].value) + "\n"))
		}
		b.WriteString("|vars=")
		b.WriteString(hex.EncodeToString(h.Sum(nil)))
	}
	return b.String()
}
