// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package containermetric

import "sync"
import "time"
import "strconv"
import "strings"
import "fmt"

type Service struct {
	repo          Repository
	lastStored    map[string]time.Time // key: "nodeID:containerID"
	lastStoredMu  sync.Mutex
}

func NewService(repo Repository) *Service {
	return &Service{
		repo:       repo,
		lastStored: make(map[string]time.Time),
	}
}

func (s *Service) GetHistory(params ContainerHistoryParams) ([]*ContainerMetricHistoryPoint, error) {
	if params.NodeID == "" {
		return nil, fmt.Errorf("node_id is required")
	}
	return s.repo.GetHistory(params)
}

// ContainerSnapshotItem is a simplified container snapshot representation from gRPC
type ContainerSnapshotItem struct {
	ID      string
	Name    string
	State   string
	Status  string
	CPU     string
	Mem     string
	MemPerc string
}

// ProcessSnapshot processes and records container metric snapshots with a minimum 30-second throttle per container.
func (s *Service) ProcessSnapshot(nodeID string, items []ContainerSnapshotItem) error {
	now := time.Now()
	var toInsert []*CreateContainerMetricRequest

	s.lastStoredMu.Lock()
	for _, item := range items {
		key := fmt.Sprintf("%s:%s", nodeID, item.ID)
		if last, ok := s.lastStored[key]; ok && now.Sub(last) < 30*time.Second {
			continue
		}
		s.lastStored[key] = now

		cpu := parsePercent(item.CPU)
		memPerc := parsePercent(item.MemPerc)
		memBytes := parseMemBytes(item.Mem)

		toInsert = append(toInsert, &CreateContainerMetricRequest{
			NodeID:             nodeID,
			ContainerID:        item.ID,
			ContainerName:      item.Name,
			CPUUsagePercent:    cpu,
			MemoryUsageBytes:   memBytes,
			MemoryUsagePercent: memPerc,
			Status:             item.Status,
			State:              item.State,
			CollectedAt:        now,
		})
	}
	s.lastStoredMu.Unlock()

	if len(toInsert) == 0 {
		return nil
	}

	return s.repo.StoreBatch(toInsert)
}

func parsePercent(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "%")
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0.0
	}
	return val
}

func parseMemBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	parts := strings.Split(s, "/")
	usedPart := strings.TrimSpace(parts[0])
	if usedPart == "" {
		return 0
	}

	// Match number and unit (e.g. 12.5MiB, 100KiB, 2GB, 500B)
	unitIdx := -1
	for i, r := range usedPart {
		if (r < '0' || r > '9') && r != '.' {
			unitIdx = i
			break
		}
	}

	if unitIdx == -1 {
		val, _ := strconv.ParseInt(usedPart, 10, 64)
		return val
	}

	numStr := usedPart[:unitIdx]
	unit := strings.ToUpper(strings.TrimSpace(usedPart[unitIdx:]))
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}

	var multiplier float64 = 1
	switch unit {
	case "B":
		multiplier = 1
	case "KIB", "KB", "K":
		multiplier = 1024
	case "MIB", "MB", "M":
		multiplier = 1024 * 1024
	case "GIB", "GB", "G":
		multiplier = 1024 * 1024 * 1024
	case "TIB", "TB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	}

	return int64(val * multiplier)
}
