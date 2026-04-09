package postgres

import (
	"time"

	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	"github.com/jmoiron/sqlx"
)

type NodeTokenRepo struct{ DB *sqlx.DB }

func NewNodeTokenRepository(db *sqlx.DB) *NodeTokenRepo { return &NodeTokenRepo{DB: db} }

func (r *NodeTokenRepo) CreateNodeToken(nodeID, tokenHash string) error {
	_, err := r.DB.Exec(`
		INSERT INTO node_tokens (node_id, token)
		VALUES ($1, $2)
	`, nodeID, tokenHash)
	if err != nil {
		return err
	}
	return nil
}

func (r *NodeTokenRepo) FindNodeTokenByHash(tokenHash string) (*nodetoken.NodeToken, error) {
	var model nodetoken.NodeToken
	err := r.DB.Get(&model, `
		SELECT id, node_id, token, created_at, last_seen_at, revoked_at
		FROM node_tokens
		WHERE token = $1
	`, tokenHash)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

func (r *NodeTokenRepo) UpdateLastSeen(tokenID string, lastSeenAt time.Time) error {
	_, err := r.DB.Exec(`
		UPDATE node_tokens
		SET last_seen_at = $1
		WHERE id = $2
	`, lastSeenAt, tokenID)
	if err != nil {
		return err
	}
	return nil
}

func (r *NodeTokenRepo) RevokeByNodeID(nodeID string) error {
	_, err := r.DB.Exec(`
		UPDATE node_tokens
		SET revoked_at = now()
		WHERE node_id = $1 AND revoked_at IS NULL
	`, nodeID)
	return err
}
