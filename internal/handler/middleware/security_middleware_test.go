// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTrustedRealIP(t *testing.T) {
	trusted := ParseTrustedProxies("") // defaults: loopback + private ranges
	cases := []struct {
		name, remote, xff, xreal, want string
	}{
		{"untrusted peer cannot spoof", "203.0.113.7:5000", "1.2.3.4", "", "203.0.113.7"},
		{"untrusted peer x-real-ip ignored", "203.0.113.7:5000", "", "1.2.3.4", "203.0.113.7"},
		{"trusted proxy forwards client", "172.18.0.3:4000", "198.51.100.9", "", "198.51.100.9"},
		{"rightmost untrusted wins", "10.0.0.2:4000", "1.1.1.1, 198.51.100.9, 10.0.0.5", "", "198.51.100.9"},
		{"trusted proxy x-real-ip", "127.0.0.1:4000", "", "198.51.100.10", "198.51.100.10"},
		{"spoofed left part is irrelevant", "127.0.0.1:4000", "evil, 198.51.100.9", "", "198.51.100.9"},
		{"malformed hop ignored", "127.0.0.1:4000", "198.51.100.9, evil", "", "127.0.0.1"},
		{"no headers keeps peer", "127.0.0.1:4000", "", "", "127.0.0.1"},
	}
	for _, tc := range cases {
		var got string
		h := TrustedRealIPWith(trusted)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = ClientIP(r)
		}))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tc.remote
		if tc.xff != "" {
			req.Header.Set("X-Forwarded-For", tc.xff)
		}
		if tc.xreal != "" {
			req.Header.Set("X-Real-IP", tc.xreal)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseTrustedProxiesNone(t *testing.T) {
	if p := ParseTrustedProxies("none"); len(p) != 0 {
		t.Fatalf("expected no trusted proxies, got %v", p)
	}
	if p := ParseTrustedProxies("10.1.0.0/16, 192.0.2.1"); len(p) != 2 {
		t.Fatalf("expected 2 entries, got %v", p)
	}
}

func TestRateLimiter(t *testing.T) {
	l := NewRateLimiter(3, time.Minute)
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	ok, retry := l.Allow("k")
	if ok || retry <= 0 || retry > time.Minute {
		t.Fatalf("4th request should be limited with a positive retry, got ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("keys must be independent")
	}
	now = now.Add(time.Minute)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("window should reset")
	}
}

func TestRateLimitByIPReturns429(t *testing.T) {
	l := NewRateLimiter(1, time.Minute)
	h := RateLimitByIP(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "198.51.100.1:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(); rec.Code != http.StatusOK {
		t.Fatalf("first request: %d", rec.Code)
	}
	rec := do()
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestRedactedRequestURI(t *testing.T) {
	u, _ := url.Parse("/v1/nodes/n1/terminal?token=eyJhbGci.secret&cols=80&st=sharetoken&rows=24")
	got := RedactedRequestURI(u)
	for _, secret := range []string{"eyJhbGci", "secret", "sharetoken"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q leaked in %q", secret, got)
		}
	}
	if !strings.Contains(got, "cols=80") || !strings.Contains(got, "rows=24") {
		t.Fatalf("non-sensitive params should be kept: %q", got)
	}
	if RedactedRequestURI(&url.URL{Path: "/health"}) != "/health" {
		t.Fatal("path without query should be unchanged")
	}
}
