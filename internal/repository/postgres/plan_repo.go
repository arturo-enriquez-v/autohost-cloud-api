package postgres

import (
	"database/sql"
	"fmt"

	"github.com/arturo/autohost-cloud-api/internal/domain/plan"
	"github.com/jmoiron/sqlx"
)

// PlanRepository implements plan.Repository using PostgreSQL.
type PlanRepository struct {
	db *sqlx.DB
}

func NewPlanRepository(db *sqlx.DB) *PlanRepository {
	return &PlanRepository{db: db}
}

func (r *PlanRepository) FindAllActive() ([]*plan.Plan, error) {
	var plans []*plan.Plan
	err := r.db.Select(&plans, `
		SELECT id, name, description, price_cents, max_nodes, max_projects, max_custom_commands,
		       is_active, created_at, updated_at
		FROM plans
		WHERE is_active = true
		ORDER BY price_cents ASC`)
	if err != nil {
		return nil, fmt.Errorf("plan_repo.FindAllActive: %w", err)
	}
	return plans, nil
}

func (r *PlanRepository) FindByID(id string) (*plan.Plan, error) {
	var p plan.Plan
	err := r.db.Get(&p, `
		SELECT id, name, description, price_cents, max_nodes, max_projects, max_custom_commands,
		       is_active, created_at, updated_at
		FROM plans
		WHERE id = $1`, id)
	if err == sql.ErrNoRows {
		return nil, plan.ErrPlanNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("plan_repo.FindByID: %w", err)
	}
	return &p, nil
}

func (r *PlanRepository) FindSubscriptionByUserID(userID string) (*plan.Subscription, error) {
	var sub plan.Subscription
	err := r.db.Get(&sub, `
		SELECT id, user_id, plan_id, status, period_start, period_end,
		       stripe_subscription_id, stripe_customer_id, created_at, updated_at
		FROM user_subscriptions
		WHERE user_id = $1`, userID)
	if err == sql.ErrNoRows {
		return nil, plan.ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("plan_repo.FindSubscriptionByUserID: %w", err)
	}
	return &sub, nil
}

func (r *PlanRepository) CreateSubscription(userID, planID string) (*plan.Subscription, error) {
	var sub plan.Subscription
	err := r.db.Get(&sub, `
		INSERT INTO user_subscriptions (user_id, plan_id)
		VALUES ($1, $2)
		RETURNING id, user_id, plan_id, status, period_start, period_end,
		          stripe_subscription_id, stripe_customer_id, created_at, updated_at`,
		userID, planID)
	if err != nil {
		return nil, fmt.Errorf("plan_repo.CreateSubscription: %w", err)
	}
	return &sub, nil
}

func (r *PlanRepository) UpdateSubscriptionPlan(userID, planID string) (*plan.Subscription, error) {
	var sub plan.Subscription
	err := r.db.Get(&sub, `
		UPDATE user_subscriptions
		SET plan_id = $2, period_start = now(), updated_at = now()
		WHERE user_id = $1
		RETURNING id, user_id, plan_id, status, period_start, period_end,
		          stripe_subscription_id, stripe_customer_id, created_at, updated_at`,
		userID, planID)
	if err == sql.ErrNoRows {
		return nil, plan.ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("plan_repo.UpdateSubscriptionPlan: %w", err)
	}
	return &sub, nil
}
