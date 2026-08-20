package logdiagnostic

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
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
	RawLogs  string `json:"raw_logs"`
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

// Repository defines the data access interface for log diagnostics.
type Repository interface {
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

