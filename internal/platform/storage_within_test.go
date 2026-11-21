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

func TestSafeSharedPathRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Skip("symlinks not supported")
	}
	if err := os.MkdirAll(filepath.Join(base, "docs"), 0755); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"/escape/secret", "/escape", "/escape/new-file"} {
		if _, _, err := SafeSharedPath(base, rel); err == nil {
			t.Errorf("%s: symlink escape must be rejected", rel)
		}
	}
	for _, rel := range []string{"/", "/docs", "/docs/new.txt", "/new-dir/new.txt"} {
		if _, _, err := SafeSharedPath(base, rel); err != nil {
			t.Errorf("%s: legitimate path rejected: %v", rel, err)
		}
	}
	if _, _, err := SafeSharedPath(base, "/../etc/passwd"); err != nil {
		t.Errorf("lexical traversal is clamped to the base by filepath.Clean, got error: %v", err)
	}
}
