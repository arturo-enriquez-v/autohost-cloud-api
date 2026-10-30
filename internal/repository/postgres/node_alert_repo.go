// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
)

type NodeAlertRepository struct {
	db *sqlx.DB
}

func NewNodeAlertRepository(db *sqlx.DB) *NodeAlertRepository {
	return &NodeAlertRepository{db: db}
}

func (r *NodeAlertRepository) Upsert(alert *node.NodeAlert) (*node.NodeAlert, error) {
	query := `
		INSERT INTO node_alerts (node_id, telegram_bot_id, telegram_bot_token, telegram_chat_id, cpu_threshold, ram_threshold, disk_threshold, alert_on_offline)
		VALUES (:node_id, :telegram_bot_id, :telegram_bot_token, :telegram_chat_id, :cpu_threshold, :ram_threshold, :disk_threshold, :alert_on_offline)
		ON CONFLICT (node_id) DO UPDATE SET
			telegram_bot_id    = EXCLUDED.telegram_bot_id,
			telegram_bot_token = EXCLUDED.telegram_bot_token,
			telegram_chat_id   = EXCLUDED.telegram_chat_id,
			cpu_threshold      = EXCLUDED.cpu_threshold,
			ram_threshold      = EXCLUDED.ram_threshold,
			disk_threshold     = EXCLUDED.disk_threshold,
			alert_on_offline   = EXCLUDED.alert_on_offline,
			updated_at         = NOW()`

	_, err := r.db.NamedExec(query, alert)
	if err != nil {
		return nil, err
	}
	return r.FindByNodeID(alert.NodeID)
}

func (r *NodeAlertRepository) FindByNodeID(nodeID string) (*node.NodeAlert, error) {
	var alert node.NodeAlert
	err := r.db.Get(&alert, `
		SELECT na.id, na.node_id, na.telegram_bot_id,
		       COALESCE(tb.bot_token, na.telegram_bot_token, '') AS telegram_bot_token,
		       COALESCE(tb.chat_id, na.telegram_chat_id, '') AS telegram_chat_id,
		       na.cpu_threshold, na.ram_threshold, na.disk_threshold,
		       na.alert_on_offline, na.created_at, na.updated_at
		FROM node_alerts na
		LEFT JOIN telegram_bots tb ON na.telegram_bot_id = tb.id
		WHERE na.node_id = $1`, nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, node.ErrNodeAlertNotFound
		}
		return nil, err
	}
	return &alert, nil
}

func (r *NodeAlertRepository) FindAllByOwnerID(ownerID string) ([]*node.NodeAlert, error) {
	var alerts []*node.NodeAlert
	err := r.db.Select(&alerts, `
		SELECT na.id, na.node_id, na.telegram_bot_id,
		       COALESCE(tb.bot_token, na.telegram_bot_token, '') AS telegram_bot_token,
		       COALESCE(tb.chat_id, na.telegram_chat_id, '') AS telegram_chat_id,
		       na.cpu_threshold, na.ram_threshold, na.disk_threshold,
		       na.alert_on_offline, na.created_at, na.updated_at
		FROM node_alerts na
		JOIN nodes n ON n.id = na.node_id
		LEFT JOIN telegram_bots tb ON na.telegram_bot_id = tb.id
		WHERE n.owner_id = $1`, ownerID)
	if err != nil {
		return nil, err
	}
	if alerts == nil {
		alerts = []*node.NodeAlert{}
	}
	return alerts, nil
}
