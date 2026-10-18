// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package logdiagnostic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/google/uuid"
)

// AIAnalyzer abstracts AI-based diagnostic log enrichment.
type AIAnalyzer interface {
	IsConfigured() bool
	AnalyzeLogs(ctx context.Context, logsText string) (string, error)
}

// Service orchestrates log diagnostic operations.
type Service struct {
	repo       Repository
	aiAnalyzer AIAnalyzer
}

// NewService creates a new log diagnostic service.
func NewService(repo Repository, aiAnalyzer *platform.GeminiClient) *Service {
	var analyzer AIAnalyzer
	if aiAnalyzer != nil && aiAnalyzer.IsConfigured() {
		analyzer = aiAnalyzer
	}
	return &Service{repo: repo, aiAnalyzer: analyzer}
}

// RequestCollection creates a pending diagnostic record and returns the
// request ID that should be sent to the agent via gRPC.
func (s *Service) RequestCollection(ctx context.Context, userID string, req CollectRequest) (*LogDiagnostic, error) {
	if strings.TrimSpace(req.NodeID) == "" {
		return nil, errors.New("node_id is required")
	}
	if len(req.Sources) == 0 && !req.IncludeContainers {
		return nil, errors.New("at least one source or include_containers must be specified")
	}
	if len(req.Sources) > 5 {
		return nil, errors.New("maximum 5 sources per collection request")
	}

	requestID := uuid.New().String()

	d := &LogDiagnostic{
		UserID:    userID,
		NodeID:    req.NodeID,
		RequestID: requestID,
		Status:    StatusPending,
		CreatedAt: time.Now().UTC(),
	}

	return s.repo.Create(ctx, d)
}

// StorePreview updates a pending diagnostic with the agent's response,
// transitioning it to the preview status. If an AI analyzer is available,
// it enriches the report with AI reasoning before storing.
func (s *Service) StorePreview(ctx context.Context, requestID string, diagnosticJSON string, sources JSONRawMessage, systemHealth string) error {
	if requestID == "" {
		return errors.New("request_id is required")
	}

	finalDiagJSON := diagnosticJSON
	finalHealth := systemHealth

	// AI Enrichment if configured
	if s.aiAnalyzer != nil && s.aiAnalyzer.IsConfigured() && len(sources) > 0 {
		var bundles []SourceBundle
		if err := json.Unmarshal(sources, &bundles); err == nil && len(bundles) > 0 {
			var sb strings.Builder
			for _, b := range bundles {
				sb.WriteString(fmt.Sprintf("=== SOURCE: %s (%d lines) ===\n", b.Source, b.LineCount))
				sb.WriteString(b.RawLogs)
				sb.WriteString("\n\n")
			}

			aiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			aiReport, err := s.aiAnalyzer.AnalyzeLogs(aiCtx, sb.String())
			if err != nil {
				log.Printf("[WARN] AI diagnostic enrichment failed (%v), falling back to agent local report", err)
			} else {
				log.Printf("[INFO] AI diagnostic enrichment succeeded for request %s", requestID)
				finalDiagJSON = aiReport

				var parsed struct {
					SystemHealth string `json:"system_health"`
				}
				if err := json.Unmarshal([]byte(aiReport), &parsed); err == nil && parsed.SystemHealth != "" {
					finalHealth = parsed.SystemHealth
				}
			}
		}
	}

	var diagRaw JSONRawMessage
	if finalDiagJSON != "" {
		diagRaw = JSONRawMessage(finalDiagJSON)
	}

	return s.repo.UpdatePreview(ctx, requestID, diagRaw, sources, finalHealth)
}

// GetByID retrieves a single diagnostic by its ID, scoped to the user.
func (s *Service) GetByID(ctx context.Context, userID, id string) (*LogDiagnostic, error) {
	return s.repo.GetByID(ctx, userID, id)
}

// GetByRequestID retrieves a diagnostic by its correlation request ID.
func (s *Service) GetByRequestID(ctx context.Context, requestID string) (*LogDiagnostic, error) {
	return s.repo.GetByRequestID(ctx, requestID)
}

// Approve transitions a diagnostic from preview to approved.
func (s *Service) Approve(ctx context.Context, userID, id string) error {
	d, err := s.repo.GetByID(ctx, userID, id)
	if err != nil {
		return err
	}
	if d.Status != StatusPreview {
		return errors.New("only diagnostics in 'preview' status can be approved")
	}
	return s.repo.Approve(ctx, userID, id)
}

// Reject transitions a diagnostic to rejected status.
func (s *Service) Reject(ctx context.Context, userID, id string) error {
	d, err := s.repo.GetByID(ctx, userID, id)
	if err != nil {
		return err
	}
	if d.Status != StatusPreview {
		return errors.New("only diagnostics in 'preview' status can be rejected")
	}
	return s.repo.Reject(ctx, userID, id)
}

// List returns paginated diagnostics for a user with optional filters.
func (s *Service) List(ctx context.Context, userID string, filter ListFilter, limit, offset int) ([]*LogDiagnostic, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.List(ctx, userID, filter, limit, offset)
}

// Delete removes a diagnostic record.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	return s.repo.Delete(ctx, userID, id)
}

// CleanupExpired removes expired diagnostics:
// - pending/rejected: older than 48 hours
// - approved: older than 30 days
func (s *Service) CleanupExpired(ctx context.Context) (int64, error) {
	return s.repo.CleanupExpired(ctx)
}
