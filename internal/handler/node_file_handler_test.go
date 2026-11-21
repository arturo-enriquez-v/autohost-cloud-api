// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"path/filepath"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/platform"
)

func TestSafeSharedPathPreventsPathTraversal(t *testing.T) {
	baseDir := t.TempDir()

	// Valid relative path
	abs, rel, err := platform.SafeSharedPath(baseDir, "configs/app.env")
	if err != nil {
		t.Fatalf("expected valid path, got error: %v", err)
	}
	if rel != "/configs/app.env" {
		t.Fatalf("expected /configs/app.env, got %s", rel)
	}
	if abs != filepath.Join(baseDir, "configs", "app.env") {
		t.Fatalf("unexpected abs path: %s", abs)
	}

	// Attempted traversal with ../.. resolves cleanly inside baseDir because of leading "/"
	abs2, rel2, err := platform.SafeSharedPath(baseDir, "../../etc/passwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel2 != "/etc/passwd" || abs2 != filepath.Join(baseDir, "etc", "passwd") {
		t.Fatalf("traversal was not sandboxed inside baseDir: abs=%s rel=%s", abs2, rel2)
	}
}
