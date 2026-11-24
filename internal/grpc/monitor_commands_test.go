// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"errors"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// oneMonitorRepo holds monitor mon-1 on node-1.
type oneMonitorRepo struct{ apps.ServiceMonitorRepository }

func (oneMonitorRepo) FindByID(id string) (*apps.ServiceMonitor, error) {
	if id != "mon-1" {
		return nil, apps.ErrServiceMonitorNotFound
	}
	return &apps.ServiceMonitor{ID: "mon-1", NodeID: "node-1", ServiceName: "web", CheckType: "process", Enabled: true}, nil
}

func newMonitorTestServer() (*NodeAgentServer, chan *pb.ServerMessage) {
	s := &NodeAgentServer{
		registry:          newStreamRegistry(),
		serviceMonitorSvc: apps.NewServiceMonitorService(oneMonitorRepo{}),
	}
	send := make(chan *pb.ServerMessage, 4)
	s.registry.register("node-1", &nodeStream{send: send})
	return s, send
}

func TestSendHealthCheckConfig(t *testing.T) {
	s, send := newMonitorTestServer()
	m, _ := s.serviceMonitorSvc.FindByID("mon-1")

	if err := s.SendHealthCheckConfig("node-1", m); err != nil {
		t.Fatalf("connected node: %v", err)
	}
	if got := (<-send).GetConfigureHealthCheck().GetMonitorId(); got != "mon-1" {
		t.Errorf("configured monitor = %q, want mon-1", got)
	}
	// Without Redis there is no other instance that could hold the node's stream.
	if err := s.SendHealthCheckConfig("node-2", m); !errors.Is(err, ErrNodeNotConnected) {
		t.Errorf("offline node: err = %v, want ErrNodeNotConnected", err)
	}
	if err := s.StopHealthCheck("node-2", "mon-1"); !errors.Is(err, ErrNodeNotConnected) {
		t.Errorf("stop on offline node: err = %v, want ErrNodeNotConnected", err)
	}
	var nilServer *NodeAgentServer
	if err := nilServer.SendHealthCheckConfig("node-1", m); !errors.Is(err, ErrNodeNotConnected) {
		t.Errorf("no gRPC server: err = %v, want ErrNodeNotConnected", err)
	}
}

// Commands published by the REST instance reach the agent through the instance that holds its stream.
func TestHandleMonitorCommand(t *testing.T) {
	s, send := newMonitorTestServer()

	s.handleMonitorCommand("node-1", `{"action":"configure","monitor_id":"mon-1"}`)
	if got := (<-send).GetConfigureHealthCheck(); got.GetMonitorId() != "mon-1" || got.GetServiceName() != "web" {
		t.Errorf("configure: sent %v, want monitor mon-1 loaded from the database", got)
	}

	s.handleMonitorCommand("node-1", `{"action":"stop","monitor_id":"mon-9"}`)
	if got := (<-send).GetStopHealthCheck().GetMonitorId(); got != "mon-9" {
		t.Errorf("stop: monitor = %q, want mon-9", got)
	}

	// A monitor is only configured on its own node; unknown monitors and bad payloads are dropped.
	s.registry.register("node-2", &nodeStream{send: send})
	for _, tc := range []struct{ node, payload string }{
		{"node-2", `{"action":"configure","monitor_id":"mon-1"}`},
		{"node-1", `{"action":"configure","monitor_id":"missing"}`},
		{"node-1", `{"action":"restart","monitor_id":"mon-1"}`},
		{"node-1", `not json`},
	} {
		s.handleMonitorCommand(tc.node, tc.payload)
		select {
		case msg := <-send:
			t.Errorf("%s %s: sent %v, want nothing", tc.node, tc.payload, msg)
		default:
		}
	}
}
