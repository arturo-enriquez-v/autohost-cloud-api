// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"context"
	"testing"
)

type ctxKey struct{}

// ctxRecordingRepo wraps mockRBACRepo and records the context each hot-path lookup receives.
type ctxRecordingRepo struct {
	*mockRBACRepo
	seen []context.Context
}

func (r *ctxRecordingRepo) GetUserByID(ctx context.Context, id string) (*User, error) {
	r.seen = append(r.seen, ctx)
	return r.mockRBACRepo.GetUserByID(ctx, id)
}

func (r *ctxRecordingRepo) GetNodeProjectAndOwner(ctx context.Context, nodeID string) (*string, *string, bool, error) {
	r.seen = append(r.seen, ctx)
	return r.mockRBACRepo.GetNodeProjectAndOwner(ctx, nodeID)
}

func (r *ctxRecordingRepo) GetUserNodePermission(ctx context.Context, userID, nodeID string) (*NodePermissionGrant, error) {
	r.seen = append(r.seen, ctx)
	return r.mockRBACRepo.GetUserNodePermission(ctx, userID, nodeID)
}

// TestHotPathPropagatesRequestContext ensures permission checks pass the caller's context down to the
// repository, so a cancelled request also cancels its authorization queries.
func TestHotPathPropagatesRequestContext(t *testing.T) {
	base := newMockRBACRepo()
	base.users["u1"] = &User{ID: "u1", Role: RoleMember, IsActive: true}
	owner := "someone-else"
	base.nodeOwners["n1"] = &owner
	repo := &ctxRecordingRepo{mockRBACRepo: base}
	svc := NewRBACService(repo)

	ctx := context.WithValue(context.Background(), ctxKey{}, "request")
	_, _ = svc.HasNodePermission(ctx, "u1", "n1", PermFilesRead)
	_, _ = svc.IsAdmin(ctx, "u1")

	if len(repo.seen) < 3 {
		t.Fatalf("expected several repository lookups, got %d", len(repo.seen))
	}
	for i, c := range repo.seen {
		if c.Value(ctxKey{}) != "request" {
			t.Fatalf("lookup %d did not receive the request context", i)
		}
	}
}
