package node

import "errors"

var (
	ErrNodeNotFound    = errors.New("node not found")
	ErrInvalidNodeData = errors.New("invalid node data")
	ErrUnauthorized    = errors.New("unauthorized")
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Register(node *Node) (*Node, error) {
	if node.Hostname == "" {
		return nil, ErrInvalidNodeData
	}
	return s.repo.Register(node)
}

func (s *Service) UpdateLastSeen(nodeID string) error {
	if nodeID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.UpdateLastSeen(nodeID)
}

func (s *Service) UpdateLastSeenAndIPs(nodeID string, ipLocal string, ipVPN string) error {
	if nodeID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.UpdateLastSeenAndIPs(nodeID, ipLocal, ipVPN)
}

func (s *Service) UpdateVPNIP(nodeID string, ipVPN string) error {
	if nodeID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.UpdateVPNIP(nodeID, ipVPN)
}

func (s *Service) UpdateVersionAgent(nodeID, version string) error {
	if nodeID == "" || version == "" {
		return ErrInvalidNodeData
	}
	return s.repo.UpdateVersionAgent(nodeID, version)
}

func (s *Service) GetByOwner(ownerID string) ([]*Node, error) {
	return s.repo.FindByOwnerID(ownerID)
}

// GetByOwnerWithMetrics obtiene todos los nodos de un propietario con sus últimas métricas
func (s *Service) GetByOwnerWithMetrics(ownerID string) ([]*NodeWithMetrics, error) {
	return s.repo.FindByOwnerIDWithMetrics(ownerID)
}

// GetByID returns a single node by its ID.
func (s *Service) GetByID(id string) (*Node, error) {
	return s.repo.FindByID(id)
}

func (s *Service) Update(id, hostname string) (*Node, error) {
	node, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, ErrNodeNotFound
	}

	if hostname != "" {
		node.Hostname = hostname
	}

	return s.repo.Update(node)
}

func (s *Service) Delete(id string) error {
	if id == "" {
		return ErrInvalidNodeData
	}
	_, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	return s.repo.Delete(id)
}

// AssignToProject sets the project_id of a node.
func (s *Service) AssignToProject(nodeID, projectID string) error {
	if nodeID == "" || projectID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.AssignProject(nodeID, &projectID)
}

// RemoveFromProject clears the project_id of a node.
func (s *Service) RemoveFromProject(nodeID string) error {
	if nodeID == "" {
		return ErrInvalidNodeData
	}
	return s.repo.AssignProject(nodeID, nil)
}
