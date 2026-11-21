// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cfMemStore is an in-memory store for the Cloudflare part of GatewayStateStore.
type cfMemStore struct {
	memStore
	cf       *CloudflareTunnelState
	failSave bool
}

func (m *cfMemStore) LoadCloudflareTunnelState(context.Context) (*CloudflareTunnelState, error) {
	if m.cf == nil {
		return &CloudflareTunnelState{}, nil
	}
	return cloneCFState(m.cf), nil
}
func (m *cfMemStore) SaveCloudflareTunnelState(_ context.Context, st *CloudflareTunnelState) error {
	if m.failSave {
		return errors.New("db down")
	}
	m.cf = cloneCFState(st)
	return nil
}

type recordingMirror struct{ upserts, deletes []string }

func (m *recordingMirror) Upsert(domain, upstream, _ string) {
	m.upserts = append(m.upserts, domain+"->"+upstream)
}
func (m *recordingMirror) Delete(domain string) { m.deletes = append(m.deletes, domain) }

func newTestTunnel(t *testing.T, store *cfMemStore, mirror CloudflareRouteMirror) *CloudflareTunnel {
	t.Helper()
	return NewCloudflareTunnel(CloudflareTunnelConfig{Store: store, BaseDir: t.TempDir(), Mirror: mirror})
}

func TestCloudflareTunnel_ImportsLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	legacy := `{"tunnel_id":"tid","tunnel_name":"gw","base_domain":"example.com","mode":"managed",
	  "routes":[{"domain":"app.example.com","target_node_id":"n1","target_ip":"100.64.0.1","port":3000,"upstream":"100.64.0.1:3000"}]}`
	if err := os.WriteFile(statePath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	tun := NewCloudflareTunnel(CloudflareTunnelConfig{Store: &cfMemStore{}, BaseDir: dir, LegacyStatePath: statePath})
	st := tun.Status().State
	if st.TunnelID != "tid" || st.BaseDomain != "example.com" || len(st.Routes) != 1 {
		t.Fatalf("cloudflare state not imported: %+v", st)
	}
	if _, err := os.Stat(statePath + ".migrated"); err != nil {
		t.Fatalf("legacy file should be renamed: %v", err)
	}
}

func TestResolveRouteDomain(t *testing.T) {
	tun := newTestTunnel(t, &cfMemStore{cf: &CloudflareTunnelState{BaseDomain: "example.com"}}, nil)
	cases := []struct {
		sub, base, domain, host string
		port                    int
		want                    string
		ok                      bool
	}{
		{"app", "", "", "n", 80, "app.example.com", true},
		{"@", "", "", "n", 80, "example.com", true},
		{"", "", "explicit.example.org", "n", 80, "explicit.example.org", true},
		{"App", "Other.COM", "", "n", 80, "app.other.com", true},
		// YAML / command-line injection attempts must be rejected.
		{"x\n  - hostname: evil.com", "", "", "n", 80, "", false},
		{"", "", "evil.com\n  service: http://1.2.3.4", "n", 80, "", false},
		{"", "bad domain", "", "n", 80, "", false},
	}
	for _, tc := range cases {
		got, _, _, err := tun.ResolveRouteDomain(tc.sub, tc.base, tc.domain, tc.host, tc.port)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("ResolveRouteDomain(%q,%q,%q) = %q, %v; want %q ok=%v", tc.sub, tc.base, tc.domain, got, err, tc.want, tc.ok)
		}
	}

	// Without a base domain, quick-tunnel names fall back to a sanitized "<host>-<port>.local".
	quick := newTestTunnel(t, &cfMemStore{}, nil)
	if got, _, _, err := quick.ResolveRouteDomain("", "", "", "My_Server!", 8080); err != nil || got != "my-server-8080.local" {
		t.Errorf("quick fallback = %q, %v", got, err)
	}
}

func TestCloudflareTunnel_Routes(t *testing.T) {
	mirror := &recordingMirror{}
	store := &cfMemStore{}
	tun := newTestTunnel(t, store, mirror)

	in := CloudflareRouteInput{Domain: "app.example.com", TargetNodeID: "n1", TargetIP: "100.64.0.1", Port: 3000}
	if _, err := tun.UpsertRoute(in, false); err != nil {
		t.Fatal(err)
	}
	// Another tenant cannot take over the same domain without proving it may manage the existing route.
	in2 := in
	in2.TargetNodeID, in2.TargetIP = "n2", "100.64.0.2"
	if _, err := tun.UpsertRoute(in2, false); !errors.Is(err, ErrCFDomainTaken) {
		t.Fatalf("expected ErrCFDomainTaken, got %v", err)
	}
	if got := tun.ResolveUpstream("APP.example.com:443"); got != "100.64.0.1:3000" {
		t.Errorf("upstream = %q", got)
	}
	if _, err := tun.UpsertRoute(in2, true); err != nil {
		t.Fatal(err)
	}
	if got := tun.ResolveUpstream("app.example.com"); got != "100.64.0.2:3000" {
		t.Errorf("authorized replacement not applied, upstream = %q", got)
	}
	if got := tun.ResolveUpstream("random.trycloudflare.com"); got != "100.64.0.2:3000" {
		t.Errorf("quick tunnel host should use the latest route, got %q", got)
	}
	if got := tun.ResolveUpstream("unknown.example.com"); got != "" {
		t.Errorf("unknown host should not resolve, got %q", got)
	}
	if len(mirror.upserts) != 2 {
		t.Errorf("expected 2 mirrored upserts, got %v", mirror.upserts)
	}

	for _, bad := range []CloudflareRouteInput{
		{Domain: "evil.com\nx", TargetIP: "100.64.0.1", Port: 80},
		{Domain: "ok.example.com", TargetIP: "1.2.3.4; reboot", Port: 80},
		{Domain: "ok.example.com", TargetIP: "100.64.0.1", Port: 0},
	} {
		if _, err := tun.UpsertRoute(bad, false); err == nil {
			t.Errorf("invalid route accepted: %+v", bad)
		}
	}

	// A failed save must leave the routes untouched.
	store.failSave = true
	if err := tun.DeleteRoute("app.example.com"); !errors.Is(err, ErrCFPersist) {
		t.Fatalf("expected ErrCFPersist, got %v", err)
	}
	if _, ok := tun.Route("app.example.com"); !ok {
		t.Fatal("route must still exist after a failed delete")
	}
	store.failSave = false
	if err := tun.DeleteRoute("app.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := tun.DeleteRoute("app.example.com"); !errors.Is(err, ErrCFRouteNotFound) {
		t.Fatalf("expected ErrCFRouteNotFound, got %v", err)
	}
	if len(mirror.deletes) != 1 {
		t.Errorf("expected 1 mirrored delete, got %v", mirror.deletes)
	}
}

func TestBuildCloudflaredConfigSkipsInvalidRows(t *testing.T) {
	routes := []*CloudflareTunnelRoute{
		{Domain: "app.example.com", Upstream: "100.64.0.1:3000"},
		// Tampered rows must not reach the YAML.
		{Domain: "evil.com\n  - hostname: x", Upstream: "100.64.0.1:80"},
		{Domain: "ok.example.com", Upstream: "1.2.3.4:80\n    service: http://169.254.169.254"},
	}
	cfg := BuildCloudflaredConfig("11111111-2222-3333-4444-555555555555", "/creds.json", routes, CloudflareProxyAddr)
	if !strings.Contains(cfg, "  - hostname: app.example.com\n    service: http://100.64.0.1:3000\n") {
		t.Errorf("valid route missing:\n%s", cfg)
	}
	for _, bad := range []string{"evil.com", "169.254.169.254", "hostname: x"} {
		if strings.Contains(cfg, bad) {
			t.Errorf("config contains %q:\n%s", bad, cfg)
		}
	}
	if !strings.HasSuffix(cfg, "  - service: http://"+CloudflareProxyAddr+"\n") {
		t.Errorf("catch-all must be last:\n%s", cfg)
	}
}

func TestSetBaseDomainValidates(t *testing.T) {
	tun := newTestTunnel(t, &cfMemStore{}, nil)
	if got, err := tun.SetBaseDomain("https://Example.COM/"); err != nil || got != "example.com" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := tun.SetBaseDomain("example.com\nevil"); !errors.Is(err, ErrCFInvalidDomain) {
		t.Fatalf("expected ErrCFInvalidDomain, got %v", err)
	}
	if tun.BaseDomain() != "example.com" {
		t.Fatal("invalid input must not change the base domain")
	}
}

func TestHTMLEscape(t *testing.T) {
	if got := htmlEscape(`<script>alert("x")</script>`); strings.ContainsAny(got, `<>"`) {
		t.Fatalf("not escaped: %q", got)
	}
}
