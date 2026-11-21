// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/go-chi/chi/v5"
)

// memClusterRepo is a minimal in-memory infra.ClusterRepository for authorization tests.
type memClusterRepo struct {
	clusters map[string]*infra.Cluster
	nodes    map[string][]*infra.ClusterNode
	services map[string]*infra.ClusterService
}

func (m *memClusterRepo) CreateCluster(_ context.Context, c *infra.Cluster) (*infra.Cluster, error) {
	if c.ID == "" {
		c.ID = "c-new"
	}
	m.clusters[c.ID] = c
	return c, nil
}
func (m *memClusterRepo) GetClusterByID(_ context.Context, id string) (*infra.Cluster, error) {
	c, ok := m.clusters[id]
	if !ok {
		return nil, infra.ErrClusterNotFound
	}
	return c, nil
}
func (m *memClusterRepo) ListClustersByUserID(_ context.Context, userID string) ([]*infra.Cluster, error) {
	var out []*infra.Cluster
	for _, c := range m.clusters {
		if c.UserID == userID {
			out = append(out, c)
		}
	}
	return out, nil
}
func (m *memClusterRepo) DeleteCluster(_ context.Context, id string) error {
	delete(m.clusters, id)
	return nil
}
func (m *memClusterRepo) AddClusterNode(_ context.Context, cn *infra.ClusterNode) (*infra.ClusterNode, error) {
	m.nodes[cn.ClusterID] = append(m.nodes[cn.ClusterID], cn)
	return cn, nil
}
func (m *memClusterRepo) GetClusterNodes(_ context.Context, clusterID string) ([]*infra.ClusterNode, error) {
	return m.nodes[clusterID], nil
}
func (m *memClusterRepo) RemoveClusterNode(_ context.Context, clusterID, nodeID string) error {
	var keep []*infra.ClusterNode
	for _, n := range m.nodes[clusterID] {
		if n.NodeID != nodeID {
			keep = append(keep, n)
		}
	}
	m.nodes[clusterID] = keep
	return nil
}
func (m *memClusterRepo) CreateClusterService(_ context.Context, s *infra.ClusterService) (*infra.ClusterService, error) {
	m.services[s.ID] = s
	return s, nil
}
func (m *memClusterRepo) GetClusterServiceByID(_ context.Context, id string) (*infra.ClusterService, error) {
	s, ok := m.services[id]
	if !ok {
		return nil, infra.ErrServiceNotFound
	}
	return s, nil
}
func (m *memClusterRepo) ListClusterServices(_ context.Context, clusterID string) ([]*infra.ClusterService, error) {
	var out []*infra.ClusterService
	for _, s := range m.services {
		if s.ClusterID == clusterID {
			out = append(out, s)
		}
	}
	return out, nil
}
func (m *memClusterRepo) UpdateClusterServiceReplicas(_ context.Context, id string, replicas int) error {
	if s, ok := m.services[id]; ok {
		s.Replicas = replicas
	}
	return nil
}
func (m *memClusterRepo) DeleteClusterService(_ context.Context, id string) error {
	delete(m.services, id)
	return nil
}
func (m *memClusterRepo) UpsertAutoscalePolicy(_ context.Context, p *infra.AutoscalePolicy) (*infra.AutoscalePolicy, error) {
	return p, nil
}
func (m *memClusterRepo) GetAutoscalePolicyByServiceID(_ context.Context, _ string) (*infra.AutoscalePolicy, error) {
	return nil, nil
}
func (m *memClusterRepo) ListAllActiveAutoscalePolicies(_ context.Context) ([]*infra.AutoscalePolicyWithContext, error) {
	return nil, nil
}
func (m *memClusterRepo) UpdateLastScaledAt(_ context.Context, _ string, _ time.Time) error {
	return nil
}
func (m *memClusterRepo) GetServiceAverageMetric(_ context.Context, _ string, _ time.Duration) (float64, float64, int, error) {
	return 0, 0, 0, nil
}

func setupClusterAuthzRouter(t *testing.T) (*chi.Mux, *memClusterRepo) {
	t.Helper()
	strPtr := func(s string) *string { return &s }
	vpn := "100.64.0.1"

	nodeRepo := &MockNodeRepo{nodes: map[string]*node.Node{
		"n-owner":    {ID: "n-owner", OwnerID: strPtr("u-owner"), IPVPN: &vpn},
		"n-attacker": {ID: "n-attacker", OwnerID: strPtr("u-attacker"), IPVPN: &vpn},
		"n-victim2":  {ID: "n-victim2", OwnerID: strPtr("u-owner"), IPVPN: &vpn},
	}}

	repo := &memClusterRepo{
		clusters: map[string]*infra.Cluster{
			"c-owner":    {ID: "c-owner", UserID: "u-owner", Name: "owner"},
			"c-attacker": {ID: "c-attacker", UserID: "u-attacker", Name: "attacker"},
		},
		nodes: map[string][]*infra.ClusterNode{
			"c-owner":    {{ClusterID: "c-owner", NodeID: "n-owner", Role: infra.RoleManager}},
			"c-attacker": {{ClusterID: "c-attacker", NodeID: "n-attacker", Role: infra.RoleManager}},
		},
		services: map[string]*infra.ClusterService{
			"s-owner":    {ID: "s-owner", ClusterID: "c-owner", Name: "web", Replicas: 1},
			"s-attacker": {ID: "s-attacker", ClusterID: "c-attacker", Name: "evil", Replicas: 1},
		},
	}

	rbacRepo := newMockRBACRepoHandler()
	rbacRepo.users["u-owner"] = &identity.User{ID: "u-owner", Role: identity.RoleMember, IsActive: true}
	rbacRepo.users["u-attacker"] = &identity.User{ID: "u-attacker", Role: identity.RoleMember, IsActive: true}
	rbacRepo.users["u-admin"] = &identity.User{ID: "u-admin", Role: identity.RoleAdmin, IsActive: true}
	rbacRepo.nodeOwners["n-owner"] = strPtr("u-owner")
	rbacRepo.nodeOwners["n-victim2"] = strPtr("u-owner")
	rbacRepo.nodeOwners["n-attacker"] = strPtr("u-attacker")

	mgr := infra.NewClusterManager(repo, nodeRepo, nil, nil)
	h := handler.NewClusterHandler(mgr)
	h.SetRBACService(identity.NewRBACService(rbacRepo))

	r := chi.NewRouter()
	r.Mount("/v1/clusters", h.Routes())
	return r, repo
}

func clusterReq(t *testing.T, r http.Handler, method, path, userID, body string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+generateToken(userID, userID+"@test"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func TestClusterAuthz_CrossTenantDenied(t *testing.T) {
	r, repo := setupClusterAuthzRouter(t)

	cases := []struct {
		name, method, path, body string
	}{
		{"get", http.MethodGet, "/v1/clusters/c-owner", ""},
		{"delete", http.MethodDelete, "/v1/clusters/c-owner", ""},
		{"add node", http.MethodPost, "/v1/clusters/c-owner/nodes", `{"node_id":"n-attacker"}`},
		{"remove node", http.MethodDelete, "/v1/clusters/c-owner/nodes/n-owner", ""},
		{"create service", http.MethodPost, "/v1/clusters/c-owner/services", `{"name":"x","image":"evil/img"}`},
		{"scale service", http.MethodPut, "/v1/clusters/c-owner/services/s-owner/scale", `{"replicas":5}`},
		{"delete service", http.MethodDelete, "/v1/clusters/c-owner/services/s-owner", ""},
		{"get autoscaling", http.MethodGet, "/v1/clusters/c-owner/services/s-owner/autoscaling", ""},
		{"set autoscaling", http.MethodPut, "/v1/clusters/c-owner/services/s-owner/autoscaling", `{"enabled":true}`},
	}
	for _, tc := range cases {
		if code := clusterReq(t, r, tc.method, tc.path, "u-attacker", tc.body); code != http.StatusNotFound {
			t.Errorf("%s: attacker got %d, want 404", tc.name, code)
		}
	}
	if _, ok := repo.clusters["c-owner"]; !ok {
		t.Fatal("victim cluster was deleted by another tenant")
	}
	if repo.services["s-owner"].Replicas != 1 {
		t.Fatal("victim service was scaled by another tenant")
	}
}

func TestClusterAuthz_ServiceMustBelongToCluster(t *testing.T) {
	r, repo := setupClusterAuthzRouter(t)
	// Owner of c-owner tries to reach the attacker's service through their own cluster ID.
	if code := clusterReq(t, r, http.MethodPut, "/v1/clusters/c-owner/services/s-attacker/scale", "u-owner", `{"replicas":9}`); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
	if repo.services["s-attacker"].Replicas != 1 {
		t.Fatal("service of another cluster was scaled")
	}
}

func TestClusterAuthz_CannotEnrollForeignNodes(t *testing.T) {
	r, _ := setupClusterAuthzRouter(t)
	if code := clusterReq(t, r, http.MethodPost, "/v1/clusters", "u-attacker", `{"name":"x","manager_node_id":"n-attacker","worker_node_ids":["n-owner"]}`); code != http.StatusForbidden {
		t.Errorf("create with foreign worker: got %d, want 403", code)
	}
	if code := clusterReq(t, r, http.MethodPost, "/v1/clusters/c-attacker/nodes", "u-attacker", `{"node_id":"n-owner"}`); code != http.StatusForbidden {
		t.Errorf("add foreign node: got %d, want 403", code)
	}
	if code := clusterReq(t, r, http.MethodDelete, "/v1/clusters/c-attacker/nodes/n-owner", "u-attacker", ""); code != http.StatusNotFound {
		t.Errorf("remove non-member node: got %d, want 404", code)
	}
}

func TestClusterAuthz_OwnerAndAdminAllowed(t *testing.T) {
	r, _ := setupClusterAuthzRouter(t)
	for _, u := range []string{"u-owner", "u-admin"} {
		if code := clusterReq(t, r, http.MethodGet, "/v1/clusters/c-owner", u, ""); code != http.StatusOK {
			t.Errorf("%s get: got %d, want 200", u, code)
		}
	}
	if code := clusterReq(t, r, http.MethodPost, "/v1/clusters/c-owner/nodes", "u-owner", `{"node_id":"n-victim2"}`); code != http.StatusCreated {
		t.Errorf("owner add own node: got %d, want 201", code)
	}
	if code := clusterReq(t, r, http.MethodPut, "/v1/clusters/c-owner/services/s-owner/scale", "u-owner", `{"replicas":3}`); code != http.StatusOK {
		t.Errorf("owner scale: got %d, want 200", code)
	}
}
