// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
)

type mockContainerSettingRepo struct {
	settings map[string]*node.ContainerSetting
}

func newMockContainerSettingRepo() *mockContainerSettingRepo {
	return &mockContainerSettingRepo{settings: make(map[string]*node.ContainerSetting)}
}

func (m *mockContainerSettingRepo) GetByNodeID(ctx context.Context, nodeID string) (*node.ContainerSetting, error) {
	s, ok := m.settings[nodeID]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}

func (m *mockContainerSettingRepo) Upsert(ctx context.Context, setting *node.ContainerSetting) (*node.ContainerSetting, error) {
	m.settings[setting.NodeID] = setting
	return setting, nil
}

func (m *mockContainerSettingRepo) IsContainerLocked(ctx context.Context, nodeID, containerName string) (bool, error) {
	s, ok := m.settings[nodeID]
	if !ok {
		return false, nil
	}
	return s.LockedContainers[containerName], nil
}

func TestContainerSettingService_GetByNodeID_Default(t *testing.T) {
	repo := newMockContainerSettingRepo()
	svc := node.NewContainerSettingService(repo)

	setting, err := svc.GetByNodeID(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if setting.NodeID != "node-1" {
		t.Errorf("expected nodeID node-1, got %s", setting.NodeID)
	}
	if len(setting.SortOrder) != 0 {
		t.Errorf("expected empty sort order, got %v", setting.SortOrder)
	}
	if len(setting.LockedContainers) != 0 {
		t.Errorf("expected empty locks, got %v", setting.LockedContainers)
	}
}

func TestContainerSettingService_SaveAndLockCheck(t *testing.T) {
	repo := newMockContainerSettingRepo()
	svc := node.NewContainerSettingService(repo)
	ctx := context.Background()

	sortOrder := []string{"web", "db", "redis"}
	locks := map[string]bool{
		"db":    true,
		"redis": false,
	}

	saved, err := svc.Save(ctx, "node-1", sortOrder, locks)
	if err != nil {
		t.Fatalf("unexpected error on save: %v", err)
	}
	if len(saved.SortOrder) != 3 {
		t.Fatalf("expected 3 items in sort order, got %d", len(saved.SortOrder))
	}

	if !svc.IsContainerLocked(ctx, "node-1", "db") {
		t.Errorf("expected container db to be locked")
	}
	if svc.IsContainerLocked(ctx, "node-1", "web") {
		t.Errorf("expected container web to not be locked")
	}
	if svc.IsContainerLocked(ctx, "node-1", "redis") {
		t.Errorf("expected container redis to not be locked")
	}
	if svc.IsContainerLocked(ctx, "non-existent-node", "db") {
		t.Errorf("expected container on non-existent node to not be locked")
	}
}
