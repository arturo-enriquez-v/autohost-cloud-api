// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// onlineTTL is how long a node stays marked online in Redis after its last heartbeat or metric.
const onlineTTL = 2 * time.Minute

// handleNodeMessage routes one message received on a node's Connect stream to its handler.
func (s *NodeAgentServer) handleNodeMessage(nodeID string, in *pb.NodeMessage) {
	switch p := in.Payload.(type) {
	case *pb.NodeMessage_JobResult:
		s.handleJobResult(nodeID, p.JobResult)
	case *pb.NodeMessage_Heartbeat:
		s.handleHeartbeat(nodeID, p.Heartbeat)
	case *pb.NodeMessage_Metric:
		s.handleMetric(nodeID, p.Metric)
	case *pb.NodeMessage_LogEntry:
		s.handleLogEntry(nodeID, p.LogEntry)
	case *pb.NodeMessage_HypervisorInfo:
		s.storeHypervisorInfo(nodeID, p.HypervisorInfo)
	case *pb.NodeMessage_ContainerList:
		s.handleContainerList(nodeID, p.ContainerList)
	case *pb.NodeMessage_HealthResult:
		go s.handleHealthCheckResult(nodeID, p.HealthResult)
	case *pb.NodeMessage_LogDiagnostic:
		go s.handleLogDiagnosticResult(nodeID, p.LogDiagnostic)
	case *pb.NodeMessage_NetworkStats:
		go s.handleNetworkStatsResult(nodeID, p.NetworkStats)
	case *pb.NodeMessage_TerminalOutput:
		s.handleTerminalOutput(nodeID, p.TerminalOutput)
	case *pb.NodeMessage_TerminalClosed:
		s.handleTerminalClosed(nodeID, p.TerminalClosed)
	case *pb.NodeMessage_FileOpResponse:
		go s.handleFileOpResponse(nodeID, p.FileOpResponse)
	default:
		log.Printf("[gRPC] unknown payload from node %s", nodeID)
	}
}

// markOnline refreshes the node's online flag in Redis, read by REST processes that do not hold the stream.
func (s *NodeAgentServer) markOnline(nodeID string) {
	if s.redis != nil {
		_ = s.redis.Set(context.Background(), fmt.Sprintf("nodes:%s:online", nodeID), "1", onlineTTL).Err()
	}
}

// publish sends a payload to a Redis channel for REST processes running separately from the gRPC server.
func (s *NodeAgentServer) publish(channel string, payload any) {
	if s.redis == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = s.redis.Publish(context.Background(), channel, data).Err()
}

func (s *NodeAgentServer) handleHeartbeat(nodeID string, hb *pb.HeartbeatPayload) {
	s.markOnline(nodeID)
	if err := s.nodeSvc.UpdateLastSeen(nodeID); err != nil {
		log.Printf("[gRPC] update last_seen for node %s: %v", nodeID, err)
	} else {
		log.Printf("[gRPC] heartbeat from node %s", hb.GetNodeId())
	}
}

func (s *NodeAgentServer) handleMetric(nodeID string, m *pb.MetricPayload) {
	s.markOnline(nodeID)
	// Fan-out to live SSE subscribers immediately — zero DB cost.
	s.metricBroker.fanOut(nodeID, m)

	// Publish to Redis for distributed subscribers (e.g. Cloud Run REST API).
	s.publish(fmt.Sprintf("nodes:%s:metrics", nodeID), map[string]any{
		"cpu_usage_percent":      m.GetCpuUsagePercent(),
		"memory_total_bytes":     m.GetRamTotalBytes(),
		"memory_used_bytes":      m.GetRamUsedBytes(),
		"memory_available_bytes": m.GetRamAvailableBytes(),
		"memory_usage_percent":   m.GetRamUsagePercent(),
		"disk_total_bytes":       m.GetDiskTotalBytes(),
		"disk_used_bytes":        m.GetDiskUsedBytes(),
		"disk_available_bytes":   m.GetDiskAvailableBytes(),
		"disk_usage_percent":     m.GetDiskUsagePercent(),
		"uptime_seconds":         m.GetUptimeSeconds(),
		"cpu_temp_celsius":       m.GetCpuTempCelsius(),
		"collected_at":           time.Now().UTC().Format(time.RFC3339),
	})

	// Persist to DB at most once per minute to keep write volume low.
	if !s.shouldPersistMetric(nodeID) {
		return
	}
	req := &node.CreateNodeMetricRequest{
		NodeID:               nodeID,
		CPUUsagePercent:      float64(m.GetCpuUsagePercent()),
		MemoryTotalBytes:     int64(m.GetRamTotalBytes()),
		MemoryUsedBytes:      int64(m.GetRamUsedBytes()),
		MemoryAvailableBytes: int64(m.GetRamAvailableBytes()),
		MemoryUsagePercent:   float64(m.GetRamUsagePercent()),
		DiskTotalBytes:       int64(m.GetDiskTotalBytes()),
		DiskUsedBytes:        int64(m.GetDiskUsedBytes()),
		DiskAvailableBytes:   int64(m.GetDiskAvailableBytes()),
		DiskUsagePercent:     float64(m.GetDiskUsagePercent()),
		CPUTempCelsius:       float64(m.GetCpuTempCelsius()),
		CollectedAt:          time.Now(),
	}
	if _, err := s.metricSvc.StoreNodeMetric(req); err != nil {
		log.Printf("[gRPC] store metrics for node %s: %v", nodeID, err)
	}
	// Piggyback last_seen update on the same throttled window.
	if err := s.nodeSvc.UpdateLastSeen(nodeID); err != nil {
		log.Printf("[gRPC] update last_seen (metric) for node %s: %v", nodeID, err)
	}
	// Check alert thresholds asynchronously; never block the stream.
	go s.checkThresholdAlerts(nodeID, req)
}

func (s *NodeAgentServer) handleLogEntry(nodeID string, e *pb.LogEntryPayload) {
	s.logs.fanOut(nodeID, e)
	if e == nil {
		return
	}
	s.publish(fmt.Sprintf("nodes:%s:logs", nodeID), map[string]any{
		"timestamp_us": e.GetTimestampUs(),
		"unit":         e.GetUnit(),
		"message":      e.GetMessage(),
	})
}

// containerInfo and containerList are the JSON shape of container snapshots published to SSE subscribers
// (see handler.sseContainerList, which must stay identical).
type containerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	CPU     string `json:"cpu,omitempty"`
	Mem     string `json:"mem,omitempty"`
	MemPerc string `json:"memPerc,omitempty"`
}

type containerList struct {
	Containers []containerInfo `json:"containers"`
	Error      string          `json:"error,omitempty"`
}

func (s *NodeAgentServer) handleContainerList(nodeID string, cl *pb.ContainerListPayload) {
	s.containerBroker.fanOut(nodeID, cl)
	if cl == nil {
		return
	}

	if s.redis != nil {
		list := containerList{
			Error:      cl.GetError(),
			Containers: make([]containerInfo, 0, len(cl.GetContainers())),
		}
		for _, c := range cl.GetContainers() {
			list.Containers = append(list.Containers, containerInfo{
				ID:      c.GetId(),
				Name:    c.GetName(),
				Image:   c.GetImage(),
				State:   c.GetState(),
				Status:  c.GetStatus(),
				CPU:     c.GetCpu(),
				Mem:     c.GetMem(),
				MemPerc: c.GetMemPerc(),
			})
		}
		if data, err := json.Marshal(list); err == nil {
			_ = s.redis.Set(context.Background(), fmt.Sprintf("nodes:%s:containers:latest", nodeID), data, 10*time.Minute).Err()
			_ = s.redis.Publish(context.Background(), fmt.Sprintf("nodes:%s:containers", nodeID), data).Err()
		}
	}

	if s.containerMetricSvc != nil && len(cl.Containers) > 0 {
		var items []node.ContainerSnapshotItem
		for _, c := range cl.Containers {
			items = append(items, node.ContainerSnapshotItem{
				ID:      c.Id,
				Name:    c.Name,
				State:   c.State,
				Status:  c.Status,
				CPU:     c.Cpu,
				Mem:     c.Mem,
				MemPerc: c.MemPerc,
			})
		}
		go func() {
			if err := s.containerMetricSvc.ProcessSnapshot(nodeID, items); err != nil {
				log.Printf("[gRPC] store container metrics for node %s: %v", nodeID, err)
			}
		}()
	}
}

func (s *NodeAgentServer) handleTerminalOutput(nodeID string, out *pb.TerminalOutputPayload) {
	if s.terminalBroker != nil {
		s.terminalBroker.handleOutput(nodeID, out)
	}
	if out == nil {
		return
	}
	s.publish(fmt.Sprintf("nodes:%s:terminal:%s:out", nodeID, out.GetSessionId()), map[string]any{
		"type":       "output",
		"session_id": out.GetSessionId(),
		"data":       out.GetData(),
	})
}

func (s *NodeAgentServer) handleTerminalClosed(nodeID string, closed *pb.TerminalClosedPayload) {
	if s.terminalBroker != nil {
		s.terminalBroker.handleClosed(nodeID, closed)
	}
	if closed == nil {
		return
	}
	s.publish(fmt.Sprintf("nodes:%s:terminal:%s:out", nodeID, closed.GetSessionId()), map[string]any{
		"type":       "closed",
		"session_id": closed.GetSessionId(),
		"exit_code":  closed.GetExitCode(),
		"error":      closed.GetError(),
	})
}
