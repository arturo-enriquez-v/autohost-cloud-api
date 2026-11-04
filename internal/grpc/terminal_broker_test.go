// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"testing"
	"time"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

func TestTerminalBrokerLifecycle(t *testing.T) {
	var sent []*pb.ServerMessage
	b := newTerminalBroker(func(nodeID string, msg *pb.ServerMessage) error {
		sent = append(sent, msg)
		return nil
	})

	session, err := b.startSession("node-1", "sess-1", 120, 35, "/bin/bash", "/root")
	if err != nil {
		t.Fatalf("unexpected error starting session: %v", err)
	}
	if len(sent) != 1 || sent[0].GetStartTerminal() == nil {
		t.Fatalf("expected StartTerminal message to be sent")
	}
	if sent[0].GetStartTerminal().GetCols() != 120 || sent[0].GetStartTerminal().GetRows() != 35 {
		t.Fatalf("unexpected cols/rows: %+v", sent[0].GetStartTerminal())
	}

	// Simulate agent sending output
	b.handleOutput("node-1", &pb.TerminalOutputPayload{
		SessionId: "sess-1",
		Data:      []byte("root@homelab:~# "),
	})

	select {
	case out := <-session.OutputCh:
		if string(out) != "root@homelab:~# " {
			t.Fatalf("unexpected output: %q", string(out))
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal output")
	}

	// Send input and resize
	if err := b.sendInput("node-1", "sess-1", []byte("ls -la\r")); err != nil {
		t.Fatalf("sendInput error: %v", err)
	}
	if err := b.resize("node-1", "sess-1", 140, 40); err != nil {
		t.Fatalf("resize error: %v", err)
	}

	// Simulate agent closing session
	b.handleClosed("node-1", &pb.TerminalClosedPayload{
		SessionId: "sess-1",
		ExitCode:  0,
	})

	select {
	case closed := <-session.ClosedCh:
		if closed.GetExitCode() != 0 {
			t.Fatalf("expected exit code 0, got %d", closed.GetExitCode())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for closed notification")
	}

	select {
	case <-session.Done():
	default:
		t.Fatal("expected session.Done() to be closed")
	}
}

