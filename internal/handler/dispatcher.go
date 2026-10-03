package handler

import (
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
)

// NodeDispatcher abstracts sending an execute_job request to a connected node,
// regardless of the underlying transport (WebSocket or gRPC).
type NodeDispatcher interface {
	DispatchJob(nodeID, jobID, commandName string, commandType nodecommand.CommandType) error
}

// ParamDispatcher extends NodeDispatcher with parameterized dispatch and connectivity checks.
type ParamDispatcher interface {
	NodeDispatcher
	DispatchJobWithParams(nodeID, jobID, commandName string, commandType nodecommand.CommandType, params string) error
}

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
	return m.DispatchJobWithParams(nodeID, jobID, commandName, commandType, "")
}

// DispatchJobWithParams tries each dispatcher that supports parameters or falls back to DispatchJob.
func (m *MultiDispatcher) DispatchJobWithParams(
	nodeID, jobID, commandName string,
	commandType nodecommand.CommandType,
	params string,
) error {
	var lastErr error
	for _, d := range m.dispatchers {
		if pd, ok := d.(ParamDispatcher); ok {
			if err := pd.DispatchJobWithParams(nodeID, jobID, commandName, commandType, params); err == nil {
				return nil
			} else {
				lastErr = err
			}
		} else {
			if err := d.DispatchJob(nodeID, jobID, commandName, commandType); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
	}
	return lastErr
}

// IsNodeConnected checks if any underlying dispatcher has an active connection (or Redis presence).
func (m *MultiDispatcher) IsNodeConnected(nodeID string) bool {
	for _, d := range m.dispatchers {
		if cd, ok := d.(interface{ IsNodeConnected(string) bool }); ok {
			if cd.IsNodeConnected(nodeID) {
				return true
			}
		}
	}
	return false
}
