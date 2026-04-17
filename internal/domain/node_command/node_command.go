package nodecommand

import (
	"errors"
	"time"
)

// CommandType distinguishes built-in commands from custom shell scripts.
type CommandType string

const (
	CommandTypeDefault    CommandType = "default"
	CommandTypeCustom     CommandType = "custom"
	CommandTypeInstallApp CommandType = "install_app"
)

var (
	ErrCommandNotFound      = errors.New("command not found")
	ErrCommandAlreadyExists = errors.New("command already exists")
	ErrInvalidCommandData   = errors.New("invalid command data")
)

// NodeCommand represents a command available on a node.
// Default commands are built-in; custom commands are .sh scripts under a
// configured folder that the agent discovers and registers.
type NodeCommand struct {
	ID          string      `db:"id"`
	NodeID      string      `db:"node_id"`
	Name        string      `db:"name"`
	Description string      `db:"description"`
	Type        CommandType `db:"type"`
	ScriptPath  string      `db:"script_path"` // only set for custom commands
	CreatedAt   time.Time   `db:"created_at"`
}

// Repository defines the persistence contract for node commands.
type Repository interface {
	Upsert(cmd *NodeCommand) (*NodeCommand, error)
	FindByNodeID(nodeID string) ([]*NodeCommand, error)
	FindByID(id string) (*NodeCommand, error)
	Delete(id string) error
}
