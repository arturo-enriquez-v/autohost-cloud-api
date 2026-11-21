// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// defaultTrustedProxies covers loopback and private ranges, where the Next.js frontend and reverse
// proxies (Caddy, Docker networks, Tailscale CGNAT) normally connect from in a self-hosted setup.
var defaultTrustedProxies = []string{
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"100.64.0.0/10", "fc00::/7",
}

// ParseTrustedProxies reads TRUSTED_PROXIES (comma-separated CIDRs or IPs). "none" disables forwarding
// headers entirely; empty uses defaultTrustedProxies.
func ParseTrustedProxies(raw string) []netip.Prefix {
	raw = strings.TrimSpace(raw)
	if strings.EqualFold(raw, "none") {
		return nil
	}
	entries := defaultTrustedProxies
	if raw != "" {
		entries = strings.Split(raw, ",")
	}
	var out []netip.Prefix
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		log.Printf("[WARN] TRUSTED_PROXIES: ignoring invalid entry %q", e)
	}
	return out
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	addr = addr.Unmap()
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// TrustedRealIP replaces chi's RealIP: X-Forwarded-For / X-Real-IP are only honored when the direct peer
// is a trusted proxy, so arbitrary clients cannot spoof their IP (which feeds rate limiting and audit logs).
// For X-Forwarded-For it walks the chain from the right and takes the first untrusted address.
func TrustedRealIP(next http.Handler) http.Handler {
	trusted := ParseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	return TrustedRealIPWith(trusted)(next)
}

// TrustedRealIPWith is TrustedRealIP with an explicit trusted list (used by tests).
func TrustedRealIPWith(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := clientIPFromRequest(r, trusted); ip != "" {
				r.RemoteAddr = net.JoinHostPort(ip, "0")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIPFromRequest(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !isTrusted(peer, trusted) {
		return "" // untrusted peer: keep the socket address
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				return "" // malformed chain: do not trust any of it
			}
			if !isTrusted(a, trusted) || i == 0 {
				return a.Unmap().String()
			}
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		if a, err := netip.ParseAddr(xr); err == nil {
			return a.Unmap().String()
		}
	}
	return ""
}
