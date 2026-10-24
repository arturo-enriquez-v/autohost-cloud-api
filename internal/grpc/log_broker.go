// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"log"
	"sync"
	"sync/atomic"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// logSubscriber receives fan-out log entries for a node.
type logSubscriber struct {
	ch chan *pb.LogEntryPayload
}

// logStreamParams records the active StreamLogs request for a node so it can
// be re-issued automatically when the agent reconnects.
type logStreamParams struct {
	unit  string
	lines int32
}

// logBroker manages per-node pub/sub of log entries between the gRPC stream
// and zero-or-more SSE subscribers.
type logBroker struct {
	mu        sync.RWMutex
	subs      map[string]map[uint64]*logSubscriber // nodeID -> subID -> subscriber
	params    map[string]*logStreamParams          // nodeID -> active stream params
	nextSubID uint64

	// sendCmd is called to ask the agent to start/stop streaming logs.
	// Injected by the server so the broker doesn't depend on streamRegistry.
	sendCmd func(nodeID string, msg *pb.ServerMessage) error
}

func newLogBroker(sendCmd func(string, *pb.ServerMessage) error) *logBroker {
	return &logBroker{
		subs:    make(map[string]map[uint64]*logSubscriber),
		params:  make(map[string]*logStreamParams),
		sendCmd: sendCmd,
	}
}

// activeParams returns the current StreamLogs parameters for a node, if any
// subscribers are waiting for logs.
func (b *logBroker) activeParams(nodeID string) (*logStreamParams, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	p, ok := b.params[nodeID]
	hasSubs := len(b.subs[nodeID]) > 0
	return p, ok && hasSubs
}

// subscribe returns a channel that receives log entries from the given node
// and a cancel function the caller must invoke when done.
// If this is the first subscriber, it also asks the agent to start streaming.
func (b *logBroker) subscribe(nodeID, unit string, lines int32) (<-chan *pb.LogEntryPayload, func()) {
	id := atomic.AddUint64(&b.nextSubID, 1)
	ch := make(chan *pb.LogEntryPayload, 256)

	b.mu.Lock()
	if b.subs[nodeID] == nil {
		b.subs[nodeID] = make(map[uint64]*logSubscriber)
	}
	first := len(b.subs[nodeID]) == 0
	b.subs[nodeID][id] = &logSubscriber{ch: ch}
	if first {
		b.params[nodeID] = &logStreamParams{unit: unit, lines: lines}
	}
	b.mu.Unlock()

	if first {
		if err := b.startStream(nodeID, unit, lines); err != nil {
			log.Printf("[gRPC] start log stream for node %s: %v", nodeID, err)
		}
	}

	cancel := func() {
		b.mu.Lock()
		delete(b.subs[nodeID], id)
		last := len(b.subs[nodeID]) == 0
		if last {
			delete(b.subs, nodeID)
			delete(b.params, nodeID)
		}
		b.mu.Unlock()
		close(ch)

		if last {
			if err := b.stopStream(nodeID); err != nil {
				log.Printf("[gRPC] stop log stream for node %s: %v", nodeID, err)
			}
		}
	}
	return ch, cancel
}

// fanOut delivers a log entry to every subscriber of a node.
func (b *logBroker) fanOut(nodeID string, entry *pb.LogEntryPayload) {
	b.mu.RLock()
	subs := b.subs[nodeID]
	b.mu.RUnlock()
	for _, sub := range subs {
		select {
		case sub.ch <- entry:
		default: // drop if subscriber is slow
		}
	}
}

// startStream sends a StreamLogs command to the agent.
func (b *logBroker) startStream(nodeID, unit string, lines int32) error {
	b.mu.Lock()
	b.params[nodeID] = &logStreamParams{unit: unit, lines: lines}
	b.mu.Unlock()

	return b.sendCmd(nodeID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_StreamLogs{
			StreamLogs: &pb.StreamLogsPayload{Unit: unit, Lines: lines},
		},
	})
}

// stopStream asks the agent to stop streaming logs and drops all subscribers.
func (b *logBroker) stopStream(nodeID string) error {
	b.mu.Lock()
	delete(b.subs, nodeID)
	delete(b.params, nodeID)
	b.mu.Unlock()

	return b.sendCmd(nodeID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_StopLogs{
			StopLogs: &pb.StopLogsPayload{},
		},
	})
}

// getParams returns active stream parameters for a node if recorded.
func (b *logBroker) getParams(nodeID string) (*logStreamParams, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	p, ok := b.params[nodeID]
	return p, ok
}
