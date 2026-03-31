package grpcserver

import (
	"fmt"
	"log"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/platform"
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
}
