// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
)

type Dispatcher interface {
	DispatchJobWithParams(nodeID, jobID, commandName string, commandType jobs.CommandType, params string) error
	IsNodeConnected(nodeID string) bool
}

type Service struct {
	repo       Repository
	nodeRepo   node.Repository
	jobService *jobs.JobService
	dispatcher Dispatcher
}

func NewService(
	repo Repository,
	nodeRepo node.Repository,
	jobService *jobs.JobService,
	dispatcher Dispatcher,
) *Service {
	return &Service{
		repo:       repo,
		nodeRepo:   nodeRepo,
		jobService: jobService,
		dispatcher: dispatcher,
	}
}

// CreateCluster orchestrates the creation of a new Swarm cluster across AutoHost nodes.
func (s *Service) CreateCluster(ctx context.Context, userID, name string, managerNodeID string, workerNodeIDs []string) (*ClusterWithDetails, error) {
	if name == "" || managerNodeID == "" {
		return nil, ErrInvalidClusterData
	}

	// 1. Verify manager node exists and has a VPN IP
	managerNode, err := s.nodeRepo.FindByID(managerNodeID)
	if err != nil || managerNode == nil {
		return nil, ErrNodeNotFound
	}
	if managerNode.IPVPN == nil || *managerNode.IPVPN == "" {
		return nil, ErrNodeNoVPNIP
	}

	// 2. Persist cluster in database
	c := &Cluster{
		UserID:         userID,
		Name:           name,
		Status:         "active",
		OverlayNetwork: "autohost-net",
	}
	savedCluster, err := s.repo.CreateCluster(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("create cluster in db: %w", err)
	}

	// 3. Add manager node to cluster_nodes
	managerCN := &ClusterNode{
		ClusterID: savedCluster.ID,
		NodeID:    managerNodeID,
		Role:      RoleManager,
		Status:    "active",
	}
	if _, err := s.repo.AddClusterNode(ctx, managerCN); err != nil {
		log.Printf("[ERROR] add manager node %s to cluster %s: %v", managerNodeID, savedCluster.ID, err)
	}

	// 4. Dispatch swarm.init to manager node via gRPC
	initPayload, _ := json.Marshal(map[string]any{
		"advertise_vpn_ip": *managerNode.IPVPN,
		"overlay_mtu":     1200,
	})
	s.dispatchSwarmJob(managerNodeID, "swarm.init", string(initPayload))

	// 5. Add worker nodes to cluster_nodes
	for _, workerID := range workerNodeIDs {
		if workerID == managerNodeID || workerID == "" {
			continue
		}
		workerCN := &ClusterNode{
			ClusterID: savedCluster.ID,
			NodeID:    workerID,
			Role:      RoleWorker,
			Status:    "pending",
		}
		if _, err := s.repo.AddClusterNode(ctx, workerCN); err != nil {
			log.Printf("[ERROR] add worker node %s to cluster %s: %v", workerID, savedCluster.ID, err)
		}
	}

	// 6. Asynchronously initialize Swarm and join all workers
	go s.orchestrateCluster(savedCluster.ID, managerNodeID, *managerNode.IPVPN, workerNodeIDs)

	return s.GetClusterWithDetails(ctx, savedCluster.ID)
}

// GetClusterWithDetails returns a cluster with all nodes and deployed services.
func (s *Service) GetClusterWithDetails(ctx context.Context, clusterID string) (*ClusterWithDetails, error) {
	c, err := s.repo.GetClusterByID(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	nodes, err := s.repo.GetClusterNodes(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	services, err := s.repo.ListClusterServices(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	for _, svc := range services {
		policy, err := s.repo.GetAutoscalePolicyByServiceID(ctx, svc.ID)
		if err == nil && policy != nil {
			svc.AutoscalePolicy = policy
		}
	}

	if nodes == nil {
		nodes = []*ClusterNode{}
	}
	if services == nil {
		services = []*ClusterService{}
	}

	return &ClusterWithDetails{
		Cluster:  c,
		Nodes:    nodes,
		Services: services,
	}, nil
}

// ListClusters returns all clusters owned by the user.
func (s *Service) ListClusters(ctx context.Context, userID string) ([]*ClusterWithDetails, error) {
	clusters, err := s.repo.ListClustersByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	var out []*ClusterWithDetails
	for _, c := range clusters {
		det, err := s.GetClusterWithDetails(ctx, c.ID)
		if err == nil {
			out = append(out, det)
		}
	}
	return out, nil
}

// DeleteCluster removes the cluster and dispatches swarm.leave to all nodes.
func (s *Service) DeleteCluster(ctx context.Context, clusterID string) error {
	nodes, err := s.repo.GetClusterNodes(ctx, clusterID)
	if err == nil {
		leavePayload, _ := json.Marshal(map[string]any{"force": true})
		for _, n := range nodes {
			s.dispatchSwarmJob(n.NodeID, "swarm.leave", string(leavePayload))
		}
	}
	return s.repo.DeleteCluster(ctx, clusterID)
}

// AddNodeToCluster adds a new node to an existing cluster.
func (s *Service) AddNodeToCluster(ctx context.Context, clusterID, nodeID string, role ClusterRole) error {
	n, err := s.nodeRepo.FindByID(nodeID)
	if err != nil || n == nil {
		return ErrNodeNotFound
	}
	if n.IPVPN == nil || *n.IPVPN == "" {
		return ErrNodeNoVPNIP
	}

	cn := &ClusterNode{
		ClusterID: clusterID,
		NodeID:    nodeID,
		Role:      role,
		Status:    "active",
	}
	_, err = s.repo.AddClusterNode(ctx, cn)
	return err
}

// RemoveNodeFromCluster removes a node and commands it to leave Swarm.
func (s *Service) RemoveNodeFromCluster(ctx context.Context, clusterID, nodeID string) error {
	leavePayload, _ := json.Marshal(map[string]any{"force": true})
	s.dispatchSwarmJob(nodeID, "swarm.leave", string(leavePayload))
	return s.repo.RemoveClusterNode(ctx, clusterID, nodeID)
}

// CreateService deploys a new stateless service to the Swarm cluster.
func (s *Service) CreateService(
	ctx context.Context,
	clusterID string,
	name, image, ports string,
	envVars map[string]string,
	replicas int,
) (*ClusterService, error) {
	if replicas < 1 {
		replicas = 1
	}

	svc := &ClusterService{
		ClusterID: clusterID,
		Name:      name,
		Image:     image,
		Ports:     ports,
		EnvVars:   envVars,
		Replicas:  replicas,
		Status:    "running",
	}

	saved, err := s.repo.CreateClusterService(ctx, svc)
	if err != nil {
		return nil, fmt.Errorf("create service in db: %w", err)
	}

	// Find the manager node to dispatch the swarm.service.deploy command
	managerNodeID, err := s.findManagerNodeID(ctx, clusterID)
	if err == nil && managerNodeID != "" {
		deployParams, _ := json.Marshal(map[string]any{
			"name":         name,
			"image":        image,
			"ports":        ports,
			"env_vars":     envVars,
			"replicas":     replicas,
			"network_name": "autohost-net",
		})
		s.dispatchSwarmJob(managerNodeID, "swarm.service.deploy", string(deployParams))
	}

	return saved, nil
}

// ScaleService changes the replica count of a running Swarm service.
func (s *Service) ScaleService(ctx context.Context, serviceID string, targetReplicas int) error {
	svc, err := s.repo.GetClusterServiceByID(ctx, serviceID)
	if err != nil {
		return err
	}

	if targetReplicas < 1 {
		targetReplicas = 1
	}

	if err := s.repo.UpdateClusterServiceReplicas(ctx, serviceID, targetReplicas); err != nil {
		return err
	}

	managerNodeID, err := s.findManagerNodeID(ctx, svc.ClusterID)
	if err == nil && managerNodeID != "" {
		scaleParams, _ := json.Marshal(map[string]any{
			"service_name": svc.Name,
			"replicas":     targetReplicas,
		})
		s.dispatchSwarmJob(managerNodeID, "swarm.service.scale", string(scaleParams))
	}
	return nil
}

// DeleteService removes a Swarm service.
func (s *Service) DeleteService(ctx context.Context, serviceID string) error {
	svc, err := s.repo.GetClusterServiceByID(ctx, serviceID)
	if err != nil {
		return err
	}

	managerNodeID, err := s.findManagerNodeID(ctx, svc.ClusterID)
	if err == nil && managerNodeID != "" {
		removeParams, _ := json.Marshal(map[string]any{
			"service_name": svc.Name,
		})
		s.dispatchSwarmJob(managerNodeID, "swarm.service.remove", string(removeParams))
	}

	return s.repo.DeleteClusterService(ctx, serviceID)
}

// ConfigureAutoscaling saves or updates the autoscaling policy for a service.
func (s *Service) ConfigureAutoscaling(ctx context.Context, serviceID string, policy *AutoscalePolicy) (*AutoscalePolicy, error) {
	policy.ClusterServiceID = serviceID
	if policy.MinReplicas < 1 {
		policy.MinReplicas = 1
	}
	if policy.MaxReplicas < policy.MinReplicas {
		policy.MaxReplicas = policy.MinReplicas
	}
	if policy.ScaleUpThreshold <= 0 {
		policy.ScaleUpThreshold = 75.0
	}
	if policy.ScaleDownThreshold <= 0 {
		policy.ScaleDownThreshold = 30.0
	}
	if policy.ScaleUpDurationSec <= 0 {
		policy.ScaleUpDurationSec = 30 // 30s
	}
	if policy.ScaleDownDurationSec <= 0 {
		policy.ScaleDownDurationSec = 60 // 1 min
	}
	if policy.CooldownSeconds <= 0 {
		policy.CooldownSeconds = 30 // 30s
	}

	return s.repo.UpsertAutoscalePolicy(ctx, policy)
}

// GetAutoscaling returns the autoscaling policy for a given service.
func (s *Service) GetAutoscaling(ctx context.Context, serviceID string) (*AutoscalePolicy, error) {
	return s.repo.GetAutoscalePolicyByServiceID(ctx, serviceID)
}

func (s *Service) findManagerNodeID(ctx context.Context, clusterID string) (string, error) {
	nodes, err := s.repo.GetClusterNodes(ctx, clusterID)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if n.Role == RoleManager {
			return n.NodeID, nil
		}
	}
	if len(nodes) > 0 {
		return nodes[0].NodeID, nil
	}
	return "", ErrManagerRequired
}

func (s *Service) orchestrateCluster(clusterID, managerNodeID, managerVPNIP string, workerNodeIDs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log.Printf("🚀 Starting Swarm cluster orchestration for cluster %s (Manager: %s, VPN: %s)...", clusterID, managerNodeID, managerVPNIP)

	// 1. Dispatch swarm.init to manager
	initPayload, _ := json.Marshal(map[string]any{
		"advertise_vpn_ip": managerVPNIP,
		"overlay_mtu":     1200,
	})
	initJob, err := s.dispatchAndAwaitJob(ctx, managerNodeID, "swarm.init", string(initPayload))
	if err != nil || initJob == nil {
		log.Printf("[ERROR] swarm.init failed on manager %s: %v", managerNodeID, err)
		return
	}
	if initJob.Status != jobs.StatusCompleted {
		log.Printf("[ERROR] swarm.init job status %s on manager %s: %s", initJob.Status, managerNodeID, initJob.Error)
		return
	}

	var tokens struct {
		NodeID      string `json:"node_id"`
		WorkerToken string `json:"worker_token"`
	}
	if err := json.Unmarshal([]byte(initJob.Output), &tokens); err != nil || tokens.WorkerToken == "" {
		log.Printf("[ERROR] failed to parse swarm tokens from manager output: %v, raw: %s", err, initJob.Output)
		return
	}

	log.Printf("✅ Swarm initialized on manager %s. NodeID: %s, WorkerToken: %s", managerNodeID, tokens.NodeID, tokens.WorkerToken)

	// 2. Dispatch swarm.join to all workers
	for _, workerID := range workerNodeIDs {
		if workerID == managerNodeID || workerID == "" {
			continue
		}
		workerNode, err := s.nodeRepo.FindByID(workerID)
		if err != nil || workerNode == nil || workerNode.IPVPN == nil || *workerNode.IPVPN == "" {
			log.Printf("[WARN] worker node %s not eligible for swarm join (no VPN IP)", workerID)
			continue
		}

		log.Printf("🔗 Dispatching swarm.join to worker %s (%s)...", workerID, *workerNode.IPVPN)
		joinPayload, _ := json.Marshal(map[string]any{
			"manager_vpn_ip":   managerVPNIP,
			"join_token":      tokens.WorkerToken,
			"advertise_vpn_ip": *workerNode.IPVPN,
		})
		joinJob, err := s.dispatchAndAwaitJob(ctx, workerID, "swarm.join", string(joinPayload))
		if err == nil && joinJob != nil && joinJob.Status == jobs.StatusCompleted {
			log.Printf("✅ Worker node %s successfully joined Swarm cluster %s", workerID, clusterID)
			workerCN := &ClusterNode{
				ClusterID: clusterID,
				NodeID:    workerID,
				Role:      RoleWorker,
				Status:    "active",
			}
			_, _ = s.repo.AddClusterNode(ctx, workerCN)
		} else {
			errMsg := "unknown error"
			if joinJob != nil {
				errMsg = joinJob.Error
			} else if err != nil {
				errMsg = err.Error()
			}
			log.Printf("[ERROR] worker node %s failed to join swarm: %s", workerID, errMsg)
		}
	}
}

func (s *Service) dispatchSwarmJob(nodeID, commandName, params string) {
	if s.jobService == nil {
		return
	}
	j, err := s.jobService.DispatchWithParams(nodeID, commandName, jobs.CommandTypeSwarm, params)
	if err != nil {
		log.Printf("[WARN] dispatch swarm job %s to node %s: %v", commandName, nodeID, err)
		return
	}

	if s.dispatcher != nil && s.dispatcher.IsNodeConnected(nodeID) {
		if err := s.dispatcher.DispatchJobWithParams(nodeID, j.ID, commandName, jobs.CommandTypeSwarm, params); err != nil {
			log.Printf("[WARN] live dispatch swarm job %s to node %s: %v", commandName, nodeID, err)
		}
	}
}

func (s *Service) dispatchAndAwaitJob(ctx context.Context, nodeID, commandName, params string) (*jobs.Job, error) {
	if s.jobService == nil {
		return nil, fmt.Errorf("job service unavailable")
	}
	j, err := s.jobService.DispatchWithParams(nodeID, commandName, jobs.CommandTypeSwarm, params)
	if err != nil {
		return nil, err
	}

	if s.dispatcher != nil && s.dispatcher.IsNodeConnected(nodeID) {
		_ = s.dispatcher.DispatchJobWithParams(nodeID, j.ID, commandName, jobs.CommandTypeSwarm, params)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			current, err := s.jobService.GetByID(j.ID)
			if err != nil {
				continue
			}
			if current.Status == jobs.StatusCompleted || current.Status == jobs.StatusFailed {
				return current, nil
			}
		}
	}
}
