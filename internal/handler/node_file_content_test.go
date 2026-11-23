// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/filetransfer"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// newTextFileRouter serves the node file routes for node-123 (local, owned by user-1) over a temporary
// shared folder, and returns that folder.
func newTextFileRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	shared := t.TempDir()
	t.Setenv("AUTOHOST_SHARED_DIR", shared)
	t.Setenv("AUTOHOST_STORAGE_DIR", t.TempDir())

	owner := "user-1"
	nodeRepo := &MockNodeRepo{nodes: map[string]*node.Node{"node-123": {ID: "node-123", OwnerID: &owner, IsLocal: true}}}
	h := handler.NewNodeFileHandler(filetransfer.NewService(nil, nil), node.NewNodeService(nodeRepo))
	h.SetRBACService(ownerRBAC())

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			user := req.Header.Get("X-Test-User")
			next.ServeHTTP(w, req.WithContext(middleware.ContextWithClaims(req.Context(), &middleware.Claims{UserID: user})))
		})
	})
	r.Mount("/v1/nodes/{nodeId}/files", stripAuth(h))
	return r, shared
}

// stripAuth serves the handler methods directly: NodeScopedRoutes starts with middleware.Auth (JWT), which
// the test replaces with injected claims. Route-level authorization is covered by the route tests.
func stripAuth(h *handler.NodeFileHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/content", h.ReadFileContent)
	r.Put("/content", h.WriteFileContent)
	return r
}

func doText(t *testing.T, r http.Handler, method, url, body, user string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("X-Test-User", user)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

const contentURL = "/v1/nodes/node-123/files/content"

func TestWriteAndReadTextFile(t *testing.T) {
	r, shared := newTextFileRouter(t)

	rec, body := doText(t, r, http.MethodPut, contentURL, `{"path":"docs/prueba.txt","content":"prueba número 1\n"}`, "user-1")
	if rec.Code != http.StatusCreated || body["path"] != "/docs/prueba.txt" || body["overwritten"] != false {
		t.Fatalf("create: %d %v", rec.Code, body)
	}
	if got, _ := os.ReadFile(filepath.Join(shared, "docs", "prueba.txt")); string(got) != "prueba número 1\n" {
		t.Fatalf("file on disk = %q", got)
	}

	rec, body = doText(t, r, http.MethodGet, contentURL+"?path=docs/prueba.txt", "", "user-1")
	if rec.Code != http.StatusOK || body["content"] != "prueba número 1\n" || body["path"] != "/docs/prueba.txt" {
		t.Fatalf("read: %d %v", rec.Code, body)
	}

	// Existing files are only replaced on request.
	rec, _ = doText(t, r, http.MethodPut, contentURL, `{"path":"docs/prueba.txt","content":"v2"}`, "user-1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("write over an existing file without overwrite: got %d, want 409", rec.Code)
	}
	rec, body = doText(t, r, http.MethodPut, contentURL, `{"path":"docs/prueba.txt","content":"v2","overwrite":true}`, "user-1")
	if rec.Code != http.StatusOK || body["overwritten"] != true {
		t.Fatalf("overwrite: %d %v", rec.Code, body)
	}
	if got, _ := os.ReadFile(filepath.Join(shared, "docs", "prueba.txt")); string(got) != "v2" {
		t.Fatalf("file after overwrite = %q", got)
	}

	// An empty file is valid content.
	if rec, _ := doText(t, r, http.MethodPut, contentURL, `{"path":"empty.txt","content":""}`, "user-1"); rec.Code != http.StatusCreated {
		t.Fatalf("empty file: got %d, want 201", rec.Code)
	}
}

func TestTextFileValidation(t *testing.T) {
	r, shared := newTextFileRouter(t)
	if err := os.WriteFile(filepath.Join(shared, "image.bin"), []byte{0x89, 'P', 'N', 'G', 0, 0, 0xff}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "big.txt"), []byte(strings.Repeat("a", 1<<20+1)), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, method, url, body string
		want                    int
	}{
		{"write without content", http.MethodPut, contentURL, `{"path":"a.txt"}`, http.StatusBadRequest},
		{"write without path", http.MethodPut, contentURL, `{"content":"x"}`, http.StatusBadRequest},
		{"write to a directory path", http.MethodPut, contentURL, `{"path":"docs/","content":"x"}`, http.StatusBadRequest},
		{"write bad json", http.MethodPut, contentURL, `not json`, http.StatusBadRequest},
		{"write too large", http.MethodPut, contentURL, `{"path":"a.txt","content":"` + strings.Repeat("a", 1<<20+1) + `"}`, http.StatusRequestEntityTooLarge},
		{"read without path", http.MethodGet, contentURL, "", http.StatusBadRequest},
		{"read missing file", http.MethodGet, contentURL + "?path=nope.txt", "", http.StatusNotFound},
		{"read binary file", http.MethodGet, contentURL + "?path=image.bin", "", http.StatusUnsupportedMediaType},
		{"read large file", http.MethodGet, contentURL + "?path=big.txt", "", http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		rec, body := doText(t, r, tc.method, tc.url, tc.body, "user-1")
		if rec.Code != tc.want {
			t.Errorf("%s: got %d %v, want %d", tc.name, rec.Code, body, tc.want)
		}
		if rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: response is not JSON", tc.name)
		}
	}
}

// Writes and reads stay inside the shared folder.
func TestTextFilePathTraversal(t *testing.T) {
	r, shared := newTextFileRouter(t)
	outside := filepath.Join(filepath.Dir(shared), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	rec, body := doText(t, r, http.MethodGet, contentURL+"?path=../outside.txt", "", "user-1")
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("read outside the shared folder: %d %v", rec.Code, body)
	}

	rec, _ = doText(t, r, http.MethodPut, contentURL, `{"path":"../../escaped.txt","content":"x"}`, "user-1")
	if _, err := os.Stat(filepath.Join(filepath.Dir(shared), "escaped.txt")); err == nil {
		t.Fatalf("write escaped the shared folder (status %d)", rec.Code)
	}
}

func TestTextFileRequiresNodeAccess(t *testing.T) {
	r, shared := newTextFileRouter(t)
	if err := os.WriteFile(filepath.Join(shared, "mine.txt"), []byte("private"), 0644); err != nil {
		t.Fatal(err)
	}
	if rec, _ := doText(t, r, http.MethodGet, contentURL+"?path=mine.txt", "", "intruder"); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "private") {
		t.Fatalf("read by another user: got %d, want 403", rec.Code)
	}
	if rec, _ := doText(t, r, http.MethodPut, contentURL, `{"path":"x.txt","content":"x"}`, "intruder"); rec.Code != http.StatusForbidden {
		t.Fatalf("write by another user: got %d, want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(shared, "x.txt")); err == nil {
		t.Fatal("the forbidden write created the file")
	}
}
