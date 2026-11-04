// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

func (s *NodeAgentServer) handleFileOpResponse(nodeID string, payload *pb.FileOperationResponsePayload) {
	if payload == nil {
		return
	}
	reqID := payload.GetRequestId()
	s.fileOpsMu.Lock()
	ch, ok := s.fileOpsPending[reqID]
	if ok {
		delete(s.fileOpsPending, reqID)
	}
	s.fileOpsMu.Unlock()

	if !ok {
		log.Printf("[gRPC] file op: received response for unknown/expired req %s from node %s", reqID, nodeID)
		return
	}

	ch <- payload
}

// ExecuteFileOperation sends a file operation request to a connected node agent
// and waits for the response or context cancellation.
func (s *NodeAgentServer) ExecuteFileOperation(
	ctx context.Context,
	nodeID string,
	opType pb.FileOpType,
	path string,
	transferURL string,
	authToken string,
) (*pb.FileOperationResponsePayload, error) {
	if !s.IsNodeConnected(nodeID) {
		return nil, errors.New("node is not connected via gRPC")
	}

	reqID := uuid.NewString()
	ch := make(chan *pb.FileOperationResponsePayload, 1)

	s.fileOpsMu.Lock()
	s.fileOpsPending[reqID] = ch
	s.fileOpsMu.Unlock()

	defer func() {
		s.fileOpsMu.Lock()
		delete(s.fileOpsPending, reqID)
		s.fileOpsMu.Unlock()
	}()

	msg := &pb.ServerMessage{
		Payload: &pb.ServerMessage_FileOpRequest{
			FileOpRequest: &pb.FileOperationRequestPayload{
				RequestId:   reqID,
				OpType:      opType,
				Path:        path,
				TransferUrl: transferURL,
				AuthToken:   authToken,
			},
		},
	}

	if err := s.SendToNode(nodeID, msg); err != nil {
		return nil, fmt.Errorf("send file op to node: %w", err)
	}

	select {
	case res := <-ch:
		if res == nil {
			return nil, errors.New("empty file operation response")
		}
		if !res.GetSuccess() {
			errMsg := res.GetError()
			if errMsg == "" {
				errMsg = "file operation failed on node"
			}
			return res, errors.New(errMsg)
		}
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

