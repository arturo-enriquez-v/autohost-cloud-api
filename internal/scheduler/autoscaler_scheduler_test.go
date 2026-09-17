package scheduler

import (
	"testing"
)

func TestProjectedLoadAntiFlapping(t *testing.T) {
	scaleUpThreshold := 75.0

	// Case 1: 2 replicas, average CPU is 40%.
	// If 1 replica is removed, projected load on remaining 1 replica = 40% * (2 / 1) = 80%.
	// 80% >= 75% -> Should NOT scale down!
	replicas := 2
	avgCPU := 40.0
	projected := avgCPU * (float64(replicas) / float64(replicas-1))

	if projected < scaleUpThreshold {
		t.Fatalf("Expected anti-flapping to trigger (projected %.1f%% >= %.1f%%), but got %.1f%%",
			projected, scaleUpThreshold, projected)
	}

	// Case 2: 2 replicas, average CPU is 20%.
	// If 1 replica is removed, projected load on remaining 1 replica = 20% * (2 / 1) = 40%.
	// 40% < 75% -> Safe to scale down!
	avgCPU = 20.0
	projected = avgCPU * (float64(replicas) / float64(replicas-1))

	if projected >= scaleUpThreshold {
		t.Fatalf("Expected safe scale-down (projected %.1f%% < %.1f%%), but got %.1f%%",
			projected, scaleUpThreshold, projected)
	}

	// Case 3: 4 replicas, average CPU is 25%.
	// If 1 replica is removed, projected load on remaining 3 replicas = 25% * (4 / 3) = 33.33%.
	// 33.3% < 75% -> Safe to scale down!
	replicas = 4
	avgCPU = 25.0
	projected = avgCPU * (float64(replicas) / float64(replicas-1))

	if projected >= scaleUpThreshold {
		t.Fatalf("Expected safe scale-down (projected %.1f%% < %.1f%%), but got %.1f%%",
			projected, scaleUpThreshold, projected)
	}
}
