// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

type fakeContainerSource struct {
	online   bool
	cached   string
	fresh    string
	fetchErr error
	block    bool // Fetch waits for ctx instead of answering
	fetched  bool
}

func (f *fakeContainerSource) Online(context.Context, string) bool { return f.online }

func (f *fakeContainerSource) Cached(context.Context, string) ([]byte, bool) {
	return []byte(f.cached), f.cached != ""
}

func (f *fakeContainerSource) Fetch(ctx context.Context, _ string) ([]byte, error) {
	f.fetched = true
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []byte(f.fresh), f.fetchErr
}

const (
	freshList  = `{"containers":[{"id":"1","name":"web","image":"nginx","state":"running","status":"Up"}]}`
	cachedList = `{"containers":[{"id":"1","name":"web","image":"nginx","state":"exited","status":"Exited"}]}`
)

func serveListContainers(t *testing.T, src *fakeContainerSource, userID string) (*httptest.ResponseRecorder, containerListResponse) {
	t.Helper()
	h := &NodeContainerHandler{snapshots: src, snapshotTimeout: 50 * time.Millisecond}
	h.SetRBACService(newOwnerRBAC("owner", "node-1"))

	req := httptest.NewRequest(http.MethodGet, "/node-1", nil)
	req = req.WithContext(middleware.ContextWithClaims(req.Context(), &middleware.Claims{UserID: userID}))
	req = withNodeID(req, "node-1")

	rec := httptest.NewRecorder()
	h.listContainers(rec, req)
	var body containerListResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
	}
	return rec, body
}

func TestListContainersFresh(t *testing.T) {
	rec, body := serveListContainers(t, &fakeContainerSource{online: true, fresh: freshList, cached: cachedList}, "owner")
	if rec.Code != http.StatusOK || body.Cached || len(body.Containers) != 1 || body.Containers[0].State != "running" {
		t.Fatalf("got %d %+v, want the fresh snapshot", rec.Code, body)
	}
}

func TestListContainersFallsBackToCache(t *testing.T) {
	cases := map[string]*fakeContainerSource{
		"node offline":   {online: false, cached: cachedList},
		"agent too slow": {online: true, block: true, cached: cachedList},
		"fetch error":    {online: true, fetchErr: errors.New("redis down"), cached: cachedList},
	}
	for name, src := range cases {
		rec, body := serveListContainers(t, src, "owner")
		if rec.Code != http.StatusOK || !body.Cached || body.Containers[0].State != "exited" {
			t.Errorf("%s: got %d %+v, want the cached snapshot", name, rec.Code, body)
		}
	}
}

func TestListContainersErrors(t *testing.T) {
	cases := []struct {
		name string
		src  *fakeContainerSource
		want int
	}{
		{"offline without cache", &fakeContainerSource{online: false}, http.StatusConflict},
		{"timeout without cache", &fakeContainerSource{online: true, block: true}, http.StatusGatewayTimeout},
		{"error without cache", &fakeContainerSource{online: true, fetchErr: errors.New("boom")}, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec, _ := serveListContainers(t, tc.src, "owner")
		if rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.want)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: errors must be JSON, got %q", tc.name, ct)
		}
	}
}

func TestListContainersRequiresNodeAccess(t *testing.T) {
	src := &fakeContainerSource{online: true, fresh: freshList, cached: cachedList}
	rec, _ := serveListContainers(t, src, "intruder")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
	if src.fetched {
		t.Fatal("the agent must not be contacted for a user without access")
	}
}
