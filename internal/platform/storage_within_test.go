// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsWithinDir(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "nodes", "n1", "app", "x.back")
	if !IsWithinDir(base, inside) {
		t.Errorf("expected %q to be inside %q", inside, base)
	}
	for _, p := range []string{base, "/etc/passwd", filepath.Join(base, "..", "secret"), filepath.Join(base+"evil", "x"), ""} {
		if IsWithinDir(base, p) {
			t.Errorf("expected %q to be outside %q", p, base)
		}
	}

	// A symlink inside the base pointing outside must not be considered inside.
	outside := t.TempDir()
	target := filepath.Join(outside, "secret")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks not supported")
	}
	if IsWithinDir(base, link) {
		t.Errorf("symlink escaping the base dir must be rejected")
	}
}
