package grpcserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"

	networkstat "github.com/arturo/autohost-cloud-api/internal/domain/network_stat"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

type networkStatsResponse struct {
	stats *networkstat.NodeNetworkStats
	err   error
}

func (s *NodeAgentServer) handleNetworkStatsResult(nodeID string, payload *pb.NetworkStatsPayload) {
	reqID := payload.GetRequestId()
	s.networkStatsMu.Lock()
	ch, ok := s.networkStatsPending[reqID]
	if ok {
		delete(s.networkStatsPending, reqID)
	}
	s.networkStatsMu.Unlock()

	if !ok {
		log.Printf("[gRPC] network stats: received response for unknown/expired req %s from node %s", reqID, nodeID)
		return
	}

	if errMsg := payload.GetError(); errMsg != "" {
		ch <- &networkStatsResponse{err: errors.New(errMsg)}
		return
	}

	var ingestPayload networkstat.IngestPayload
	if err := json.Unmarshal([]byte(payload.GetStatsJson()), &ingestPayload); err != nil {
		ch <- &networkStatsResponse{err: fmt.Errorf("unmarshal stats json: %w", err)}
		return
	}

	if s.networkStatSvc == nil {
		ch <- &networkStatsResponse{err: errors.New("networkstat service not available")}
		return
	}

	snap, err := s.networkStatSvc.IngestAndBuild(context.Background(), nodeID, &ingestPayload)
	if err != nil {
		ch <- &networkStatsResponse{err: fmt.Errorf("persist network stats: %w", err)}
		return
	}

	ch <- &networkStatsResponse{stats: snap}
}

// RequestNetworkStats asks the agent over gRPC for an immediate network snapshot.
func (s *NodeAgentServer) RequestNetworkStats(ctx context.Context, nodeID string) (*networkstat.NodeNetworkStats, error) {
	if !s.IsNodeConnected(nodeID) {
		return nil, errors.New("node is not connected")
	}

	reqID := uuid.New().String()
	ch := make(chan *networkStatsResponse, 1)

	s.networkStatsMu.Lock()
	s.networkStatsPending[reqID] = ch
	s.networkStatsMu.Unlock()

	defer func() {
		s.networkStatsMu.Lock()
		delete(s.networkStatsPending, reqID)
		s.networkStatsMu.Unlock()
	}()

	msg := &pb.ServerMessage{
		Payload: &pb.ServerMessage_RequestNetworkStats{
			RequestNetworkStats: &pb.RequestNetworkStatsPayload{
				RequestId: reqID,
			},
		},
	}

	if err := s.SendToNode(nodeID, msg); err != nil {
		return nil, fmt.Errorf("send to node: %w", err)
	}

	select {
	case res := <-ch:
		if res == nil {
			return nil, errors.New("empty response received")
		}
		return res.stats, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
