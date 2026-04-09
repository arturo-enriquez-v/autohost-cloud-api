package handler

import (
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
)

// MultiDispatcher tries each dispatcher in order and returns nil on the first
// success.  This lets the server dispatch jobs over gRPC or WebSocket
// transparently: whichever transport the node is currently connected on wins.
type MultiDispatcher struct {
	dispatchers []NodeDispatcher
}

// NewMultiDispatcher creates a dispatcher that fans out to all provided
// dispatchers in order (first success wins).
func NewMultiDispatcher(dispatchers ...NodeDispatcher) *MultiDispatcher {
	return &MultiDispatcher{dispatchers: dispatchers}
}

// DispatchJob tries each dispatcher until one succeeds.
func (m *MultiDispatcher) DispatchJob(
	nodeID, jobID, commandName string,
	commandType nodecommand.CommandType,
) error {
	var lastErr error
	for _, d := range m.dispatchers {
		if err := d.DispatchJob(nodeID, jobID, commandName, commandType); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}
