// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package platform

import "testing"

func TestAgentAPIURL(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		ips      []string
		fallback string
		want     string
	}{
		{name: "explicit public URL wins", env: map[string]string{"API_PUBLIC_URL": "https://api.example.com/"}, ips: []string{"100.64.0.1"}, want: "https://api.example.com"},
		{name: "VPN IP with default port", ips: []string{"100.64.0.1", "192.168.1.10"}, want: "http://100.64.0.1:8080"},
		{name: "API_PORT is the published host port", env: map[string]string{"API_PORT": "18080", "PORT": "8080"}, ips: []string{"192.168.1.10"}, want: "http://192.168.1.10:18080"},
		{name: "PORT when API_PORT is unset", env: map[string]string{"PORT": "9000"}, ips: []string{"192.168.1.10"}, want: "http://192.168.1.10:9000"},
		{name: "inet prefix length is stripped", ips: []string{"100.64.0.1/32"}, want: "http://100.64.0.1:8080"},
		{name: "empty and invalid IPs are skipped", ips: []string{"", "not-an-ip", "192.168.1.10"}, want: "http://192.168.1.10:8080"},
		{name: "IPv6 is bracketed", ips: []string{"fd7a:115c:a1e0::1"}, want: "http://[fd7a:115c:a1e0::1]:8080"},
		{name: "NEXT_PUBLIC_API_URL without a local node", env: map[string]string{"NEXT_PUBLIC_API_URL": "http://pi.lan:8080/"}, want: "http://pi.lan:8080"},
		{name: "fallback last", fallback: "http://req-host", want: "http://req-host"},
		{name: "nothing known", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"API_PUBLIC_URL", "API_PORT", "PORT", "NEXT_PUBLIC_API_URL"} {
				t.Setenv(k, tt.env[k])
			}
			if got := AgentAPIURL(tt.ips, tt.fallback); got != tt.want {
				t.Errorf("AgentAPIURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
