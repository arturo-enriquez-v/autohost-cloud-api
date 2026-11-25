// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package safehttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func TestCheckAddr(t *testing.T) {
	cases := []struct {
		addr        string
		public, lan bool // allowed without / with AllowPrivate
	}{
		{"8.8.8.8", true, true},
		{"2606:4700::1111", true, true},
		{"127.0.0.1", false, false},
		{"::1", false, false},
		{"::ffff:127.0.0.1", false, false},
		{"169.254.169.254", false, false},
		{"::ffff:169.254.169.254", false, false},
		{"100.100.100.200", false, false},
		{"0.0.0.0", false, false},
		{"fe80::1", false, false},
		{"fd00:ec2::254", false, false},
		{"64:ff9b::a9fe:a9fe", false, false},
		{"224.0.0.1", false, false},
		{"10.0.0.5", false, true},
		{"172.20.1.1", false, true},
		{"192.168.1.50", false, true},
		{"100.64.0.7", false, true},
		{"fd12:3456::1", false, true},
	}
	for _, c := range cases {
		a := netip.MustParseAddr(c.addr)
		if got := CheckAddr(a, false) == nil; got != c.public {
			t.Errorf("CheckAddr(%s, false) allowed=%v, want %v", c.addr, got, c.public)
		}
		if got := CheckAddr(a, true) == nil; got != c.lan {
			t.Errorf("CheckAddr(%s, true) allowed=%v, want %v", c.addr, got, c.lan)
		}
	}
}

func TestCheckHost(t *testing.T) {
	for _, h := range []string{"localhost", "LOCALHOST.", "api.localhost", "metadata.google.internal", ""} {
		if err := CheckHost(h); !errors.Is(err, ErrBlockedDestination) {
			t.Errorf("CheckHost(%q) = %v, want blocked", h, err)
		}
	}
	if err := CheckHost("api.github.com"); err != nil {
		t.Errorf("CheckHost(api.github.com) = %v", err)
	}
}

func TestClientBlocksLoopbackServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	for _, allowPrivate := range []bool{false, true} {
		_, err := NewClient(Options{AllowPrivate: allowPrivate}).Get(srv.URL)
		if !errors.Is(err, ErrBlockedDestination) {
			t.Fatalf("allowPrivate=%v: err = %v, want ErrBlockedDestination", allowPrivate, err)
		}
	}
}

func TestClientBlocksNameResolvingToPrivate(t *testing.T) {
	orig := lookup
	defer func() { lookup = orig }()
	lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		// One public and one internal record: the whole name is rejected.
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.1.2.3")}, nil
	}
	_, err := NewClient(Options{}).Get("http://rebind.example.test/")
	if !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("err = %v, want ErrBlockedDestination", err)
	}
}

func TestRedirectPolicy(t *testing.T) {
	policy := redirectPolicy(3)
	mk := func(raw string) *http.Request { u, _ := url.Parse(raw); return &http.Request{URL: u} }
	via := []*http.Request{mk("https://api.example.com/a")}

	if err := policy(mk("https://api.example.com/b"), via); err != nil {
		t.Errorf("same origin: %v", err)
	}
	for _, target := range []string{"https://evil.example.com/b", "http://api.example.com/b", "https://api.example.com:8443/b"} {
		if err := policy(mk(target), via); !errors.Is(err, http.ErrUseLastResponse) {
			t.Errorf("%s: err = %v, want ErrUseLastResponse", target, err)
		}
	}
	long := []*http.Request{via[0], via[0], via[0]}
	if err := policy(mk("https://api.example.com/c"), long); err == nil {
		t.Error("expected redirect limit error")
	}
}
