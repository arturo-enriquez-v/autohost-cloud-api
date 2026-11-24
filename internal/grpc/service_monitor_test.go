// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import "testing"

// The agent reports every check while a service stays down; only the first one may send a Telegram alert.
func TestWentDown(t *testing.T) {
	tests := []struct {
		previous, current string
		want              bool
	}{
		{"healthy", "down", true},
		{"degraded", "down", true},
		{"unknown", "down", true},
		{"down", "down", false},
		{"down", "degraded", false},
		{"down", "healthy", false},
		{"healthy", "degraded", false},
	}
	for _, tt := range tests {
		if got := wentDown(tt.previous, tt.current); got != tt.want {
			t.Errorf("wentDown(%q, %q) = %v, want %v", tt.previous, tt.current, got, tt.want)
		}
	}
}
