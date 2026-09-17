package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/notification"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

type mockNotificationMarkerRepo struct {
	summary  *notification.NotificationSummary
	ackCalls []string
}

func (m *mockNotificationMarkerRepo) GetSummary(ctx context.Context, userID string) (*notification.NotificationSummary, error) {
	return m.summary, nil
}

func (m *mockNotificationMarkerRepo) Acknowledge(ctx context.Context, userID, scope string) error {
	m.ackCalls = append(m.ackCalls, scope)
	if scope == "alerts" || scope == "all" {
		m.summary.UnreadAlertsCount = 0
	}
	if scope == "logs" || scope == "all" {
		m.summary.UnreadLogsCount = 0
	}
	m.summary.TotalUnread = m.summary.UnreadAlertsCount + m.summary.UnreadLogsCount
	return nil
}

func TestNotificationHandler_GetSummary(t *testing.T) {
	repo := &mockNotificationMarkerRepo{
		summary: &notification.NotificationSummary{
			UnreadAlertsCount: 2,
			UnreadLogsCount:   5,
			TotalUnread:       7,
			LastSeenAlertsAt:  time.Now().Add(-1 * time.Hour),
			LastSeenLogsAt:    time.Now().Add(-2 * time.Hour),
			RecentAlerts: []*notification.NotificationAlertItem{
				{
					ID:        "a1",
					NodeID:    "n1",
					NodeName:  "prod-node",
					AlertType: "cpu",
					Message:   "CPU > 90%",
					CreatedAt: time.Now(),
				},
			},
			RecentLogs: []*notification.NotificationLogItem{
				{
					ID:        "l1",
					Title:     "nginx-error",
					Level:     "error",
					CreatedAt: time.Now(),
				},
			},
		},
	}
	h := handler.NewNotificationHandler(repo)

	req := httptest.NewRequest("GET", "/v1/notifications/summary", nil)
	req = req.WithContext(middleware.ContextWithClaims(req.Context(), &middleware.Claims{
		UserID: "user-test-1",
	}))
	rec := httptest.NewRecorder()

	h.GetSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var res notification.NotificationSummary
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if res.UnreadAlertsCount != 2 || res.UnreadLogsCount != 5 || res.TotalUnread != 7 {
		t.Errorf("unexpected counts: %+v", res)
	}
	if len(res.RecentAlerts) != 1 || res.RecentAlerts[0].NodeName != "prod-node" {
		t.Errorf("unexpected recent alerts: %+v", res.RecentAlerts)
	}
}

func TestNotificationHandler_Acknowledge(t *testing.T) {
	repo := &mockNotificationMarkerRepo{
		summary: &notification.NotificationSummary{
			UnreadAlertsCount: 3,
			UnreadLogsCount:   4,
			TotalUnread:       7,
		},
	}
	h := handler.NewNotificationHandler(repo)

	// Acknowledge alerts
	body, _ := json.Marshal(map[string]string{"scope": "alerts"})
	req := httptest.NewRequest("POST", "/v1/notifications/ack", bytes.NewReader(body))
	req = req.WithContext(middleware.ContextWithClaims(req.Context(), &middleware.Claims{
		UserID: "user-test-1",
	}))
	rec := httptest.NewRecorder()

	h.Acknowledge(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if len(repo.ackCalls) != 1 || repo.ackCalls[0] != "alerts" {
		t.Errorf("expected ackCalls ['alerts'], got %v", repo.ackCalls)
	}
	if repo.summary.UnreadAlertsCount != 0 {
		t.Errorf("expected unread alerts to be 0, got %d", repo.summary.UnreadAlertsCount)
	}
	if repo.summary.UnreadLogsCount != 4 {
		t.Errorf("expected unread logs to remain 4, got %d", repo.summary.UnreadLogsCount)
	}
}
