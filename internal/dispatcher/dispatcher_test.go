// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package dispatcher

import (
	"errors"
	"testing"

	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
)

type mockDispatcher struct {
	shouldFail bool
	called     bool
}

func (m *mockDispatcher) DispatchJob(nodeID, jobID, commandName string, commandType nodecommand.CommandType) error {
	m.called = true
	if m.shouldFail {
		return errors.New("dispatch failed")
	}
	return nil
}

func TestMultiDispatcher_FirstSucceeds(t *testing.T) {
	d1 := &mockDispatcher{shouldFail: false}
	d2 := &mockDispatcher{shouldFail: false}

	md := NewMultiDispatcher(d1, d2)
	err := md.DispatchJob("node1", "job1", "app.start", nodecommand.CommandTypeDefault)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if !d1.called {
		t.Fatal("expected d1 to be called")
	}
	if d2.called {
		t.Fatal("expected d2 to not be called after first success")
	}
}

func TestMultiDispatcher_FallsBackToSecond(t *testing.T) {
	d1 := &mockDispatcher{shouldFail: true}
	d2 := &mockDispatcher{shouldFail: false}

	md := NewMultiDispatcher(d1, d2)
	err := md.DispatchJob("node1", "job1", "app.start", nodecommand.CommandTypeDefault)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if !d1.called {
		t.Fatal("expected d1 to be called")
	}
	if !d2.called {
		t.Fatal("expected d2 to be called on fallback")
	}
}
