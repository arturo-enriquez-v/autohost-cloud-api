// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"encoding/json"
	"time"
)

// Auto-heal event actions.
const (
	AutoHealRestart   = "restart"   // the agent ran "docker restart"
	AutoHealRecreate  = "recreate"  // the agent ran "docker compose up --force-recreate"
	AutoHealGaveUp    = "gave_up"   // every enabled step was used and the service is still down
	AutoHealRecovered = "recovered" // the service passed a check again after an auto-heal action
)

// AutoHealEvent is one thing auto-heal did, or its outcome.
type AutoHealEvent struct {
	ID              string    `db:"id"               json:"id"`
	MonitorID       string    `db:"monitor_id"       json:"monitor_id"`
	NodeID          string    `db:"node_id"          json:"node_id"`
	Action          string    `db:"action"           json:"action"`
	Attempt         int       `db:"attempt"          json:"attempt"`      // restart/recreate: which attempt
	MaxAttempts     int       `db:"max_attempts"     json:"max_attempts"` // restart/recreate: configured limit
	Error           string    `db:"error"            json:"error"`        // restart/recreate: why the command failed
	CheckMessage    string    `db:"check_message"    json:"check_message"`
	DowntimeSeconds *int      `db:"downtime_seconds" json:"downtime_seconds"` // recovered: first failure to recovery
	CreatedAt       time.Time `db:"created_at"       json:"created_at"`
}

// autoHealStateView is the part of the agent's auto-heal state used to detect events.
type autoHealStateView struct {
	Status              string  `json:"status"`
	RestartTimestamps   []int64 `json:"restart_timestamps"`
	RecreateAttempts    int     `json:"recreate_attempts"`
	LastActionTimestamp *int64  `json:"last_action_timestamp"`
	FailureStartedAt    *int64  `json:"failure_started_at"`
	RecoveredAt         *int64  `json:"recovered_at"`
	LastActionError     string  `json:"last_action_error"`
}

type autoHealConfigView struct {
	Restart struct {
		MaxAttempts int `json:"max_attempts"`
	} `json:"restart"`
	Recreate struct {
		MaxAttempts int `json:"max_attempts"`
	} `json:"recreate"`
}

func parseStateView(raw json.RawMessage) autoHealStateView {
	var v autoHealStateView
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

func unixTime(ts *int64, fallback time.Time) time.Time {
	if ts == nil || *ts <= 0 {
		return fallback
	}
	return time.Unix(*ts, 0)
}

// DetectAutoHealEvent compares the auto-heal state before and after a check result and returns what auto-heal
// did, or nil. The agent runs at most one action per check, so there is at most one event.
func DetectAutoHealEvent(oldRaw, newRaw, configRaw json.RawMessage, now time.Time) *AutoHealEvent {
	oldState, newState := parseStateView(oldRaw), parseStateView(newRaw)
	if newState.Status == "" {
		return nil
	}
	var cfg autoHealConfigView
	if len(configRaw) > 0 {
		_ = json.Unmarshal(configRaw, &cfg)
	}

	// A new action timestamp means the agent ran a restart or a recreate in this check. It was a restart
	// when that timestamp is the newest restart.
	acted := newState.LastActionTimestamp != nil &&
		(oldState.LastActionTimestamp == nil || *newState.LastActionTimestamp != *oldState.LastActionTimestamp)
	if acted {
		at := unixTime(newState.LastActionTimestamp, now)
		n := len(newState.RestartTimestamps)
		if n > 0 && newState.RestartTimestamps[n-1] == *newState.LastActionTimestamp {
			return &AutoHealEvent{Action: AutoHealRestart, Attempt: n, MaxAttempts: cfg.Restart.MaxAttempts,
				Error: newState.LastActionError, CreatedAt: at}
		}
		return &AutoHealEvent{Action: AutoHealRecreate, Attempt: newState.RecreateAttempts,
			MaxAttempts: cfg.Recreate.MaxAttempts, Error: newState.LastActionError, CreatedAt: at}
	}

	if newState.Status == "CRITICAL_FAILED" && oldState.Status != "CRITICAL_FAILED" {
		return &AutoHealEvent{Action: AutoHealGaveUp, CreatedAt: now}
	}

	// Recovery only counts when auto-heal acted during this outage; a service that came back on its own
	// is an incident, not an auto-heal outcome.
	if newState.Status == "HEALTHY" && oldState.Status != "" && oldState.Status != "HEALTHY" &&
		oldState.LastActionTimestamp != nil && oldState.FailureStartedAt != nil &&
		*oldState.LastActionTimestamp >= *oldState.FailureStartedAt {
		recovered := unixTime(newState.RecoveredAt, now)
		e := &AutoHealEvent{Action: AutoHealRecovered, CreatedAt: recovered}
		if d := int(recovered.Unix() - *oldState.FailureStartedAt); d >= 0 {
			e.DowntimeSeconds = &d
		}
		return e
	}
	return nil
}
