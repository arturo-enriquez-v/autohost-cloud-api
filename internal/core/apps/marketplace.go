// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package apps

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AppStatus represents the lifecycle of a deployed marketplace app.
type AppStatus string

const (
	StatusInstalling AppStatus = "installing"
	StatusRunning    AppStatus = "running"
	StatusFailed     AppStatus = "failed"
)

var (
	ErrAppNotFound = errors.New("marketplace app not found")
	// ErrMissingAppSetting is returned when a required catalog variable has no value.
	ErrMissingAppSetting = errors.New("missing required app setting")
	// ErrInvalidAppSetting is returned when a value could break the compose template.
	ErrInvalidAppSetting = errors.New("invalid app setting")
)

// GenerateHex32 marks a variable the API fills with 32 random bytes in hex (64 chars) when empty.
const GenerateHex32 = "hex32"

// maxAppSettingLen bounds a single variable value.
const maxAppSettingLen = 256

// forbiddenAppSettingChars would break the quoted YAML scalars in the templates or trigger
// Docker Compose interpolation.
const forbiddenAppSettingChars = "\"$`\\\r\n"

// AppEnvVar describes a single environment variable required by an app.
type AppEnvVar struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Default     string `json:"default,omitempty"`
	// Generate, when set (GenerateHex32), makes the API generate the value if none is given.
	Generate string `json:"generate,omitempty"`
}

// AppCatalogEntry is the static definition of an available app.
type AppCatalogEntry struct {
	Name        string      `json:"name"`
	DisplayName string      `json:"display_name"`
	Description string      `json:"description"`
	Category    string      `json:"category"`
	Icon        string      `json:"icon,omitempty"`
	EnvVars     []AppEnvVar `json:"env_vars"`
}

// MarketplaceApp is a deployed app instance tracked in the database.
type MarketplaceApp struct {
	ID        string    `db:"id"       json:"id"`
	NodeID    string    `db:"node_id"  json:"node_id"`
	AppName   string    `db:"app_name" json:"app_name"`
	JobID     *string   `db:"job_id"   json:"job_id,omitempty"`
	Status    AppStatus `db:"status"   json:"status"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// MarketplaceRepository defines the persistence contract for marketplace apps.
type MarketplaceRepository interface {
	Create(app *MarketplaceApp) (*MarketplaceApp, error)
	FindByNodeID(nodeID string) ([]*MarketplaceApp, error)
	FindByJobID(jobID string) (*MarketplaceApp, error)
	UpdateStatus(id string, status AppStatus) error
	UpdateJobID(id, jobID string) error
}

// Catalog holds the static definition of all installable apps.
var Catalog = []AppCatalogEntry{
	{
		Name:        "joplin",
		DisplayName: "Joplin Server",
		Description: "Open-source note-taking and to-do application with end-to-end encryption.",
		Category:    "productivity",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "joplin"},
			{Key: "port", Label: "Joplin Port", Description: "External port to expose Joplin", Required: true, Default: "22300"},
			{Key: "postgres-database", Label: "Postgres DB Name", Required: true, Default: "joplin"},
			{Key: "postgres-user", Label: "Postgres User", Required: true, Default: "joplin"},
			{Key: "postgres-password", Label: "Postgres Password", Required: true, Secret: true},
			{Key: "postgres-port", Label: "Postgres Port", Required: true, Default: "5433"},
		},
	},
	{
		Name:        "mysql",
		DisplayName: "MySQL",
		Description: "Widely used open-source relational database management system.",
		Category:    "database",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "mysql"},
			{Key: "port", Label: "MySQL Port", Description: "External port to expose MySQL", Required: true, Default: "3306"},
			{Key: "mysql-root-password", Label: "Root Password", Required: true, Secret: true},
			{Key: "mysql-database", Label: "Database Name", Required: true, Default: "app"},
			{Key: "mysql-user", Label: "Database User", Required: true, Default: "appuser"},
			{Key: "mysql-password", Label: "Database Password", Required: true, Secret: true},
		},
	},
	{
		Name:        "navidrome",
		DisplayName: "Navidrome",
		Description: "Self-hosted music streaming server compatible with Subsonic/Airsonic clients.",
		Category:    "media",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "navidrome"},
			{Key: "port", Label: "Navidrome Port", Description: "External port to expose Navidrome", Required: true, Default: "4533"},
		},
	},
	{
		Name:        "nextcloud",
		DisplayName: "Nextcloud",
		Description: "Self-hosted productivity platform with file sync, calendar, contacts and more.",
		Category:    "storage",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "nextcloud"},
			{Key: "port", Label: "Nextcloud Port", Description: "External port to expose Nextcloud", Required: true, Default: "8080"},
			{Key: "mysql-database", Label: "MySQL DB Name", Required: true, Default: "nextcloud"},
			{Key: "mysql-user", Label: "MySQL User", Required: true, Default: "nextcloud"},
			{Key: "mysql-password", Label: "MySQL Password", Required: true, Secret: true},
			{Key: "mysql-root-password", Label: "MySQL Root Password", Required: true, Secret: true},
			{Key: "mysql-port", Label: "MySQL Internal Port", Required: true, Default: "3307"},
		},
	},
	{
		Name:        "vaultwarden",
		DisplayName: "Vaultwarden",
		Description: "Lightweight password manager compatible with the Bitwarden apps and browser extensions. The web vault needs HTTPS (for example through a Caddy route).",
		Category:    "security",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "vaultwarden"},
			{Key: "port", Label: "Web Port", Description: "External port to expose Vaultwarden", Required: true, Default: "8222"},
			{Key: "signups-allowed", Label: "Allow Sign-ups", Description: "true lets anyone create an account; set it to false once your users are registered", Required: true, Default: "true"},
		},
	},
	{
		Name:        "uptime-kuma",
		DisplayName: "Uptime Kuma",
		Description: "Monitors websites, ports and services, with status pages and notifications.",
		Category:    "monitoring",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "uptime-kuma"},
			{Key: "port", Label: "Web Port", Description: "External port to expose Uptime Kuma", Required: true, Default: "3001"},
		},
	},
	{
		Name:        "stirling-pdf",
		DisplayName: "Stirling-PDF",
		Description: "Web toolbox to merge, split, convert, compress, sign and OCR PDF files locally.",
		Category:    "productivity",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "stirling-pdf"},
			{Key: "port", Label: "Web Port", Description: "External port to expose Stirling-PDF", Required: true, Default: "8090"},
		},
	},
	{
		Name:        "gitea",
		DisplayName: "Gitea",
		Description: "Self-hosted Git service with pull requests, issues, packages and CI (Actions), backed by PostgreSQL.",
		Category:    "development",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "gitea"},
			{Key: "port", Label: "Web Port", Description: "External port to expose Gitea", Required: true, Default: "3000"},
			{Key: "ssh-port", Label: "SSH Port", Description: "External port for git over SSH", Required: true, Default: "2222"},
			{Key: "db-password", Label: "Database Password", Description: "Internal PostgreSQL password; leave empty to generate one", Secret: true, Generate: GenerateHex32},
		},
	},
	{
		Name:        "adguard-home",
		DisplayName: "AdGuard Home",
		Description: "Network-wide DNS server that blocks ads and trackers. Port 53 is often taken by systemd-resolved; free it or pick another DNS port.",
		Category:    "network",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "adguard-home"},
			{Key: "dns-port", Label: "DNS Port", Description: "External DNS port (TCP and UDP)", Required: true, Default: "53"},
			{Key: "setup-port", Label: "Setup Wizard Port", Description: "External port for the first-run wizard", Required: true, Default: "3003"},
			{Key: "port", Label: "Web Port", Description: "External port for the admin panel; keep the admin port at 80 in the setup wizard", Required: true, Default: "8053"},
		},
	},
	{
		Name:        "homarr",
		DisplayName: "Homarr",
		Description: "Interactive home dashboard: drag-and-drop boards with app links, widgets, calendars and integrations, edited from the browser.",
		Category:    "dashboard",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "homarr"},
			{Key: "port", Label: "Web Port", Description: "External port to expose Homarr", Required: true, Default: "7575"},
			{Key: "encryption-key", Label: "Encryption Key", Description: "64 hex characters; leave empty to generate one", Secret: true, Generate: GenerateHex32},
		},
	},
	{
		Name:        "postgres",
		DisplayName: "PostgreSQL",
		Description: "Advanced open-source relational database.",
		Category:    "database",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "postgres"},
			{Key: "port", Label: "PostgreSQL Port", Description: "External port to expose PostgreSQL", Required: true, Default: "5432"},
			{Key: "postgres-database", Label: "Database Name", Required: true, Default: "app"},
			{Key: "postgres-user", Label: "Database User", Required: true, Default: "app"},
			{Key: "postgres-password", Label: "Database Password", Required: true, Secret: true},
		},
	},
	{
		Name:        "redis",
		DisplayName: "Redis",
		Description: "In-memory key-value store for caching, queues and pub/sub, with persistence enabled.",
		Category:    "database",
		EnvVars: []AppEnvVar{
			{Key: "service-name", Label: "Service Name", Required: true, Default: "redis"},
			{Key: "port", Label: "Redis Port", Description: "External port to expose Redis", Required: true, Default: "6379"},
			{Key: "redis-password", Label: "Password", Required: true, Secret: true},
		},
	},
}

// GetCatalogEntry returns the catalog entry for the given app name.
func GetCatalogEntry(name string) (AppCatalogEntry, bool) {
	for _, e := range Catalog {
		if e.Name == name {
			return e, true
		}
	}
	return AppCatalogEntry{}, false
}

// ResolveEnv builds the variables for a catalog install from the user's values: it keeps only
// the app's own keys, applies defaults, generates the values marked with Generate, and rejects
// missing required values and values that would break the template. Every key is always present,
// so no `$key` placeholder is left in the compose file.
func (e AppCatalogEntry) ResolveEnv(in map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(e.EnvVars))
	for _, v := range e.EnvVars {
		val := strings.TrimSpace(in[v.Key])
		if val == "" {
			val = v.Default
		}
		if val == "" && v.Generate != "" {
			generated, err := generateAppSetting(v.Generate)
			if err != nil {
				return nil, fmt.Errorf("generate %s: %w", v.Key, err)
			}
			val = generated
		}
		if val == "" && v.Required {
			return nil, fmt.Errorf("%w: %s", ErrMissingAppSetting, v.Key)
		}
		if len(val) > maxAppSettingLen {
			return nil, fmt.Errorf("%w: %s is longer than %d characters", ErrInvalidAppSetting, v.Key, maxAppSettingLen)
		}
		if strings.ContainsAny(val, forbiddenAppSettingChars) {
			return nil, fmt.Errorf("%w: %s cannot contain quotes, $, backticks, backslashes or line breaks", ErrInvalidAppSetting, v.Key)
		}
		out[v.Key] = val
	}
	return out, nil
}

func generateAppSetting(kind string) (string, error) {
	switch kind {
	case GenerateHex32:
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return hex.EncodeToString(b), nil
	default:
		return "", fmt.Errorf("unknown generator %q", kind)
	}
}
