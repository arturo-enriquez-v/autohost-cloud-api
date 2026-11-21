// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"fmt"
	"net"
	"net/netip"
	"path"
	"regexp"
	"strings"

	"github.com/arturo/autohost-cloud-api/internal/core/network"
)

var (
	// safeContainerNameRe mirrors the agent's container name validation.
	safeContainerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-.]{0,127}$`)
	// safePgIdentifierRe matches PostgreSQL role/database names accepted for backups.
	safePgIdentifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.@-]{0,62}$`)
	// safeVolumeNameRe matches Docker named volumes.
	safeVolumeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
)

func validateContainerName(name string) error {
	if !safeContainerNameRe.MatchString(name) {
		return fmt.Errorf("invalid container name %q", name)
	}
	return nil
}

func validatePgIdentifier(kind, value string) error {
	if !safePgIdentifierRe.MatchString(value) {
		return fmt.Errorf("invalid PostgreSQL %s %q", kind, value)
	}
	return nil
}

// validateBackupVolume accepts a Docker named volume or a clean absolute bind-mount source.
// The agent additionally verifies that the value is an actual mount of the target container.
func validateBackupVolume(v string) error {
	if strings.ContainsAny(v, ":,\n\r\x00") {
		return fmt.Errorf("invalid volume %q", v)
	}
	if safeVolumeNameRe.MatchString(v) {
		return nil
	}
	if strings.HasPrefix(v, "/") && path.Clean(v) == v && v != "/" {
		return nil
	}
	return fmt.Errorf("invalid volume %q", v)
}

// normalizeFunnelPathPrefix and shellQuote live in core/network (the Funnel gateway builds the root
// shell script); these aliases keep the handler-level validation and its tests in one place.
var (
	normalizeFunnelPathPrefix = network.NormalizeFunnelPathPrefix
	shellQuote                = network.ShellQuote
)

// normalizeVPNAddress accepts an IP address or CIDR prefix (as reported by Tailscale/Headscale)
// and returns it in canonical form. Empty input is allowed and means "no VPN address".
func normalizeVPNAddress(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if ip := net.ParseIP(v); ip != nil {
		return ip.String(), nil
	}
	if pfx, err := netip.ParsePrefix(v); err == nil {
		return pfx.String(), nil
	}
	return "", fmt.Errorf("invalid VPN address %q", raw)
}

// hostIPOnly strips an optional "/prefix" and returns the IP if it parses, or "".
func hostIPOnly(raw string) string {
	v := strings.TrimSpace(raw)
	if idx := strings.IndexByte(v, '/'); idx != -1 {
		v = v[:idx]
	}
	if ip := net.ParseIP(v); ip != nil {
		return ip.String()
	}
	return ""
}
