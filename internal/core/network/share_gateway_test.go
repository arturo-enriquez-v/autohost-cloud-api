// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package network

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// memStore is an in-memory GatewayStateStore; failSave makes the next saves fail.
type memStore struct {
	shares   *ShareFunnelState
	failSave bool
	saves    int
}

func (m *memStore) LoadShareFunnelState(context.Context) (*ShareFunnelState, error) {
	if m.shares == nil {
		return &ShareFunnelState{}, nil
	}
	return cloneState(m.shares), nil
}
func (m *memStore) SaveShareFunnelState(_ context.Context, st *ShareFunnelState) error {
	if m.failSave {
		return errors.New("db down")
	}
	m.saves++
	m.shares = cloneState(st)
	return nil
}
func (m *memStore) LoadCloudflareTunnelState(context.Context) (*CloudflareTunnelState, error) {
	return &CloudflareTunnelState{}, nil
}
func (m *memStore) SaveCloudflareTunnelState(context.Context, *CloudflareTunnelState) error {
	return nil
}

func newTestGateway(t *testing.T, store *memStore) *ShareGateway {
	t.Helper()
	return NewShareGateway(ShareGatewayConfig{Store: store, SigningSecret: "test-secret"})
}

func TestGateway_ImportsLegacyJSONAndHashesPasswords(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
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
	store := &memStore{}
	g := NewShareGateway(ShareGatewayConfig{Store: store, LegacyStatePath: statePath})

	shares := g.Shares()
	if len(shares) != 1 || shares[0].ID != "sh_1" {
		t.Fatalf("expected the share with a password to be imported, got %+v", shares)
	}
	if strings.Contains(shares[0].PasswordHash, "AH-ABCD-EFGH") || platform.CheckPassword(shares[0].PasswordHash, "AH-ABCD-EFGH") != nil {
		t.Fatal("password must be stored as a bcrypt hash of the legacy password")
	}
	if dns, routes := g.FunnelStatus(); dns != "host.tailnet.ts.net" || len(routes) != 1 {
		t.Fatalf("funnel state not imported: dns=%q routes=%d", dns, len(routes))
	}
	if _, err := os.Stat(statePath + ".migrated"); err != nil {
		t.Fatalf("legacy file should be renamed: %v", err)
	}
	// A second start loads from the store and does not re-import.
	if g2 := NewShareGateway(ShareGatewayConfig{Store: store, LegacyStatePath: statePath}); len(g2.Shares()) != 1 {
		t.Fatal("expected state loaded from the store")
	}
}

func TestGateway_FailedSaveLeavesStateUnchanged(t *testing.T) {
	store := &memStore{}
	g := newTestGateway(t, store)
	share, _, err := g.CreateShare(NewShareInput{OwnerID: "u1", NodeID: "n1", FolderPath: "/docs", Title: "Docs"})
	if err != nil {
		t.Fatal(err)
	}

	store.failSave = true
	newTitle := "Renamed"
	if _, _, err := g.UpdateShare(share.ID, ShareUpdate{Title: &newTitle}); !errors.Is(err, ErrGatewayPersist) {
		t.Fatalf("expected ErrGatewayPersist, got %v", err)
	}
	if got, _ := g.Share(share.ID); got.Title != "Docs" {
		t.Fatalf("in-memory state must not change when persisting fails, title=%q", got.Title)
	}
	if err := g.DeleteShare(share.ID); !errors.Is(err, ErrGatewayPersist) {
		t.Fatalf("expected ErrGatewayPersist, got %v", err)
	}
	if _, ok := g.Share(share.ID); !ok {
		t.Fatal("share must still exist after a failed delete")
	}
}

func TestGateway_ReturnedValuesAreCopies(t *testing.T) {
	g := newTestGateway(t, &memStore{})
	share, _, _ := g.CreateShare(NewShareInput{OwnerID: "u1", NodeID: "n1", FolderPath: "/"})
	got, _ := g.Share(share.ID)
	got.Permission = SharePermEditor
	got.FolderPath = "/etc"
	if again, _ := g.Share(share.ID); again.Permission != SharePermRead || again.FolderPath != "/" {
		t.Fatal("mutating a returned share must not change gateway state")
	}
}

func TestGateway_PasswordsAndTokens(t *testing.T) {
	g := newTestGateway(t, &memStore{})
	share, pwd, err := g.CreateShare(NewShareInput{OwnerID: "u1", NodeID: "n1", FolderPath: "/docs", Password: "correct horse"})
	if err != nil || pwd != "correct horse" {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := g.CreateShare(NewShareInput{OwnerID: "u1", NodeID: "n1", Password: "short"}); !errors.Is(err, ErrInvalidSharePasswd) {
		t.Fatalf("short custom password should be rejected, got %v", err)
	}
	if _, _, err := g.Unlock(share.ID, "wrong"); !errors.Is(err, ErrWrongSharePassword) {
		t.Fatalf("wrong password: got %v", err)
	}
	token, _, err := g.Unlock(share.ID, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.VerifyToken(token, share.ID); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if _, err := g.VerifyToken(token, "sh_other"); err == nil {
		t.Fatal("token must be bound to its share")
	}
	if _, err := g.VerifyToken(token+"x", share.ID); err == nil {
		t.Fatal("tampered token must be rejected")
	}
	expired, _ := g.issueToken(share, time.Now().Add(-time.Minute))
	if _, err := g.VerifyToken(expired, share.ID); err == nil {
		t.Fatal("expired token must be rejected")
	}

	// Regenerating the password invalidates the old one; revoking the share invalidates tokens.
	_, newPwd, err := g.UpdateShare(share.ID, ShareUpdate{RegeneratePassword: true})
	if err != nil || newPwd == "" {
		t.Fatalf("regenerate: %v", err)
	}
	if _, _, err := g.Unlock(share.ID, "correct horse"); !errors.Is(err, ErrWrongSharePassword) {
		t.Fatal("old password must stop working after regeneration")
	}
	if err := g.DeleteShare(share.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.VerifyToken(token, share.ID); err == nil {
		t.Fatal("token of a deleted share must be rejected")
	}
}

func TestGateway_FunnelRoutes(t *testing.T) {
	g := newTestGateway(t, &memStore{})
	in := FunnelRouteInput{FunnelPort: 443, PathPrefix: "/app", TargetNodeID: "n1", TargetIP: "100.64.0.1", ContainerPort: 8080}
	first, err := g.UpsertFunnelRoute(in, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Upstream != "100.64.0.1:8080" {
		t.Fatalf("unexpected upstream %q", first.Upstream)
	}

	// The same public path cannot be taken over without proving the caller may replace that route.
	in2 := in
	in2.TargetNodeID, in2.TargetIP = "n2", "100.64.0.2"
	if _, err := g.UpsertFunnelRoute(in2, ""); !errors.Is(err, ErrFunnelPathTaken) {
		t.Fatalf("expected ErrFunnelPathTaken, got %v", err)
	}
	replaced, err := g.UpsertFunnelRoute(in2, first.ID)
	if err != nil || replaced.ID != first.ID || replaced.Upstream != "100.64.0.2:8080" {
		t.Fatalf("authorized replacement failed: %+v, %v", replaced, err)
	}

	for _, bad := range []FunnelRouteInput{
		{FunnelPort: 443, PathPrefix: "/x; reboot", TargetIP: "100.64.0.1", ContainerPort: 80},
		{FunnelPort: 443, PathPrefix: "/ok", TargetIP: "evil; reboot", ContainerPort: 80},
		{FunnelPort: 443, PathPrefix: "/ok", TargetIP: "100.64.0.1", ContainerPort: 0},
	} {
		if _, err := g.UpsertFunnelRoute(bad, ""); err == nil {
			t.Errorf("invalid route accepted: %+v", bad)
		}
	}

	if _, err := g.UpsertFunnelRoute(FunnelRouteInput{FunnelPort: 443, PathPrefix: "/", TargetIP: "100.64.0.3", ContainerPort: 3000}, ""); err != nil {
		t.Fatal(err)
	}
	if got := g.ResolveUpstream(443, "/app/page"); got != "100.64.0.2:8080" {
		t.Errorf("specific prefix should win, got %q", got)
	}
	if got := g.ResolveUpstream(443, "/other"); got != "100.64.0.3:3000" {
		t.Errorf("catch-all expected, got %q", got)
	}
	if got := g.ResolveUpstream(8443, "/app"); got != "" {
		t.Errorf("no route on 8443, got %q", got)
	}

	if err := g.DeleteFunnelRoute(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := g.DeleteFunnelRoute(first.ID); !errors.Is(err, ErrFunnelRouteNotFound) {
		t.Fatalf("expected ErrFunnelRouteNotFound, got %v", err)
	}
}

func TestBuildFunnelSyncScriptIsInjectionSafe(t *testing.T) {
	routes := []*TailscaleFunnelRoute{
		{ID: "ok", FunnelPort: 443, PathPrefix: "/app", Upstream: "100.64.0.1:8080"},
		// Tampered rows (e.g. from an older version or a direct DB edit) must be skipped, not executed.
		{ID: "evil-path", FunnelPort: 443, PathPrefix: "/x$(touch /tmp/pwned)", Upstream: "100.64.0.1:80"},
		{ID: "evil-upstream", FunnelPort: 443, PathPrefix: "/y", Upstream: "1.2.3.4:80; reboot"},
		{ID: "bad-port", FunnelPort: 22, PathPrefix: "/z", Upstream: "100.64.0.1:80"},
	}
	script := BuildFunnelSyncScript([]*SharedFolderLink{{FunnelEnabled: true}}, routes)

	for _, bad := range []string{"pwned", "reboot", "--https=22"} {
		if strings.Contains(script, bad) {
			t.Errorf("script contains %q:\n%s", bad, script)
		}
	}
	if !strings.Contains(script, `'--set-path=/app' 'http://100.64.0.1:8080'`) {
		t.Errorf("valid route missing or not quoted:\n%s", script)
	}
	if !strings.Contains(script, "--set-path=/share") {
		t.Error("funnel-enabled share should expose the share portal")
	}
	if sh, err := exec.LookPath("bash"); err == nil {
		if out, err := exec.Command(sh, "-n", "-c", script).CombinedOutput(); err != nil {
			t.Errorf("generated script is not valid bash: %v\n%s", err, out)
		}
	}
}

func TestResolveJailedPath(t *testing.T) {
	cases := []struct {
		root, sub, want string
		ok              bool
	}{
		{"/docs", "/", "/docs", true},
		{"/docs", "/a/b.txt", "/docs/a/b.txt", true},
		{"/docs", "/../etc/passwd", "/docs/etc/passwd", true}, // clamped by Clean, never escapes
		{"/", "/etc", "/etc", true},
	}
	for _, tc := range cases {
		got, _, err := ResolveJailedPath(tc.root, tc.sub)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("ResolveJailedPath(%q,%q) = %q, %v", tc.root, tc.sub, got, err)
		}
	}
}
