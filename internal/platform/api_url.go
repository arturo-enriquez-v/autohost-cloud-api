// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package platform

import (
	"net"
	"os"
	"strings"
)

// AgentAPIURL returns the base URL remote agents should use to reach this API, in priority order:
//
//  1. API_PUBLIC_URL, when set explicitly (lets operators choose the scheme, e.g. HTTPS behind a proxy).
//  2. http://<ip>:<port> for the first valid IP in localIPs (the local node's VPN IP, then its LAN IP), where
//     port is the host port the API is published on (see AgentAPIPort).
//  3. NEXT_PUBLIC_API_URL.
//  4. fallback (typically derived from the current request).
//
// It returns "" when none of them is available.
func AgentAPIURL(localIPs []string, fallback string) string {
	if u := strings.TrimSpace(os.Getenv("API_PUBLIC_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	for _, raw := range localIPs {
		// inet columns may carry a prefix length ("100.64.0.1/32").
		ip := strings.TrimSpace(strings.Split(strings.TrimSpace(raw), "/")[0])
		if net.ParseIP(ip) != nil {
			return "http://" + net.JoinHostPort(ip, AgentAPIPort())
		}
	}
	if u := strings.TrimSpace(os.Getenv("NEXT_PUBLIC_API_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return fallback
}

// AgentAPIPort is the port agents reach the API on when they connect by IP. API_PORT is the host port the
// self-hosted compose publishes the API on, which differs from the container's PORT when remapped.
func AgentAPIPort() string {
	for _, key := range []string{"API_PORT", "PORT"} {
		if p := strings.TrimSpace(os.Getenv(key)); p != "" {
			return p
		}
	}
	return "8080"
}
