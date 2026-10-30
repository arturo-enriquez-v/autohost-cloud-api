// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package jobs

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
	CommandTypeSwarm      CommandType = "swarm"
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

// NodeCommandRepository defines the persistence contract for node commands.
type NodeCommandRepository interface {
	Upsert(cmd *NodeCommand) (*NodeCommand, error)
	FindByNodeID(nodeID string) ([]*NodeCommand, error)
	FindByID(id string) (*NodeCommand, error)
	Delete(id string) error
}

// NodeCommandService manages registration and discovery of node commands.
type NodeCommandService struct {
	repo NodeCommandRepository
}

func NewNodeCommandService(repo NodeCommandRepository) *NodeCommandService {
	return &NodeCommandService{repo: repo}
}

// Register upserts a command for a node (called by the agent on startup / discovery).
func (s *NodeCommandService) Register(cmd *NodeCommand) (*NodeCommand, error) {
	if cmd.NodeID == "" || cmd.Name == "" {
		return nil, ErrInvalidCommandData
	}
	if cmd.Type == "" {
		cmd.Type = CommandTypeDefault
	}
	return s.repo.Upsert(cmd)
}

// ListByNode returns all commands registered for a given node.
func (s *NodeCommandService) ListByNode(nodeID string) ([]*NodeCommand, error) {
	return s.repo.FindByNodeID(nodeID)
}

// Delete removes a command by its ID.
func (s *NodeCommandService) Delete(id string) error {
	return s.repo.Delete(id)
}
