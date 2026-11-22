// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestToVisitorPath(t *testing.T) {
	tests := []struct{ root, nodePath, want string }{
		{"/", "/docs/a.pdf", "/docs/a.pdf"},
		{"/clients/acme", "/clients/acme", "/"},
		{"/clients/acme", "/clients/acme/uploads/a.pdf", "/uploads/a.pdf"},
		{"clients/acme/", "clients/acme/a.pdf", "/a.pdf"},
		// A sibling with the same prefix is not inside the share root.
		{"/clients/acme", "/clients/acmecorp/a.pdf", "/clients/acmecorp/a.pdf"},
	}
	for _, tt := range tests {
		if got := toVisitorPath(tt.root, tt.nodePath); got != tt.want {
			t.Errorf("toVisitorPath(%q, %q) = %q, want %q", tt.root, tt.nodePath, got, tt.want)
		}
	}
}

// Public write operations must not reveal where the shared folder lives on the node.
func TestRelayWithVisitorPaths(t *testing.T) {
	share := &SharedFolderLink{FolderPath: "/clients/acme"}

	rec := httptest.NewRecorder()
	relayWithVisitorPaths(rec, share, func(w http.ResponseWriter) {
		writeFileJSON(w, http.StatusCreated, map[string]any{"success": true, "path": "/clients/acme/uploads/a.pdf", "size_bytes": 3})
	}, "path")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got["path"] != "/uploads/a.pdf" {
		t.Errorf("path = %v, want /uploads/a.pdf", got["path"])
	}
	if got["size_bytes"] != float64(3) || got["success"] != true {
		t.Errorf("other fields changed: %v", got)
	}

	// Errors are relayed unchanged.
	rec = httptest.NewRecorder()
	relayWithVisitorPaths(rec, share, func(w http.ResponseWriter) { writeFileError(w, "invalid path", http.StatusBadRequest) }, "path")
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("error relay: status %d, content-type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
