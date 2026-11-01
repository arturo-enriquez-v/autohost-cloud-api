// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// alertCooldown is the minimum duration between repeated threshold alerts for
// the same node+metric, shared across the gRPC path.
const grpcAlertCooldown = 1 * time.Minute

var (
	grpcCooldownMu sync.Mutex
	grpcCooldowns  = make(map[string]time.Time) // "nodeID:metric" -> last fired
)

// sendOfflineAlert waits briefly to see if the node reconnects, then sends a
// Telegram notification if it remains offline and the alert is configured.
func (s *NodeAgentServer) sendOfflineAlert(nodeID string) {
	// Give the node 30 s to reconnect before firing the alert.
	time.Sleep(30 * time.Second)
	if s.IsNodeConnected(nodeID) {
		return // node came back — no alert needed
	}
	if s.alertSvc == nil {
		return
	}
	cfg, err := s.alertSvc.FindByNodeID(nodeID)
	if err != nil || !cfg.AlertOnOffline || cfg.TelegramBotToken == "" || cfg.TelegramChatID == "" {
		return
	}
	nodeName := nodeID
	if s.nodeSvc != nil {
		if n, err := s.nodeSvc.GetByID(nodeID); err == nil {
			nodeName = n.Hostname
		}
	}
	msg := fmt.Sprintf("🔴 <b>AutoHost Alert</b>\nNode <b>%s</b> <code>%s</code> went <b>offline</b>.", nodeName, nodeID)
	if err := platform.SendTelegramMessage(cfg.TelegramBotToken, cfg.TelegramChatID, msg); err != nil {
		log.Printf("[alert] telegram offline alert for node %s: %v", nodeID, err)
	}

	// Persist the alert event so it appears in the cloud UI.
	if s.alertEventSvc != nil {
		var userID *string
		if s.nodeSvc != nil {
			if n, err := s.nodeSvc.GetByID(nodeID); err == nil && n.OwnerID != nil {
				userID = n.OwnerID
			}
		}
		ev := &node.AlertEvent{
			UserID:    userID,
			NodeID:    nodeID,
			AlertType: node.AlertEventTypeOffline,
			Message:   msg,
		}
		if _, err := s.alertEventSvc.Create(ev); err != nil {
			log.Printf("[alert] persist offline alert event for node %s: %v", nodeID, err)
		}
	}
}

// checkThresholdAlerts checks CPU/RAM/Disk metrics against the node's alert
// config and fires Telegram + persists an alert event for each breached metric.
// It is called at most once per persist-throttle window (≈1 min) from the
// gRPC metric path.
func (s *NodeAgentServer) checkThresholdAlerts(nodeID string, m *node.CreateNodeMetricRequest) {
	if s.alertSvc == nil {
		return
	}
	cfg, err := s.alertSvc.FindByNodeID(nodeID)
	if err != nil || cfg.TelegramBotToken == "" || cfg.TelegramChatID == "" {
		return
	}

	nodeName := nodeID
	if s.nodeSvc != nil {
		if n, err := s.nodeSvc.GetByID(nodeID); err == nil {
			nodeName = n.Hostname
		}
	}

	var userID *string
	if s.nodeSvc != nil {
		if n, err := s.nodeSvc.GetByID(nodeID); err == nil && n.OwnerID != nil {
			userID = n.OwnerID
		}
	}

	check := func(name string, value float64, threshold *float32, evType node.AlertEventType) {
		if threshold == nil || value < float64(*threshold) {
			return
		}
		key := nodeID + ":" + name
		now := time.Now()
		grpcCooldownMu.Lock()
		if last, ok := grpcCooldowns[key]; ok && now.Before(last.Add(grpcAlertCooldown)) {
			grpcCooldownMu.Unlock()
			return
		}
		grpcCooldowns[key] = now
		grpcCooldownMu.Unlock()

		msg := fmt.Sprintf("⚠️ <b>AutoHost Alert</b>\nNode: <b>%s</b> <code>%s</code>\n%s usage: <b>%.1f%%</b> (threshold: %.1f%%)", nodeName, nodeID, name, value, *threshold)
		if err := platform.SendTelegramMessage(cfg.TelegramBotToken, cfg.TelegramChatID, msg); err != nil {
			log.Printf("[alert] telegram send for node %s (%s): %v", nodeID, name, err)
		}
		if s.alertEventSvc != nil {
			ev := &node.AlertEvent{
				UserID:    userID,
				NodeID:    nodeID,
				AlertType: evType,
				Message:   msg,
			}
			if _, err := s.alertEventSvc.Create(ev); err != nil {
				log.Printf("[alert] persist threshold alert event for node %s (%s): %v", nodeID, name, err)
			}
		}
	}

	check("CPU", m.CPUUsagePercent, cfg.CPUThreshold, node.AlertEventTypeCPU)
	check("RAM", m.MemoryUsagePercent, cfg.RAMThreshold, node.AlertEventTypeRAM)
	check("Disk", m.DiskUsagePercent, cfg.DiskThreshold, node.AlertEventTypeDisk)
	if m.CPUTempCelsius >= 85.0 {
		tempThreshold := float32(85.0)
		check("Temperature (°C)", m.CPUTempCelsius, &tempThreshold, node.AlertEventTypeCPU)
	}
}
