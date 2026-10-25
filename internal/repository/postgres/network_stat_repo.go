// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/arturo/autohost-cloud-api/internal/core/network"
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
func (r *NetworkStatRepo) Upsert(ctx context.Context, s *network.NodeNetworkStats) error {
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
	connsJSON, err := json.Marshal(s.Connections)
	if err != nil {
		return fmt.Errorf("marshal connections: %w", err)
	}
	reqsJSON, err := json.Marshal(s.Requests)
	if err != nil {
		return fmt.Errorf("marshal requests: %w", err)
	}

	_, err = r.DB.ExecContext(ctx, `
		INSERT INTO node_network_stats
		    (node_id, rx_bytes, tx_bytes, rx_dropped, tx_dropped, rx_errors, tx_errors, rps, interfaces, ports, peers, connections, requests)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		s.NodeID,
		s.RxBytes, s.TxBytes,
		s.RxDropped, s.TxDropped,
		s.RxErrors, s.TxErrors,
		s.RPS,
		ifacesJSON, portsJSON, peersJSON, connsJSON, reqsJSON,
	)
	return err
}

// GetLatest returns the most recent snapshot for the node.
func (r *NetworkStatRepo) GetLatest(ctx context.Context, nodeID string) (*network.NodeNetworkStats, error) {
	type row struct {
		ID          string  `db:"id"`
		NodeID      string  `db:"node_id"`
		CollectedAt string  `db:"collected_at"`
		RxBytes     int64   `db:"rx_bytes"`
		TxBytes     int64   `db:"tx_bytes"`
		RxDropped   int64   `db:"rx_dropped"`
		TxDropped   int64   `db:"tx_dropped"`
		RxErrors    int64   `db:"rx_errors"`
		TxErrors    int64   `db:"tx_errors"`
		RPS         float64 `db:"rps"`
		Interfaces  []byte  `db:"interfaces"`
		Ports       []byte  `db:"ports"`
		Peers       []byte  `db:"peers"`
		Connections []byte  `db:"connections"`
		Requests    []byte  `db:"requests"`
	}

	var r0 row
	err := r.DB.GetContext(ctx, &r0, `
		SELECT id, node_id, collected_at,
		       rx_bytes, tx_bytes, rx_dropped, tx_dropped, rx_errors, tx_errors, rps,
		       interfaces, ports, peers, connections, requests
		FROM node_network_stats
		WHERE node_id = $1
		ORDER BY collected_at DESC
		LIMIT 1
	`, nodeID)
	if err != nil {
		return nil, err
	}

	snap := &network.NodeNetworkStats{
		ID:        r0.ID,
		NodeID:    r0.NodeID,
		RxBytes:   r0.RxBytes,
		TxBytes:   r0.TxBytes,
		RxDropped: r0.RxDropped,
		TxDropped: r0.TxDropped,
		RxErrors:  r0.RxErrors,
		TxErrors:  r0.TxErrors,
		RPS:       r0.RPS,
	}

	_ = json.Unmarshal(r0.Interfaces, &snap.Interfaces)
	_ = json.Unmarshal(r0.Ports, &snap.Ports)
	_ = json.Unmarshal(r0.Peers, &snap.Peers)
	_ = json.Unmarshal(r0.Connections, &snap.Connections)
	_ = json.Unmarshal(r0.Requests, &snap.Requests)

	return snap, nil
}
