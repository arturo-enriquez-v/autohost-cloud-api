// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package connectors

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/arturo/autohost-cloud-api/internal/platform/safehttp"
)

// Limits on a definition.
const (
	maxNameLen        = 100
	maxDescriptionLen = 500
	maxURLLen         = 2048
	maxHeaders        = 30
	maxHeaderValueLen = 4096
	maxBodyLen        = 64 << 10
	maxParams         = 20
	maxParamValueLen  = 2048
	maxFields         = 30
	maxPathLen        = 256
	maxCacheTTL       = 3600
	maxTimeout        = 30
	defaultTimeout    = 15
	maxSecretLen      = 4096
)

var (
	paramNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	headerNameRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,128}$")
	placeholder  = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	// anyBraces finds "{{" so malformed placeholders are reported instead of sent literally.
	anyBraces = regexp.MustCompile(`\{\{[^}]*\}\}`)
)

// Headers the client sets itself; letting users set them would break or smuggle the request.
var forbiddenHeaders = map[string]bool{
	"host": true, "content-length": true, "transfer-encoding": true, "connection": true,
	"te": true, "trailer": true, "upgrade": true, "proxy-authorization": true, "keep-alive": true,
}

var allowedMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodHead: true,
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Normalize fills defaults and canonicalizes a definition before validation.
func (d *Definition) Normalize() {
	d.Name = strings.TrimSpace(d.Name)
	d.Description = strings.TrimSpace(d.Description)
	d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
	d.Method = strings.ToUpper(strings.TrimSpace(d.Method))
	d.URL = strings.TrimSpace(d.URL)
	d.BodyType = strings.ToLower(strings.TrimSpace(d.BodyType))
	if d.BodyType == "" {
		d.BodyType = BodyNone
	}
	if d.BodyType == BodyNone {
		d.Body = ""
	}
	d.Auth.Type = strings.ToLower(strings.TrimSpace(d.Auth.Type))
	if d.Auth.Type == "" {
		d.Auth.Type = AuthNone
	}
	d.Auth.In = strings.ToLower(strings.TrimSpace(d.Auth.In))
	d.Auth.Name = strings.TrimSpace(d.Auth.Name)
	switch d.Auth.Type {
	case AuthNone, AuthBearer:
		d.Auth = Auth{Type: d.Auth.Type}
	case AuthAPIKey:
		if d.Auth.In == "" {
			d.Auth.In = APIKeyInHeader
		}
		d.Auth.Username = ""
	case AuthBasic:
		d.Auth.Name, d.Auth.In = "", ""
	}
	for i := range d.Headers {
		d.Headers[i].Name = strings.TrimSpace(d.Headers[i].Name)
	}
	for i := range d.Params {
		p := &d.Params[i]
		p.Name = strings.TrimSpace(p.Name)
		p.Type = strings.ToLower(strings.TrimSpace(p.Type))
		if p.Type == "" {
			p.Type = ParamString
		}
	}
	d.Response.Root = strings.TrimSpace(d.Response.Root)
	d.Response.View = strings.ToLower(strings.TrimSpace(d.Response.View))
	if d.Response.View == "" {
		d.Response.View = ViewJSON
	}
	for i := range d.Response.Fields {
		d.Response.Fields[i].Path = strings.TrimSpace(d.Response.Fields[i].Path)
		d.Response.Fields[i].Label = strings.TrimSpace(d.Response.Fields[i].Label)
	}
	if d.Kind == KindAction {
		d.CacheTTLSeconds = 0
	}
	if d.TimeoutSeconds == 0 {
		d.TimeoutSeconds = defaultTimeout
	}
	if d.Headers == nil {
		d.Headers = []KeyValue{}
	}
	if d.Params == nil {
		d.Params = []Param{}
	}
	if d.Response.Fields == nil {
		d.Response.Fields = []FieldMapping{}
	}
}

// Validate checks a normalized definition. The project is checked by the caller.
func (d *Definition) Validate() error {
	if d.ProjectID == "" {
		return invalid("project_id is required")
	}
	if d.Name == "" || len(d.Name) > maxNameLen {
		return invalid("name is required (max %d characters)", maxNameLen)
	}
	if len(d.Description) > maxDescriptionLen {
		return invalid("description is too long (max %d characters)", maxDescriptionLen)
	}
	if d.Kind != KindQuery && d.Kind != KindAction {
		return invalid("kind must be 'query' or 'action'")
	}
	if !allowedMethods[d.Method] {
		return invalid("method must be GET, POST, PUT, PATCH, DELETE or HEAD")
	}
	if err := validateURL(d.URL); err != nil {
		return err
	}
	if len(d.Headers) > maxHeaders {
		return invalid("too many headers (max %d)", maxHeaders)
	}
	for _, h := range d.Headers {
		if !headerNameRe.MatchString(h.Name) {
			return invalid("invalid header name %q", h.Name)
		}
		if forbiddenHeaders[strings.ToLower(h.Name)] {
			return invalid("header %q cannot be set", h.Name)
		}
		if len(h.Value) > maxHeaderValueLen || strings.ContainsAny(h.Value, "\r\n\x00") {
			return invalid("invalid value for header %q", h.Name)
		}
	}
	switch d.BodyType {
	case BodyNone, BodyJSON, BodyForm, BodyText:
	default:
		return invalid("body_type must be none, json, form or text")
	}
	if len(d.Body) > maxBodyLen {
		return invalid("body is too long (max %d bytes)", maxBodyLen)
	}
	if err := d.validateParams(); err != nil {
		return err
	}
	if err := d.validateAuth(); err != nil {
		return err
	}
	if err := d.validateResponse(); err != nil {
		return err
	}
	if d.CacheTTLSeconds < 0 || d.CacheTTLSeconds > maxCacheTTL {
		return invalid("cache_ttl_seconds must be between 0 and %d", maxCacheTTL)
	}
	if d.TimeoutSeconds < 1 || d.TimeoutSeconds > maxTimeout {
		return invalid("timeout_seconds must be between 1 and %d", maxTimeout)
	}
	return nil
}

// validateURL requires an absolute http(s) URL whose scheme, host and port are fixed: placeholders may
// only appear in the path and query, so running a connector cannot change the server it talks to.
func validateURL(raw string) error {
	if raw == "" || len(raw) > maxURLLen {
		return invalid("url is required (max %d characters)", maxURLLen)
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return invalid("url must start with http:// or https://")
	}
	if s := strings.ToLower(scheme); s != "http" && s != "https" {
		return invalid("url must start with http:// or https://")
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	if strings.Contains(authority, "{") || strings.Contains(authority, "}") {
		return invalid("placeholders are not allowed in the url host")
	}
	u, err := url.Parse(placeholder.ReplaceAllString(raw, "x"))
	if err != nil || u.Host == "" {
		return invalid("url is not valid")
	}
	if u.User != nil {
		return invalid("put credentials in auth, not in the url")
	}
	if strings.ContainsAny(raw, " \r\n\t\x00") {
		return invalid("url must not contain spaces or control characters")
	}
	if err := safehttp.CheckHost(u.Hostname()); err != nil {
		return invalid("url host is not allowed")
	}
	return nil
}

func (d *Definition) validateParams() error {
	if len(d.Params) > maxParams {
		return invalid("too many params (max %d)", maxParams)
	}
	declared := make(map[string]bool, len(d.Params))
	for _, p := range d.Params {
		if !paramNameRe.MatchString(p.Name) {
			return invalid("invalid param name %q (letters, digits and _)", p.Name)
		}
		if declared[p.Name] {
			return invalid("duplicate param %q", p.Name)
		}
		declared[p.Name] = true
		if p.Type != ParamString && p.Type != ParamNumber && p.Type != ParamBoolean {
			return invalid("param %q: type must be string, number or boolean", p.Name)
		}
		if len(p.Label) > maxNameLen || len(p.Description) > maxDescriptionLen {
			return invalid("param %q: label or description too long", p.Name)
		}
		if p.Default != "" {
			if _, err := normalizeValue(p, p.Default); err != nil {
				return invalid("param %q: default value does not match its type", p.Name)
			}
		}
	}
	// Every placeholder must be a declared param, and "{{" must only appear as a valid placeholder.
	texts := []string{d.URL, d.Body}
	for _, h := range d.Headers {
		texts = append(texts, h.Value)
	}
	for _, t := range texts {
		for _, m := range anyBraces.FindAllString(t, -1) {
			sub := placeholder.FindStringSubmatch(m)
			if sub == nil || sub[0] != m {
				return invalid("malformed placeholder %q", m)
			}
			if !declared[sub[1]] {
				return invalid("placeholder {{%s}} is not a declared param", sub[1])
			}
		}
	}
	return nil
}

func (d *Definition) validateAuth() error {
	switch d.Auth.Type {
	case AuthNone, AuthBearer:
	case AuthAPIKey:
		if d.Auth.In != APIKeyInHeader && d.Auth.In != APIKeyInQuery {
			return invalid("auth.in must be 'header' or 'query'")
		}
		if d.Auth.In == APIKeyInHeader {
			if !headerNameRe.MatchString(d.Auth.Name) || forbiddenHeaders[strings.ToLower(d.Auth.Name)] {
				return invalid("auth.name must be a valid header name")
			}
		} else if !paramNameRe.MatchString(strings.ReplaceAll(d.Auth.Name, "-", "_")) {
			return invalid("auth.name must be a valid query parameter name")
		}
	case AuthBasic:
		if len(d.Auth.Username) > maxNameLen || strings.ContainsAny(d.Auth.Username, ":\r\n") {
			return invalid("auth.username is not valid")
		}
	default:
		return invalid("auth.type must be none, bearer, api_key or basic")
	}
	return nil
}

func (d *Definition) validateResponse() error {
	switch d.Response.View {
	case ViewJSON, ViewTable, ViewValue, ViewList:
	default:
		return invalid("response.view must be json, table, value or list")
	}
	if d.Response.Root != "" {
		if _, err := parsePath(d.Response.Root); err != nil {
			return invalid("response.root: %v", err)
		}
	}
	if len(d.Response.Fields) > maxFields {
		return invalid("too many response fields (max %d)", maxFields)
	}
	for _, f := range d.Response.Fields {
		if f.Label == "" || len(f.Label) > maxNameLen {
			return invalid("every response field needs a label (max %d characters)", maxNameLen)
		}
		if _, err := parsePath(f.Path); err != nil {
			return invalid("response field %q: %v", f.Label, err)
		}
	}
	return nil
}

// normalizeValue checks a value against its param type and returns its canonical text.
func normalizeValue(p Param, v string) (string, error) {
	if len(v) > maxParamValueLen {
		return "", fmt.Errorf("value too long")
	}
	switch p.Type {
	case ParamNumber:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return "", fmt.Errorf("not a number")
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	case ParamBoolean:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return "", fmt.Errorf("not a boolean")
		}
		return strconv.FormatBool(b), nil
	default:
		return v, nil
	}
}

// origin returns the scheme and authority of a validated connector URL.
func origin(raw string) string {
	scheme, rest, _ := strings.Cut(raw, "://")
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return strings.ToLower(scheme) + "://" + strings.ToLower(rest)
}
