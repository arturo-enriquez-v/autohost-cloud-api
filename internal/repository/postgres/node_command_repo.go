package postgres

import (
	"context"
	"database/sql"

	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	"github.com/jmoiron/sqlx"
)

// NodeCommandRepository implements nodecommand.Repository using PostgreSQL.
type NodeCommandRepository struct {
	db *sqlx.DB
}

func NewNodeCommandRepository(db *sqlx.DB) *NodeCommandRepository {
	return &NodeCommandRepository{db: db}
}

// Upsert inserts or updates a node command (matched on node_id + name).
func (r *NodeCommandRepository) Upsert(cmd *nodecommand.NodeCommand) (*nodecommand.NodeCommand, error) {
	var m NodeCommandModel
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO node_commands (node_id, name, description, type, script_path)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (node_id, name) DO UPDATE
		    SET description  = COALESCE(NULLIF(EXCLUDED.description, ''), node_commands.description),
		        type         = EXCLUDED.type,
		        script_path  = EXCLUDED.script_path
		RETURNING id, node_id, name, description, type, script_path, created_at`,
		cmd.NodeID, cmd.Name, cmd.Description, cmd.Type, cmd.ScriptPath,
	).Scan(&m.ID, &m.NodeID, &m.Name, &m.Description, &m.Type, &m.ScriptPath, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return modelToNodeCommand(m), nil
}

// FindByNodeID returns all commands registered for a node.
func (r *NodeCommandRepository) FindByNodeID(nodeID string) ([]*nodecommand.NodeCommand, error) {
	var models []NodeCommandModel
	err := r.db.SelectContext(context.Background(), &models,
		`SELECT id, node_id, name, description, type, script_path, created_at
		 FROM node_commands WHERE node_id = $1 ORDER BY name`, nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]*nodecommand.NodeCommand, len(models))
	for i, m := range models {
		out[i] = modelToNodeCommand(m)
	}
	return out, nil
}

// FindByID returns a single command by its UUID.
func (r *NodeCommandRepository) FindByID(id string) (*nodecommand.NodeCommand, error) {
	var m NodeCommandModel
	err := r.db.GetContext(context.Background(), &m,
		`SELECT id, node_id, name, description, type, script_path, created_at
		 FROM node_commands WHERE id = $1`, id)
	if err == sql.ErrNoRows {
		return nil, nodecommand.ErrCommandNotFound
	}
	if err != nil {
		return nil, err
	}
	return modelToNodeCommand(m), nil
}

// Delete removes a command by its UUID.
func (r *NodeCommandRepository) Delete(id string) error {
	res, err := r.db.ExecContext(context.Background(),
		`DELETE FROM node_commands WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nodecommand.ErrCommandNotFound
	}
	return nil
}

func modelToNodeCommand(m NodeCommandModel) *nodecommand.NodeCommand {
	return &nodecommand.NodeCommand{
		ID:          m.ID,
		NodeID:      m.NodeID,
		Name:        m.Name,
		Description: m.Description.String,
		Type:        nodecommand.CommandType(m.Type),
		ScriptPath:  m.ScriptPath.String,
		CreatedAt:   m.CreatedAt,
	}
}
