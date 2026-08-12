package networkstat

import "context"

// Repository defines the persistence contract.
type Repository interface {
	Upsert(ctx context.Context, stats *NodeNetworkStats) error
	GetLatest(ctx context.Context, nodeID string) (*NodeNetworkStats, error)
}

// Service handles business logic for node network stats.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Ingest stores a new snapshot from the agent.
func (s *Service) Ingest(ctx context.Context, nodeID string, payload *IngestPayload) error {
	var rxBytes, txBytes, rxDropped, txDropped, rxErrors, txErrors int64
	for _, iface := range payload.Interfaces {
		rxBytes += iface.RxBytes
		txBytes += iface.TxBytes
		rxDropped += iface.RxDropped
		txDropped += iface.TxDropped
		rxErrors += iface.RxErrors
		txErrors += iface.TxErrors
	}

	snap := &NodeNetworkStats{
		NodeID:     nodeID,
		RxBytes:    rxBytes,
		TxBytes:    txBytes,
		RxDropped:  rxDropped,
		TxDropped:  txDropped,
		RxErrors:   rxErrors,
		TxErrors:   txErrors,
		Interfaces: payload.Interfaces,
		Ports:      payload.Ports,
		Peers:      payload.Peers,
	}
	return s.repo.Upsert(ctx, snap)
}

// GetLatest returns the most recent snapshot for a node.
func (s *Service) GetLatest(ctx context.Context, nodeID string) (*NodeNetworkStats, error) {
	return s.repo.GetLatest(ctx, nodeID)
}
