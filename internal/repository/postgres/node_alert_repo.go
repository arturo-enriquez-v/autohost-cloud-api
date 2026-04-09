package postgres

import (
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
)

type NodeAlertRepository struct {
	db *sqlx.DB
}

func NewNodeAlertRepository(db *sqlx.DB) *NodeAlertRepository {
	return &NodeAlertRepository{db: db}
}

func (r *NodeAlertRepository) Upsert(alert *nodealert.NodeAlert) (*nodealert.NodeAlert, error) {
	query := `
		INSERT INTO node_alerts (node_id, telegram_bot_token, telegram_chat_id, cpu_threshold, ram_threshold, disk_threshold, alert_on_offline)
		VALUES (:node_id, :telegram_bot_token, :telegram_chat_id, :cpu_threshold, :ram_threshold, :disk_threshold, :alert_on_offline)
		ON CONFLICT (node_id) DO UPDATE SET
			telegram_bot_token = EXCLUDED.telegram_bot_token,
			telegram_chat_id   = EXCLUDED.telegram_chat_id,
			cpu_threshold      = EXCLUDED.cpu_threshold,
			ram_threshold      = EXCLUDED.ram_threshold,
			disk_threshold     = EXCLUDED.disk_threshold,
			alert_on_offline   = EXCLUDED.alert_on_offline,
			updated_at         = NOW()
		RETURNING id, node_id, telegram_bot_token, telegram_chat_id, cpu_threshold, ram_threshold, disk_threshold, alert_on_offline, created_at, updated_at`

	rows, err := r.db.NamedQuery(query, alert)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, errors.New("upsert returned no rows")
	}
	var result nodealert.NodeAlert
	if err := rows.StructScan(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *NodeAlertRepository) FindByNodeID(nodeID string) (*nodealert.NodeAlert, error) {
	var alert nodealert.NodeAlert
	err := r.db.Get(&alert, `
		SELECT id, node_id, telegram_bot_token, telegram_chat_id, cpu_threshold, ram_threshold, disk_threshold, alert_on_offline, created_at, updated_at
		FROM node_alerts
		WHERE node_id = $1`, nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nodealert.ErrNotFound
		}
		return nil, err
	}
	return &alert, nil
}
