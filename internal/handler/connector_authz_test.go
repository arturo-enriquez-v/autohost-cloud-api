// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/connectors"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/platform/secretbox"
)

const (
	projA = "00000000-0000-4000-8000-00000000000a"
	projB = "00000000-0000-4000-8000-00000000000b"
)

// --- fakes ---

type connProjectRepo struct{ projects map[string]*apps.Project }

func (m *connProjectRepo) Create(p *apps.Project) (*apps.Project, error) { return p, nil }
func (m *connProjectRepo) FindByID(id string) (*apps.Project, error) {
	if p, ok := m.projects[id]; ok {
		return p, nil
	}
	return nil, apps.ErrProjectNotFound
}
func (m *connProjectRepo) FindByUserID(userID string) ([]*apps.Project, error) {
	var out []*apps.Project
	for _, p := range m.projects {
		if p.UserID == userID {
			out = append(out, p)
		}
	}
	return out, nil
}
func (m *connProjectRepo) Update(p *apps.Project) (*apps.Project, error) { return p, nil }
func (m *connProjectRepo) Delete(string) error                           { return nil }

// connRBACRepo: active members, memberships keyed "user:project". Unlisted methods panic.
type connRBACRepo struct {
	identity.RBACRepository
	members map[string]*identity.ProjectMembership
}

func (m *connRBACRepo) GetUserByID(_ context.Context, id string) (*identity.User, error) {
	return &identity.User{ID: id, Role: identity.RoleMember, IsActive: true}, nil
}
func (m *connRBACRepo) GetProjectMembership(_ context.Context, projectID, userID string) (*identity.ProjectMembership, error) {
	return m.members[userID+":"+projectID], nil
}
func (m *connRBACRepo) ListUserProjects(userID string) ([]*identity.ProjectMembership, error) {
	var out []*identity.ProjectMembership
	for _, pm := range m.members {
		if pm.UserID == userID {
			out = append(out, pm)
		}
	}
	return out, nil
}

type connMemRepo struct {
	mu    sync.Mutex
	items map[string]*connectors.Connector
	runs  []*connectors.Run
}

func (m *connMemRepo) Create(_ context.Context, c *connectors.Connector) (*connectors.Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	cp.HasSecret = cp.SecretCiphertext != ""
	m.items[c.ID] = &cp
	out := cp
	return &out, nil
}
func (m *connMemRepo) Get(_ context.Context, id string) (*connectors.Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.items[id]
	if !ok {
		return nil, connectors.ErrNotFound
	}
	cp := *c
	return &cp, nil
}
func (m *connMemRepo) ListByProjects(_ context.Context, ids []string) ([]*connectors.Connector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*connectors.Connector
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
func (m *connMemRepo) ListAll(ctx context.Context) ([]*connectors.Connector, error) {
	return m.ListByProjects(ctx, []string{projA, projB})
}
func (m *connMemRepo) Update(ctx context.Context, c *connectors.Connector) (*connectors.Connector, error) {
	return m.Create(ctx, c)
}
func (m *connMemRepo) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}
func (m *connMemRepo) InsertRun(_ context.Context, r *connectors.Run, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, r)
	return nil
}
func (m *connMemRepo) ListRuns(context.Context, string, int) ([]*connectors.Run, error) {
	return m.runs, nil
}

// --- setup ---

type connFixture struct {
	router         http.Handler
	query, action  *connectors.Connector
	otherProjectID string // connector in project B
	upstreamHits   *int
}

// Users: owner (owns A), viewer/operator/editor (members of A), outsider (owns B only).
func newConnFixture(t *testing.T) *connFixture {
	t.Helper()
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"a","internal":"x"}]}`))
	}))
	t.Cleanup(upstream.Close)

	projects := &connProjectRepo{projects: map[string]*apps.Project{
		projA: {ID: projA, UserID: "owner", Name: "A"},
		projB: {ID: projB, UserID: "outsider", Name: "B"},
	}}
	member := func(user, role string) *identity.ProjectMembership {
		return &identity.ProjectMembership{ProjectID: projA, UserID: user, DefaultRole: role}
	}
	rbac := identity.NewRBACService(&connRBACRepo{members: map[string]*identity.ProjectMembership{
		"viewer:" + projA:   member("viewer", identity.NodePresetViewer),
		"operator:" + projA: member("operator", identity.NodePresetOperator),
		"editor:" + projA:   member("editor", identity.NodePresetEditor),
	}})
	box, _ := secretbox.New(bytes.Repeat([]byte{9}, 32))
	repo := &connMemRepo{items: map[string]*connectors.Connector{}}
	svc := connectors.NewServiceWith(repo, box, http.DefaultClient)

	secret := "tok-secret-123"
	mk := func(project, name, kind string) *connectors.Connector {
		in := connectors.Input{Definition: connectors.Definition{
			ProjectID: project, Name: name, Kind: kind, Method: "POST", URL: upstream.URL + "/x",
			Headers:  []connectors.KeyValue{{Name: "X-Static", Value: "static-token"}},
			Auth:     connectors.Auth{Type: connectors.AuthBearer},
			Response: connectors.ResponseMapping{Root: "items", Fields: []connectors.FieldMapping{{Label: "Name", Path: "name"}}},
		}, Secret: &secret}
		c, err := svc.Create(context.Background(), in, "owner")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	f := &connFixture{
		query:        mk(projA, "q", connectors.KindQuery),
		action:       mk(projA, "a", connectors.KindAction),
		upstreamHits: &hits,
	}
	f.otherProjectID = mk(projB, "b", connectors.KindQuery).ID

	h := handler.NewConnectorHandler(svc, apps.NewProjectService(projects))
	h.SetRBACService(rbac)
	r := chi.NewRouter()
	r.Mount("/v1/connectors", h.Routes())
	f.router = r
	return f
}

func (f *connFixture) do(t *testing.T, user, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+generateToken(user, user+"@example.com"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func definitionBody(project string) map[string]any {
	return map[string]any{
		"project_id": project, "name": "new", "kind": "query", "method": "GET", "url": "https://api.example.com/",
	}
}

// --- tests ---

func TestConnectorAuthzMatrix(t *testing.T) {
	f := newConnFixture(t)
	q, a := "/v1/connectors/"+f.query.ID, "/v1/connectors/"+f.action.ID
	update := definitionBody(projA)

	cases := []struct {
		name, method, path string
		body               any
		want               map[string]int // user -> status
	}{
		{"get", http.MethodGet, q, nil, map[string]int{"owner": 200, "viewer": 200, "operator": 200, "editor": 200, "outsider": 404}},
		{"runs", http.MethodGet, a + "/runs", nil, map[string]int{"viewer": 200, "outsider": 404}},
		{"run query", http.MethodPost, q + "/run", map[string]any{}, map[string]int{"viewer": 200, "operator": 200, "outsider": 404}},
		{"run action", http.MethodPost, a + "/run", map[string]any{}, map[string]int{"viewer": 403, "operator": 200, "editor": 200, "outsider": 404}},
		{"update", http.MethodPut, q, update, map[string]int{"viewer": 403, "operator": 403, "editor": 200, "outsider": 404}},
		{"create", http.MethodPost, "/v1/connectors/", definitionBody(projA), map[string]int{"viewer": 403, "operator": 403, "outsider": 404}},
		{"create in other project", http.MethodPost, "/v1/connectors/", definitionBody(projB), map[string]int{"owner": 404, "editor": 404, "outsider": 201}},
		{"create with bad project id", http.MethodPost, "/v1/connectors/", definitionBody("not-a-uuid"), map[string]int{"owner": 404}},
		{"test", http.MethodPost, "/v1/connectors/test", map[string]any{"connector": definitionBody(projA)}, map[string]int{"viewer": 403, "operator": 403, "outsider": 404}},
		{"other project's connector", http.MethodGet, "/v1/connectors/" + f.otherProjectID, nil, map[string]int{"owner": 404, "editor": 404, "outsider": 200}},
		{"unknown id", http.MethodGet, "/v1/connectors/not-a-uuid", nil, map[string]int{"owner": 404}},
		{"delete", http.MethodDelete, a, nil, map[string]int{"viewer": 403, "operator": 403, "outsider": 404}},
	}
	for _, c := range cases {
		for user, want := range c.want {
			if rec := f.do(t, user, c.method, c.path, c.body); rec.Code != want {
				t.Errorf("%s as %s: status %d, want %d (%s)", c.name, user, rec.Code, want, rec.Body.String())
			}
		}
	}
}

func TestConnectorListIsolation(t *testing.T) {
	f := newConnFixture(t)
	names := func(user, query string) []string {
		rec := f.do(t, user, http.MethodGet, "/v1/connectors/"+query, nil)
		var list []connectors.Connector
		_ = json.Unmarshal(rec.Body.Bytes(), &list)
		var out []string
		for _, c := range list {
			out = append(out, c.ProjectID[len(c.ProjectID)-1:]+":"+c.Name)
		}
		return out
	}
	if got := names("viewer", ""); len(got) != 2 {
		t.Errorf("viewer sees %v, want the 2 connectors of A", got)
	}
	if got := names("outsider", ""); len(got) != 1 || got[0] != "b:b" {
		t.Errorf("outsider sees %v, want only b", got)
	}
	if got := names("outsider", "?project_id="+projA); len(got) != 0 {
		t.Errorf("outsider filtering by A sees %v", got)
	}
	if got := names("owner", "?project_id="+projA); len(got) != 2 {
		t.Errorf("owner filtering by A sees %v", got)
	}
}

func TestConnectorHidesRequestDetailsAndSecrets(t *testing.T) {
	f := newConnFixture(t)
	path := "/v1/connectors/" + f.query.ID

	for _, user := range []string{"viewer", "operator", "editor", "owner"} {
		body := f.do(t, user, http.MethodGet, path, nil).Body.String()
		for _, leak := range []string{"tok-secret-123", "v1:", "secret_ciphertext"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: response leaks %q: %s", user, leak, body)
			}
		}
		manager := user == "editor" || user == "owner"
		if strings.Contains(body, "static-token") != manager || strings.Contains(body, "/x\"") != manager {
			t.Errorf("%s: request details visible=%v, want %v: %s", user, !manager, manager, body)
		}
		if !strings.Contains(body, `"has_secret":true`) {
			t.Errorf("%s: has_secret missing: %s", user, body)
		}
	}

	// Running: viewers get the mapped rows but not the raw body; managers get both.
	var res connectors.Result
	_ = json.Unmarshal(f.do(t, "viewer", http.MethodPost, path+"/run", nil).Body.Bytes(), &res)
	if res.Body != nil || res.Mapped == nil || res.Mapped.Rows[0][0] != "a" {
		t.Errorf("viewer run result: %+v", res)
	}
	res = connectors.Result{}
	_ = json.Unmarshal(f.do(t, "editor", http.MethodPost, path+"/run", nil).Body.Bytes(), &res)
	if res.Body == nil {
		t.Errorf("editor run result has no body: %+v", res)
	}
}

func TestConnectorTestCannotBorrowAnotherProjectsSecret(t *testing.T) {
	f := newConnFixture(t)
	def := definitionBody(projB)
	def["auth"] = map[string]any{"type": "bearer"}
	// connector_id of project A, which has a stored token.
	body := map[string]any{"connector": def, "connector_id": f.query.ID}
	hits := *f.upstreamHits
	if rec := f.do(t, "outsider", http.MethodPost, "/v1/connectors/test", body); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if *f.upstreamHits != hits {
		t.Fatal("the external API was called")
	}
}

func TestConnectorUpdateKeepsProject(t *testing.T) {
	f := newConnFixture(t)
	body := definitionBody(projB) // tries to move the connector to project B
	rec := f.do(t, "owner", http.MethodPut, "/v1/connectors/"+f.query.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var c connectors.Connector
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	if c.ProjectID != projA {
		t.Fatalf("project changed to %s", c.ProjectID)
	}
}

// The definition's declared params and the values to test with are separate fields.
func TestConnectorTestWithParams(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotPath = r.URL.Path }))
	defer upstream.Close()
	f := newConnFixture(t)
	def := definitionBody(projA)
	def["url"] = upstream.URL + "/users/{{id}}"
	def["params"] = []map[string]any{{"name": "id", "type": "number", "required": true}}
	rec := f.do(t, "owner", http.MethodPost, "/v1/connectors/test", map[string]any{"connector": def, "params": map[string]any{"id": 42}})
	if rec.Code != http.StatusOK || gotPath != "/users/42" {
		t.Fatalf("status %d, upstream path %q: %s", rec.Code, gotPath, rec.Body.String())
	}
}

// An unreachable external API is 424, not 502: Cloudflare replaces 502 responses with its own page and
// the user would not see why it failed.
func TestConnectorUpstreamFailureIs424(t *testing.T) {
	f := newConnFixture(t)
	def := definitionBody(projA)
	def["url"] = "http://127.0.0.1:1/unreachable"
	rec := f.do(t, "owner", http.MethodPost, "/v1/connectors/test", map[string]any{"connector": def})
	if rec.Code != http.StatusFailedDependency || !strings.Contains(rec.Body.String(), "could not reach the external API") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

// Values outside "params" are refused instead of being ignored (the connector would run with its
// defaults, as happened when the assistant sent {"texto": "..."}).
func TestConnectorRunRejectsValuesOutsideParams(t *testing.T) {
	f := newConnFixture(t)
	hits := *f.upstreamHits
	rec := f.do(t, "owner", http.MethodPost, "/v1/connectors/"+f.query.ID+"/run", map[string]any{"texto": "hola"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `\"texto\"`) || !strings.Contains(rec.Body.String(), "params") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if *f.upstreamHits != hits {
		t.Fatal("the connector ran")
	}
	if rec := f.do(t, "owner", http.MethodPost, "/v1/connectors/"+f.query.ID+"/run", map[string]any{"params": map[string]any{}}); rec.Code != http.StatusOK {
		t.Fatalf("valid body: %d %s", rec.Code, rec.Body.String())
	}
}
