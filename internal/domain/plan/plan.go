package plan

import (
	"errors"
	"time"
)

var (
	ErrPlanNotFound         = errors.New("plan not found")
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrPlanLimitReached     = errors.New("plan limit reached")
)

const (
	PlanFree       = "free"
	PlanStarter    = "starter"
	PlanPayAsYouGo = "starter"
	PlanPro        = "pro"

	PlanIndie    = "indie"
	PlanBusiness = "business"

	StatusActive    = "active"
	StatusCancelled = "cancelled"
	StatusPastDue   = "past_due"
)

// Plan represents a subscription tier with its feature limits.
type Plan struct {
	ID                       string    `db:"id"                            json:"id"`
	Name                     string    `db:"name"                          json:"name"`
	Description              *string   `db:"description"                   json:"description"`
	PriceCents               int       `db:"price_cents"                   json:"price_cents"`
	MaxNodes                 int       `db:"max_nodes"                     json:"max_nodes"`
	MaxProjects              int       `db:"max_projects"                  json:"max_projects"`
	MaxCustomCommands        int       `db:"max_custom_commands"           json:"max_custom_commands"`
	RetentionDays            int       `db:"retention_days"                json:"retention_days"`
	PollingIntervalSec       int       `db:"polling_interval_sec"          json:"polling_interval_sec"`
	MaxBackupStorageBytes    int64     `db:"max_backup_storage_bytes"      json:"max_backup_storage_bytes"`
	MaxBackupRetentionDays   int       `db:"max_backup_retention_days"     json:"max_backup_retention_days"`
	MaxAIDiagnosticsPerMonth int       `db:"max_ai_diagnostics_per_month"  json:"max_ai_diagnostics_per_month"`
	IsActive                 bool      `db:"is_active"                     json:"is_active"`
	CreatedAt                time.Time `db:"created_at"                    json:"created_at"`
	UpdatedAt                time.Time `db:"updated_at"                    json:"updated_at"`
}

// Subscription links a user to their current plan.
type Subscription struct {
	ID                   string     `db:"id"                     json:"id"`
	UserID               string     `db:"user_id"                json:"user_id"`
	PlanID               string     `db:"plan_id"                json:"plan_id"`
	Status               string     `db:"status"                 json:"status"`
	PeriodStart          time.Time  `db:"period_start"           json:"period_start"`
	PeriodEnd            *time.Time `db:"period_end"             json:"period_end"`
	StripeSubscriptionID *string    `db:"stripe_subscription_id" json:"stripe_subscription_id,omitempty"`
	StripeCustomerID     *string    `db:"stripe_customer_id"     json:"stripe_customer_id,omitempty"`
	CreatedAt            time.Time  `db:"created_at"             json:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at"             json:"updated_at"`
}

// UserPlan bundles a user's subscription with the resolved plan details.
type UserPlan struct {
	Subscription *Subscription `json:"subscription"`
	Plan         *Plan         `json:"plan"`
}

// PlanUsage captures current resource utilization compared to the plan limits.
type PlanUsage struct {
	Plan                     *Plan `json:"plan"`
	NodesCount               int   `json:"nodes_count"`
	NodesLimit               int   `json:"nodes_limit"` // -1 = unlimited
	ProjectsCount            int   `json:"projects_count"`
	ProjectsLimit            int   `json:"projects_limit"`
	CustomCommandsCount      int   `json:"custom_commands_count"`
	CustomCommandsLimit      int   `json:"custom_commands_limit"`
	BackupStorageBytes       int64 `json:"backup_storage_bytes"`
	BackupStorageLimitBytes  int64 `json:"backup_storage_limit_bytes"`
	BackupRetentionDaysLimit int   `json:"backup_retention_days_limit"`
	AIDiagnosticsThisMonth   int   `json:"ai_diagnostics_this_month"`
	AIDiagnosticsMonthLimit  int   `json:"ai_diagnostics_month_limit"` // -1 = unlimited
}

// Repository defines persistence operations for plans and subscriptions.
type Repository interface {
	FindAllActive() ([]*Plan, error)
	FindByID(id string) (*Plan, error)
	FindSubscriptionByUserID(userID string) (*Subscription, error)
	CreateSubscription(userID, planID string) (*Subscription, error)
	UpdateSubscriptionPlan(userID, planID string) (*Subscription, error)
	GetUserUsageCounts(userID string) (nodesCount, projectsCount, commandsCount int, backupStorageBytes int64, aiDiagnosticsThisMonth int, err error)
}
