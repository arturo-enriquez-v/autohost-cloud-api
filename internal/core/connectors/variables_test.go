// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type memVarRepo struct {
	mu    sync.Mutex
	items map[string]*Variable
}

func newMemVarRepo() *memVarRepo { return &memVarRepo{items: map[string]*Variable{}} }

func (m *memVarRepo) ListVariables(_ context.Context, projectID string) ([]*Variable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Variable
	for _, v := range m.items {
		if v.ProjectID == projectID {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *memVarRepo) GetVariable(_ context.Context, id string) (*Variable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[id]
	if !ok {
		return nil, ErrVariableNotFound
	}
	cp := *v
	return &cp, nil
}

func (m *memVarRepo) CreateVariable(_ context.Context, v *Variable) (*Variable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.items {
		if e.ProjectID == v.ProjectID && e.Name == v.Name && e.ID != v.ID {
			return nil, ErrVariableExists
		}
	}
	cp := *v
	m.items[v.ID] = &cp
	out := cp
	return &out, nil
}

func (m *memVarRepo) UpdateVariable(ctx context.Context, v *Variable) (*Variable, error) {
	return m.CreateVariable(ctx, v)
}

func (m *memVarRepo) DeleteVariable(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func newVarTestService(t *testing.T) (*Service, *memRepo) {
	svc, repo := newTestService(t)
	svc.WithVariables(newMemVarRepo())
	return svc, repo
}

func mustVar(t *testing.T, svc *Service, in VariableInput) *Variable {
	t.Helper()
	if in.ProjectID == "" {
		in.ProjectID = "p1"
	}
	v, err := svc.CreateVariable(context.Background(), in, "u1")
	if err != nil {
		t.Fatalf("create variable %s: %v", in.Name, err)
	}
	return v
}

// echoServer answers with what it received, like httpbin, so tests can check what was sent and whether
// secrets come back.
func echoServer(t *testing.T, hits *int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"path": r.URL.Path, "auth": r.Header.Get("Authorization")})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVariablesAreSentAndSecretsHidden(t *testing.T) {
	srv := echoServer(t, nil)
	svc, _ := newVarTestService(t)
	mustVar(t, svc, VariableInput{Name: "ZONE", Value: strp("z 1")})
	tok := mustVar(t, svc, VariableInput{Name: "TOKEN", Secret: true, Value: strp("tok-shared-999"), AllowedOrigins: []string{srv.URL + "/"}})
	if tok.Value != "" || tok.ValueCipher == "" || strings.Contains(tok.ValueCipher, "tok-shared") || !tok.HasValue {
		t.Fatalf("secret variable not stored encrypted: %+v", tok)
	}

	in := Input{Definition: baseDef(srv.URL + "/zones/{{vars.ZONE}}")}
	in.Headers = []KeyValue{{Name: "Authorization", Value: "Bearer {{vars.TOKEN}}"}}
	c, err := svc.Create(context.Background(), in, "u1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Run(context.Background(), c, nil, "u1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(res.Body)
	if !strings.Contains(string(body), `"path":"/zones/z 1"`) {
		t.Errorf("plain variable not in the path: %s", body)
	}
	if strings.Contains(string(body), "tok-shared-999") || !strings.Contains(string(body), "Bearer "+redacted) {
		t.Errorf("secret variable not sent or not hidden in the response: %s", body)
	}
}

func TestSecretVariableOnlyGoesToAllowedOrigins(t *testing.T) {
	hits := 0
	srv := echoServer(t, &hits)
	svc, _ := newVarTestService(t)
	tok := mustVar(t, svc, VariableInput{Name: "TOKEN", Secret: true, Value: strp("tok-1234"), AllowedOrigins: []string{srv.URL}})

	// Saving a connector that would send it elsewhere is refused.
	in := Input{Definition: baseDef("https://evil.example.com/steal?t={{vars.TOKEN}}")}
	if _, err := svc.Create(context.Background(), in, "u1"); !errors.Is(err, ErrVariableOrigin) {
		t.Fatalf("create to another origin: err = %v, want ErrVariableOrigin", err)
	}

	// Allowed now; after the origin is removed from the variable, running it is refused before sending.
	in = Input{Definition: baseDef(srv.URL + "/x?t={{vars.TOKEN}}")}
	c, err := svc.Create(context.Background(), in, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateVariable(context.Background(), tok, VariableInput{Name: "TOKEN", Secret: true, Value: strp("tok-1234"), AllowedOrigins: []string{"https://api.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(context.Background(), c, nil, "u1"); !errors.Is(err, ErrVariableOrigin) {
		t.Fatalf("run: err = %v, want ErrVariableOrigin", err)
	}
	if hits != 0 {
		t.Fatalf("the request was sent %d times", hits)
	}
}

func TestBaseURLVariable(t *testing.T) {
	hits := 0
	srv := echoServer(t, &hits)
	svc, _ := newVarTestService(t)
	api := mustVar(t, svc, VariableInput{Name: "API", Value: strp(srv.URL + "/")})
	mustVar(t, svc, VariableInput{Name: "TOKEN", Secret: true, Value: strp("tok-1234"), AllowedOrigins: []string{srv.URL}})

	in := Input{Definition: baseDef("{{vars.API}}/v1/users")}
	in.Headers = []KeyValue{{Name: "Authorization", Value: "Bearer {{vars.TOKEN}}"}}
	c, err := svc.Create(context.Background(), in, "u1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Run(context.Background(), c, nil, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res.Body); !strings.Contains(string(b), `"path":"/v1/users"`) {
		t.Errorf("base url not applied: %s", b)
	}

	// Pointing the base URL elsewhere does not take the secret variable with it.
	if _, err := svc.UpdateVariable(context.Background(), api, VariableInput{Name: "API", Value: strp("https://evil.example.com")}); err != nil {
		t.Fatal(err)
	}
	hits = 0
	if _, err := svc.Run(context.Background(), c, nil, "u1"); !errors.Is(err, ErrVariableOrigin) || hits != 0 {
		t.Fatalf("run after moving the base: err = %v, hits = %d", err, hits)
	}

	// A connector with its own credentials cannot take its host from a variable.
	own := Input{Definition: baseDef("{{vars.API}}/x"), Secret: strp("own-secret")}
	own.Name, own.Auth = "own", Auth{Type: AuthBearer}
	if _, err := svc.Create(context.Background(), own, "u1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("own credentials with a base variable: err = %v, want ErrInvalid", err)
	}
	// The base variable cannot be secret, and what follows must be a path.
	for _, u := range []string{"{{vars.TOKEN}}/x", "{{vars.API}}evil.com/x"} {
		bad := Input{Definition: baseDef(u)}
		bad.Name = "bad"
		if _, err := svc.Create(context.Background(), bad, "u1"); !errors.Is(err, ErrInvalid) {
			t.Errorf("url %s: err = %v, want ErrInvalid", u, err)
		}
	}
}

func TestUndefinedVariableIsRefused(t *testing.T) {
	svc, _ := newVarTestService(t)
	// Defined in another project only.
	mustVar(t, svc, VariableInput{ProjectID: "p2", Name: "TOKEN", Value: strp("x")})
	in := Input{Definition: baseDef("https://api.example.com/{{vars.TOKEN}}")}
	if _, err := svc.Create(context.Background(), in, "u1"); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v, want ErrInvalidParams", err)
	}
}

func TestVariableUpdateRules(t *testing.T) {
	svc, _ := newVarTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateVariable(ctx, VariableInput{ProjectID: "p1", Name: "T", Secret: true, Value: strp("x")}, "u1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("secret without origins: err = %v", err)
	}
	if _, err := svc.CreateVariable(ctx, VariableInput{ProjectID: "p1", Name: "T", Secret: true, AllowedOrigins: []string{"https://a.example.com"}}, "u1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("secret without value: err = %v", err)
	}
	for _, o := range []string{"ftp://a.example.com", "https://a.example.com/path", "a.example.com"} {
		if _, err := svc.CreateVariable(ctx, VariableInput{ProjectID: "p1", Name: "T", Secret: true, Value: strp("x"), AllowedOrigins: []string{o}}, "u1"); !errors.Is(err, ErrInvalid) {
			t.Errorf("origin %q: err = %v", o, err)
		}
	}
	v := mustVar(t, svc, VariableInput{Name: "T", Secret: true, Value: strp("x"), AllowedOrigins: []string{"https://A.example.com/"}})
	if len(v.AllowedOrigins) != 1 || v.AllowedOrigins[0] != "https://a.example.com" {
		t.Errorf("origins not normalized: %v", v.AllowedOrigins)
	}

	keep := func(secret bool, origins ...string) VariableInput {
		return VariableInput{Name: "T", Secret: secret, AllowedOrigins: origins}
	}
	if _, err := svc.UpdateVariable(ctx, v, keep(true, "https://a.example.com", "https://b.example.com")); !errors.Is(err, ErrVariableValueRequired) {
		t.Errorf("adding an origin without the value: err = %v", err)
	}
	if _, err := svc.UpdateVariable(ctx, v, keep(false)); !errors.Is(err, ErrVariableValueRequired) {
		t.Errorf("secret to plain without the value: err = %v", err)
	}
	if _, err := svc.UpdateVariable(ctx, v, keep(true, "https://a.example.com")); err != nil {
		t.Errorf("same origins without the value: %v", err)
	}
	withValue := keep(true, "https://a.example.com", "https://b.example.com")
	withValue.Value = strp("y")
	if _, err := svc.UpdateVariable(ctx, v, withValue); err != nil {
		t.Errorf("adding an origin with the value: %v", err)
	}
	if _, err := svc.CreateVariable(ctx, VariableInput{ProjectID: "p1", Name: "bad-name", Value: strp("x")}, "u1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad name: err = %v", err)
	}
	if _, err := svc.CreateVariable(ctx, VariableInput{ProjectID: "p1", Name: "N", Value: strp("a\nb")}, "u1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("line break in value: err = %v", err)
	}
}

func TestVariableInUseCannotBeDeletedOrRenamed(t *testing.T) {
	svc, _ := newVarTestService(t)
	ctx := context.Background()
	v := mustVar(t, svc, VariableInput{Name: "ZONE", Value: strp("z")})
	in := Input{Definition: baseDef("https://api.example.com/{{vars.ZONE}}")}
	c, err := svc.Create(ctx, in, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVariable(ctx, v); !errors.Is(err, ErrVariableInUse) || !strings.Contains(err.Error(), "Users") {
		t.Errorf("delete: err = %v", err)
	}
	if _, err := svc.UpdateVariable(ctx, v, VariableInput{Name: "ZONE2"}); !errors.Is(err, ErrVariableInUse) {
		t.Errorf("rename: err = %v", err)
	}
	if _, err := svc.UpdateVariable(ctx, v, VariableInput{Name: "ZONE", Description: "the zone"}); err != nil {
		t.Errorf("same name: %v", err)
	}
	if err := svc.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVariable(ctx, v); err != nil {
		t.Errorf("delete once unused: %v", err)
	}
}

func TestQueryCacheFollowsVariableValues(t *testing.T) {
	hits := 0
	srv := echoServer(t, &hits)
	svc, _ := newVarTestService(t)
	ctx := context.Background()
	v := mustVar(t, svc, VariableInput{Name: "ZONE", Value: strp("a")})
	in := Input{Definition: baseDef(srv.URL + "/{{vars.ZONE}}")}
	in.CacheTTLSeconds = 60
	c, err := svc.Create(ctx, in, "u1")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := svc.Run(ctx, c, nil, "u1"); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1 (second run cached)", hits)
	}
	if _, err := svc.UpdateVariable(ctx, v, VariableInput{Name: "ZONE", Value: strp("b")}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Run(ctx, c, nil, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if hits != 2 || res.Cached {
		t.Fatalf("a new variable value used the cached result (hits %d)", hits)
	}
}

func TestFolderNormalizeAndValidate(t *testing.T) {
	d := baseDef("https://api.example.com/")
	d.Folder = "  Cloudflare / /DNS/ "
	d.Normalize()
	if d.Folder != "Cloudflare/DNS" {
		t.Errorf("folder = %q", d.Folder)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("valid folder: %v", err)
	}
	for _, f := range []string{"a/b/c/d/e/f", strings.Repeat("x", 51), "a\tb"} {
		d.Folder = f
		if err := d.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("folder %q: err = %v", f, err)
		}
	}
}
