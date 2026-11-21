// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// AllowedOrigins parses FRONTEND_URL as a comma-separated list of origins (scheme://host[:port]).
func AllowedOrigins() []string {
	var out []string
	for _, o := range strings.Split(os.Getenv("FRONTEND_URL"), ",") {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o != "" {
			out = append(out, o)
		}
	}
	return out
}

// IsAllowedOrigin reports whether a browser Origin may call this API with credentials or open a WebSocket:
// it must be listed in FRONTEND_URL, or share the hostname of the API itself (any port), which covers the
// self-hosted layout where the dashboard (host:3000) talks to the API (host:8080) without FRONTEND_URL.
func IsAllowedOrigin(origin, requestHost string, allowed []string) bool {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return false
	}
	for _, a := range allowed {
		if strings.EqualFold(origin, a) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	reqHost := requestHost
	if h, _, err := net.SplitHostPort(requestHost); err == nil {
		reqHost = h
	}
	return strings.EqualFold(u.Hostname(), strings.Trim(reqHost, "[]"))
}

// CheckWebSocketOrigin is a websocket.Upgrader CheckOrigin func. Requests without an Origin header come from
// non-browser clients (agents, CLI) and are allowed; browser origins must pass IsAllowedOrigin, which blocks
// cross-site WebSocket hijacking from arbitrary websites.
func CheckWebSocketOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return IsAllowedOrigin(origin, r.Host, AllowedOrigins())
}
