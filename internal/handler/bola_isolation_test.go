package handler_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	caddyroute "github.com/arturo/autohost-cloud-api/internal/domain/caddy_route"
	containermetric "github.com/arturo/autohost-cloud-api/internal/domain/container_metric"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	"github.com/arturo/autohost-cloud-api/internal/domain/project"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "super-secret-jwt-key-for-bola-testing-123456"

func init() {
	os.Setenv("JWT_SECRET", testSecret)
}

func generateToken(userID, email string) string {
	claims := &middleware.Claims{
		UserID: userID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := t.SignedString([]byte(testSecret))
	return tokenStr
}

// MockNodeRepo implements node.Repository for testing
type MockNodeRepo struct {
	nodes map[string]*node.Node
}

func (m *MockNodeRepo) Register(n *node.Node) (*node.Node, error) { return n, nil }
func (m *MockNodeRepo) FindByID(id string) (*node.Node, error) {
	n, ok := m.nodes[id]
	if !ok {
		return nil, node.ErrNodeNotFound
	}
	return n, nil
}
func (m *MockNodeRepo) Update(n *node.Node) (*node.Node, error) { return n, nil }
func (m *MockNodeRepo) Delete(id string) error                 { return nil }
func (m *MockNodeRepo) FindByOwnerID(ownerID string) ([]*node.Node, error) {
	var res []*node.Node
	for _, n := range m.nodes {
		if n.OwnerID != nil && *n.OwnerID == ownerID {
			res = append(res, n)
		}
	}
	return res, nil
}
func (m *MockNodeRepo) FindByOwnerIDWithMetrics(ownerID string) ([]*node.NodeWithMetrics, error) {
	return nil, nil
}
func (m *MockNodeRepo) UpdateLastSeen(nodeID string) error                                  { return nil }
func (m *MockNodeRepo) UpdateLastSeenAndIPs(nodeID string, ipLocal string, ipVPN string) error { return nil }
func (m *MockNodeRepo) UpdateVPNIP(nodeID string, ipVPN string) error                        { return nil }
func (m *MockNodeRepo) UpdateVersionAgent(nodeID, version string) error                      { return nil }
func (m *MockNodeRepo) AssignProject(nodeID string, projectID *string) error                 { return nil }

// MockProjectRepo implements project.Repository for testing
type MockProjectRepo struct {
	projects map[string]*project.Project
}

func (m *MockProjectRepo) Create(p *project.Project) (*project.Project, error) { return p, nil }
func (m *MockProjectRepo) FindByID(id string) (*project.Project, error) {
	p, ok := m.projects[id]
	if !ok {
		return nil, project.ErrProjectNotFound
	}
	return p, nil
}
func (m *MockProjectRepo) FindByUserID(userID string) ([]*project.Project, error) { return nil, nil }
func (m *MockProjectRepo) Update(p *project.Project) (*project.Project, error)     { return p, nil }
func (m *MockProjectRepo) Delete(id string) error                                  { return nil }

// MockNodeTokenRepo implements nodetoken.Repository for testing
type MockNodeTokenRepo struct{}

func (m *MockNodeTokenRepo) CreateNodeToken(nodeID, token string) error               { return nil }
func (m *MockNodeTokenRepo) FindNodeTokenByHash(tokenHash string) (*nodetoken.NodeToken, error) { return nil, nil }
func (m *MockNodeTokenRepo) UpdateLastSeen(tokenID string, lastSeenAt time.Time) error  { return nil }
func (m *MockNodeTokenRepo) RevokeByNodeID(nodeID string) error                        { return nil }

// MockCaddyRouteRepo implements caddyroute.Repository
type MockCaddyRouteRepo struct{}

func (m *MockCaddyRouteRepo) Upsert(r *caddyroute.CaddyRoute) (*caddyroute.CaddyRoute, error) {
	return r, nil
}
func (m *MockCaddyRouteRepo) ListByNode(nodeID string) ([]*caddyroute.CaddyRoute, error) {
	return []*caddyroute.CaddyRoute{}, nil
}
func (m *MockCaddyRouteRepo) Delete(nodeID, domain string) error { return nil }

// MockContainerMetricRepo implements containermetric.Repository
type MockContainerMetricRepo struct{}

func (m *MockContainerMetricRepo) StoreBatch(metrics []*containermetric.CreateContainerMetricRequest) error {
	return nil
}
func (m *MockContainerMetricRepo) GetHistory(params containermetric.ContainerHistoryParams) ([]*containermetric.ContainerMetricHistoryPoint, error) {
	return nil, nil
}

func setupTestRouter() (*chi.Mux, *MockNodeRepo, *MockProjectRepo) {
	ownerUserA := "user_a_123"
	nodeRepo := &MockNodeRepo{
		nodes: map[string]*node.Node{
			"node_owned_by_a": {
				ID:      "node_owned_by_a",
				OwnerID: &ownerUserA,
			},
		},
	}
	projectRepo := &MockProjectRepo{
		projects: map[string]*project.Project{
			"proj_owned_by_a": {
				ID:     "proj_owned_by_a",
				UserID: ownerUserA,
				Name:   "User A Project",
			},
		},
	}

	nodeSvc := node.NewService(nodeRepo)
	projSvc := project.NewService(projectRepo)
	nodeTokenSvc := nodetoken.NewService(&MockNodeTokenRepo{})
	caddySvc := caddyroute.NewService(&MockCaddyRouteRepo{})
	containerMetricSvc := containermetric.NewService(&MockContainerMetricRepo{})

	nodeHandler := handler.NewNodeHandler(nodeSvc, nodeTokenSvc)
	projHandler := handler.NewProjectHandler(projSvc, nodeSvc, nil)
	caddyHandler := handler.NewCaddyRouteHandler(caddySvc, nil, nil, nodeSvc)
	containerHandler := handler.NewNodeContainerHandler(nil, nodeSvc, containerMetricSvc, nil)
	logHandler := handler.NewNodeLogHandler(nil, nodeSvc)

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Mount("/nodes", nodeHandler.Routes())
		r.Mount("/projects", projHandler.Routes())
		r.Mount("/caddy-routes", caddyHandler.Routes())
		r.Mount("/node-containers", containerHandler.Routes())
		r.Mount("/node-logs", logHandler.Routes())
	})

	return r, nodeRepo, projectRepo
}

func TestBOLA_CrossTenantAccessDenied(t *testing.T) {
	router, _, _ := setupTestRouter()
	userBToken := generateToken("user_b_999", "userb@example.com")

	tests := []struct {
		name           string
		method         string
		url            string
		body           string
		expectedStatus int
	}{
		// 1. Projects module BOLA
		{
			name:           "User B cannot GET User A project",
			method:         "GET",
			url:            "/v1/projects/proj_owned_by_a",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "User B cannot PUT User A project",
			method:         "PUT",
			url:            "/v1/projects/proj_owned_by_a",
			body:           `{"name":"Hacked Name"}`,
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "User B cannot DELETE User A project",
			method:         "DELETE",
			url:            "/v1/projects/proj_owned_by_a",
			expectedStatus: http.StatusForbidden,
		},

		// 2. Nodes module BOLA
		{
			name:           "User B cannot GET User A node",
			method:         "GET",
			url:            "/v1/nodes/node_owned_by_a",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "User B cannot DELETE User A node",
			method:         "DELETE",
			url:            "/v1/nodes/node_owned_by_a",
			expectedStatus: http.StatusForbidden,
		},

		// 3. Caddy Routes BOLA
		{
			name:           "User B cannot List Caddy Routes for User A node",
			method:         "GET",
			url:            "/v1/caddy-routes/node_owned_by_a",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "User B cannot Upsert Caddy Route for User A node",
			method:         "PUT",
			url:            "/v1/caddy-routes/node_owned_by_a",
			body:           `{"domain":"test.com","upstream":"localhost:8080"}`,
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "User B cannot Delete Caddy Route for User A node",
			method:         "DELETE",
			url:            "/v1/caddy-routes/node_owned_by_a/test.com",
			expectedStatus: http.StatusForbidden,
		},

		// 4. Container Metrics BOLA
		{
			name:           "User B cannot GET Container Metrics for User A node",
			method:         "GET",
			url:            "/v1/node-containers/node_owned_by_a/metrics?container_name=test",
			expectedStatus: http.StatusForbidden,
		},

		// 5. Node System Logs SSE Stream BOLA
		{
			name:           "User B cannot Stream System Logs for User A node",
			method:         "GET",
			url:            "/v1/node-logs/node_owned_by_a/stream",
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.url, bytes.NewBufferString(tt.body))
			req.Header.Set("Authorization", "Bearer "+userBToken)
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("%s: expected HTTP status %d, got %d (body: %s)", tt.name, tt.expectedStatus, rec.Code, rec.Body.String())
			}
		})
	}
}
