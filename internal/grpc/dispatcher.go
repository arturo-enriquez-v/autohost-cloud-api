package grpcserver

import (
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
	ct := pb.CommandType_COMMAND_TYPE_DEFAULT
	if commandType == nodecommand.CommandTypeCustom {
		ct = pb.CommandType_COMMAND_TYPE_CUSTOM
	}
	msg := &pb.ServerMessage{
		Payload: &pb.ServerMessage_ExecuteJob{
			ExecuteJob: &pb.ExecuteJobPayload{
				JobId:       jobID,
				CommandName: commandName,
				CommandType: ct,
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
