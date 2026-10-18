// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// hypervisorInfoTTL bounds how long a report stays valid in Redis. The agent
// re-reports on every Connect session, so a stale entry means the host has
// been offline for a day.
const hypervisorInfoTTL = 24 * time.Hour

// HypervisorInfo is the virtualization capacity reported by an agent.
type HypervisorInfo struct {
	Capable               bool   `json:"capable"`
	KVMAvailable          bool   `json:"kvm_available"`
	LibvirtAvailable      bool   `json:"libvirt_available"`
	LibvirtVersion        string `json:"libvirt_version"`
	Reason                string `json:"reason,omitempty"`
	NetworkName           string `json:"network_name"`
	CPUCores              uint32 `json:"cpu_cores"`
	RAMTotalBytes         uint64 `json:"ram_total_bytes"`
	StorageTotalBytes     uint64 `json:"storage_total_bytes"`
	StorageAvailableBytes uint64 `json:"storage_available_bytes"`
	ReportedAt            string `json:"reported_at"`
}

func hypervisorRedisKey(nodeID string) string {
	return fmt.Sprintf("nodes:%s:hypervisor", nodeID)
}

// storeHypervisorInfo keeps the latest report in memory and, when available,
// in Redis so a REST instance that is not the one holding the agent's gRPC
// stream can still serve it.
func (s *NodeAgentServer) storeHypervisorInfo(nodeID string, p *pb.HypervisorInfoPayload) {
	if p == nil {
		return
	}
	info := &HypervisorInfo{
		Capable:               p.GetCapable(),
		KVMAvailable:          p.GetKvmAvailable(),
		LibvirtAvailable:      p.GetLibvirtAvailable(),
		LibvirtVersion:        p.GetLibvirtVersion(),
		Reason:                p.GetReason(),
		NetworkName:           p.GetNetworkName(),
		CPUCores:              p.GetCpuCores(),
		RAMTotalBytes:         p.GetRamTotalBytes(),
		StorageTotalBytes:     p.GetStorageTotalBytes(),
		StorageAvailableBytes: p.GetStorageAvailableBytes(),
		ReportedAt:            time.Now().UTC().Format(time.RFC3339),
	}
	s.hypervisorInfo.Store(nodeID, info)
	log.Printf("[gRPC] hypervisor info from node %s: capable=%t libvirt=%s cores=%d", nodeID, info.Capable, info.LibvirtVersion, info.CPUCores)

	if s.redis != nil {
		if data, err := json.Marshal(info); err == nil {
			if err := s.redis.Set(context.Background(), hypervisorRedisKey(nodeID), data, hypervisorInfoTTL).Err(); err != nil {
				log.Printf("[gRPC] store hypervisor info in redis for node %s: %v", nodeID, err)
			}
		}
	}
}

// GetHypervisorInfo returns the last virtualization report for a node. Redis is
// consulted first (shared across instances) with the in-memory copy as fallback.
func (s *NodeAgentServer) GetHypervisorInfo(ctx context.Context, nodeID string) (*HypervisorInfo, bool) {
	if s.redis != nil {
		if raw, err := s.redis.Get(ctx, hypervisorRedisKey(nodeID)).Result(); err == nil && raw != "" {
			var info HypervisorInfo
			if json.Unmarshal([]byte(raw), &info) == nil {
				return &info, true
			}
		}
	}
	if v, ok := s.hypervisorInfo.Load(nodeID); ok {
		return v.(*HypervisorInfo), true
	}
	return nil, false
}
