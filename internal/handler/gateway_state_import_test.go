// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/network"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

type memGatewayStore struct {
	shares *network.ShareFunnelState
	cf     *network.CloudflareTunnelState
}

func (m *memGatewayStore) LoadShareFunnelState(context.Context) (*network.ShareFunnelState, error) {
	if m.shares == nil {
		return &network.ShareFunnelState{}, nil
	}
	cp := *m.shares
	return &cp, nil
}
func (m *memGatewayStore) SaveShareFunnelState(_ context.Context, st *network.ShareFunnelState) error {
	cp := *st
	m.shares = &cp
	return nil
}
func (m *memGatewayStore) LoadCloudflareTunnelState(context.Context) (*network.CloudflareTunnelState, error) {
	if m.cf == nil {
		return &network.CloudflareTunnelState{}, nil
	}
	cp := *m.cf
	return &cp, nil
}
func (m *memGatewayStore) SaveCloudflareTunnelState(_ context.Context, st *network.CloudflareTunnelState) error {
	cp := *st
	m.cf = &cp
	return nil
}

func TestPublicShareLoadState_ImportsLegacyJSONAndHashesPasswords(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	legacy := `{
	  "funnel_dns_name": "host.tailnet.ts.net",
	  "shares": [
	    {"id":"sh_1","owner_id":"u1","node_id":"n1","folder_path":"/docs","title":"Docs","permission":"read","password":"AH-ABCD-EFGH"},
	    {"id":"sh_nopw","owner_id":"u1","node_id":"n1","folder_path":"/","permission":"read","password":""}
	  ],
	  "funnel_routes": [{"id":"fn_1","funnel_port":443,"path_prefix":"/app","target_node_id":"n1","upstream":"100.64.0.1:80"}]
	}`
	if err := os.WriteFile(statePath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}

	store := &memGatewayStore{}
	h := &PublicShareHandler{store: store, statePath: statePath, state: &shareAndFunnelState{}}
	h.loadState()

	if len(h.state.Shares) != 1 || h.state.Shares[0].ID != "sh_1" {
		t.Fatalf("expected the share with a password to be imported, got %+v", h.state.Shares)
	}
	hash := h.state.Shares[0].PasswordHash
	if hash == "" || strings.Contains(hash, "AH-ABCD-EFGH") {
		t.Fatalf("password must be stored hashed, got %q", hash)
	}
	if platform.CheckPassword(hash, "AH-ABCD-EFGH") != nil {
		t.Fatal("imported hash does not match the legacy password")
	}
	if h.state.FunnelDNSName != "host.tailnet.ts.net" || len(h.state.FunnelRoutes) != 1 {
		t.Fatalf("funnel state not imported: %+v", h.state)
	}
	if _, err := os.Stat(statePath + ".migrated"); err != nil {
		t.Fatalf("legacy file should be renamed after import: %v", err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("legacy state.json should no longer exist")
	}

	// A second start must not re-import (the file is gone, the DB has data).
	h2 := &PublicShareHandler{store: store, statePath: statePath, state: &shareAndFunnelState{}}
	h2.loadState()
	if len(h2.state.Shares) != 1 {
		t.Fatalf("expected state loaded from the store, got %d shares", len(h2.state.Shares))
	}
}

func TestCloudflareLoadState_ImportsLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	legacy := `{"tunnel_id":"tid","tunnel_name":"gw","base_domain":"example.com","mode":"managed",
	  "routes":[{"domain":"app.example.com","target_node_id":"n1","target_ip":"100.64.0.1","port":3000,"upstream":"100.64.0.1:3000"}]}`
	if err := os.WriteFile(statePath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	h := &CloudflareTunnelHandler{store: &memGatewayStore{}, statePath: statePath, state: &CloudflareTunnelState{}}
	h.loadState()
	if h.state.TunnelID != "tid" || h.state.BaseDomain != "example.com" || len(h.state.Routes) != 1 {
		t.Fatalf("cloudflare state not imported: %+v", h.state)
	}
	if _, err := os.Stat(statePath + ".migrated"); err != nil {
		t.Fatalf("legacy file should be renamed: %v", err)
	}
}
