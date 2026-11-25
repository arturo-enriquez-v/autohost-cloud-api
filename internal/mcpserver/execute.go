// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// maxResultBytes bounds the response body handed back to the model.
	maxResultBytes = 100 << 10
	// executeTimeout bounds one API call made over MCP.
	executeTimeout = 60 * time.Second
	userAgent      = "AutoHost-MCP/1.0"
)

var (
	ErrUnknownOperation = errors.New("unknown operation_id")
	ErrBlocked          = errors.New("operation not available over MCP")
	ErrBadArguments     = errors.New("invalid arguments")
)

// Caller is the identity of the MCP client, copied to every API call made for it.
type Caller struct {
	Authorization string
	RemoteAddr    string
	RequestID     string
}

// Result is what execute_endpoint returns.
type Result struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Body        any    `json:"body,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// buildRequest turns tool arguments into an HTTP request for op: path parameters are escaped into the
// path, query and header parameters set, and the body sent as JSON. Unknown parameter names are
// rejected so a typo is reported instead of silently ignored.
func buildRequest(ctx context.Context, op *Operation, params map[string]any, body any, caller Caller) (*http.Request, error) {
	path := op.Path
	query := url.Values{}
	header := http.Header{}
	known := map[string]bool{}
	for _, p := range op.params {
		known[p.Name] = true
		v, given := params[p.Name]
		if !given || v == nil {
			if p.Required {
				return nil, fmt.Errorf("%w: missing required %s parameter %q", ErrBadArguments, p.In, p.Name)
			}
			continue
		}
		switch p.In {
		case "path":
			s, err := scalar(v)
			if err != nil || s == "" {
				return nil, fmt.Errorf("%w: path parameter %q must be a non-empty string or number", ErrBadArguments, p.Name)
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(s))
		case "query":
			if arr, ok := v.([]any); ok {
				for _, item := range arr {
					s, err := scalar(item)
					if err != nil {
						return nil, fmt.Errorf("%w: query parameter %q: %v", ErrBadArguments, p.Name, err)
					}
					query.Add(p.Name, s)
				}
				continue
			}
			s, err := scalar(v)
			if err != nil {
				return nil, fmt.Errorf("%w: query parameter %q: %v", ErrBadArguments, p.Name, err)
			}
			query.Set(p.Name, s)
		case "header":
			if blockedHeader(p.Name) {
				continue
			}
			s, err := scalar(v)
			if err != nil || strings.ContainsAny(s, "\r\n") {
				return nil, fmt.Errorf("%w: header parameter %q is not valid", ErrBadArguments, p.Name)
			}
			header.Set(p.Name, s)
		}
	}
	var unknown []string
	for name := range params {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("%w: unknown parameters %s (see get_endpoint_schema)", ErrBadArguments, strings.Join(unknown, ", "))
	}
	if strings.Contains(path, "{") {
		return nil, fmt.Errorf("%w: missing path parameters in %s", ErrBadArguments, op.Path)
	}

	var reader io.Reader
	switch {
	case body != nil && op.bodyContentType == "":
		return nil, fmt.Errorf("%w: %s takes no request body", ErrBadArguments, op.ID)
	case body != nil:
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%w: body is not valid JSON", ErrBadArguments)
		}
		reader = bytes.NewReader(raw)
		header.Set("Content-Type", "application/json")
	case op.bodyRequired:
		return nil, fmt.Errorf("%w: %s requires a JSON body", ErrBadArguments, op.ID)
	}

	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadArguments, err)
	}
	if req.Body == nil {
		req.Body = http.NoBody // as the HTTP server does: handlers may read the body of any request
	}
	req.Header = header
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", caller.Authorization)
	if caller.RequestID != "" {
		req.Header.Set("X-Request-Id", caller.RequestID)
	}
	// The call keeps the MCP client's address, already resolved by TrustedRealIP: rate limits and logs
	// see the real caller.
	req.RemoteAddr = caller.RemoteAddr
	req.RequestURI = req.URL.RequestURI()
	return req, nil
}

// Credentials and connection headers come only from the MCP session.
func blockedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "cookie", "host", "content-length", "transfer-encoding", "connection", "upgrade",
		"x-forwarded-for", "x-real-ip", "x-forwarded-proto", "x-forwarded-host":
		return true
	}
	return false
}

func scalar(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case json.Number:
		return t.String(), nil
	case int:
		return strconv.Itoa(t), nil
	default:
		return "", fmt.Errorf("must be a string, number or boolean")
	}
}

// Execute runs op through handler (the API's root router) as the caller.
func Execute(ctx context.Context, handler http.Handler, op *Operation, params map[string]any, body any, caller Caller) (*Result, error) {
	if op.Blocked != "" {
		return nil, fmt.Errorf("%w: %s", ErrBlocked, op.Blocked)
	}
	ctx, cancel := detachedContext(ctx, executeTimeout)
	defer cancel()
	req, err := buildRequest(ctx, op, params, body, caller)
	if err != nil {
		return nil, err
	}
	rec := newRecorder(maxResultBytes)
	handler.ServeHTTP(rec, req)

	res := &Result{Status: rec.status(), ContentType: rec.header.Get("Content-Type"), Truncated: rec.truncated}
	raw := bytes.TrimSpace(rec.body.Bytes())
	if len(raw) == 0 {
		return res, nil
	}
	if !rec.truncated && strings.Contains(res.ContentType, "json") {
		var v any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&v) == nil {
			res.Body = v
			return res, nil
		}
	}
	res.Body = strings.ToValidUTF8(string(raw), "�")
	return res, nil
}

// detachedContext returns a context with none of parent's values, cancelled when parent is or after
// timeout. The API call must start clean: the /mcp request's context carries chi's routing state (method
// POST, matched path), which chi would reuse and route the call to the wrong handler, and its auth
// values, which only the call's own middlewares may set.
func detachedContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	stop := context.AfterFunc(parent, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// recorder is an in-memory ResponseWriter that keeps at most limit bytes of the body.
type recorder struct {
	header    http.Header
	code      int
	body      bytes.Buffer
	limit     int
	truncated bool
}

func newRecorder(limit int) *recorder { return &recorder{header: http.Header{}, limit: limit} }

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	if room := r.limit - r.body.Len(); room < len(p) {
		if room > 0 {
			r.body.Write(p[:room])
		}
		r.truncated = true
		return len(p), nil
	}
	return r.body.Write(p)
}

func (r *recorder) status() int {
	if r.code == 0 {
		return http.StatusOK
	}
	return r.code
}
