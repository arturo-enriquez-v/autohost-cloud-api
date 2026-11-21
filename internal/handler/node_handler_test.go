// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func doNodeRequest(t *testing.T, method, url, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	router, _, _ := setupTestRouter()
	req := httptest.NewRequest(method, url, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestNodeHandler_UpdateByID(t *testing.T) {
	ownerToken := generateToken("user_a_123", "usera@example.com")

	for _, tc := range []struct{ name, method, url, body string }{
		{"ID in the URL", http.MethodPut, "/v1/nodes/node_owned_by_a", `{"hostname":"renamed"}`},
		{"deprecated ID in the body", http.MethodPut, "/v1/nodes", `{"id":"node_owned_by_a","hostname":"renamed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doNodeRequest(t, tc.method, tc.url, tc.body, ownerToken)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			var got struct {
				ID       string `json:"id"`
				Hostname string `json:"hostname"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("response is not JSON: %v", err)
			}
			if got.ID != "node_owned_by_a" || got.Hostname != "renamed" {
				t.Errorf("got %+v, want node_owned_by_a renamed", got)
			}
		})
	}
}

// Node errors are JSON ({"error": ...}) so clients can parse them uniformly.
func TestNodeHandler_ErrorsAreJSON(t *testing.T) {
	ownerToken := generateToken("user_a_123", "usera@example.com")

	for _, tc := range []struct {
		name, method, url, body string
		status                  int
	}{
		{"get unknown node", http.MethodGet, "/v1/nodes/missing", "", http.StatusNotFound},
		{"update unknown node", http.MethodPut, "/v1/nodes/missing", `{"hostname":"x"}`, http.StatusNotFound},
		{"update with malformed body", http.MethodPut, "/v1/nodes/node_owned_by_a", `{`, http.StatusBadRequest},
		{"delete unknown node", http.MethodDelete, "/v1/nodes/missing", "", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doNodeRequest(t, tc.method, tc.url, tc.body, ownerToken)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.status, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
				t.Errorf("body %q is not a JSON error", rec.Body.String())
			}
		})
	}
}
