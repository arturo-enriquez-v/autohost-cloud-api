// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDetectAutoHealEvent(t *testing.T) {
	now := time.Unix(2000, 0)
	cfg := json.RawMessage(`{"enabled":true,"restart":{"enabled":true,"max_attempts":3},"recreate":{"enabled":true,"max_attempts":1}}`)
	tests := []struct {
		name       string
		old, new   string
		wantAction string
		check      func(t *testing.T, e *AutoHealEvent)
	}{
		{"healthy check", `{"status":"HEALTHY"}`, `{"status":"HEALTHY"}`, "", nil},
		{"failing below threshold", `{"status":"HEALTHY"}`,
			`{"status":"DEGRADED","failure_started_at":1000}`, "", nil},
		{"first restart",
			`{"status":"DEGRADED","failure_started_at":1000}`,
			`{"status":"COOLING_DOWN","failure_started_at":1000,"restart_timestamps":[1030],"last_action_timestamp":1030}`,
			AutoHealRestart, func(t *testing.T, e *AutoHealEvent) {
				if e.Attempt != 1 || e.MaxAttempts != 3 || e.CreatedAt.Unix() != 1030 || e.Error != "" {
					t.Errorf("event = %+v", e)
				}
			}},
		{"failed restart",
			`{"status":"COOLING_DOWN","restart_timestamps":[1030],"last_action_timestamp":1030}`,
			`{"status":"COOLING_DOWN","restart_timestamps":[1030,1090],"last_action_timestamp":1090,"last_action_error":"No such container: web"}`,
			AutoHealRestart, func(t *testing.T, e *AutoHealEvent) {
				if e.Attempt != 2 || e.Error != "No such container: web" {
					t.Errorf("event = %+v", e)
				}
			}},
		// Restarts outside the window are dropped, so the count can stay the same; the new timestamp tells.
		{"restart after the window slid",
			`{"status":"COOLING_DOWN","restart_timestamps":[100],"last_action_timestamp":100}`,
			`{"status":"COOLING_DOWN","restart_timestamps":[1500],"last_action_timestamp":1500}`,
			AutoHealRestart, nil},
		{"recreate",
			`{"status":"DEGRADED","restart_timestamps":[1030,1090,1150],"last_action_timestamp":1150}`,
			`{"status":"COOLING_DOWN","restart_timestamps":[1030,1090,1150],"recreate_attempts":1,"last_action_timestamp":1210}`,
			AutoHealRecreate, func(t *testing.T, e *AutoHealEvent) {
				if e.Attempt != 1 || e.MaxAttempts != 1 {
					t.Errorf("event = %+v", e)
				}
			}},
		{"cooling down without action",
			`{"status":"COOLING_DOWN","last_action_timestamp":1030}`,
			`{"status":"COOLING_DOWN","last_action_timestamp":1030}`, "", nil},
		{"gave up",
			`{"status":"DEGRADED","last_action_timestamp":1210}`,
			`{"status":"CRITICAL_FAILED","last_action_timestamp":1210}`, AutoHealGaveUp, nil},
		{"still gave up", `{"status":"CRITICAL_FAILED"}`, `{"status":"CRITICAL_FAILED"}`, "", nil},
		{"recovered after a restart",
			`{"status":"COOLING_DOWN","failure_started_at":1000,"last_action_timestamp":1030}`,
			`{"status":"HEALTHY","failure_started_at":1000,"last_action_timestamp":1030,"recovered_at":1060}`,
			AutoHealRecovered, func(t *testing.T, e *AutoHealEvent) {
				if e.DowntimeSeconds == nil || *e.DowntimeSeconds != 60 || e.CreatedAt.Unix() != 1060 {
					t.Errorf("event = %+v", e)
				}
			}},
		{"recovered on its own",
			`{"status":"DEGRADED","failure_started_at":1000,"last_action_timestamp":500}`,
			`{"status":"HEALTHY","failure_started_at":1000,"last_action_timestamp":500,"recovered_at":1060}`, "", nil},
		{"no previous state", ``, `{"status":"HEALTHY"}`, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := DetectAutoHealEvent(json.RawMessage(tt.old), json.RawMessage(tt.new), cfg, now)
			got := ""
			if e != nil {
				got = e.Action
			}
			if got != tt.wantAction {
				t.Fatalf("action = %q, want %q (event %+v)", got, tt.wantAction, e)
			}
			if tt.check != nil {
				tt.check(t, e)
			}
		})
	}
}
