package plan

// Service handles business logic for plans and subscriptions.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// ListPlans returns all active plans ordered by price.
func (s *Service) ListPlans() ([]*Plan, error) {
	return s.repo.FindAllActive()
}

// GetUserPlan returns the current plan and subscription for a user.
func (s *Service) GetUserPlan(userID string) (*UserPlan, error) {
	sub, err := s.repo.FindSubscriptionByUserID(userID)
	if err != nil {
		return nil, err
	}
	p, err := s.repo.FindByID(sub.PlanID)
	if err != nil {
		return nil, err
	}
	return &UserPlan{Subscription: sub, Plan: p}, nil
}

// AssignStarterPlan assigns the default starter plan to a newly created user.
func (s *Service) AssignStarterPlan(userID string) error {
	_, err := s.repo.CreateSubscription(userID, PlanStarter)
	return err
}

// ChangePlan updates the user's active plan.
func (s *Service) ChangePlan(userID, planID string) (*UserPlan, error) {
	p, err := s.repo.FindByID(planID)
	if err != nil {
		return nil, err
	}
	sub, err := s.repo.UpdateSubscriptionPlan(userID, planID)
	if err != nil {
		return nil, err
	}
	return &UserPlan{Subscription: sub, Plan: p}, nil
}

// CheckNodeLimit returns ErrPlanLimitReached if the user cannot add more nodes.
// currentCount is the number of nodes the user already owns.
func (s *Service) CheckNodeLimit(userID string, currentCount int) error {
	return s.checkLimit(userID, currentCount, func(p *Plan) int { return p.MaxNodes })
}

// CheckProjectLimit returns ErrPlanLimitReached if the user cannot create more projects.
func (s *Service) CheckProjectLimit(userID string, currentCount int) error {
	return s.checkLimit(userID, currentCount, func(p *Plan) int { return p.MaxProjects })
}

// CheckCustomCommandLimit returns ErrPlanLimitReached if the user cannot add more custom commands.
func (s *Service) CheckCustomCommandLimit(userID string, currentCount int) error {
	return s.checkLimit(userID, currentCount, func(p *Plan) int { return p.MaxCustomCommands })
}

func (s *Service) checkLimit(userID string, current int, limit func(*Plan) int) error {
	up, err := s.GetUserPlan(userID)
	if err != nil {
		return err
	}
	max := limit(up.Plan)
	if max == -1 {
		return nil // unlimited
	}
	if current >= max {
		return ErrPlanLimitReached
	}
	return nil
}
