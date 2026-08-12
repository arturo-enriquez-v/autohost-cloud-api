package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	networkstat "github.com/arturo/autohost-cloud-api/internal/domain/network_stat"
	"github.com/jmoiron/sqlx"
)

type NetworkStatRepo struct {
	DB *sqlx.DB
}

func NewNetworkStatRepository(db *sqlx.DB) *NetworkStatRepo {
	return &NetworkStatRepo{DB: db}
}

// Upsert inserts a new snapshot. The table retains history; the handler
// exposes only the most-recent row via GetLatest.
func (r *NetworkStatRepo) Upsert(ctx context.Context, s *networkstat.NodeNetworkStats) error {
	ifacesJSON, err := json.Marshal(s.Interfaces)
	if err != nil {
		return fmt.Errorf("marshal interfaces: %w", err)
	}
	portsJSON, err := json.Marshal(s.Ports)
	if err != nil {
		return fmt.Errorf("marshal ports: %w", err)
	}
	peersJSON, err := json.Marshal(s.Peers)
	if err != nil {
		return fmt.Errorf("marshal peers: %w", err)
	}

	_, err = r.DB.ExecContext(ctx, `
		INSERT INTO node_network_stats
		    (node_id, rx_bytes, tx_bytes, rx_dropped, tx_dropped, rx_errors, tx_errors, interfaces, ports, peers)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		s.NodeID,
		s.RxBytes, s.TxBytes,
		s.RxDropped, s.TxDropped,
		s.RxErrors, s.TxErrors,
		ifacesJSON, portsJSON, peersJSON,
	)
	return err
}

// GetLatest returns the most recent snapshot for the node.
func (r *NetworkStatRepo) GetLatest(ctx context.Context, nodeID string) (*networkstat.NodeNetworkStats, error) {
	type row struct {
		ID          string `db:"id"`
		NodeID      string `db:"node_id"`
		CollectedAt string `db:"collected_at"`
		RxBytes     int64  `db:"rx_bytes"`
		TxBytes     int64  `db:"tx_bytes"`
		RxDropped   int64  `db:"rx_dropped"`
		TxDropped   int64  `db:"tx_dropped"`
		RxErrors    int64  `db:"rx_errors"`
		TxErrors    int64  `db:"tx_errors"`
		Interfaces  []byte `db:"interfaces"`
		Ports       []byte `db:"ports"`
		Peers       []byte `db:"peers"`
	}

	var r0 row
	err := r.DB.GetContext(ctx, &r0, `
		SELECT id, node_id, collected_at,
		       rx_bytes, tx_bytes, rx_dropped, tx_dropped, rx_errors, tx_errors,
		       interfaces, ports, peers
		FROM node_network_stats
		WHERE node_id = $1
		ORDER BY collected_at DESC
		LIMIT 1
	`, nodeID)
	if err != nil {
		return nil, err
	}

	snap := &networkstat.NodeNetworkStats{
		ID:        r0.ID,
		NodeID:    r0.NodeID,
		RxBytes:   r0.RxBytes,
		TxBytes:   r0.TxBytes,
		RxDropped: r0.RxDropped,
		TxDropped: r0.TxDropped,
		RxErrors:  r0.RxErrors,
		TxErrors:  r0.TxErrors,
	}

	_ = json.Unmarshal(r0.Interfaces, &snap.Interfaces)
	_ = json.Unmarshal(r0.Ports, &snap.Ports)
	_ = json.Unmarshal(r0.Peers, &snap.Peers)

	return snap, nil
}
