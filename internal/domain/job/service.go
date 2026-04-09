package job

import nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Dispatch creates a new pending job and returns it so the caller can send it
// via WebSocket.
func (s *Service) Dispatch(nodeID, commandName string, commandType nodecommand.CommandType) (*Job, error) {
	if nodeID == "" || commandName == "" {
		return nil, ErrInvalidJobData
	}
	if commandType == "" {
		commandType = nodecommand.CommandTypeDefault
	}
	j := &Job{
		NodeID:      nodeID,
		CommandName: commandName,
		CommandType: commandType,
		Status:      StatusPending,
	}
	return s.repo.Create(j)
}

// GetByID returns a job by its ID.
func (s *Service) GetByID(id string) (*Job, error) {
	return s.repo.FindByID(id)
}

// ListByNode returns all jobs for a given node.
func (s *Service) ListByNode(nodeID string) ([]*Job, error) {
	return s.repo.FindByNodeID(nodeID)
}

// UpdateResult is called when the node reports back the execution result.
func (s *Service) UpdateResult(id string, status JobStatus, output, errMsg string) error {
	return s.repo.UpdateStatus(id, status, output, errMsg)
}
