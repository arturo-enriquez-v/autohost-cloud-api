// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package logdiagnostic_test

import (
	"context"
	"encoding/json"
	"testing"

	logdiagnostic "github.com/arturo/autohost-cloud-api/internal/domain/log_diagnostic"
)

type mockRepo struct {
	created       *logdiagnostic.LogDiagnostic
	updatedHealth string
	approved      bool
	rejected      bool
}

func (m *mockRepo) Create(ctx context.Context, d *logdiagnostic.LogDiagnostic) (*logdiagnostic.LogDiagnostic, error) {
	d.ID = "diag-123"
	m.created = d
	return d, nil
}

func (m *mockRepo) GetByID(ctx context.Context, userID, id string) (*logdiagnostic.LogDiagnostic, error) {
	return &logdiagnostic.LogDiagnostic{
		ID:        id,
		UserID:    userID,
		Status:    logdiagnostic.StatusPreview,
		RequestID: "req-123",
	}, nil
}

func (m *mockRepo) GetByRequestID(ctx context.Context, requestID string) (*logdiagnostic.LogDiagnostic, error) {
	return &logdiagnostic.LogDiagnostic{
		ID:        "diag-123",
		RequestID: requestID,
		Status:    logdiagnostic.StatusPreview,
	}, nil
}

func (m *mockRepo) UpdateStatus(ctx context.Context, id, status string) error {
	return nil
}

func (m *mockRepo) UpdatePreview(ctx context.Context, requestID string, diagnosticJSON, sources logdiagnostic.JSONRawMessage, systemHealth string) error {
	m.updatedHealth = systemHealth
	return nil
}

func (m *mockRepo) Approve(ctx context.Context, userID, id string) error {
	m.approved = true
	return nil
}

func (m *mockRepo) Reject(ctx context.Context, userID, id string) error {
	m.rejected = true
	return nil
}

func (m *mockRepo) List(ctx context.Context, userID string, filter logdiagnostic.ListFilter, limit, offset int) ([]*logdiagnostic.LogDiagnostic, int, error) {
	return []*logdiagnostic.LogDiagnostic{}, 0, nil
}

func (m *mockRepo) Delete(ctx context.Context, userID, id string) error {
	return nil
}

func (m *mockRepo) CleanupExpired(ctx context.Context) (int64, error) {
	return 5, nil
}

func TestService_RequestCollection(t *testing.T) {
	repo := &mockRepo{}
	svc := logdiagnostic.NewService(repo, nil)

	req := logdiagnostic.CollectRequest{
		NodeID:  "node-1",
		Sources: []string{"autohost-agent", "docker:nginx"},
	}

	diag, err := svc.RequestCollection(context.Background(), "user-1", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if diag.Status != logdiagnostic.StatusPending {
		t.Errorf("expected status=pending, got %s", diag.Status)
	}
	if diag.RequestID == "" {
		t.Errorf("expected non-empty RequestID")
	}
}

func TestService_ApproveAndReject(t *testing.T) {
	repo := &mockRepo{}
	svc := logdiagnostic.NewService(repo, nil)

	if err := svc.Approve(context.Background(), "user-1", "diag-123"); err != nil {
		t.Errorf("unexpected approve error: %v", err)
	}
	if !repo.approved {
		t.Errorf("expected repo.approved=true")
	}

	if err := svc.Reject(context.Background(), "user-1", "diag-123"); err != nil {
		t.Errorf("unexpected reject error: %v", err)
	}
	if !repo.rejected {
		t.Errorf("expected repo.rejected=true")
	}
}

func TestJSONRawMessage_ScanAndValue(t *testing.T) {
	var j logdiagnostic.JSONRawMessage

	// Test scanning nil (SQL NULL)
	if err := j.Scan(nil); err != nil {
		t.Fatalf("expected no error scanning nil, got: %v", err)
	}
	if j != nil {
		t.Errorf("expected nil after scanning nil, got: %s", string(j))
	}

	// Test scanning bytes
	input := []byte(`{"hello":"world"}`)
	if err := j.Scan(input); err != nil {
		t.Fatalf("expected no error scanning bytes, got: %v", err)
	}
	if string(j) != `{"hello":"world"}` {
		t.Errorf("expected %s, got %s", input, string(j))
	}

	// Test scanning string
	if err := j.Scan(`{"foo":"bar"}`); err != nil {
		t.Fatalf("expected no error scanning string, got: %v", err)
	}
	if string(j) != `{"foo":"bar"}` {
		t.Errorf("expected {\"foo\":\"bar\"}, got %s", string(j))
	}

	// Test Value()
	val, err := j.Value()
	if err != nil {
		t.Fatalf("unexpected Value error: %v", err)
	}
	if string(val.([]byte)) != `{"foo":"bar"}` {
		t.Errorf("expected bytes matching JSON, got %v", val)
	}

	// Test empty Value()
	var emptyJ logdiagnostic.JSONRawMessage
	emptyVal, err := emptyJ.Value()
	if err != nil {
		t.Fatalf("unexpected Value error: %v", err)
	}
	if emptyVal != nil {
		t.Errorf("expected nil value for empty JSONRawMessage, got %v", emptyVal)
	}

	// Test JSON marshal / unmarshal
	bytes, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if string(bytes) != `{"foo":"bar"}` {
		t.Errorf("expected {\"foo\":\"bar\"}, got %s", string(bytes))
	}
}

