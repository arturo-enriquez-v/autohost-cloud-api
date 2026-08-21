package plan

import (
	"errors"
	"fmt"
)

var (
	ErrNodeLimitReached          = errors.New("node limit reached for your active plan")
	ErrProjectLimitReached       = errors.New("project limit reached for your active plan")
	ErrCustomCommandLimitReached = errors.New("custom command limit reached for your active plan")
	ErrBackupStorageLimitReached = errors.New("cloud backup storage limit reached for your active plan")
	ErrBackupRetentionExceeded   = errors.New("requested backup retention exceeds your active plan limit")
	ErrAIDiagnosticsLimitReached = errors.New("monthly AI diagnostics limit reached for your active plan")
)

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
		if errors.Is(err, ErrSubscriptionNotFound) {
			sub, err = s.repo.CreateSubscription(userID, PlanFree)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	p, err := s.repo.FindByID(sub.PlanID)
	if err != nil {
		return nil, err
	}
	return &UserPlan{Subscription: sub, Plan: p}, nil
}

// AssignStarterPlan assigns the default free plan to a newly created user.
func (s *Service) AssignStarterPlan(userID string) error {
	_, err := s.repo.CreateSubscription(userID, PlanFree)
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

// GetPlanUsage calculates all current utilization figures compared to user plan limits.
func (s *Service) GetPlanUsage(userID string) (*PlanUsage, error) {
	up, err := s.GetUserPlan(userID)
	if err != nil {
		return nil, err
	}

	nodes, projects, commands, backupBytes, aiDiagCount, err := s.repo.GetUserUsageCounts(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get usage counts: %w", err)
	}

	p := up.Plan
	return &PlanUsage{
		Plan:                     p,
		NodesCount:               nodes,
		NodesLimit:               p.MaxNodes,
		ProjectsCount:            projects,
		ProjectsLimit:            p.MaxProjects,
		CustomCommandsCount:      commands,
		CustomCommandsLimit:      p.MaxCustomCommands,
		BackupStorageBytes:       backupBytes,
		BackupStorageLimitBytes:  p.MaxBackupStorageBytes,
		BackupRetentionDaysLimit: p.MaxBackupRetentionDays,
		AIDiagnosticsThisMonth:   aiDiagCount,
		AIDiagnosticsMonthLimit:  p.MaxAIDiagnosticsPerMonth,
	}, nil
}

// CheckNodeLimit returns ErrNodeLimitReached if the user cannot add more nodes.
func (s *Service) CheckNodeLimit(userID string, currentCount int) error {
	up, err := s.GetUserPlan(userID)
	if err != nil {
		return err
	}
	if up.Plan.MaxNodes != -1 && currentCount >= up.Plan.MaxNodes {
		return fmt.Errorf("%w: %s allows up to %d node(s)", ErrNodeLimitReached, up.Plan.Name, up.Plan.MaxNodes)
	}
	return nil
}

// CheckProjectLimit returns ErrProjectLimitReached if the user cannot create more projects.
func (s *Service) CheckProjectLimit(userID string, currentCount int) error {
	up, err := s.GetUserPlan(userID)
	if err != nil {
		return err
	}
	if up.Plan.MaxProjects != -1 && currentCount >= up.Plan.MaxProjects {
		return fmt.Errorf("%w: %s allows up to %d project(s)", ErrProjectLimitReached, up.Plan.Name, up.Plan.MaxProjects)
	}
	return nil
}

// CheckCustomCommandLimit returns ErrCustomCommandLimitReached if the user cannot add more custom commands.
func (s *Service) CheckCustomCommandLimit(userID string, currentCount int) error {
	up, err := s.GetUserPlan(userID)
	if err != nil {
		return err
	}
	if up.Plan.MaxCustomCommands != -1 && currentCount >= up.Plan.MaxCustomCommands {
		return fmt.Errorf("%w: %s allows up to %d custom command(s)", ErrCustomCommandLimitReached, up.Plan.Name, up.Plan.MaxCustomCommands)
	}
	return nil
}

// CheckBackupRetentionLimit checks if requested retention days is within plan allowance.
func (s *Service) CheckBackupRetentionLimit(userID string, requestedDays int) error {
	up, err := s.GetUserPlan(userID)
	if err != nil {
		return err
	}
	if up.Plan.MaxBackupRetentionDays != -1 && requestedDays > up.Plan.MaxBackupRetentionDays {
		return fmt.Errorf("%w: %s allows max %d days retention (requested %d days)",
			ErrBackupRetentionExceeded, up.Plan.Name, up.Plan.MaxBackupRetentionDays, requestedDays)
	}
	return nil
}

// CheckBackupStorageLimit checks if additional backup bytes would exceed storage quota.
func (s *Service) CheckBackupStorageLimit(userID string, currentBytes, incomingBytes int64) error {
	up, err := s.GetUserPlan(userID)
	if err != nil {
		return err
	}
	if up.Plan.MaxBackupStorageBytes != -1 && (currentBytes+incomingBytes) > up.Plan.MaxBackupStorageBytes {
		return fmt.Errorf("%w: %s limit is %d MB (current: %d MB, new: %d MB)",
			ErrBackupStorageLimitReached, up.Plan.Name,
			up.Plan.MaxBackupStorageBytes/(1024*1024),
			currentBytes/(1024*1024),
			incomingBytes/(1024*1024))
	}
	return nil
}

// CheckAIDiagnosticsLimit checks if monthly diagnostic count has reached the plan limit.
func (s *Service) CheckAIDiagnosticsLimit(userID string, currentCount int) error {
	up, err := s.GetUserPlan(userID)
	if err != nil {
		return err
	}
	if up.Plan.MaxAIDiagnosticsPerMonth != -1 && currentCount >= up.Plan.MaxAIDiagnosticsPerMonth {
		return fmt.Errorf("%w: %s allows %d diagnostic(s) per month (used: %d). Upgrade to Starter or Pro for more",
			ErrAIDiagnosticsLimitReached, up.Plan.Name, up.Plan.MaxAIDiagnosticsPerMonth, currentCount)
	}
	return nil
}
