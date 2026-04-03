package nodemetric

import "errors"

var (
	ErrInvalidNodeMetricData = errors.New("invalid node metric data")
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) StoreNodeMetric(req *CreateNodeMetricRequest) (*NodeMetric, error) {
	if req == nil || req.NodeID == "" {
		return nil, ErrInvalidNodeMetricData
	}
	return s.repo.StoreNodeMetric(req)
}

func (s *Service) GetHistory(params HistoryParams) ([]*NodeMetricHistoryPoint, error) {
	if params.NodeID == "" {
		return nil, ErrInvalidNodeMetricData
	}
	return s.repo.GetHistoryByNodeID(params)
}

// func (s *Service) GetMetricsByNodeID(nodeID string, limit int) ([]*NodeMetric, error) {
// 	if nodeID == "" || limit <= 0 {
// 		return nil, ErrInvalidNodeMetricData
// 	}
// 	return s.repo.GetMetricsByNodeID(nodeID, limit)
// }

// func (s *Service) GetLatestMetricByNodeID(nodeID string) (*NodeMetric, error) {
// 	if nodeID == "" {
// 		return nil, ErrInvalidNodeMetricData
// 	}
// 	return s.repo.GetLatestMetricByNodeID(nodeID)
// }
