// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package app

import (
	"strings"
	"testing"
	"time"
)

func TestWithStatementTimeout(t *testing.T) {
	cases := []struct{ in, want string }{
		{"postgres://u:p@db:5432/autohost?sslmode=disable", "statement_timeout=30000"},
		{"postgres://u:p@db/autohost?statement_timeout=5000", "statement_timeout=5000"},
		{"host=db dbname=autohost sslmode=disable", "statement_timeout=30000"},
	}
	for _, tc := range cases {
		got, err := withStatementTimeout(tc.in, 30*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if !strings.Contains(got, tc.want) || strings.Count(got, "statement_timeout") != 1 {
			t.Errorf("withStatementTimeout(%q) = %q, want it to contain %q once", tc.in, got, tc.want)
		}
		if strings.Contains(tc.in, "sslmode=disable") && !strings.Contains(got, "sslmode=disable") {
			t.Errorf("existing parameters must be kept: %q", got)
		}
	}
	if got, _ := withStatementTimeout("postgres://db/x", 0); got != "postgres://db/x" {
		t.Errorf("zero timeout must leave the URL unchanged, got %q", got)
	}
}
