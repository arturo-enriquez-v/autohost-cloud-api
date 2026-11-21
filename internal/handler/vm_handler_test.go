// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/go-chi/chi/v5"
)

// vmRepo serves one VM ("vm-1", owned by user-1); "vm-broken" fails with a database-like error.
type vmRepo struct{}

func (vmRepo) GetByID(_ context.Context, id string) (*infra.VM, error) {
	switch id {
	case "vm-1":
		return &infra.VM{ID: "vm-1", OwnerID: "user-1", HostNodeID: "node-1", Name: "web"}, nil
	case "vm-broken":
		return nil, errors.New(`pq: relation "vms" does not exist`)
	}
	return nil, infra.ErrNotFound
}
func (vmRepo) Create(context.Context, *infra.VM) (*infra.VM, error)                { return nil, nil }
func (vmRepo) GetByHostAndName(context.Context, string, string) (*infra.VM, error) { return nil, nil }
func (vmRepo) ListByHost(context.Context, string) ([]*infra.VM, error)             { return nil, nil }
func (vmRepo) ListByOwner(context.Context, string) ([]*infra.VM, error)            { return nil, nil }
func (vmRepo) Update(_ context.Context, v *infra.VM) (*infra.VM, error)            { return v, nil }
func (vmRepo) Delete(context.Context, string) error                                { return nil }
func (vmRepo) LinkNodeByHostname(context.Context, string, string, string, string) error {
	return nil
}

func TestVMHandler_ErrorStatuses(t *testing.T) {
	h := handler.NewVMHandler(infra.NewVMService(vmRepo{}, nil, nil, nil, nil))
	r := chi.NewRouter()
	r.Mount("/v1/vms", h.VMRoutes())
	token := generateToken("user-1", "user1@example.com")

	for _, tc := range []struct {
		name, url, body string
		want            int
	}{
		{"unsupported action", "/v1/vms/vm-1/actions", `{"action":"explode"}`, http.StatusBadRequest},
		{"unknown vm", "/v1/vms/vm-missing/actions", `{"action":"start"}`, http.StatusNotFound},
		{"internal error is not leaked", "/v1/vms/vm-broken/actions", `{"action":"start"}`, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.url, bytes.NewBufferString(tc.body))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.want, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "pq:") {
				t.Errorf("response leaks the internal error: %q", rec.Body.String())
			}
		})
	}
}
