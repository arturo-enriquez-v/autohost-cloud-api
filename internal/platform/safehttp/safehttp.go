// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

// Package safehttp builds HTTP clients for requests whose URL comes from users (connectors). It checks
// every address the client connects to, after DNS resolution, so a user cannot reach the API's own
// network: loopback, cloud metadata, link-local and, unless allowed, private ranges. Checking at dial time
// also covers DNS rebinding and redirects.
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ErrBlockedDestination is returned (wrapped) when a request targets a forbidden address.
var ErrBlockedDestination = errors.New("destination address is not allowed")

// blockedHostnames are cloud metadata names that must never be reached, whatever they resolve to.
var blockedHostnames = map[string]bool{
	"metadata.google.internal": true,
	"metadata.internal":        true,
	"instance-data":            true,
	"localhost":                true,
}

// alwaysBlocked lists ranges blocked even with AllowPrivate: the API's own host and cloud metadata.
var alwaysBlocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),          // "this network"
	netip.MustParsePrefix("127.0.0.0/8"),        // loopback
	netip.MustParsePrefix("169.254.0.0/16"),     // link-local, cloud metadata (169.254.169.254)
	netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud metadata
	netip.MustParsePrefix("192.0.0.0/24"),       // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),      // benchmarking
	netip.MustParsePrefix("224.0.0.0/4"),        // multicast
	netip.MustParsePrefix("240.0.0.0/4"),        // reserved, broadcast
	netip.MustParsePrefix("::/128"),             // unspecified
	netip.MustParsePrefix("::1/128"),            // loopback
	netip.MustParsePrefix("fe80::/10"),          // link-local
	netip.MustParsePrefix("ff00::/8"),           // multicast
	netip.MustParsePrefix("64:ff9b::/96"),       // NAT64: could embed any IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"),     // local-use NAT64
	netip.MustParsePrefix("2002::/16"),          // 6to4: embeds an IPv4 address
	netip.MustParsePrefix("2001::/32"),          // Teredo
	netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS metadata (IPv6)
}

// privateRanges are blocked unless AllowPrivate is set (a self-hosted API calling services on its LAN or
// VPN).
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT, also Tailscale/Headscale addresses
	netip.MustParsePrefix("fc00::/7"),      // unique local
}

// Options configures a client.
type Options struct {
	// AllowPrivate permits private, CGNAT and unique-local ranges. Loopback, link-local and metadata stay
	// blocked.
	AllowPrivate bool
	// Timeout bounds the whole request, including reading the body. Defaults to 15s.
	Timeout time.Duration
	// MaxRedirects bounds same-origin redirects. Defaults to 5.
	MaxRedirects int
}

// CheckAddr reports whether a connection to addr is allowed.
func CheckAddr(addr netip.Addr, allowPrivate bool) error {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" {
		return fmt.Errorf("%w: %s", ErrBlockedDestination, addr)
	}
	for _, p := range alwaysBlocked {
		if p.Contains(addr) {
			return fmt.Errorf("%w: %s", ErrBlockedDestination, addr)
		}
	}
	if !allowPrivate {
		for _, p := range privateRanges {
			if p.Contains(addr) {
				return fmt.Errorf("%w: %s is a private address", ErrBlockedDestination, addr)
			}
		}
	}
	return nil
}

// CheckHost rejects host names that are blocked by name (metadata endpoints, localhost).
func CheckHost(host string) error {
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if h == "" {
		return fmt.Errorf("%w: empty host", ErrBlockedDestination)
	}
	if blockedHostnames[h] || strings.HasSuffix(h, ".localhost") {
		return fmt.Errorf("%w: %s", ErrBlockedDestination, host)
	}
	return nil
}

// lookup resolves a host name; replaced in tests.
var lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// NewClient returns a client that only connects to allowed addresses, does not use proxies from the
// environment (a proxy would hide the real destination), and only follows redirects to the same origin.
func NewClient(opts Options) *http.Client {
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	if opts.MaxRedirects <= 0 {
		opts.MaxRedirects = 5
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: opts.Timeout,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialChecked(ctx, dialer, network, addr, opts.AllowPrivate)
		},
	}
	return &http.Client{
		Timeout:       opts.Timeout,
		Transport:     transport,
		CheckRedirect: redirectPolicy(opts.MaxRedirects),
	}
}

// dialChecked resolves addr, rejects the connection if any resolved address is blocked (so a name that
// mixes public and private records cannot be used), and connects to the checked address itself, never
// re-resolving.
func dialChecked(ctx context.Context, dialer *net.Dialer, network, addr string, allowPrivate bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if err := CheckHost(host); err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		addrs = []netip.Addr{ip}
	} else {
		addrs, err = lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("no addresses for %s", host)
		}
	}
	for _, ip := range addrs {
		if err := CheckAddr(ip, allowPrivate); err != nil {
			return nil, err
		}
	}
	var lastErr error
	for _, ip := range addrs {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// redirectPolicy follows redirects only within the original origin (scheme, host and port), so headers
// carrying credentials are never sent to another server, and never downgrades https to http. A redirect
// elsewhere is returned to the caller as the response.
func redirectPolicy(max int) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return fmt.Errorf("stopped after %d redirects", max)
		}
		first := via[0].URL
		if req.URL.Scheme != first.Scheme || !strings.EqualFold(req.URL.Host, first.Host) {
			return http.ErrUseLastResponse
		}
		return nil
	}
}
