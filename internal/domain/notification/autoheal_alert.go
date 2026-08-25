package notification

import (
	"context"
	"fmt"
	"time"
)

type AutoHealConfig struct {
	Enabled                      bool           `json:"enabled"`
	ConsecutiveFailuresThreshold int            `json:"consecutive_failures_threshold"`
	GracePeriodSeconds           int64          `json:"grace_period_seconds"`
	Restart                      RestartConfig  `json:"restart"`
	Recreate                     RecreateConfig `json:"recreate"`
}

type RestartConfig struct {
	Enabled       bool  `json:"enabled"`
	MaxAttempts   int   `json:"max_attempts"`
	WindowSeconds int64 `json:"window_seconds"`
}

type RecreateConfig struct {
	Enabled         bool   `json:"enabled"`
	ComposeFilePath string `json:"compose_file_path"`
	ServiceName     string `json:"service_name"`
	MaxAttempts     int    `json:"max_attempts"`
}

type AutoHealState struct {
	Status              string  `json:"status"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	RestartTimestamps   []int64 `json:"restart_timestamps"`
	RecreateAttempts    int     `json:"recreate_attempts"`
	LastActionTimestamp *int64  `json:"last_action_timestamp"`
	FailureStartedAt    *int64  `json:"failure_started_at,omitempty"`
	RecoveredAt         *int64  `json:"recovered_at,omitempty"`
}

type AutoHealAlertContext struct {
	Recipient    string
	NodeID       string
	NodeHostname string
	NodeIP       string
	MonitorID    string
	ServiceName  string
	CheckType    string
	Config       AutoHealConfig
	OldState     AutoHealState
	NewState     AutoHealState
	LastMessage  string
	LatencyMs    int
}

func formatDuration(seconds int64) string {
	if seconds <= 0 {
		return "< 1s"
	}
	if seconds < 60 {
		return fmt.Sprintf("%d seconds", seconds)
	}
	mins := seconds / 60
	secs := seconds % 60
	if mins < 60 {
		if secs == 0 {
			return fmt.Sprintf("%d min", mins)
		}
		return fmt.Sprintf("%d min %d sec", mins, secs)
	}
	hours := mins / 60
	mins = mins % 60
	if mins == 0 {
		return fmt.Sprintf("%d hour(s)", hours)
	}
	return fmt.Sprintf("%d hour(s) %d min", hours, mins)
}

func formatTimestamp(ts *int64, fallback time.Time) string {
	if ts != nil && *ts > 0 {
		return time.Unix(*ts, 0).UTC().Format("2006-01-02 15:04:05 UTC")
	}
	return fallback.UTC().Format("2006-01-02 15:04:05 UTC")
}

// SendAutoHealTransitionAlert evaluates state transition and sends the appropriate SystemAlert.
func (s *Service) SendAutoHealTransitionAlert(ctx context.Context, ac AutoHealAlertContext) {
	if s == nil || s.redis == nil || ac.Recipient == "" {
		return
	}

	now := time.Now()
	nowStr := now.UTC().Format("2006-01-02 15:04:05 UTC")
	nodeLabel := ac.NodeHostname
	if nodeLabel == "" {
		nodeLabel = ac.NodeID
	}
	ipLabel := ac.NodeIP
	if ipLabel == "" {
		ipLabel = "N/A"
	}

	metadata := map[string]string{
		"Node ID":      ac.NodeID,
		"Node Name":    nodeLabel,
		"IP Address":   ipLabel,
		"Monitor ID":   ac.MonitorID,
		"Service Name": ac.ServiceName,
		"Check Type":   ac.CheckType,
	}

	// 1. Restart action executed
	if len(ac.NewState.RestartTimestamps) > len(ac.OldState.RestartTimestamps) {
		attempt := len(ac.NewState.RestartTimestamps)
		maxAttempts := ac.Config.Restart.MaxAttempts
		threshold := ac.Config.ConsecutiveFailuresThreshold
		if threshold <= 0 {
			threshold = 3
		}

		outageStarted := formatTimestamp(ac.NewState.FailureStartedAt, now)
		actionTime := formatTimestamp(ac.NewState.LastActionTimestamp, now)

		var timeToRestart string
		if ac.NewState.FailureStartedAt != nil && ac.NewState.LastActionTimestamp != nil {
			timeToRestart = formatDuration(*ac.NewState.LastActionTimestamp - *ac.NewState.FailureStartedAt)
		} else {
			timeToRestart = "Immediate"
		}

		metadata["Status"] = "RESTARTING"
		metadata["Outage Detected At"] = outageStarted
		metadata["Action Executed At"] = actionTime

		md := fmt.Sprintf(
			"### 🚨 Service Incident & Auto-Heal Triggered\n"+
				"The service **%s** on node **%s** stopped responding and the automatic recovery workflow was initiated.\n\n"+
				"#### 📋 Outage & Action Timeline\n"+
				"- **Service Name:** `%s` (Check Type: `%s`)\n"+
				"- **Outage Detected At:** `%s`\n"+
				"- **Restart Executed At:** `%s`\n"+
				"- **Time in Outage Before Action:** `%s`\n"+
				"- **Last Error Message:** `%s`\n"+
				"- **Consecutive Failures:** %d failure(s) detected\n\n"+
				"#### ⚙️ Configured Auto-Heal Strategy\n"+
				"- **Activation Threshold:** %d consecutive failures\n"+
				"- **Action Executed:** `docker restart %s` (Attempt %d of %d)\n"+
				"- **Cooling Down Period:** %ds\n"+
				"- **Restart Window:** %ds (~%d min)\n\n"+
				"#### 🛠️ Current State\n"+
				"- **Status:** <span style=\"color:#f59e0b;font-weight:700;\">🟡 RESTARTING / COOLING DOWN</span>\n"+
				"- The container has been restarted. Waiting for the cooling down period to verify service health.\n",
			ac.ServiceName, nodeLabel,
			ac.ServiceName, ac.CheckType,
			outageStarted,
			actionTime,
			timeToRestart,
			ac.LastMessage,
			ac.NewState.ConsecutiveFailures,
			threshold,
			ac.ServiceName, attempt, maxAttempts,
			ac.Config.GracePeriodSeconds,
			ac.Config.Restart.WindowSeconds, ac.Config.Restart.WindowSeconds/60,
		)

		_ = s.SendSystemAlert(ctx, SystemAlert{
			Recipient:    ac.Recipient,
			Severity:     string(SeverityWarning),
			Service:      ac.ServiceName,
			Title:        fmt.Sprintf("⚠️ Auto-Heal: Restarting service %s on %s", ac.ServiceName, nodeLabel),
			Timestamp:    nowStr,
			MarkdownLogs: md,
			Metadata:     metadata,
		})
		return
	}

	// 2. Recreate action executed
	if ac.NewState.RecreateAttempts > ac.OldState.RecreateAttempts {
		attempt := ac.NewState.RecreateAttempts
		maxAttempts := ac.Config.Recreate.MaxAttempts
		composeFile := ac.Config.Recreate.ComposeFilePath
		if composeFile == "" {
			composeFile = "docker-compose.yml"
		}

		outageStarted := formatTimestamp(ac.NewState.FailureStartedAt, now)
		actionTime := formatTimestamp(ac.NewState.LastActionTimestamp, now)

		var timeToRecreate string
		if ac.NewState.FailureStartedAt != nil && ac.NewState.LastActionTimestamp != nil {
			timeToRecreate = formatDuration(*ac.NewState.LastActionTimestamp - *ac.NewState.FailureStartedAt)
		} else {
			timeToRecreate = "N/A"
		}

		metadata["Status"] = "RECREATING"
		metadata["Outage Detected At"] = outageStarted
		metadata["Action Executed At"] = actionTime

		md := fmt.Sprintf(
			"### 🚨 Auto-Heal: Docker Compose Service Re-creation\n"+
				"Standard restarts were insufficient. Initiating Docker Compose re-creation for service **%s** on node **%s**.\n\n"+
				"#### 📋 Outage & Action Timeline\n"+
				"- **Service Name:** `%s` (Check Type: `%s`)\n"+
				"- **Outage Detected At:** `%s`\n"+
				"- **Re-creation Executed At:** `%s`\n"+
				"- **Time in Outage Before Action:** `%s`\n"+
				"- **Compose File:** `%s`\n"+
				"- **Action:** `docker compose up -d --force-recreate` (Attempt %d of %d)\n"+
				"- **Cooling Down Period:** %ds\n"+
				"- **Status:** <span style=\"color:#f59e0b;font-weight:700;\">🟡 RECREATING / COOLING DOWN</span>\n",
			ac.ServiceName, nodeLabel,
			ac.ServiceName, ac.CheckType,
			outageStarted,
			actionTime,
			timeToRecreate,
			composeFile,
			attempt, maxAttempts,
			ac.Config.GracePeriodSeconds,
		)

		_ = s.SendSystemAlert(ctx, SystemAlert{
			Recipient:    ac.Recipient,
			Severity:     string(SeverityWarning),
			Service:      ac.ServiceName,
			Title:        fmt.Sprintf("⚠️ Auto-Heal: Recreating Compose service %s on %s", ac.ServiceName, nodeLabel),
			Timestamp:    nowStr,
			MarkdownLogs: md,
			Metadata:     metadata,
		})
		return
	}

	// 3. Critical Failure (Exhausted all recovery attempts)
	if ac.NewState.Status == "CRITICAL_FAILED" && ac.OldState.Status != "CRITICAL_FAILED" {
		outageStarted := formatTimestamp(ac.NewState.FailureStartedAt, now)
		var totalDowntime string
		if ac.NewState.FailureStartedAt != nil {
			totalDowntime = formatDuration(now.Unix() - *ac.NewState.FailureStartedAt)
		} else {
			totalDowntime = "N/A"
		}

		metadata["Status"] = "CRITICAL_FAILED"
		metadata["Outage Detected At"] = outageStarted
		metadata["Total Downtime"] = totalDowntime

		md := fmt.Sprintf(
			"### 🛑 Critical Failure: Auto-Heal Exhausted\n"+
				"The service **%s** on node **%s** failed to recover after exhausting all configured recovery strategies.\n\n"+
				"#### 📋 Incident Summary\n"+
				"- **Service Name:** `%s` (Check Type: `%s`)\n"+
				"- **Outage Detected At:** `%s`\n"+
				"- **Total Downtime Duration:** `%s`\n"+
				"- **Restarts Executed:** %d of %d\n"+
				"- **Re-creations Executed:** %d of %d\n"+
				"- **Last Error:** `%s`\n"+
				"- **Final State:** <span style=\"color:#ef4444;font-weight:700;\">🔴 CRITICAL_FAILED</span>\n\n"+
				"#### ⚠️ Action Required\n"+
				"Immediate manual intervention is required to inspect container logs and server status.\n",
			ac.ServiceName, nodeLabel,
			ac.ServiceName, ac.CheckType,
			outageStarted,
			totalDowntime,
			len(ac.NewState.RestartTimestamps), ac.Config.Restart.MaxAttempts,
			ac.NewState.RecreateAttempts, ac.Config.Recreate.MaxAttempts,
			ac.LastMessage,
		)

		_ = s.SendSystemAlert(ctx, SystemAlert{
			Recipient:    ac.Recipient,
			Severity:     string(SeverityCritical),
			Service:      ac.ServiceName,
			Title:        fmt.Sprintf("🛑 Critical Failure: Auto-Heal exhausted retries for %s on %s", ac.ServiceName, nodeLabel),
			Timestamp:    nowStr,
			MarkdownLogs: md,
			Metadata:     metadata,
		})
		return
	}

	// 4. Recovered to HEALTHY after having experienced issues / AutoHeal actions
	if ac.NewState.Status == "HEALTHY" && ac.OldState.Status != "HEALTHY" &&
		(ac.OldState.Status == "COOLING_DOWN" ||
			ac.OldState.Status == "DEGRADED" ||
			ac.OldState.Status == "CRITICAL_FAILED" ||
			ac.OldState.Status == "RESTARTING" ||
			ac.OldState.Status == "RECREATING" ||
			len(ac.OldState.RestartTimestamps) > 0 ||
			ac.OldState.RecreateAttempts > 0 ||
			ac.OldState.FailureStartedAt != nil) {

		failureStartedTs := ac.NewState.FailureStartedAt
		if failureStartedTs == nil {
			failureStartedTs = ac.OldState.FailureStartedAt
		}
		recoveredTs := ac.NewState.RecoveredAt
		if recoveredTs == nil {
			recoveredTs = ac.OldState.RecoveredAt
		}

		outageStarted := formatTimestamp(failureStartedTs, now.Add(-time.Minute))
		recoveredAt := formatTimestamp(recoveredTs, now)

		var totalDowntime string
		if failureStartedTs != nil {
			var endTime int64
			if recoveredTs != nil {
				endTime = *recoveredTs
			} else {
				endTime = now.Unix()
			}
			diff := endTime - *failureStartedTs
			if diff < 0 {
				diff = 0
			}
			totalDowntime = formatDuration(diff)
		} else {
			totalDowntime = "< 1 minute"
		}

		metadata["Status"] = "RUNNING (HEALTHY)"
		metadata["Outage Detected At"] = outageStarted
		metadata["Recovered At"] = recoveredAt
		metadata["Total Downtime"] = totalDowntime

		md := fmt.Sprintf(
			"### 🟢 Service Status: RUNNING / OPERATIONAL\n"+
				"The service **%s** on node **%s** has successfully recovered and is now verified **RUNNING** and healthy following the Auto-Heal process.\n\n"+
				"#### 📋 Recovery & Downtime Details\n"+
				"- **Service Name:** `%s`\n"+
				"- **Current Status:** <span style=\"color:#10b981;font-weight:700;\">● RUNNING / HEALTHY</span>\n"+
				"- **Outage Detected At (Down):** `%s`\n"+
				"- **Recovered At (Back to Life):** `%s`\n"+
				"- **Total Downtime Duration:** `%s`\n"+
				"- **Response Latency:** %dms\n"+
				"- **Total Restarts Applied:** %d\n\n"+
				"#### ✅ Verification\n"+
				"The health check passed successfully. All traffic and processes for this service are operating normally.\n",
			ac.ServiceName, nodeLabel,
			ac.ServiceName,
			outageStarted,
			recoveredAt,
			totalDowntime,
			ac.LatencyMs,
			len(ac.NewState.RestartTimestamps),
		)

		_ = s.SendSystemAlert(ctx, SystemAlert{
			Recipient:    ac.Recipient,
			Severity:     string(SeveritySuccess),
			Service:      ac.ServiceName,
			Title:        fmt.Sprintf("🟢 Service Recovered: %s on %s is RUNNING", ac.ServiceName, nodeLabel),
			Timestamp:    nowStr,
			MarkdownLogs: md,
			Metadata:     metadata,
		})
		return
	}
}
