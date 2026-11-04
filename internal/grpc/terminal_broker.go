// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"log"
	"sync"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// TerminalSession represents an active interactive PTY session on a remote node.
type TerminalSession struct {
	SessionID string
	NodeID    string
	OutputCh  chan []byte
	ClosedCh  chan *pb.TerminalClosedPayload
	done      chan struct{}
	closeOnce sync.Once
}

func (ts *TerminalSession) closeChannels() {
	ts.closeOnce.Do(func() {
		close(ts.done)
	})
}

// Done returns a channel that is closed when the terminal session ends.
func (ts *TerminalSession) Done() <-chan struct{} {
	return ts.done
}

// terminalBroker manages active interactive terminal sessions across connected nodes.
type terminalBroker struct {
	mu       sync.RWMutex
	sessions map[string]*TerminalSession // sessionID -> session
	sendCmd  func(nodeID string, msg *pb.ServerMessage) error
}

func newTerminalBroker(sendCmd func(string, *pb.ServerMessage) error) *terminalBroker {
	return &terminalBroker{
		sessions: make(map[string]*TerminalSession),
		sendCmd:  sendCmd,
	}
}

func (b *terminalBroker) startSession(nodeID, sessionID string, cols, rows uint32, shell, cwd string) (*TerminalSession, error) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	ts := &TerminalSession{
		SessionID: sessionID,
		NodeID:    nodeID,
		OutputCh:  make(chan []byte, 512),
		ClosedCh:  make(chan *pb.TerminalClosedPayload, 1),
		done:      make(chan struct{}),
	}

	b.mu.Lock()
	b.sessions[sessionID] = ts
	b.mu.Unlock()

	err := b.sendCmd(nodeID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_StartTerminal{
			StartTerminal: &pb.StartTerminalPayload{
				SessionId: sessionID,
				Cols:      cols,
				Rows:      rows,
				Shell:     shell,
				Cwd:       cwd,
			},
		},
	})
	if err != nil {
		b.mu.Lock()
		delete(b.sessions, sessionID)
		b.mu.Unlock()
		ts.closeChannels()
		return nil, err
	}

	return ts, nil
}

func (b *terminalBroker) sendInput(nodeID, sessionID string, data []byte) error {
	return b.sendCmd(nodeID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_TerminalInput{
			TerminalInput: &pb.TerminalInputPayload{
				SessionId: sessionID,
				Data:      data,
			},
		},
	})
}

func (b *terminalBroker) resize(nodeID, sessionID string, cols, rows uint32) error {
	return b.sendCmd(nodeID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_TerminalResize{
			TerminalResize: &pb.TerminalResizePayload{
				SessionId: sessionID,
				Cols:      cols,
				Rows:      rows,
			},
		},
	})
}

func (b *terminalBroker) closeSession(nodeID, sessionID string, notifyAgent bool) {
	b.mu.Lock()
	ts, exists := b.sessions[sessionID]
	if exists {
		delete(b.sessions, sessionID)
	}
	b.mu.Unlock()

	if exists && ts != nil {
		ts.closeChannels()
	}

	if notifyAgent {
		if err := b.sendCmd(nodeID, &pb.ServerMessage{
			Payload: &pb.ServerMessage_CloseTerminal{
				CloseTerminal: &pb.CloseTerminalPayload{
					SessionId: sessionID,
				},
			},
		}); err != nil {
			log.Printf("[gRPC] close terminal session %s on node %s: %v", sessionID, nodeID, err)
		}
	}
}

func (b *terminalBroker) handleOutput(nodeID string, p *pb.TerminalOutputPayload) {
	if p == nil {
		return
	}
	b.mu.RLock()
	ts, ok := b.sessions[p.GetSessionId()]
	b.mu.RUnlock()
	if !ok || ts == nil || ts.NodeID != nodeID {
		return
	}

	// Copy slice so protobuf buffer reuse cannot mutate queued bytes
	buf := make([]byte, len(p.GetData()))
	copy(buf, p.GetData())

	select {
	case <-ts.done:
		return
	case ts.OutputCh <- buf:
	default:
		// Drop oldest chunk if subscriber is severely backed up to prevent blocking gRPC recv loop
		select {
		case <-ts.OutputCh:
		default:
		}
		select {
		case ts.OutputCh <- buf:
		default:
		}
	}
}

func (b *terminalBroker) handleClosed(nodeID string, p *pb.TerminalClosedPayload) {
	if p == nil {
		return
	}
	b.mu.Lock()
	ts, ok := b.sessions[p.GetSessionId()]
	if ok {
		delete(b.sessions, p.GetSessionId())
	}
	b.mu.Unlock()

	if !ok || ts == nil {
		return
	}

	select {
	case ts.ClosedCh <- p:
	default:
	}
	ts.closeChannels()
}

func (b *terminalBroker) closeAllForNode(nodeID string) {
	b.mu.Lock()
	var toClose []*TerminalSession
	for id, ts := range b.sessions {
		if ts.NodeID == nodeID {
			toClose = append(toClose, ts)
			delete(b.sessions, id)
		}
	}
	b.mu.Unlock()

	for _, ts := range toClose {
		select {
		case ts.ClosedCh <- &pb.TerminalClosedPayload{
			SessionId: ts.SessionID,
			ExitCode:  -1,
			Error:     "node disconnected from server",
		}:
		default:
		}
		ts.closeChannels()
	}
}

// ---- NodeAgentServer Terminal Helpers ---------------------------------------

func (s *NodeAgentServer) StartTerminalSession(nodeID, sessionID string, cols, rows uint32, shell, cwd string) (*TerminalSession, error) {
	return s.terminalBroker.startSession(nodeID, sessionID, cols, rows, shell, cwd)
}

func (s *NodeAgentServer) SendTerminalInput(nodeID, sessionID string, data []byte) error {
	return s.terminalBroker.sendInput(nodeID, sessionID, data)
}

func (s *NodeAgentServer) ResizeTerminalSession(nodeID, sessionID string, cols, rows uint32) error {
	return s.terminalBroker.resize(nodeID, sessionID, cols, rows)
}

func (s *NodeAgentServer) CloseTerminalSession(nodeID, sessionID string) {
	s.terminalBroker.closeSession(nodeID, sessionID, true)
}

