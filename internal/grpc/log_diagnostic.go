// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"encoding/json"
	"log"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// handleLogDiagnosticResult processes a LogDiagnosticPayload received from the
// agent and stores it as a preview in the database.
func (s *NodeAgentServer) handleLogDiagnosticResult(nodeID string, payload *pb.LogDiagnosticPayload) {
	if s.logDiagnosticSvc == nil {
		log.Printf("[gRPC] log diagnostic: service not configured, dropping result for node %s", nodeID)
		return
	}

	requestID := payload.GetRequestId()

	if errMsg := payload.GetError(); errMsg != "" {
		log.Printf("[gRPC] log diagnostic %s from node %s: agent error: %s", requestID, nodeID, errMsg)
		// Still store as preview with the error so the user can see it.
	}

	// Serialize the source bundles to JSON for storage.
	var sourcesJSON node.JSONRawMessage
	if sources := payload.GetSources(); len(sources) > 0 {
		bundles := make([]node.SourceBundle, 0, len(sources))
		for _, s := range sources {
			bundles = append(bundles, node.SourceBundle{
				Source:    s.GetSource(),
				RawLogs:  s.GetRawLogs(),
				LineCount: s.GetLineCount(),
			})
		}
		data, err := json.Marshal(bundles)
		if err != nil {
			log.Printf("[gRPC] log diagnostic %s: marshal sources: %v", requestID, err)
			return
		}
		sourcesJSON = node.JSONRawMessage(data)
	}

	// Extract system_health from the diagnostic JSON.
	systemHealth := "UNKNOWN"
	if diagJSON := payload.GetDiagnosticJson(); diagJSON != "" {
		var report struct {
			SystemHealth string `json:"system_health"`
		}
		if err := json.Unmarshal([]byte(diagJSON), &report); err == nil && report.SystemHealth != "" {
			systemHealth = report.SystemHealth
		}
	}

	if err := s.logDiagnosticSvc.StorePreview(
		context.Background(),
		requestID,
		payload.GetDiagnosticJson(),
		sourcesJSON,
		systemHealth,
	); err != nil {
		log.Printf("[gRPC] log diagnostic %s: store preview: %v", requestID, err)
		return
	}

	log.Printf("[gRPC] log diagnostic %s from node %s: stored as preview (health=%s, %d sources)",
		requestID, nodeID, systemHealth, len(payload.GetSources()))
}

// DispatchLogCollection sends a CollectLogsPayload to the agent, telling it to
// collect, sanitize, and analyze logs from the specified sources.
func (s *NodeAgentServer) DispatchLogCollection(nodeID string, requestID string, sources []string, linesPerSource int32, includeContainers bool) error {
	msg := &pb.ServerMessage{
		Payload: &pb.ServerMessage_CollectLogs{
			CollectLogs: &pb.CollectLogsPayload{
				RequestId:         requestID,
				Sources:           sources,
				LinesPerSource:    linesPerSource,
				IncludeContainers: includeContainers,
			},
		},
	}
	return s.SendToNode(nodeID, msg)
}
