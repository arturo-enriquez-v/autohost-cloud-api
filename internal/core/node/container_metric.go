// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ContainerMetric struct {
	ID                 string    `db:"id" json:"id"`
	NodeID             string    `db:"node_id" json:"node_id"`
	ContainerID        string    `db:"container_id" json:"container_id"`
	ContainerName      string    `db:"container_name" json:"container_name"`
	CPUUsagePercent    float64   `db:"cpu_usage_percent" json:"cpu_usage_percent"`
	MemoryUsageBytes   int64     `db:"memory_usage_bytes" json:"memory_usage_bytes"`
	MemoryUsagePercent float64   `db:"memory_usage_percent" json:"memory_usage_percent"`
	Status             string    `db:"status" json:"status"`
	State              string    `db:"state" json:"state"`
	CollectedAt        time.Time `db:"collected_at" json:"collected_at"`
	CreatedAt          time.Time `db:"created_at" json:"created_at"`
}

type CreateContainerMetricRequest struct {
	NodeID             string    `json:"node_id"`
	ContainerID        string    `json:"container_id"`
	ContainerName      string    `json:"container_name"`
	CPUUsagePercent    float64   `json:"cpu_usage_percent"`
	MemoryUsageBytes   int64     `json:"memory_usage_bytes"`
	MemoryUsagePercent float64   `json:"memory_usage_percent"`
	Status             string    `json:"status"`
	State              string    `json:"state"`
	CollectedAt        time.Time `json:"collected_at"`
}

type ContainerMetricHistoryPoint struct {
	Bucket             time.Time `db:"bucket" json:"bucket"`
	CPUUsagePercent    float64   `db:"cpu_usage_percent" json:"cpu_usage_percent"`
	MemoryUsagePercent float64   `db:"memory_usage_percent" json:"memory_usage_percent"`
	MemoryUsageBytes   int64     `db:"memory_usage_bytes" json:"memory_usage_bytes"`
	State              string    `db:"state" json:"state"`
}

type ContainerHistoryParams struct {
	NodeID        string
	ContainerName string
	ContainerID   string
	From          time.Time
	To            time.Time
	Bucket        string
}

type ContainerMetricRepository interface {
	StoreBatch(metrics []*CreateContainerMetricRequest) error
	GetHistory(params ContainerHistoryParams) ([]*ContainerMetricHistoryPoint, error)
}

type ContainerSnapshotItem struct {
	ID      string
	Name    string
	State   string
	Status  string
	CPU     string
	Mem     string
	MemPerc string
}

type ContainerMetricService struct {
	repo         ContainerMetricRepository
	lastStored   map[string]time.Time // key: "nodeID:containerID"
	lastStoredMu sync.Mutex
}

func NewContainerMetricService(repo ContainerMetricRepository) *ContainerMetricService {
	return &ContainerMetricService{
		repo:       repo,
		lastStored: make(map[string]time.Time),
	}
}

func (s *ContainerMetricService) GetHistory(params ContainerHistoryParams) ([]*ContainerMetricHistoryPoint, error) {
	if params.NodeID == "" {
		return nil, fmt.Errorf("node_id is required")
	}
	return s.repo.GetHistory(params)
}

func (s *ContainerMetricService) ProcessSnapshot(nodeID string, items []ContainerSnapshotItem) error {
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

	multiplier := int64(1)
	clean := usedPart
	switch {
	case strings.HasSuffix(usedPart, "GiB") || strings.HasSuffix(usedPart, "GB"):
		multiplier = 1024 * 1024 * 1024
		clean = strings.TrimSuffix(strings.TrimSuffix(usedPart, "GiB"), "GB")
	case strings.HasSuffix(usedPart, "MiB") || strings.HasSuffix(usedPart, "MB"):
		multiplier = 1024 * 1024
		clean = strings.TrimSuffix(strings.TrimSuffix(usedPart, "MiB"), "MB")
	case strings.HasSuffix(usedPart, "KiB") || strings.HasSuffix(usedPart, "kB"):
		multiplier = 1024
		clean = strings.TrimSuffix(strings.TrimSuffix(usedPart, "KiB"), "kB")
	case strings.HasSuffix(usedPart, "B"):
		clean = strings.TrimSuffix(usedPart, "B")
	}

	val, err := strconv.ParseFloat(strings.TrimSpace(clean), 64)
	if err != nil {
		return 0
	}
	return int64(val * float64(multiplier))
}
