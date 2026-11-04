// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed templates/*.yml
var embeddedTemplates embed.FS

// GetAppTemplate returns the compose template for a catalog app.
// Priority:
// 1. Local custom directory if specified by AUTOHOST_TEMPLATES_DIR or /var/lib/autohost/templates/{name}.yml
// 2. Local ./templates/{name}.yml relative to working directory
// 3. Embedded template bundled in binary
func GetAppTemplate(appName string) (string, error) {
	// 1. Check custom or standard system directory
	customDir := os.Getenv("AUTOHOST_TEMPLATES_DIR")
	if customDir == "" {
		customDir = "/var/lib/autohost/templates"
	}
	localPath := filepath.Join(customDir, appName+".yml")
	if content, err := os.ReadFile(localPath); err == nil {
		return string(content), nil
	}

	// 2. Check ./templates relative to current working directory
	if content, err := os.ReadFile(filepath.Join("templates", appName+".yml")); err == nil {
		return string(content), nil
	}

	// 3. Fallback to embedded template
	content, err := embeddedTemplates.ReadFile("templates/" + appName + ".yml")
	if err != nil {
		return "", fmt.Errorf("template for app %q not found: %w", appName, err)
	}
	return string(content), nil
}

