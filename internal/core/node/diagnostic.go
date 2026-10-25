// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/google/uuid"
)

// Status constants for log diagnostic lifecycle.
const (
	StatusPending  = "pending"
	StatusPreview  = "preview"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// JSONRawMessage is a raw encoded JSON value that supports database NULL scanning
// and driver.Valuer.
type JSONRawMessage []byte

// Scan implements the database/sql.Scanner interface.
func (j *JSONRawMessage) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	switch v := value.(type) {
	case []byte:
		*j = append((*j)[0:0], v...)
		return nil
	case string:
		*j = append((*j)[0:0], []byte(v)...)
		return nil
	default:
		return fmt.Errorf("unsupported type for JSONRawMessage Scan: %T", value)
	}
}

// Value implements the database/sql/driver.Valuer interface.
func (j JSONRawMessage) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return []byte(j), nil
}

// MarshalJSON returns j as the JSON encoding of j.
func (j JSONRawMessage) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// UnmarshalJSON sets *j to a copy of data.
func (j *JSONRawMessage) UnmarshalJSON(data []byte) error {
	if j == nil {
		return errors.New("JSONRawMessage: UnmarshalJSON on nil pointer")
	}
	*j = append((*j)[0:0], data...)
	return nil
}

// LogDiagnostic represents a collected, sanitized, and analyzed log bundle
// with its lifecycle status.
type LogDiagnostic struct {
	ID             string         `db:"id" json:"id"`
	UserID         string         `db:"user_id" json:"user_id"`
	NodeID         string         `db:"node_id" json:"node_id"`
	RequestID      string         `db:"request_id" json:"request_id"`
	Status         string         `db:"status" json:"status"`
	DiagnosticJSON JSONRawMessage `db:"diagnostic_json" json:"diagnostic_json,omitempty"`
	Sources        JSONRawMessage `db:"sources" json:"sources,omitempty"`
	SystemHealth   *string        `db:"system_health" json:"system_health,omitempty"`
	CreatedAt      time.Time      `db:"created_at" json:"created_at"`
	ApprovedAt     *time.Time     `db:"approved_at" json:"approved_at,omitempty"`
}

// SourceBundle mirrors the proto LogSourceBundle for JSON serialization into
// the sources JSONB column.
type SourceBundle struct {
	Source    string `json:"source"`
	RawLogs   string `json:"raw_logs"`
	LineCount int32  `json:"line_count"`
}

// CollectRequest is the input for triggering a log collection from the API.
type CollectRequest struct {
	NodeID            string   `json:"node_id"`
	Sources           []string `json:"sources"`
	LinesPerSource    int32    `json:"lines_per_source,omitempty"`
	IncludeContainers bool     `json:"include_containers,omitempty"`
}

// ListFilter contains optional filters for listing diagnostics.
type ListFilter struct {
	NodeID       *string
	Status       *string
	SystemHealth *string
}

// LogDiagnosticRepository defines the data access interface for log diagnostics.
type LogDiagnosticRepository interface {
	Create(ctx context.Context, d *LogDiagnostic) (*LogDiagnostic, error)
	GetByID(ctx context.Context, userID, id string) (*LogDiagnostic, error)
	GetByRequestID(ctx context.Context, requestID string) (*LogDiagnostic, error)
	UpdateStatus(ctx context.Context, id, status string) error
	UpdatePreview(ctx context.Context, requestID string, diagnosticJSON, sources JSONRawMessage, systemHealth string) error
	Approve(ctx context.Context, userID, id string) error
	Reject(ctx context.Context, userID, id string) error
	List(ctx context.Context, userID string, filter ListFilter, limit, offset int) ([]*LogDiagnostic, int, error)
	Delete(ctx context.Context, userID, id string) error
	CleanupExpired(ctx context.Context) (int64, error)
}

// AIAnalyzer abstracts AI-based diagnostic log enrichment.
type AIAnalyzer interface {
	IsConfigured() bool
	AnalyzeLogs(ctx context.Context, logsText string) (string, error)
}

// LogDiagnosticService orchestrates log diagnostic operations.
type LogDiagnosticService struct {
	repo       LogDiagnosticRepository
	aiAnalyzer AIAnalyzer
}

// NewLogDiagnosticService creates a new log diagnostic service.
func NewLogDiagnosticService(repo LogDiagnosticRepository, aiAnalyzer *platform.GeminiClient) *LogDiagnosticService {
	var analyzer AIAnalyzer
	if aiAnalyzer != nil && aiAnalyzer.IsConfigured() {
		analyzer = aiAnalyzer
	}
	return &LogDiagnosticService{repo: repo, aiAnalyzer: analyzer}
}

// RequestCollection creates a pending diagnostic record and returns the
// request ID that should be sent to the agent via gRPC.
func (s *LogDiagnosticService) RequestCollection(ctx context.Context, userID string, req CollectRequest) (*LogDiagnostic, error) {
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
	}

	return s.repo.Create(ctx, d)
}

// StorePreview updates a pending diagnostic with the agent's response,
// transitioning it to the preview status. If an AI analyzer is available,
// it enriches the report with AI reasoning before storing.
func (s *LogDiagnosticService) StorePreview(ctx context.Context, requestID string, diagnosticJSON string, sources JSONRawMessage, systemHealth string) error {
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

func (s *LogDiagnosticService) Approve(ctx context.Context, userID, id string) error {
	return s.repo.Approve(ctx, userID, id)
}

func (s *LogDiagnosticService) Reject(ctx context.Context, userID, id string) error {
	return s.repo.Reject(ctx, userID, id)
}

func (s *LogDiagnosticService) GetByID(ctx context.Context, userID, id string) (*LogDiagnostic, error) {
	return s.repo.GetByID(ctx, userID, id)
}

func (s *LogDiagnosticService) List(ctx context.Context, userID string, filter ListFilter, limit, offset int) ([]*LogDiagnostic, int, error) {
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

func (s *LogDiagnosticService) Delete(ctx context.Context, userID, id string) error {
	return s.repo.Delete(ctx, userID, id)
}

func (s *LogDiagnosticService) CleanupExpired(ctx context.Context) (int64, error) {
	return s.repo.CleanupExpired(ctx)
}
