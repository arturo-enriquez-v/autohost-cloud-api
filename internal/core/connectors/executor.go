// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package connectors

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arturo/autohost-cloud-api/internal/platform/safehttp"
)

const (
	// maxResponseBytes bounds how much of a response is read.
	maxResponseBytes = 1 << 20
	userAgent        = "AutoHost-Connector/1.0"
	redacted         = "[REDACTED]"
)

// Result is what running a connector returns: the external API answered (any status code).
type Result struct {
	OK           bool      `json:"ok"`
	StatusCode   int       `json:"status_code"`
	DurationMS   int64     `json:"duration_ms"`
	ContentType  string    `json:"content_type,omitempty"`
	Body         any       `json:"body,omitempty"`
	Truncated    bool      `json:"truncated,omitempty"`
	Mapped       *Mapped   `json:"mapped,omitempty"`
	MappingError string    `json:"mapping_error,omitempty"`
	Cached       bool      `json:"cached"`
	FetchedAt    time.Time `json:"fetched_at"`
}

// Doer sends HTTP requests; *http.Client from safehttp in production.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// buildRequest renders the definition with the param values and the project variables, and attaches
// the secret.
func buildRequest(ctx context.Context, d *Definition, values map[string]string, secret string, vars map[string]resolvedVar) (*http.Request, error) {
	tmplURL, err := effectiveURL(d.URL, vars)
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			// The variable changed after the connector was saved: a problem with this run, not the form.
			return nil, fmt.Errorf("%w: %s", ErrInvalidParams, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
		}
		return nil, err
	}
	if len(vars) > 0 {
		merged := make(map[string]string, len(values)+len(vars))
		for k, v := range values {
			merged[k] = v
		}
		for name, v := range vars {
			merged[varPrefix+name] = v.value
		}
		values = merged
	}
	rawURL, err := renderURL(tmplURL, values)
	if err != nil {
		return nil, err
	}
	body, err := renderBody(d, values)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if d.BodyType != BodyNone {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, d.Method, rawURL, reader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.5")
	switch d.BodyType {
	case BodyJSON:
		req.Header.Set("Content-Type", "application/json")
	case BodyForm:
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	case BodyText:
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	for _, h := range d.Headers {
		v, err := renderHeader(h.Value, values)
		if err != nil {
			return nil, err
		}
		req.Header.Set(h.Name, v)
	}

	if d.Auth.Type != AuthNone && secret == "" {
		return nil, ErrSecretRequired
	}
	switch d.Auth.Type {
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+secret)
	case AuthBasic:
		req.SetBasicAuth(d.Auth.Username, secret)
	case AuthAPIKey:
		if d.Auth.In == APIKeyInQuery {
			q := req.URL.Query()
			q.Set(d.Auth.Name, secret)
			req.URL.RawQuery = q.Encode()
		} else {
			req.Header.Set(d.Auth.Name, secret)
		}
	}
	return req, nil
}

// execute sends the request and reads at most maxResponseBytes of the answer. Occurrences of the secret
// and of secret variables in the response (APIs that echo the request) are replaced, so members who can
// run a connector do not learn its credentials.
func execute(ctx context.Context, client Doer, d *Definition, values map[string]string, secret string, vars map[string]resolvedVar, now time.Time) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(d.TimeoutSeconds)*time.Second)
	defer cancel()

	req, err := buildRequest(ctx, d, values, secret, vars)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, upstreamError(err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, upstreamError(err)
	}
	res := &Result{
		OK:          resp.StatusCode >= 200 && resp.StatusCode < 300,
		StatusCode:  resp.StatusCode,
		DurationMS:  time.Since(start).Milliseconds(),
		ContentType: resp.Header.Get("Content-Type"),
		FetchedAt:   now,
	}
	if len(raw) > maxResponseBytes {
		raw, res.Truncated = raw[:maxResponseBytes], true
	}
	raw = redactSecret(raw, secret, d.Auth)
	for _, v := range vars {
		if v.secret {
			raw = redactSecret(raw, v.value, Auth{Type: AuthNone})
		}
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		return res, nil
	}
	if !res.Truncated && isJSON(res.ContentType, raw) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var body any
		if err := dec.Decode(&body); err == nil {
			res.Body = body
			mapped, err := applyMapping(body, d.Response)
			if err != nil {
				res.MappingError = err.Error()
			}
			res.Mapped = mapped
			return res, nil
		}
	}
	res.Body = strings.ToValidUTF8(string(raw), "�")
	if len(d.Response.Fields) > 0 || d.Response.Root != "" {
		if res.Truncated {
			res.MappingError = "the response is too large to map"
		} else {
			res.MappingError = "the response is not JSON"
		}
	}
	return res, nil
}

func isJSON(contentType string, raw []byte) bool {
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		if mt == "application/json" || strings.HasSuffix(mt, "+json") {
			return true
		}
	}
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && (t[0] == '{' || t[0] == '[') && utf8.Valid(t)
}

// redactSecret hides the secret in the response, also URL-encoded and, for Basic auth, as the encoded
// header value.
func redactSecret(raw []byte, secret string, auth Auth) []byte {
	if len(secret) < 4 {
		return raw
	}
	forms := []string{secret, url.QueryEscape(secret)}
	if auth.Type == AuthBasic {
		forms = append(forms, base64.StdEncoding.EncodeToString([]byte(auth.Username+":"+secret)))
	}
	for _, f := range forms {
		raw = bytes.ReplaceAll(raw, []byte(f), []byte(redacted))
	}
	return raw
}

// upstreamError turns a transport error into a message that is safe to show: it says what kind of
// failure happened without internal details.
func upstreamError(err error) error {
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(err, safehttp.ErrBlockedDestination):
		return fmt.Errorf("%w: the destination address is not allowed", ErrUpstream)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Errorf("%w: the request timed out", ErrUpstream)
	case errors.As(err, &dnsErr):
		return fmt.Errorf("%w: could not resolve the host", ErrUpstream)
	case strings.Contains(err.Error(), "certificate"), strings.Contains(err.Error(), "tls:"):
		return fmt.Errorf("%w: TLS handshake failed", ErrUpstream)
	case strings.Contains(err.Error(), "redirects"):
		return fmt.Errorf("%w: too many redirects", ErrUpstream)
	default:
		return fmt.Errorf("%w: connection failed", ErrUpstream)
	}
}
