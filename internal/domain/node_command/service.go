package nodecommand

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Register upserts a command for a node (called by the agent on startup / discovery).
func (s *Service) Register(cmd *NodeCommand) (*NodeCommand, error) {
	if cmd.NodeID == "" || cmd.Name == "" {
		return nil, ErrInvalidCommandData
	}
	if cmd.Type == "" {
		cmd.Type = CommandTypeDefault
	}
	return s.repo.Upsert(cmd)
}

// ListByNode returns all commands registered for a given node.
func (s *Service) ListByNode(nodeID string) ([]*NodeCommand, error) {
	return s.repo.FindByNodeID(nodeID)
}

// Delete removes a command by its ID.
func (s *Service) Delete(id string) error {
	return s.repo.Delete(id)
}
