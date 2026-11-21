// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package network

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withFakeRelease serves a fake cloudflared binary and points the downloader (and PATH) at test fixtures so no
// system cloudflared is picked up.
func withFakeRelease(t *testing.T, binary []byte) (requests *int) {
	t.Helper()
	if _, ok := pinnedCloudflaredSHA256[runtime.GOARCH]; !ok {
		t.Skipf("no pinned cloudflared for %s", runtime.GOARCH)
	}
	for _, p := range []string{"/usr/local/bin/cloudflared", "/usr/bin/cloudflared"} {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("system cloudflared at %s takes precedence", p)
		}
	}
	t.Setenv("PATH", t.TempDir())
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if !strings.HasSuffix(r.URL.Path, "/cloudflared-linux-"+runtime.GOARCH) {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(binary)
	}))
	t.Cleanup(srv.Close)
	old := cloudflaredDownloadBase
	cloudflaredDownloadBase = srv.URL
	t.Cleanup(func() { cloudflaredDownloadBase = old })
	return &count
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestEnsureBinary_VerifiesChecksum(t *testing.T) {
	good := []byte("#!/bin/sh\necho cloudflared\n")
	requests := withFakeRelease(t, good)
	t.Setenv("CLOUDFLARED_VERSION", "2099.1.0")
	t.Setenv("CLOUDFLARED_SHA256", sum(good))

	tun := &CloudflareTunnel{cfg: CloudflareTunnelConfig{BaseDir: t.TempDir()}}
	path, err := tun.ensureBinary()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(good) {
		t.Fatal("installed binary differs from the verified download")
	}
	if info, _ := os.Stat(path); info.Mode().Perm()&0100 == 0 {
		t.Fatal("binary must be executable")
	}
	// A matching binary is reused without downloading again.
	if _, err := tun.ensureBinary(); err != nil || *requests != 1 {
		t.Fatalf("expected reuse without a new download, requests=%d err=%v", *requests, err)
	}
}

func TestEnsureBinary_RejectsTamperedDownload(t *testing.T) {
	withFakeRelease(t, []byte("malicious payload"))
	t.Setenv("CLOUDFLARED_VERSION", "2099.1.0")
	t.Setenv("CLOUDFLARED_SHA256", sum([]byte("the real binary")))

	base := t.TempDir()
	tun := &CloudflareTunnel{cfg: CloudflareTunnelConfig{BaseDir: base}}
	if _, err := tun.ensureBinary(); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(base, "bin"))
	if len(entries) != 0 {
		t.Fatalf("nothing must be left in bin/ after a rejected download, found %d entries", len(entries))
	}
}

func TestEnsureBinary_ReplacesUnverifiedManagedBinary(t *testing.T) {
	good := []byte("verified release")
	withFakeRelease(t, good)
	t.Setenv("CLOUDFLARED_VERSION", "2099.1.0")
	t.Setenv("CLOUDFLARED_SHA256", sum(good))

	base := t.TempDir()
	_ = os.MkdirAll(filepath.Join(base, "bin"), 0755)
	stale := filepath.Join(base, "bin", "cloudflared")
	_ = os.WriteFile(stale, []byte("old unverified latest download"), 0755)

	tun := &CloudflareTunnel{cfg: CloudflareTunnelConfig{BaseDir: base}}
	if _, err := tun.ensureBinary(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(stale); string(b) != string(good) {
		t.Fatal("unverified managed binary should be replaced by the verified release")
	}
}

func TestCloudflaredReleaseOverrides(t *testing.T) {
	if _, ok := pinnedCloudflaredSHA256[runtime.GOARCH]; !ok {
		t.Skip("unsupported arch")
	}
	t.Setenv("CLOUDFLARED_VERSION", "")
	t.Setenv("CLOUDFLARED_SHA256", "")
	if v, _, s, err := cloudflaredRelease(); err != nil || v != pinnedCloudflaredVersion || s != pinnedCloudflaredSHA256[runtime.GOARCH] {
		t.Fatalf("pinned defaults: %q %q %v", v, s, err)
	}
	t.Setenv("CLOUDFLARED_VERSION", "2099.1.0")
	if _, _, _, err := cloudflaredRelease(); err == nil {
		t.Error("version without checksum must be rejected")
	}
	t.Setenv("CLOUDFLARED_SHA256", "not-a-hash")
	if _, _, _, err := cloudflaredRelease(); err == nil {
		t.Error("malformed checksum must be rejected")
	}
	t.Setenv("CLOUDFLARED_VERSION", "../../evil")
	t.Setenv("CLOUDFLARED_SHA256", strings.Repeat("a", 64))
	if _, _, _, err := cloudflaredRelease(); err == nil {
		t.Error("version with path characters must be rejected")
	}
}
