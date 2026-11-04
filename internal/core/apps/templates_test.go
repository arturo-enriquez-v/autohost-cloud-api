// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"testing"
)

func TestGetAppTemplate(t *testing.T) {
	for _, app := range Catalog {
		tmpl, err := GetAppTemplate(app.Name)
		if err != nil {
			t.Fatalf("expected template for %q, got error: %v", app.Name, err)
		}
		if len(tmpl) == 0 {
			t.Fatalf("expected non-empty template for %q", app.Name)
		}
	}

	// Non-existent app should fail
	_, err := GetAppTemplate("non-existent-app-12345")
	if err == nil {
		t.Fatal("expected error for non-existent app template, got nil")
	}
}

