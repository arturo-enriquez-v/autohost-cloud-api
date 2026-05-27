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

// SubscribeToContainers returns a channel that receives ContainerListPayload
// snapshots from the given node and a cancel function the caller must invoke
// when done.  The first subscriber causes a StreamContainers command to be sent
// to the agent; the last unsubscribe sends StopContainers.
func (s *NodeAgentServer) SubscribeToContainers(nodeID string) (<-chan *pb.ContainerListPayload, func()) {
	return s.containerBroker.subscribe(nodeID)
}

// RefreshContainerStream re-sends a StreamContainers command to the agent,
// causing it to push a fresh container snapshot immediately.  This is used as
// a server-side backup for agents that do not send periodic snapshots on their
// own (i.e. older agent builds without the 15 s internal ticker).
func (s *NodeAgentServer) RefreshContainerStream(nodeID string) error {
	return s.containerBroker.startStream(nodeID)
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
