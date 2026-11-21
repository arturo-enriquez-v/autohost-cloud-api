// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/network"
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
