package grpcserver

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// SendToNode pushes a ServerMessage to a connected node.
// Returns an error if the node has no live gRPC Connect stream.
func (s *NodeAgentServer) SendToNode(nodeID string, msg *pb.ServerMessage) error {
	return s.registry.sendToNode(nodeID, msg)
}

// IsNodeConnected reports whether the node has an active gRPC Connect stream.
func (s *NodeAgentServer) IsNodeConnected(nodeID string) bool {
	return s.registry.isConnected(nodeID)
}

// DispatchJob builds a gRPC ServerMessage and pushes it to the connected node.
// Implements handler.NodeDispatcher.
func (s *NodeAgentServer) DispatchJob(
	nodeID, jobID, commandName string,
	commandType nodecommand.CommandType,
) error {
	return s.DispatchJobWithParams(nodeID, jobID, commandName, commandType, "")
}

// DispatchJobWithParams is like DispatchJob but also sends a JSON-encoded
// params string that the agent passes to the command handler as payload.
func (s *NodeAgentServer) DispatchJobWithParams(
	nodeID, jobID, commandName string,
	commandType nodecommand.CommandType,
	params string,
) error {
	ct := pb.CommandType_COMMAND_TYPE_DEFAULT
	switch commandType {
	case nodecommand.CommandTypeCustom:
		ct = pb.CommandType_COMMAND_TYPE_CUSTOM
	case nodecommand.CommandTypeInstallApp:
		ct = pb.CommandType_COMMAND_TYPE_INSTALL_APP
	}
	msg := &pb.ServerMessage{
		Payload: &pb.ServerMessage_ExecuteJob{
			ExecuteJob: &pb.ExecuteJobPayload{
				JobId:       jobID,
				CommandName: commandName,
				CommandType: ct,
				Params:      params,
			},
		},
	}
	return s.SendToNode(nodeID, msg)
}

// SubscribeToNodeLogs returns a channel that receives log entries from the
// given node and a cancel function the caller must invoke when done.
func (s *NodeAgentServer) SubscribeToNodeLogs(nodeID, unit string, lines int32) (<-chan *pb.LogEntryPayload, func()) {
	return s.logs.subscribe(nodeID, unit, lines)
}

// StartNodeLogStream sends a StreamLogs command to the agent so it starts
// sending LogEntry messages back. unit="" means all units.
func (s *NodeAgentServer) StartNodeLogStream(nodeID, unit string, lines int32) error {
	return s.logs.startStream(nodeID, unit, lines)
}

// StopNodeLogStream asks the agent to stop streaming logs.
func (s *NodeAgentServer) StopNodeLogStream(nodeID string) error {
	return s.logs.stopStream(nodeID)
}

// RequestProfile sends a profile-collection request to the agent and blocks
// until the agent replies with the raw pprof bytes (or ctx is cancelled).
//
// profileType must be "cpu" or "heap".
// durationSec is only used for CPU profiles; use 0 to accept the agent default (30 s).
//
// The returned bytes are the raw output of pprof.StartCPUProfile /
// pprof.WriteHeapProfile — compatible with `go tool pprof`.
func (s *NodeAgentServer) RequestProfile(
	ctx context.Context,
	nodeID, profileType string,
	durationSec int32,
) ([]byte, error) {
	var pt pb.ProfileType
	switch profileType {
	case "cpu":
		pt = pb.ProfileType_PROFILE_TYPE_CPU
	case "heap":
		pt = pb.ProfileType_PROFILE_TYPE_HEAP
	default:
		return nil, fmt.Errorf("unknown profile type %q; use \"cpu\" or \"heap\"", profileType)
	}

	requestID := uuid.New().String()

	// Register before sending so we never miss the reply.
	resultCh := s.profileBroker.register(requestID)
	defer s.profileBroker.cancel(requestID)

	// Budget: sampling window + 15 s grace for collection + transmission.
	timeout := time.Duration(durationSec+15) * time.Second
	if pt == pb.ProfileType_PROFILE_TYPE_HEAP {
		timeout = 15 * time.Second
	}
	if timeout < 15*time.Second {
		timeout = 15 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	msg := &pb.ServerMessage{
		Payload: &pb.ServerMessage_RequestProfile{
			RequestProfile: &pb.RequestProfilePayload{
				RequestId:       requestID,
				ProfileType:     pt,
				DurationSeconds: durationSec,
			},
		},
	}
	if err := s.SendToNode(nodeID, msg); err != nil {
		return nil, fmt.Errorf("send profile request to node %s: %w", nodeID, err)
	}

	select {
	case result := <-resultCh:
		if result.GetError() != "" {
			return nil, fmt.Errorf("agent profiling error: %s", result.GetError())
		}
		return result.GetData(), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("profile request timed out waiting for node %s: %w", nodeID, ctx.Err())
	}
}
