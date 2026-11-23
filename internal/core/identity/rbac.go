// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/platform"
)

var (
	ErrUserNotFound          = errors.New("user not found")
	ErrUserDeactivated       = errors.New("user account is deactivated")
	ErrForbidden             = errors.New("forbidden: insufficient permissions")
	ErrCannotDeleteSelf      = errors.New("cannot delete your own account")
	ErrCannotDemoteLastAdmin = errors.New("cannot remove or deactivate the last active administrator")
	ErrInvalidRole           = errors.New("invalid role: must be 'admin' or 'member'")
	ErrCredentialsRequired   = errors.New("email and password are required")
	ErrNodeIDRequired        = errors.New("node_id is required")
	ErrNodeNotFound          = errors.New("node not found")
	ErrNodeNotInProject      = errors.New("node does not belong to this project")
)

// ProjectMembership represents a user's assignment to a project.
type ProjectMembership struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"project_id"`
	ProjectName        string    `json:"project_name,omitempty"`
	UserID             string    `json:"user_id"`
	DefaultRole        string    `json:"default_role"`
	AllNodes           bool      `json:"all_nodes"`
	DefaultPermissions []string  `json:"default_permissions"`
	AddedBy            *string   `json:"added_by,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// NodePermissionGrant represents a user's granular permission configuration on a specific node.
type NodePermissionGrant struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	ProjectID    *string   `json:"project_id,omitempty"`
	ProjectName  string    `json:"project_name,omitempty"`
	NodeID       string    `json:"node_id"`
	NodeHostname string    `json:"node_hostname,omitempty"`
	RolePreset   string    `json:"role_preset"`
	Permissions  []string  `json:"permissions"`
	GrantedBy    *string   `json:"granted_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// UserWithPermissions aggregates a user profile with their global role, project assignments, and per-node permissions.
type UserWithPermissions struct {
	ID              string                 `json:"id"`
	Email           string                 `json:"email"`
	Name            string                 `json:"name"`
	AvatarURL       string                 `json:"avatar_url"`
	Role            string                 `json:"role"`
	IsAdmin         bool                   `json:"is_admin"`
	IsActive        bool                   `json:"is_active"`
	CreatedBy       *string                `json:"created_by,omitempty"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
	Projects        []*ProjectMembership   `json:"projects"`
	NodePermissions []*NodePermissionGrant `json:"node_permissions"`
}

// ProjectMembershipInput is the payload for assigning a user to a project.
type ProjectMembershipInput struct {
	ProjectID          string   `json:"project_id"`
	DefaultRole        string   `json:"default_role"`
	AllNodes           bool     `json:"all_nodes"`
	DefaultPermissions []string `json:"default_permissions,omitempty"`
}

// NodePermissionGrantInput is the payload for granting a user granular permissions on a node.
type NodePermissionGrantInput struct {
	NodeID      string   `json:"node_id"`
	ProjectID   *string  `json:"project_id,omitempty"`
	RolePreset  string   `json:"role_preset"`
	Permissions []string `json:"permissions,omitempty"`
}

// CreateUserInput is the payload for creating a new user by an administrator.
type CreateUserInput struct {
	Email           string                     `json:"email"`
	Name            string                     `json:"name"`
	Password        string                     `json:"password"`
	Role            string                     `json:"role"`
	IsActive        *bool                      `json:"is_active,omitempty"`
	Projects        []ProjectMembershipInput   `json:"projects,omitempty"`
	NodePermissions []NodePermissionGrantInput `json:"node_permissions,omitempty"`
}

// UpdateUserInput is the payload for updating an existing user and optionally replacing their permissions.
type UpdateUserInput struct {
	Email           *string                     `json:"email,omitempty"`
	Name            *string                     `json:"name,omitempty"`
	Password        *string                     `json:"password,omitempty"`
	Role            *string                     `json:"role,omitempty"`
	IsActive        *bool                       `json:"is_active,omitempty"`
	Projects        *[]ProjectMembershipInput   `json:"projects,omitempty"`
	NodePermissions *[]NodePermissionGrantInput `json:"node_permissions,omitempty"`
}

// ProjectMemberDetail describes a user assigned to a project and their node permissions within that project.
type ProjectMemberDetail struct {
	UserID             string                 `json:"user_id"`
	Email              string                 `json:"email"`
	Name               string                 `json:"name"`
	AvatarURL          string                 `json:"avatar_url"`
	Role               string                 `json:"role"`
	IsActive           bool                   `json:"is_active"`
	ProjectID          string                 `json:"project_id"`
	DefaultRole        string                 `json:"default_role"`
	AllNodes           bool                   `json:"all_nodes"`
	DefaultPermissions []string               `json:"default_permissions"`
	NodePermissions    []*NodePermissionGrant `json:"node_permissions"`
	AddedAt            time.Time              `json:"added_at"`
}

// RBACRepository defines persistence operations for user management, project memberships, and granular node permissions.
type RBACRepository interface {
	// User CRUD
	ListUsers() ([]*User, error)
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetUserByEmail(email string) (*User, error)
	CreateUserWithRole(email, name, passwordHash, role string, isActive bool, createdBy *string) (*User, error)
	UpdateUser(id string, email, name, passwordHash, role *string, isActive *bool) (*User, error)
	DeleteUser(id string) error
	CountActiveAdmins() (int, error)
	RevokeAllUserRefreshTokens(userID string) error

	// Project Memberships
	ListUserProjects(userID string) ([]*ProjectMembership, error)
	GetProjectMembership(ctx context.Context, projectID, userID string) (*ProjectMembership, error)
	UpsertProjectMembership(m *ProjectMembership) (*ProjectMembership, error)
	DeleteProjectMembership(projectID, userID string) error
	ReplaceUserProjects(userID string, memberships []*ProjectMembership) error
	ListProjectMembers(projectID string) ([]*ProjectMemberDetail, error)

	// Node Permissions
	ListUserNodePermissions(ctx context.Context, userID string) ([]*NodePermissionGrant, error)
	GetUserNodePermission(ctx context.Context, userID, nodeID string) (*NodePermissionGrant, error)
	UpsertUserNodePermission(g *NodePermissionGrant) (*NodePermissionGrant, error)
	DeleteUserNodePermission(userID, nodeID string) error
	DeleteUserNodePermissionsByProject(userID, projectID string) error
	ReplaceUserNodePermissions(userID string, grants []*NodePermissionGrant) error

	// Lookup helpers for node/project resolution
	GetNodeProjectAndOwner(ctx context.Context, nodeID string) (projectID *string, ownerID *string, exists bool, err error)
}

// RBACService manages users, roles, project memberships, and granular per-node permission checks.
type RBACService struct {
	repo RBACRepository
}

func NewRBACService(repo RBACRepository) *RBACService {
	return &RBACService{repo: repo}
}

// GetCatalog returns the complete permissions and role presets catalog.
func (s *RBACService) GetCatalog() *PermissionsCatalog {
	return GetPermissionsCatalog()
}

// IsAdmin returns true if the user exists, is active, and has the global "admin" role.
func (s *RBACService) IsAdmin(ctx context.Context, userID string) (bool, error) {
	if userID == "" || s == nil || s.repo == nil {
		return false, nil
	}
	u, err := s.repo.GetUserByID(ctx, userID)
	if err != nil || u == nil {
		return false, err
	}
	if !u.IsActive {
		return false, nil
	}
	return u.Role == RoleAdmin, nil
}

// GetUserWithPermissions returns a user's profile enriched with their project assignments and node permissions.
func (s *RBACService) GetUserWithPermissions(userID string) (*UserWithPermissions, error) {
	u, err := s.repo.GetUserByID(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, ErrUserNotFound
	}
	return s.enrichUser(u)
}

func (s *RBACService) enrichUser(u *User) (*UserWithPermissions, error) {
	projects, err := s.repo.ListUserProjects(u.ID)
	if err != nil {
		return nil, err
	}
	if projects == nil {
		projects = []*ProjectMembership{}
	}

	nodePerms, err := s.repo.ListUserNodePermissions(context.Background(), u.ID)
	if err != nil {
		return nil, err
	}
	if nodePerms == nil {
		nodePerms = []*NodePermissionGrant{}
	}

	name := ""
	if u.Name != nil {
		name = *u.Name
	}
	avatarURL := ""
	if u.AvatarURL != nil {
		avatarURL = *u.AvatarURL
	}
	role := u.Role
	if role == "" {
		role = RoleMember
	}

	return &UserWithPermissions{
		ID:              u.ID,
		Email:           u.Email,
		Name:            name,
		AvatarURL:       avatarURL,
		Role:            role,
		IsAdmin:         role == RoleAdmin,
		IsActive:        u.IsActive,
		CreatedBy:       u.CreatedBy,
		CreatedAt:       u.CreatedAt,
		UpdatedAt:       u.UpdatedAt,
		Projects:        projects,
		NodePermissions: nodePerms,
	}, nil
}

// ListUsers returns all users with their assigned projects and node permissions.
func (s *RBACService) ListUsers() ([]*UserWithPermissions, error) {
	users, err := s.repo.ListUsers()
	if err != nil {
		return nil, err
	}
	out := make([]*UserWithPermissions, 0, len(users))
	for _, u := range users {
		enriched, err := s.enrichUser(u)
		if err != nil {
			return nil, err
		}
		out = append(out, enriched)
	}
	return out, nil
}

// CreateUser creates a new user account and configures their initial project memberships and node permissions.
func (s *RBACService) CreateUser(actorID string, in CreateUserInput) (*UserWithPermissions, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	name := strings.TrimSpace(in.Name)
	if email == "" || in.Password == "" {
		return nil, ErrCredentialsRequired
	}

	role := strings.ToLower(strings.TrimSpace(in.Role))
	if role == "" {
		role = RoleMember
	}
	if !IsValidGlobalRole(role) {
		return nil, ErrInvalidRole
	}

	existing, err := s.repo.GetUserByEmail(email)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrUserAlreadyExists
	}

	hash, err := platform.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	isActive := true
	if in.IsActive != nil {
		isActive = *in.IsActive
	}

	var createdBy *string
	if actorID != "" {
		createdBy = &actorID
	}

	user, err := s.repo.CreateUserWithRole(email, name, hash, role, isActive, createdBy)
	if err != nil {
		return nil, err
	}

	if err := s.applyAssignments(actorID, user.ID, in.Projects, in.NodePermissions); err != nil {
		return nil, err
	}

	return s.GetUserWithPermissions(user.ID)
}

// UpdateUser updates a user's attributes, password, global role, active status, and optionally replaces their permissions.
func (s *RBACService) UpdateUser(actorID, targetUserID string, in UpdateUserInput) (*UserWithPermissions, error) {
	target, err := s.repo.GetUserByID(context.Background(), targetUserID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, ErrUserNotFound
	}

	var newEmail *string
	if in.Email != nil {
		trimmed := strings.ToLower(strings.TrimSpace(*in.Email))
		if trimmed != "" && trimmed != strings.ToLower(target.Email) {
			existing, err := s.repo.GetUserByEmail(trimmed)
			if err != nil {
				return nil, err
			}
			if existing != nil && existing.ID != targetUserID {
				return nil, ErrUserAlreadyExists
			}
			newEmail = &trimmed
		}
	}

	var newName *string
	if in.Name != nil {
		trimmed := strings.TrimSpace(*in.Name)
		newName = &trimmed
	}

	var newPasswordHash *string
	if in.Password != nil && strings.TrimSpace(*in.Password) != "" {
		hash, err := platform.HashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		newPasswordHash = &hash
	}

	var newRole *string
	if in.Role != nil {
		r := strings.ToLower(strings.TrimSpace(*in.Role))
		if !IsValidGlobalRole(r) {
			return nil, ErrInvalidRole
		}
		newRole = &r
	}

	// Guard against removing or deactivating the last active admin
	if target.Role == RoleAdmin && target.IsActive {
		demoting := newRole != nil && *newRole != RoleAdmin
		deactivating := in.IsActive != nil && !*in.IsActive
		if demoting || deactivating {
			adminCount, err := s.repo.CountActiveAdmins()
			if err != nil {
				return nil, err
			}
			if adminCount <= 1 {
				return nil, ErrCannotDemoteLastAdmin
			}
		}
	}

	if _, err := s.repo.UpdateUser(targetUserID, newEmail, newName, newPasswordHash, newRole, in.IsActive); err != nil {
		return nil, err
	}

	// If user was deactivated or password changed, revoke active refresh tokens
	if (in.IsActive != nil && !*in.IsActive) || newPasswordHash != nil {
		_ = s.repo.RevokeAllUserRefreshTokens(targetUserID)
	}

	if in.Projects != nil {
		if err := s.replaceProjects(actorID, targetUserID, *in.Projects); err != nil {
			return nil, err
		}
	}
	if in.NodePermissions != nil {
		if err := s.replaceNodePermissions(actorID, targetUserID, *in.NodePermissions); err != nil {
			return nil, err
		}
	}

	return s.GetUserWithPermissions(targetUserID)
}

// DeleteUser removes a user account, guarding against self-deletion and deleting the last admin.
func (s *RBACService) DeleteUser(actorID, targetUserID string) error {
	if actorID != "" && actorID == targetUserID {
		return ErrCannotDeleteSelf
	}
	target, err := s.repo.GetUserByID(context.Background(), targetUserID)
	if err != nil {
		return err
	}
	if target == nil {
		return ErrUserNotFound
	}

	if target.Role == RoleAdmin && target.IsActive {
		adminCount, err := s.repo.CountActiveAdmins()
		if err != nil {
			return err
		}
		if adminCount <= 1 {
			return ErrCannotDemoteLastAdmin
		}
	}

	_ = s.repo.RevokeAllUserRefreshTokens(targetUserID)
	return s.repo.DeleteUser(targetUserID)
}

// SetUserPermissions replaces a user's project memberships and node permission grants.
func (s *RBACService) SetUserPermissions(actorID, targetUserID string, projects []ProjectMembershipInput, nodePerms []NodePermissionGrantInput) (*UserWithPermissions, error) {
	target, err := s.repo.GetUserByID(context.Background(), targetUserID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, ErrUserNotFound
	}

	if err := s.applyAssignments(actorID, targetUserID, projects, nodePerms); err != nil {
		return nil, err
	}
	return s.GetUserWithPermissions(targetUserID)
}

// UpsertNodePermission sets or updates a user's granular permissions for a single node.
func (s *RBACService) UpsertNodePermission(actorID, targetUserID string, in NodePermissionGrantInput) (*NodePermissionGrant, error) {
	if in.NodeID == "" {
		return nil, ErrNodeIDRequired
	}
	target, err := s.repo.GetUserByID(context.Background(), targetUserID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, ErrUserNotFound
	}

	nodeProjID, _, exists, err := s.repo.GetNodeProjectAndOwner(context.Background(), in.NodeID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNodeNotFound
	}

	projectID := in.ProjectID
	if projectID == nil || *projectID == "" {
		projectID = nodeProjID
	}

	// If the node belongs to a project, ensure the user is also registered as a member of that project
	if projectID != nil && *projectID != "" {
		existingMem, _ := s.repo.GetProjectMembership(context.Background(), *projectID, targetUserID)
		if existingMem == nil {
			var addedBy *string
			if actorID != "" {
				addedBy = &actorID
			}
			_, _ = s.repo.UpsertProjectMembership(&ProjectMembership{
				ProjectID:          *projectID,
				UserID:             targetUserID,
				DefaultRole:        NodePresetViewer,
				AllNodes:           false,
				DefaultPermissions: PresetViewerPermissions,
				AddedBy:            addedBy,
			})
		}
	}

	preset, perms := ResolveNodePermissions(in.RolePreset, in.Permissions)
	var grantedBy *string
	if actorID != "" {
		grantedBy = &actorID
	}

	return s.repo.UpsertUserNodePermission(&NodePermissionGrant{
		UserID:      targetUserID,
		ProjectID:   projectID,
		NodeID:      in.NodeID,
		RolePreset:  preset,
		Permissions: perms,
		GrantedBy:   grantedBy,
	})
}

// RemoveNodePermission revokes a user's permissions on a specific node.
func (s *RBACService) RemoveNodePermission(targetUserID, nodeID string) error {
	return s.repo.DeleteUserNodePermission(targetUserID, nodeID)
}

// AssignProjectMember adds or updates a user in a project and optionally sets node permissions in that project.
func (s *RBACService) AssignProjectMember(actorID, projectID, targetUserID string, defaultRole string, allNodes bool, defaultPerms []string, nodePerms []NodePermissionGrantInput) error {
	target, err := s.repo.GetUserByID(context.Background(), targetUserID)
	if err != nil {
		return err
	}
	if target == nil {
		return ErrUserNotFound
	}

	// Project managers may only grant access to nodes inside their project. Validate every node before
	// writing anything, so a rejected request leaves no partial membership behind.
	for _, np := range nodePerms {
		nodeProjID, _, exists, err := s.repo.GetNodeProjectAndOwner(context.Background(), np.NodeID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNodeNotFound
		}
		if nodeProjID == nil || *nodeProjID != projectID {
			return ErrNodeNotInProject
		}
	}

	preset, perms := ResolveNodePermissions(defaultRole, defaultPerms)
	var addedBy *string
	if actorID != "" {
		addedBy = &actorID
	}

	if _, err := s.repo.UpsertProjectMembership(&ProjectMembership{
		ProjectID:          projectID,
		UserID:             targetUserID,
		DefaultRole:        preset,
		AllNodes:           allNodes,
		DefaultPermissions: perms,
		AddedBy:            addedBy,
	}); err != nil {
		return err
	}

	for _, np := range nodePerms {
		np.ProjectID = &projectID
		if _, err := s.UpsertNodePermission(actorID, targetUserID, np); err != nil {
			return err
		}
	}
	return nil
}

// RemoveProjectMember removes a user from a project and revokes their node permissions tied to that project.
func (s *RBACService) RemoveProjectMember(projectID, targetUserID string) error {
	_ = s.repo.DeleteUserNodePermissionsByProject(targetUserID, projectID)
	return s.repo.DeleteProjectMembership(projectID, targetUserID)
}

// ListProjectMembers returns all members assigned to a project along with their node permissions.
func (s *RBACService) ListProjectMembers(projectID string) ([]*ProjectMemberDetail, error) {
	return s.repo.ListProjectMembers(projectID)
}

func (s *RBACService) applyAssignments(actorID, userID string, projects []ProjectMembershipInput, nodePerms []NodePermissionGrantInput) error {
	if err := s.replaceProjects(actorID, userID, projects); err != nil {
		return err
	}
	return s.replaceNodePermissions(actorID, userID, nodePerms)
}

func (s *RBACService) replaceProjects(actorID, userID string, inputs []ProjectMembershipInput) error {
	var addedBy *string
	if actorID != "" {
		addedBy = &actorID
	}
	memberships := make([]*ProjectMembership, 0, len(inputs))
	seen := make(map[string]bool)
	for _, in := range inputs {
		pid := strings.TrimSpace(in.ProjectID)
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		preset, perms := ResolveNodePermissions(in.DefaultRole, in.DefaultPermissions)
		memberships = append(memberships, &ProjectMembership{
			ProjectID:          pid,
			UserID:             userID,
			DefaultRole:        preset,
			AllNodes:           in.AllNodes,
			DefaultPermissions: perms,
			AddedBy:            addedBy,
		})
	}
	return s.repo.ReplaceUserProjects(userID, memberships)
}

func (s *RBACService) replaceNodePermissions(actorID, userID string, inputs []NodePermissionGrantInput) error {
	var grantedBy *string
	if actorID != "" {
		grantedBy = &actorID
	}

	// Ensure any project referenced by a node grant is also in project_members
	existingProjs, _ := s.repo.ListUserProjects(userID)
	projMap := make(map[string]bool, len(existingProjs))
	for _, p := range existingProjs {
		projMap[p.ProjectID] = true
	}

	grants := make([]*NodePermissionGrant, 0, len(inputs))
	seenNodes := make(map[string]bool)
	for _, in := range inputs {
		nid := strings.TrimSpace(in.NodeID)
		if nid == "" || seenNodes[nid] {
			continue
		}
		seenNodes[nid] = true

		nodeProjID, _, exists, err := s.repo.GetNodeProjectAndOwner(context.Background(), nid)
		if err != nil || !exists {
			continue
		}

		projID := in.ProjectID
		if projID == nil || *projID == "" {
			projID = nodeProjID
		}

		if projID != nil && *projID != "" && !projMap[*projID] {
			projMap[*projID] = true
			_, _ = s.repo.UpsertProjectMembership(&ProjectMembership{
				ProjectID:          *projID,
				UserID:             userID,
				DefaultRole:        NodePresetViewer,
				AllNodes:           false,
				DefaultPermissions: PresetViewerPermissions,
				AddedBy:            grantedBy,
			})
		}

		preset, perms := ResolveNodePermissions(in.RolePreset, in.Permissions)
		grants = append(grants, &NodePermissionGrant{
			UserID:      userID,
			ProjectID:   projID,
			NodeID:      nid,
			RolePreset:  preset,
			Permissions: perms,
			GrantedBy:   grantedBy,
		})
	}
	return s.repo.ReplaceUserNodePermissions(userID, grants)
}

// GetEffectiveNodePermissions computes a user's effective role_preset and granular permissions on a node.
// When ctx carries an API token limit (WithTokenPermissions), the result is the intersection of the
// user's permissions and the token's, reported with the "custom" preset.
func (s *RBACService) GetEffectiveNodePermissions(ctx context.Context, userID, nodeID string) (rolePreset string, permissions []string, allowed bool, err error) {
	rolePreset, permissions, allowed, err = s.userNodePermissions(ctx, userID, nodeID)
	if err != nil || !allowed {
		return rolePreset, permissions, allowed, err
	}
	if limit, ok := tokenPermissions(ctx); ok && !slices.Contains(limit, PermWildcard) {
		limited := intersectPermissions(permissions, limit)
		return NodePresetCustom, limited, len(limited) > 0, nil
	}
	return rolePreset, permissions, allowed, nil
}

// userNodePermissions computes the user's own permissions on a node, ignoring any API token limit.
func (s *RBACService) userNodePermissions(ctx context.Context, userID, nodeID string) (rolePreset string, permissions []string, allowed bool, err error) {
	if userID == "" || nodeID == "" || s == nil || s.repo == nil {
		return "", nil, false, nil
	}

	u, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return "", nil, false, err
	}
	if u == nil || !u.IsActive {
		return "", nil, false, nil
	}

	// 1. Global Admin has unrestricted access to all nodes
	if u.Role == RoleAdmin {
		return NodePresetAdmin, []string{PermWildcard}, true, nil
	}

	// 2. Check node existence and direct ownership
	nodeProjID, ownerID, exists, err := s.repo.GetNodeProjectAndOwner(ctx, nodeID)
	if err != nil || !exists {
		return "", nil, false, err
	}
	if ownerID != nil && *ownerID == userID {
		return NodePresetAdmin, []string{PermWildcard}, true, nil
	}

	// 3. Explicit per-node permission grant
	grant, err := s.repo.GetUserNodePermission(ctx, userID, nodeID)
	if err != nil {
		return "", nil, false, err
	}
	if grant != nil {
		_, norm := ResolveNodePermissions(grant.RolePreset, grant.Permissions)
		return grant.RolePreset, norm, true, nil
	}

	// 4. Inherited project-wide access if the node belongs to a project where user has all_nodes = true
	if nodeProjID != nil && *nodeProjID != "" {
		pm, err := s.repo.GetProjectMembership(ctx, *nodeProjID, userID)
		if err != nil {
			return "", nil, false, err
		}
		if pm != nil && pm.AllNodes {
			_, norm := ResolveNodePermissions(pm.DefaultRole, pm.DefaultPermissions)
			return pm.DefaultRole, norm, true, nil
		}
	}

	return "", nil, false, nil
}

// HasNodePermission returns true if the user has the specified granular permission on the node.
func (s *RBACService) HasNodePermission(ctx context.Context, userID, nodeID, requiredPerm string) (bool, error) {
	_, perms, allowed, err := s.GetEffectiveNodePermissions(ctx, userID, nodeID)
	if err != nil || !allowed {
		return false, err
	}
	return HasPermission(perms, requiredPerm), nil
}

// CanAccessProject checks whether a user is a global admin, or a member of the given project.
func (s *RBACService) CanAccessProject(ctx context.Context, userID, projectID string) (isAdmin bool, isMember bool, err error) {
	if userID == "" || projectID == "" || s == nil || s.repo == nil {
		return false, false, nil
	}
	u, err := s.repo.GetUserByID(ctx, userID)
	if err != nil || u == nil || !u.IsActive {
		return false, false, err
	}
	if u.Role == RoleAdmin {
		return true, true, nil
	}
	pm, err := s.repo.GetProjectMembership(ctx, projectID, userID)
	if err != nil {
		return false, false, err
	}
	if pm != nil {
		return false, true, nil
	}
	// Also check if user has any explicit node permission inside this project
	grants, err := s.repo.ListUserNodePermissions(ctx, userID)
	if err == nil {
		for _, g := range grants {
			if g.ProjectID != nil && *g.ProjectID == projectID {
				return false, true, nil
			}
		}
	}
	return false, false, nil
}
