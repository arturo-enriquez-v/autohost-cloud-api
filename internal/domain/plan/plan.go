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
	PlanStarter  = "starter"
	PlanIndie    = "indie"
	PlanBusiness = "business"

	StatusActive    = "active"
	StatusCancelled = "cancelled"
	StatusPastDue   = "past_due"
)

// Plan represents a subscription tier with its feature limits.
type Plan struct {
	ID                 string    `db:"id"                  json:"id"`
	Name               string    `db:"name"                json:"name"`
	Description        *string   `db:"description"         json:"description"`
	PriceCents         int       `db:"price_cents"         json:"price_cents"`
	MaxNodes           int       `db:"max_nodes"           json:"max_nodes"`
	MaxProjects        int       `db:"max_projects"        json:"max_projects"`
	MaxCustomCommands  int       `db:"max_custom_commands" json:"max_custom_commands"`
	IsActive           bool      `db:"is_active"           json:"is_active"`
	CreatedAt          time.Time `db:"created_at"          json:"created_at"`
	UpdatedAt          time.Time `db:"updated_at"          json:"updated_at"`
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

// Repository defines persistence operations for plans and subscriptions.
type Repository interface {
	FindAllActive() ([]*Plan, error)
	FindByID(id string) (*Plan, error)
	FindSubscriptionByUserID(userID string) (*Subscription, error)
	CreateSubscription(userID, planID string) (*Subscription, error)
	UpdateSubscriptionPlan(userID, planID string) (*Subscription, error)
}
