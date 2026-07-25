package nodealert

import "time"

// NodeAlert stores the alert configuration for a specific node.
type NodeAlert struct {
	ID               string    `db:"id"                 json:"id"`
	NodeID           string    `db:"node_id"            json:"node_id"`
	TelegramBotID    *string   `db:"telegram_bot_id"    json:"telegram_bot_id"`
	TelegramBotToken string    `db:"telegram_bot_token" json:"telegram_bot_token"`
	TelegramChatID   string    `db:"telegram_chat_id"   json:"telegram_chat_id"`
	CPUThreshold     *float32  `db:"cpu_threshold"      json:"cpu_threshold"`
	RAMThreshold     *float32  `db:"ram_threshold"      json:"ram_threshold"`
	DiskThreshold    *float32  `db:"disk_threshold"     json:"disk_threshold"`
	AlertOnOffline   bool      `db:"alert_on_offline"   json:"alert_on_offline"`
	CreatedAt        time.Time `db:"created_at"         json:"created_at"`
	UpdatedAt        time.Time `db:"updated_at"         json:"updated_at"`
}

// Repository defines the persistence interface for node alert configs.
type Repository interface {
	Upsert(alert *NodeAlert) (*NodeAlert, error)
	FindByNodeID(nodeID string) (*NodeAlert, error)
	// FindAllByOwnerID returns all alert configs for nodes owned by ownerID.
	FindAllByOwnerID(ownerID string) ([]*NodeAlert, error)
}
