// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/arturo/autohost-cloud-api/internal/core/network"
)

// TestGatewayStateRepository_RoundTrip runs against a real Postgres. It only runs when
// AUTOHOST_DB_INTEGRATION=1 and DATABASE_URL are set, and requires the gateway tables to be empty
// (it restores them to empty afterwards).
func TestGatewayStateRepository_RoundTrip(t *testing.T) {
	if os.Getenv("AUTOHOST_DB_INTEGRATION") != "1" || os.Getenv("DATABASE_URL") == "" {
		t.Skip("set AUTOHOST_DB_INTEGRATION=1 and DATABASE_URL to run")
	}
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	var rows int
	_ = db.Get(&rows, `SELECT (SELECT count(*) FROM file_shares) + (SELECT count(*) FROM funnel_routes) + (SELECT count(*) FROM cloudflare_tunnel_routes)`)
	if rows != 0 {
		t.Skip("gateway tables are not empty; refusing to overwrite real data")
	}
	var nodeID, userID string
	if err := db.Get(&nodeID, `SELECT id FROM nodes LIMIT 1`); err != nil {
		t.Skip("needs at least one node")
	}
	if err := db.Get(&userID, `SELECT id FROM users LIMIT 1`); err != nil {
		t.Skip("needs at least one user")
	}
	defer func() {
		_, _ = db.Exec(`DELETE FROM file_shares; DELETE FROM funnel_routes; DELETE FROM cloudflare_tunnel_routes;
			DELETE FROM platform_settings WHERE key LIKE 'funnel.%' OR key LIKE 'cloudflare.%'`)
	}()

	repo := NewGatewayStateRepository(db)
	in := &network.ShareFunnelState{
		FunnelDNSName: "host.tailnet.ts.net",
		Shares: []*network.SharedFolderLink{
			{ID: "sh_test1", OwnerID: userID, NodeID: nodeID, FolderPath: "/docs", Title: "Docs",
				Permission: network.SharePermWrite, PasswordHash: "$2a$10$hash"},
			// References a node that does not exist: must be dropped, not abort the save.
			{ID: "sh_orphan", OwnerID: userID, NodeID: "00000000-0000-0000-0000-000000000000", FolderPath: "/",
				Permission: network.SharePermRead, PasswordHash: "x"},
			// Malformed legacy IDs must be skipped too.
			{ID: "sh_bad", OwnerID: "not-a-uuid", NodeID: nodeID, FolderPath: "/", Permission: network.SharePermRead, PasswordHash: "x"},
		},
		FunnelRoutes: []*network.TailscaleFunnelRoute{
			{ID: "fn_test1", FunnelPort: 443, PathPrefix: "/app", TargetNodeID: nodeID, TargetIP: "100.64.0.1",
				ContainerPort: 8080, Upstream: "100.64.0.1:8080"},
		},
	}
	if err := repo.SaveShareFunnelState(ctx, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := repo.LoadShareFunnelState(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.FunnelDNSName != in.FunnelDNSName || len(out.Shares) != 1 || len(out.FunnelRoutes) != 1 {
		t.Fatalf("unexpected state: dns=%q shares=%d routes=%d", out.FunnelDNSName, len(out.Shares), len(out.FunnelRoutes))
	}
	if s := out.Shares[0]; s.ID != "sh_test1" || s.PasswordHash != "$2a$10$hash" || s.Permission != network.SharePermWrite {
		t.Fatalf("share not round-tripped: %+v", s)
	}

	// Saving again replaces the previous state (here: deletes everything).
	if err := repo.SaveShareFunnelState(ctx, &network.ShareFunnelState{}); err != nil {
		t.Fatalf("save empty: %v", err)
	}
	if out, _ := repo.LoadShareFunnelState(ctx); len(out.Shares) != 0 || len(out.FunnelRoutes) != 0 {
		t.Fatalf("expected empty state after replace")
	}

	cf := &network.CloudflareTunnelState{
		TunnelID: "tid", TunnelName: "autohost-gateway", BaseDomain: "example.com", Mode: "managed",
		Routes: []*network.CloudflareTunnelRoute{
			{Domain: "app.example.com", TargetNodeID: nodeID, TargetIP: "100.64.0.1", Port: 3000, Upstream: "100.64.0.1:3000"},
		},
	}
	if err := repo.SaveCloudflareTunnelState(ctx, cf); err != nil {
		t.Fatalf("save cloudflare: %v", err)
	}
	gotCF, err := repo.LoadCloudflareTunnelState(ctx)
	if err != nil || gotCF.TunnelID != "tid" || gotCF.BaseDomain != "example.com" || len(gotCF.Routes) != 1 {
		t.Fatalf("cloudflare state not round-tripped: %+v, %v", gotCF, err)
	}
}
