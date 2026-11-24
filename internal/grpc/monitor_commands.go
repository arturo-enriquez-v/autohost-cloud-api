// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// ErrNodeNotConnected is returned when a message cannot reach a node because it has no live gRPC stream on
// any instance.
var ErrNodeNotConnected = errors.New("node not connected")

// monitorCommand is published on "nodes:<nodeID>:monitors:cmd" when the node's stream is held by another
// instance (REST and gRPC deployed separately). The instance holding the stream reloads the monitor from the
// database, so the message carries only its ID.
type monitorCommand struct {
	Action    string `json:"action"` // "configure" or "stop"
	MonitorID string `json:"monitor_id"`
}

const (
	monitorActionConfigure = "configure"
	monitorActionStop      = "stop"
)

func healthCheckConfigMessage(m *apps.ServiceMonitor) *pb.ServerMessage {
	return &pb.ServerMessage{
		Payload: &pb.ServerMessage_ConfigureHealthCheck{
			ConfigureHealthCheck: &pb.ConfigureHealthCheckPayload{
				MonitorId:          m.ID,
				ServiceName:        m.ServiceName,
				CheckType:          m.CheckType,
				HttpUrl:            m.HTTPURL,
				HttpExpectedStatus: int32(m.HTTPExpectedStatus),
				TcpHost:            m.TCPHost,
				TcpPort:            int32(m.TCPPort),
				IntervalSeconds:    int32(m.IntervalSeconds),
				FailureThreshold:   int32(m.FailureThreshold),
				Enabled:            m.Enabled,
				AutoHealConfig:     string(m.AutoHealConfig),
				AutoHealState:      string(m.AutoHealState),
			},
		},
	}
}

func stopHealthCheckMessage(monitorID string) *pb.ServerMessage {
	return &pb.ServerMessage{
		Payload: &pb.ServerMessage_StopHealthCheck{
			StopHealthCheck: &pb.StopHealthCheckPayload{MonitorId: monitorID},
		},
	}
}

// SendHealthCheckConfig pushes a monitor's configuration to its node's agent, which (re)starts the check and
// runs it right away. Returns ErrNodeNotConnected when the node is offline; it gets every monitor again
// when it reconnects.
func (s *NodeAgentServer) SendHealthCheckConfig(nodeID string, m *apps.ServiceMonitor) error {
	if s == nil {
		return ErrNodeNotConnected
	}
	if s.registry != nil && s.registry.isConnected(nodeID) {
		return s.registry.sendToNode(nodeID, healthCheckConfigMessage(m))
	}
	return s.publishMonitorCommand(nodeID, monitorCommand{Action: monitorActionConfigure, MonitorID: m.ID})
}

// StopHealthCheck tells the node's agent to drop a monitor. Returns ErrNodeNotConnected when the node is
// offline; deleted monitors are simply not sent again when it reconnects.
func (s *NodeAgentServer) StopHealthCheck(nodeID, monitorID string) error {
	if s == nil {
		return ErrNodeNotConnected
	}
	if s.registry != nil && s.registry.isConnected(nodeID) {
		return s.registry.sendToNode(nodeID, stopHealthCheckMessage(monitorID))
	}
	return s.publishMonitorCommand(nodeID, monitorCommand{Action: monitorActionStop, MonitorID: monitorID})
}

func (s *NodeAgentServer) publishMonitorCommand(nodeID string, cmd monitorCommand) error {
	if s.redis == nil || !s.IsNodeConnected(nodeID) {
		return ErrNodeNotConnected
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	return s.redis.Publish(context.Background(), fmt.Sprintf("nodes:%s:monitors:cmd", nodeID), data).Err()
}

// handleMonitorCommand delivers a monitor command received through Redis to a node connected here.
func (s *NodeAgentServer) handleMonitorCommand(nodeID, payload string) {
	var cmd monitorCommand
	if err := json.Unmarshal([]byte(payload), &cmd); err != nil || cmd.MonitorID == "" {
		log.Printf("[gRPC] invalid monitor command for node %s: %q", nodeID, payload)
		return
	}
	var msg *pb.ServerMessage
	switch cmd.Action {
	case monitorActionConfigure:
		if s.serviceMonitorSvc == nil {
			return
		}
		m, err := s.serviceMonitorSvc.FindByID(cmd.MonitorID)
		if err != nil {
			log.Printf("[gRPC] monitor command for node %s: load monitor %s: %v", nodeID, cmd.MonitorID, err)
			return
		}
		if m.NodeID != nodeID {
			log.Printf("[gRPC] monitor command for node %s: monitor %s belongs to another node", nodeID, cmd.MonitorID)
			return
		}
		msg = healthCheckConfigMessage(m)
	case monitorActionStop:
		msg = stopHealthCheckMessage(cmd.MonitorID)
	default:
		log.Printf("[gRPC] unknown monitor command %q for node %s", cmd.Action, nodeID)
		return
	}
	if err := s.registry.sendToNode(nodeID, msg); err != nil {
		log.Printf("[gRPC] monitor command %s %s to node %s: %v", cmd.Action, cmd.MonitorID, nodeID, err)
	}
}
