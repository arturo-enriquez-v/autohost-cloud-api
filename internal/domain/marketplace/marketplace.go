package marketplace

import (
	"errors"
	"time"
)

// AppStatus represents the lifecycle of a deployed marketplace app.
type AppStatus string

const (
	StatusInstalling AppStatus = "installing"
	StatusRunning    AppStatus = "running"
	StatusFailed     AppStatus = "failed"
)

var ErrAppNotFound = errors.New("marketplace app not found")

// AppEnvVar describes a single environment variable required by an app.
type AppEnvVar struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Default     string `json:"default,omitempty"`
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

// Repository defines the persistence contract for marketplace apps.
type Repository interface {
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
