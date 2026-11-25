// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/platform/secretbox"
)

// --- fakes ---

type memRepo struct {
	mu    sync.Mutex
	items map[string]*Connector
	runs  []*Run
}

func newMemRepo() *memRepo { return &memRepo{items: map[string]*Connector{}} }

func (m *memRepo) Create(_ context.Context, c *Connector) (*Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	cp.HasSecret = cp.SecretCiphertext != ""
	cp.CreatedAt, cp.UpdatedAt = time.Now(), time.Now()
	m.items[c.ID] = &cp
	out := cp
	return &out, nil
}

func (m *memRepo) Get(_ context.Context, id string) (*Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.items[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *memRepo) ListByProjects(_ context.Context, ids []string) ([]*Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Connector
	for _, c := range m.items {
		for _, id := range ids {
			if c.ProjectID == id {
				cp := *c
				out = append(out, &cp)
			}
		}
	}
	return out, nil
}

func (m *memRepo) ListAll(ctx context.Context) ([]*Connector, error) {
	m.mu.Lock()
	var ids []string
	for _, c := range m.items {
		ids = append(ids, c.ProjectID)
	}
	m.mu.Unlock()
	return m.ListByProjects(ctx, ids)
}

func (m *memRepo) Update(_ context.Context, c *Connector) (*Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	cp.HasSecret = cp.SecretCiphertext != ""
	cp.UpdatedAt = time.Now().Add(time.Millisecond)
	m.items[c.ID] = &cp
	out := cp
	return &out, nil
}

func (m *memRepo) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return ErrNotFound
	}
	delete(m.items, id)
	return nil
}

func (m *memRepo) InsertRun(_ context.Context, r *Run, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, r)
	return nil
}

func (m *memRepo) ListRuns(_ context.Context, id string, _ int) ([]*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Run
	for _, r := range m.runs {
		if r.ConnectorID == id {
			out = append(out, r)
		}
	}
	return out, nil
}

func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	b, err := secretbox.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func strp(s string) *string { return &s }

// newTestService uses a plain client: httptest servers listen on loopback, which safehttp blocks.
func newTestService(t *testing.T) (*Service, *memRepo) {
	repo := newMemRepo()
	return NewServiceWith(repo, testBox(t), http.DefaultClient), repo
}

func baseDef(url string) Definition {
	return Definition{ProjectID: "p1", Name: "Users", Kind: KindQuery, Method: "GET", URL: url}
}

// --- validation ---

func TestValidateDefinition(t *testing.T) {
	ok := func(mut func(d *Definition)) Definition {
		d := baseDef("https://api.example.com/users/{{id}}?q={{q}}")
		d.Params = []Param{{Name: "id", Type: "number"}, {Name: "q"}}
		mut(&d)
		d.Normalize()
		return d
	}
	if d := ok(func(*Definition) {}); d.Validate() != nil {
		t.Fatalf("valid definition rejected: %v", d.Validate())
	}
	bad := map[string]func(d *Definition){
		"placeholder in host":    func(d *Definition) { d.URL = "https://{{q}}.example.com/x" },
		"placeholder in port":    func(d *Definition) { d.URL = "https://api.example.com:{{id}}/x" },
		"undeclared placeholder": func(d *Definition) { d.URL = "https://api.example.com/{{nope}}" },
		"malformed placeholder":  func(d *Definition) { d.Body, d.BodyType = `{"a":"{{ q-1 }}"}`, "json" },
		"ftp scheme":             func(d *Definition) { d.URL = "ftp://example.com/" },
		"no scheme":              func(d *Definition) { d.URL = "example.com/x" },
		"userinfo":               func(d *Definition) { d.URL = "https://u:p@example.com/" },
		"localhost":              func(d *Definition) { d.URL = "http://localhost:5432/" },
		"metadata host":          func(d *Definition) { d.URL = "http://metadata.google.internal/" },
		"bad method":             func(d *Definition) { d.Method = "TRACE" },
		"bad kind":               func(d *Definition) { d.Kind = "stream" },
		"host header":            func(d *Definition) { d.Headers = []KeyValue{{Name: "Host", Value: "x"}} },
		"header newline":         func(d *Definition) { d.Headers = []KeyValue{{Name: "X-A", Value: "a\r\nX-B: b"}} },
		"bad header name":        func(d *Definition) { d.Headers = []KeyValue{{Name: "X A", Value: "a"}} },
		"duplicate param":        func(d *Definition) { d.Params = append(d.Params, Param{Name: "q"}) },
		"bad param type":         func(d *Definition) { d.Params[1].Type = "date" },
		"bad default":            func(d *Definition) { d.Params[0].Default = "abc" },
		"api key without name":   func(d *Definition) { d.Auth = Auth{Type: AuthAPIKey, In: "header"} },
		"bad auth":               func(d *Definition) { d.Auth = Auth{Type: "oauth2"} },
		"bad view":               func(d *Definition) { d.Response.View = "chart" },
		"bad field path":         func(d *Definition) { d.Response.Fields = []FieldMapping{{Label: "A", Path: "a..b"}} },
		"field without label":    func(d *Definition) { d.Response.Fields = []FieldMapping{{Path: "a"}} },
		"timeout too long":       func(d *Definition) { d.TimeoutSeconds = 120 },
		"cache too long":         func(d *Definition) { d.CacheTTLSeconds = 99999 },
		"missing name":           func(d *Definition) { d.Name = " " },
		"missing project":        func(d *Definition) { d.ProjectID = "" },
	}
	for name, mut := range bad {
		d := ok(mut)
		if err := d.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestNormalizeActionHasNoCache(t *testing.T) {
	d := baseDef("https://api.example.com/")
	d.Kind, d.CacheTTLSeconds = KindAction, 60
	d.Normalize()
	if d.CacheTTLSeconds != 0 || d.TimeoutSeconds != defaultTimeout || d.Auth.Type != AuthNone {
		t.Fatalf("normalize: %+v", d)
	}
}

// --- params and templates ---

func TestResolveParams(t *testing.T) {
	params := []Param{
		{Name: "name", Type: ParamString, Required: true},
		{Name: "count", Type: ParamNumber, Default: "10"},
		{Name: "force", Type: ParamBoolean},
	}
	got, err := ResolveParams(params, map[string]any{"name": "a b", "force": true})
	if err != nil {
		t.Fatal(err)
	}
	if got["name"] != "a b" || got["count"] != "10" || got["force"] != "true" {
		t.Fatalf("got %v", got)
	}
	got, err = ResolveParams(params, map[string]any{"name": "x", "count": json.Number("2.50"), "force": "false"})
	if err != nil || got["count"] != "2.5" || got["force"] != "false" {
		t.Fatalf("got %v, %v", got, err)
	}
	for name, in := range map[string]map[string]any{
		"missing required": {"count": 1.0},
		"unknown":          {"name": "x", "other": "y"},
		"bad number":       {"name": "x", "count": "ten"},
		"bad bool":         {"name": "x", "force": "maybe"},
		"object":           {"name": map[string]any{}},
		"too long":         {"name": strings.Repeat("a", maxParamValueLen+1)},
	} {
		if _, err := ResolveParams(params, in); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s: err = %v, want ErrInvalidParams", name, err)
		}
	}
}

func TestRenderURLEscapesValues(t *testing.T) {
	got, err := renderURL("https://api.example.com/users/{{id}}/x?q={{q}}&n=1#frag", map[string]string{
		"id": "../admin?x=1#", "q": "a&b=c d",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.example.com/users/..%2Fadmin%3Fx=1%23/x?q=a%26b%3Dc+d&n=1"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestRenderHeaderRejectsLineBreaks(t *testing.T) {
	if _, err := renderHeader("Bearer {{t}}", map[string]string{"t": "x\r\nEvil: 1"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderJSONBody(t *testing.T) {
	d := &Definition{BodyType: BodyJSON, Body: `{"name":"{{name}}","n":{{n}},"on":{{on}},"opt":{{opt}}}`, Params: []Param{
		{Name: "name", Type: ParamString}, {Name: "n", Type: ParamNumber}, {Name: "on", Type: ParamBoolean}, {Name: "opt", Type: ParamNumber},
	}}
	got, err := renderBody(d, map[string]string{"name": `x", "admin": true, "y":"`, "n": "3", "on": "false", "opt": ""})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("invalid JSON %s: %v", got, err)
	}
	if m["name"] != `x", "admin": true, "y":"` || m["admin"] != nil || m["n"] != 3.0 || m["on"] != false || m["opt"] != nil {
		t.Fatalf("body injection or wrong values: %s", got)
	}

	d.Body = `{"name": {{name}}}` // string param without quotes
	if _, err := renderBody(d, map[string]string{"name": "x"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v, want ErrInvalidParams", err)
	}
}

// --- mapping ---

func TestParsePathAndExtract(t *testing.T) {
	var body any
	_ = json.Unmarshal([]byte(`{"data":{"items":[{"name":"a","tags":["x","y"]},{"name":"b","tags":[]}],"total":2}}`), &body)
	cases := map[string]any{
		"data.total":            2.0,
		"data.items[0].name":    "a",
		"data.items[-1].name":   "b",
		"data.items[].name":     []any{"a", "b"},
		"data.items[0].tags[1]": "y",
		"data.missing":          nil,
		"data.items[9].name":    nil,
		"data.total.x":          nil,
	}
	for path, want := range cases {
		steps, err := parsePath(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		got, _ := json.Marshal(extract(body, steps))
		w, _ := json.Marshal(want)
		if string(got) != string(w) {
			t.Errorf("%s = %s, want %s", path, got, w)
		}
	}
	for _, bad := range []string{"", ".a", "a.", "a..b", "a[", "a[x]", "a]b", "a[0]b", "a.[0]"} {
		if _, err := parsePath(bad); err == nil {
			t.Errorf("parsePath(%q) accepted", bad)
		}
	}
}

func TestApplyMapping(t *testing.T) {
	var body any
	_ = json.Unmarshal([]byte(`{"results":[{"id":1,"user":{"name":"ana"}},{"id":2,"user":{"name":"bob"}}]}`), &body)
	m, err := applyMapping(body, ResponseMapping{Root: "results", Fields: []FieldMapping{{Label: "ID", Path: "id"}, {Label: "User", Path: "user.name"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(m)
	if want := `{"columns":["ID","User"],"rows":[[1,"ana"],[2,"bob"]]}`; string(got) != want {
		t.Fatalf("got %s", got)
	}
	m, _ = applyMapping(body, ResponseMapping{Root: "results[0].user.name"})
	if got, _ := json.Marshal(m); string(got) != `{"columns":["value"],"rows":[["ana"]]}` {
		t.Fatalf("single value: %s", got)
	}
	if m, _ := applyMapping(body, ResponseMapping{View: ViewJSON}); m != nil {
		t.Fatalf("no mapping should give nil, got %+v", m)
	}
}

// --- execution ---

func TestRunSendsRequestWithAuthAndParams(t *testing.T) {
	var gotReq *http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		// Echo the credentials back, like httpbin does.
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"n": "a"}}, "echo": r.Header.Get("X-Api-Key"), "q": r.URL.RawQuery})
	}))
	defer srv.Close()

	svc, _ := newTestService(t)
	in := Input{Definition: baseDef(srv.URL + "/v1/{{path}}?limit={{limit}}")}
	in.Kind, in.Method = KindAction, "POST"
	in.Params = []Param{{Name: "path"}, {Name: "limit", Type: ParamNumber, Default: "5"}, {Name: "msg", Sensitive: true}}
	in.Headers = []KeyValue{{Name: "X-Trace", Value: "t-{{path}}"}}
	in.BodyType, in.Body = BodyJSON, `{"msg":"{{msg}}"}`
	in.Auth = Auth{Type: AuthAPIKey, Name: "X-Api-Key", In: "header"}
	in.Response = ResponseMapping{Root: "items", Fields: []FieldMapping{{Label: "N", Path: "n"}}}
	in.Secret = strp("super-secret-key")
	c, err := svc.Create(context.Background(), in, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasSecret || strings.Contains(c.SecretCiphertext, "super-secret") {
		t.Fatalf("secret not stored encrypted: %+v", c)
	}
	res, err := svc.Run(context.Background(), c, map[string]any{"path": "a b", "msg": "hi"}, "u2")
	if err != nil {
		t.Fatal(err)
	}
	if gotReq.Method != "POST" || gotReq.URL.Path != "/v1/a b" || gotReq.URL.RawQuery != "limit=5" {
		t.Errorf("request: %s %s ?%s", gotReq.Method, gotReq.URL.Path, gotReq.URL.RawQuery)
	}
	if gotReq.Header.Get("X-Api-Key") != "super-secret-key" || gotReq.Header.Get("X-Trace") != "t-a b" || gotReq.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers: %v", gotReq.Header)
	}
	if gotBody != `{"msg":"hi"}` {
		t.Errorf("body: %s", gotBody)
	}
	if !res.OK || res.StatusCode != 200 || res.Mapped == nil || res.Mapped.Rows[0][0] != "a" {
		t.Fatalf("result: %+v", res)
	}
	if b, _ := json.Marshal(res.Body); strings.Contains(string(b), "super-secret-key") {
		t.Fatalf("secret echoed back in the result: %s", b)
	}
}

func TestRunAuthVariants(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r }))
	defer srv.Close()
	svc, _ := newTestService(t)
	cases := []struct {
		auth  Auth
		check func(r *http.Request) bool
	}{
		{Auth{Type: AuthBearer}, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tok-123" }},
		{Auth{Type: AuthBasic, Username: "me"}, func(r *http.Request) bool { u, p, ok := r.BasicAuth(); return ok && u == "me" && p == "tok-123" }},
		{Auth{Type: AuthAPIKey, Name: "api_key", In: "query"}, func(r *http.Request) bool { return r.URL.Query().Get("api_key") == "tok-123" }},
	}
	for _, c := range cases {
		in := Input{Definition: baseDef(srv.URL + "/x"), Secret: strp("tok-123")}
		in.Name, in.Auth = "c-"+c.auth.Type, c.auth
		conn, err := svc.Create(context.Background(), in, "u1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Run(context.Background(), conn, nil, "u1"); err != nil {
			t.Fatal(err)
		}
		if !c.check(got) {
			t.Errorf("%s: auth not sent correctly: %v %s", c.auth.Type, got.Header, got.URL)
		}
	}
}

func TestRunWithoutSecretFails(t *testing.T) {
	svc, _ := newTestService(t)
	in := Input{Definition: baseDef("https://api.example.com/")}
	in.Auth = Auth{Type: AuthBearer}
	c, err := svc.Create(context.Background(), in, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(context.Background(), c, nil, "u1"); !errors.Is(err, ErrSecretRequired) {
		t.Fatalf("err = %v, want ErrSecretRequired", err)
	}
}

func TestCreateSecretWithoutKey(t *testing.T) {
	svc := NewServiceWith(newMemRepo(), nil, http.DefaultClient)
	in := Input{Definition: baseDef("https://api.example.com/"), Secret: strp("x")}
	in.Auth = Auth{Type: AuthBearer}
	if _, err := svc.Create(context.Background(), in, "u1"); !errors.Is(err, ErrSecretsNotConfigured) {
		t.Fatalf("err = %v, want ErrSecretsNotConfigured", err)
	}
}

func TestUpdateSecretRules(t *testing.T) {
	svc, _ := newTestService(t)
	in := Input{Definition: baseDef("https://api.example.com/v1"), Secret: strp("tok")}
	in.Auth = Auth{Type: AuthBearer}
	c, err := svc.Create(context.Background(), in, "u1")
	if err != nil {
		t.Fatal(err)
	}

	// Same origin, no secret: kept.
	upd := Input{Definition: c.Definition}
	upd.URL = "https://api.example.com/v2?x=1"
	c2, err := svc.Update(context.Background(), c, upd)
	if err != nil || !c2.HasSecret || c2.SecretCiphertext != c.SecretCiphertext {
		t.Fatalf("same origin: %+v, %v", c2, err)
	}

	// Other host or scheme without a new secret: rejected, so the stored token cannot be redirected.
	for _, u := range []string{"https://attacker.example.net/v1", "http://api.example.com/v1", "https://api.example.com:8443/v1"} {
		upd.URL = u
		if _, err := svc.Update(context.Background(), c2, upd); !errors.Is(err, ErrSecretOriginChanged) {
			t.Errorf("%s: err = %v, want ErrSecretOriginChanged", u, err)
		}
	}

	// Other host with a new secret: allowed.
	upd.URL, upd.Secret = "https://other.example.net/v1", strp("tok2")
	c3, err := svc.Update(context.Background(), c2, upd)
	if err != nil || !c3.HasSecret {
		t.Fatalf("new secret: %+v, %v", c3, err)
	}
	if s, _ := svc.openSecret(c3); s != "tok2" {
		t.Fatalf("secret = %q", s)
	}

	// Clearing.
	upd.Secret = strp("")
	c4, err := svc.Update(context.Background(), c3, upd)
	if err != nil || c4.HasSecret {
		t.Fatalf("clear: %+v, %v", c4, err)
	}

	// The project cannot change.
	upd.ProjectID, upd.Secret = "p2", nil
	c5, err := svc.Update(context.Background(), c4, upd)
	if err != nil || c5.ProjectID != "p1" {
		t.Fatalf("project changed: %+v, %v", c5, err)
	}
}

func TestTestUsesStoredSecretOnlyOnSameOrigin(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { auth = r.Header.Get("Authorization") }))
	defer srv.Close()
	svc, _ := newTestService(t)
	in := Input{Definition: baseDef(srv.URL + "/a"), Secret: strp("stored")}
	in.Auth = Auth{Type: AuthBearer}
	c, err := svc.Create(context.Background(), in, "u1")
	if err != nil {
		t.Fatal(err)
	}
	draft := Input{Definition: c.Definition}
	draft.URL = srv.URL + "/b"
	if _, err := svc.Test(context.Background(), draft, c, nil); err != nil || auth != "Bearer stored" {
		t.Fatalf("same origin test: auth=%q err=%v", auth, err)
	}
	draft.URL = "https://attacker.example.net/"
	if _, err := svc.Test(context.Background(), draft, c, nil); !errors.Is(err, ErrSecretOriginChanged) {
		t.Fatalf("other origin test: err = %v", err)
	}
}

func TestQueryCacheAndActionHistory(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("fail") == "1" {
			w.WriteHeader(500)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	svc, repo := newTestService(t)

	q := Input{Definition: baseDef(srv.URL + "/?k={{k}}")}
	q.Params = []Param{{Name: "k"}}
	q.CacheTTLSeconds = 60
	qc, _ := svc.Create(context.Background(), q, "u1")
	r1, _ := svc.Run(context.Background(), qc, map[string]any{"k": "a"}, "u1")
	r2, _ := svc.Run(context.Background(), qc, map[string]any{"k": "a"}, "u2")
	_, _ = svc.Run(context.Background(), qc, map[string]any{"k": "b"}, "u1")
	if calls != 2 || r1.Cached || !r2.Cached {
		t.Fatalf("calls=%d cached=%v/%v", calls, r1.Cached, r2.Cached)
	}
	svc.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if r, _ := svc.Run(context.Background(), qc, map[string]any{"k": "a"}, "u1"); r.Cached || calls != 3 {
		t.Fatalf("expired entry served: calls=%d", calls)
	}
	if len(repo.runs) != 0 {
		t.Fatalf("query runs should not be recorded, got %d", len(repo.runs))
	}

	a := Input{Definition: baseDef(srv.URL + "/?fail={{fail}}")}
	a.Name, a.Kind, a.Method = "Restart", KindAction, "POST"
	a.Params = []Param{{Name: "fail"}, {Name: "pin", Sensitive: true}}
	ac, _ := svc.Create(context.Background(), a, "u1")
	_, _ = svc.Run(context.Background(), ac, map[string]any{"fail": "1", "pin": "1234"}, "u2")
	_, _ = svc.Run(context.Background(), ac, map[string]any{"fail": "0"}, "u2")
	if len(repo.runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(repo.runs))
	}
	if r := repo.runs[0]; r.OK || r.StatusCode != 500 || r.UserID != "u2" || r.Params["pin"] != "***" {
		t.Fatalf("run[0] = %+v", r)
	}
	if !repo.runs[1].OK {
		t.Fatalf("run[1] = %+v", repo.runs[1])
	}
}

func TestResponseLimitsAndNonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = w.Write(bytes.Repeat([]byte("a"), maxResponseBytes+10))
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	svc, _ := newTestService(t)
	d := baseDef(srv.URL + "/big")
	d.Response = ResponseMapping{Root: "x"}
	c, _ := svc.Create(context.Background(), Input{Definition: d}, "u1")
	res, err := svc.Run(context.Background(), c, nil, "u1")
	if err != nil || !res.Truncated || len(res.Body.(string)) != maxResponseBytes || res.MappingError == "" {
		t.Fatalf("big: truncated=%v len=%d mappingErr=%q err=%v", res.Truncated, len(res.Body.(string)), res.MappingError, err)
	}
	d.Name, d.URL = "text", srv.URL+"/text"
	c, _ = svc.Create(context.Background(), Input{Definition: d}, "u1")
	res, _ = svc.Run(context.Background(), c, nil, "u1")
	if res.Body != "hello" || res.MappingError != "the response is not JSON" {
		t.Fatalf("text: %+v", res)
	}
}

func TestUpstreamErrorsAreGeneric(t *testing.T) {
	repo := newMemRepo()
	// Real safe client: a loopback URL is blocked at dial time.
	svc, err := func() (*Service, error) {
		t.Setenv("SECRETS_ENCRYPTION_KEY", "")
		return NewService(repo)
	}()
	if err != nil {
		t.Fatal(err)
	}
	c := &Connector{ID: "00000000-0000-0000-0000-000000000001", Definition: baseDef("http://127.0.0.1:1/")}
	c.Normalize()
	_, err = svc.Run(context.Background(), c, nil, "u1")
	if !errors.Is(err, ErrUpstream) || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v", err)
	}
}
