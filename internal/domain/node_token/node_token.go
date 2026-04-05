package nodetoken

import "time"

type NodeToken struct {
	ID         string     `db:"id"`
	NodeID     string     `db:"node_id"`
	Token      string     `db:"token"`
	CreatedAt  time.Time  `db:"created_at"`
	LastSeenAt *time.Time `db:"last_seen_at"`
	RevokedAt  *time.Time `db:"revoked_at"`
}

type Repository interface {
	CreateNodeToken(nodeID, token string) error
	FindNodeTokenByHash(tokenHash string) (*NodeToken, error)
	UpdateLastSeen(tokenID string, lastSeenAt time.Time) error
	RevokeByNodeID(nodeID string) error
}
