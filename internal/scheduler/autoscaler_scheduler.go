package scheduler

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/cluster"
	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
)

type AutoscalerDispatcher interface {
	DispatchJobWithParams(nodeID, jobID, commandName string, commandType nodecommand.CommandType, params string) error
	IsNodeConnected(nodeID string) bool
}

type AutoscalerScheduler struct {
	clusterRepo cluster.Repository
	jobSvc      *job.Service
	dispatcher  AutoscalerDispatcher
}

func NewAutoscalerScheduler(
	clusterRepo cluster.Repository,
	jobSvc *job.Service,
	dispatcher AutoscalerDispatcher,
) *AutoscalerScheduler {
	return &AutoscalerScheduler{
		clusterRepo: clusterRepo,
		jobSvc:      jobSvc,
		dispatcher:  dispatcher,
	}
}

// Start runs the periodic evaluation loop for active autoscaling policies.
func (s *AutoscalerScheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	log.Println("[autoscaler] started, evaluating cluster services every 15 seconds")

	for {
		select {
		case <-ctx.Done():
			log.Println("[autoscaler] shutting down")
			return
		case <-ticker.C:
			s.evaluatePolicies(ctx)
		}
	}
}

func (s *AutoscalerScheduler) evaluatePolicies(ctx context.Context) {
	policies, err := s.clusterRepo.ListAllActiveAutoscalePolicies(ctx)
	if err != nil {
		log.Printf("[autoscaler] error listing policies: %v", err)
		return
	}

	for _, item := range policies {
		s.evaluateSinglePolicy(ctx, item)
	}
}

func (s *AutoscalerScheduler) evaluateSinglePolicy(ctx context.Context, item *cluster.AutoscalePolicyWithContext) {
	policy := item.Policy
	now := time.Now()

	// 1. Check cooldown period
	if policy.LastScaledAt != nil {
		cooldown := time.Duration(policy.CooldownSeconds) * time.Second
		if now.Before(policy.LastScaledAt.Add(cooldown)) {
			return // Still in cooldown
		}
	}

	// 2. Query metric average for Scale-Up window (e.g. 5 minutes)
	upDuration := time.Duration(policy.ScaleUpDurationSec) * time.Second
	if upDuration <= 0 {
		upDuration = 5 * time.Minute
	}

	avgCPU, avgRAM, sampleCount, err := s.clusterRepo.GetServiceAverageMetric(ctx, item.ServiceName, upDuration)
	if err != nil {
		log.Printf("[autoscaler] metric query error for %s: %v", item.ServiceName, err)
		return
	}

	if sampleCount == 0 {
		return // No samples yet
	}

	metricValue := avgCPU
	if policy.MetricType == "ram" {
		metricValue = avgRAM
	}

	// 3. Evaluate Scale-Up
	if metricValue >= policy.ScaleUpThreshold && item.CurrentReplicas < policy.MaxReplicas {
		newReplicas := item.CurrentReplicas + 1
		log.Printf("[autoscaler] 🚀 SCALE-UP for %s: %.1f%% >= %.1f%% (samples: %d). Scaling %d -> %d replicas",
			item.ServiceName, metricValue, policy.ScaleUpThreshold, sampleCount, item.CurrentReplicas, newReplicas)

		s.executeScaling(ctx, item, newReplicas)
		return
	}

	// 4. Evaluate Scale-Down with Projected Load Anti-Flapping
	if item.CurrentReplicas > policy.MinReplicas {
		downDuration := time.Duration(policy.ScaleDownDurationSec) * time.Second
		if downDuration <= 0 {
			downDuration = 10 * time.Minute
		}

		downCPU, downRAM, downSamples, err := s.clusterRepo.GetServiceAverageMetric(ctx, item.ServiceName, downDuration)
		if err == nil && downSamples > 0 {
			downMetricValue := downCPU
			if policy.MetricType == "ram" {
				downMetricValue = downRAM
			}

			if downMetricValue <= policy.ScaleDownThreshold {
				// Calculate projected load: CPU_promedio * (N / (N - 1))
				projectedLoad := downMetricValue * (float64(item.CurrentReplicas) / float64(item.CurrentReplicas-1))

				// Anti-Flapping check: only scale down if projected load remains safely below ScaleUpThreshold
				if projectedLoad >= policy.ScaleUpThreshold {
					log.Printf("[autoscaler] ⏸️ SCALE-DOWN aborted for %s (anti-flapping): current %.1f%%, projected %.1f%% >= %.1f%% threshold",
						item.ServiceName, downMetricValue, projectedLoad, policy.ScaleUpThreshold)
					return
				}

				newReplicas := item.CurrentReplicas - 1
				log.Printf("[autoscaler] 📉 SCALE-DOWN for %s: %.1f%% <= %.1f%% (projected %.1f%%). Scaling %d -> %d replicas",
					item.ServiceName, downMetricValue, policy.ScaleDownThreshold, projectedLoad, item.CurrentReplicas, newReplicas)

				s.executeScaling(ctx, item, newReplicas)
			}
		}
	}
}

func (s *AutoscalerScheduler) executeScaling(ctx context.Context, item *cluster.AutoscalePolicyWithContext, targetReplicas int) {
	// Update replicas in DB
	_ = s.clusterRepo.UpdateClusterServiceReplicas(ctx, item.ServiceID, targetReplicas)
	_ = s.clusterRepo.UpdateLastScaledAt(ctx, item.Policy.ID, time.Now())

	// Dispatch scale command to Swarm Manager node
	if item.ManagerNodeID == "" {
		return
	}

	params, _ := json.Marshal(map[string]any{
		"service_name": item.ServiceName,
		"replicas":     targetReplicas,
	})

	if s.jobSvc != nil {
		j, err := s.jobSvc.DispatchWithParams(item.ManagerNodeID, "swarm.service.scale", nodecommand.CommandTypeSwarm, string(params))
		if err == nil && s.dispatcher != nil && s.dispatcher.IsNodeConnected(item.ManagerNodeID) {
			_ = s.dispatcher.DispatchJobWithParams(item.ManagerNodeID, j.ID, "swarm.service.scale", nodecommand.CommandTypeSwarm, string(params))
		}
	}
}
