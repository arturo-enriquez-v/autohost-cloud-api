// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/jmoiron/sqlx"
)

// RBACRepository implements identity.RBACRepository using PostgreSQL.
type RBACRepository struct {
	db *sqlx.DB
}

func NewRBACRepository(db *sqlx.DB) *RBACRepository {
	return &RBACRepository{db: db}
}

func (r *RBACRepository) ListUsers() ([]*identity.User, error) {
	var models []UserModel
	err := r.db.SelectContext(context.Background(), &models, `
		SELECT id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at
		FROM users
		ORDER BY CASE WHEN role = 'admin' THEN 0 ELSE 1 END, created_at ASC`)
	if err != nil {
		return nil, err
	}
	out := make([]*identity.User, len(models))
	for i := range models {
		out[i] = userModelToDomain(&models[i])
	}
	return out, nil
}

func (r *RBACRepository) GetUserByID(ctx context.Context, id string) (*identity.User, error) {
	var model UserModel
	err := r.db.GetContext(ctx, &model, `
		SELECT id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at
		FROM users
		WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return userModelToDomain(&model), nil
}

func (r *RBACRepository) GetUserByEmail(email string) (*identity.User, error) {
	var model UserModel
	err := r.db.GetContext(context.Background(), &model, `
		SELECT id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at
		FROM users
		WHERE email = $1`, email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return userModelToDomain(&model), nil
}

func (r *RBACRepository) CreateUserWithRole(email, name, passwordHash, role string, isActive bool, createdBy *string) (*identity.User, error) {
	var model UserModel
	err := r.db.GetContext(context.Background(), &model, `
		INSERT INTO users (email, name, password_hash, role, is_active, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at`,
		email, name, passwordHash, role, isActive, createdBy)
	if err != nil {
		return nil, err
	}
	return userModelToDomain(&model), nil
}

func (r *RBACRepository) UpdateUser(id string, email, name, passwordHash, role *string, isActive *bool) (*identity.User, error) {
	var model UserModel
	err := r.db.GetContext(context.Background(), &model, `
		UPDATE users
		SET email = COALESCE($1, email),
		    name = COALESCE($2, name),
		    password_hash = COALESCE($3, password_hash),
		    role = COALESCE($4, role),
		    is_active = COALESCE($5, is_active),
		    updated_at = NOW()
		WHERE id = $6
		RETURNING id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at`,
		email, name, passwordHash, role, isActive, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, identity.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return userModelToDomain(&model), nil
}

func (r *RBACRepository) DeleteUser(id string) error {
	res, err := r.db.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return identity.ErrUserNotFound
	}
	return nil
}

func (r *RBACRepository) CountActiveAdmins() (int, error) {
	var count int
	err := r.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM users WHERE role = 'admin' AND is_active = true`).Scan(&count)
	return count, err
}

func (r *RBACRepository) RevokeAllUserRefreshTokens(userID string) error {
	_, err := r.db.ExecContext(context.Background(), `
		UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return err
}

// ─── Project Memberships ──────────────────────────────────────────────────────

type projectMembershipRow struct {
	ID                 string    `db:"id"`
	ProjectID          string    `db:"project_id"`
	ProjectName        string    `db:"project_name"`
	UserID             string    `db:"user_id"`
	DefaultRole        string    `db:"default_role"`
	AllNodes           bool      `db:"all_nodes"`
	DefaultPermissions []byte    `db:"default_permissions"`
	AddedBy            *string   `db:"added_by"`
	CreatedAt          time.Time `db:"created_at"`
	UpdatedAt          time.Time `db:"updated_at"`
}

func rowToProjectMembership(row *projectMembershipRow) *identity.ProjectMembership {
	var perms []string
	if len(row.DefaultPermissions) > 0 {
		_ = json.Unmarshal(row.DefaultPermissions, &perms)
	}
	if perms == nil {
		perms = []string{}
	}
	return &identity.ProjectMembership{
		ID:                 row.ID,
		ProjectID:          row.ProjectID,
		ProjectName:        row.ProjectName,
		UserID:             row.UserID,
		DefaultRole:        row.DefaultRole,
		AllNodes:           row.AllNodes,
		DefaultPermissions: perms,
		AddedBy:            row.AddedBy,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}

func (r *RBACRepository) ListUserProjects(userID string) ([]*identity.ProjectMembership, error) {
	var rows []projectMembershipRow
	err := r.db.SelectContext(context.Background(), &rows, `
		SELECT pm.id, pm.project_id, COALESCE(p.name, '') AS project_name, pm.user_id,
		       pm.default_role, pm.all_nodes, pm.default_permissions, pm.added_by, pm.created_at, pm.updated_at
		FROM project_members pm
		JOIN projects p ON p.id = pm.project_id
		WHERE pm.user_id = $1
		ORDER BY p.name ASC`, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*identity.ProjectMembership, len(rows))
	for i := range rows {
		out[i] = rowToProjectMembership(&rows[i])
	}
	return out, nil
}

func (r *RBACRepository) GetProjectMembership(ctx context.Context, projectID, userID string) (*identity.ProjectMembership, error) {
	var row projectMembershipRow
	err := r.db.GetContext(ctx, &row, `
		SELECT pm.id, pm.project_id, COALESCE(p.name, '') AS project_name, pm.user_id,
		       pm.default_role, pm.all_nodes, pm.default_permissions, pm.added_by, pm.created_at, pm.updated_at
		FROM project_members pm
		JOIN projects p ON p.id = pm.project_id
		WHERE pm.project_id = $1 AND pm.user_id = $2`, projectID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rowToProjectMembership(&row), nil
}

func (r *RBACRepository) UpsertProjectMembership(m *identity.ProjectMembership) (*identity.ProjectMembership, error) {
	permsJSON, err := json.Marshal(m.DefaultPermissions)
	if err != nil {
		permsJSON = []byte("[]")
	}
	var row projectMembershipRow
	err = r.db.GetContext(context.Background(), &row, `
		INSERT INTO project_members (project_id, user_id, default_role, all_nodes, default_permissions, added_by)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT (project_id, user_id) DO UPDATE
		SET default_role = EXCLUDED.default_role,
		    all_nodes = EXCLUDED.all_nodes,
		    default_permissions = EXCLUDED.default_permissions,
		    updated_at = NOW()
		RETURNING id, project_id, '' AS project_name, user_id, default_role, all_nodes, default_permissions, added_by, created_at, updated_at`,
		m.ProjectID, m.UserID, m.DefaultRole, m.AllNodes, string(permsJSON), m.AddedBy)
	if err != nil {
		return nil, err
	}
	return rowToProjectMembership(&row), nil
}

func (r *RBACRepository) DeleteProjectMembership(projectID, userID string) error {
	_, err := r.db.ExecContext(context.Background(), `
		DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	return err
}

func (r *RBACRepository) ReplaceUserProjects(userID string, memberships []*identity.ProjectMembership) error {
	tx, err := r.db.BeginTxx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM project_members WHERE user_id = $1`, userID); err != nil {
		return err
	}

	for _, m := range memberships {
		permsJSON, _ := json.Marshal(m.DefaultPermissions)
		if len(permsJSON) == 0 {
			permsJSON = []byte("[]")
		}
		if _, err := tx.Exec(`
			INSERT INTO project_members (project_id, user_id, default_role, all_nodes, default_permissions, added_by)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6)
			ON CONFLICT (project_id, user_id) DO UPDATE
			SET default_role = EXCLUDED.default_role,
			    all_nodes = EXCLUDED.all_nodes,
			    default_permissions = EXCLUDED.default_permissions,
			    updated_at = NOW()`,
			m.ProjectID, userID, m.DefaultRole, m.AllNodes, string(permsJSON), m.AddedBy); err != nil {
			return err
		}
	}

	return tx.Commit()
}

type projectMemberDetailRow struct {
	UserID             string    `db:"user_id"`
	Email              string    `db:"email"`
	Name               *string   `db:"name"`
	AvatarURL          *string   `db:"avatar_url"`
	Role               string    `db:"role"`
	IsActive           bool      `db:"is_active"`
	ProjectID          string    `db:"project_id"`
	DefaultRole        string    `db:"default_role"`
	AllNodes           bool      `db:"all_nodes"`
	DefaultPermissions []byte    `db:"default_permissions"`
	AddedAt            time.Time `db:"created_at"`
}

func (r *RBACRepository) ListProjectMembers(projectID string) ([]*identity.ProjectMemberDetail, error) {
	var rows []projectMemberDetailRow
	err := r.db.SelectContext(context.Background(), &rows, `
		SELECT u.id AS user_id, u.email, u.name, u.avatar_url, u.role, u.is_active,
		       pm.project_id, pm.default_role, pm.all_nodes, pm.default_permissions, pm.created_at
		FROM project_members pm
		JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = $1
		ORDER BY u.email ASC`, projectID)
	if err != nil {
		return nil, err
	}

	out := make([]*identity.ProjectMemberDetail, 0, len(rows))
	for _, row := range rows {
		var defPerms []string
		if len(row.DefaultPermissions) > 0 {
			_ = json.Unmarshal(row.DefaultPermissions, &defPerms)
		}
		if defPerms == nil {
			defPerms = []string{}
		}
		name := ""
		if row.Name != nil {
			name = *row.Name
		}
		avatar := ""
		if row.AvatarURL != nil {
			avatar = *row.AvatarURL
		}

		allUserNodePerms, _ := r.ListUserNodePermissions(context.Background(), row.UserID)
		projNodePerms := make([]*identity.NodePermissionGrant, 0)
		for _, np := range allUserNodePerms {
			if np.ProjectID != nil && *np.ProjectID == projectID {
				projNodePerms = append(projNodePerms, np)
			}
		}

		out = append(out, &identity.ProjectMemberDetail{
			UserID:             row.UserID,
			Email:              row.Email,
			Name:               name,
			AvatarURL:          avatar,
			Role:               row.Role,
			IsActive:           row.IsActive,
			ProjectID:          row.ProjectID,
			DefaultRole:        row.DefaultRole,
			AllNodes:           row.AllNodes,
			DefaultPermissions: defPerms,
			NodePermissions:    projNodePerms,
			AddedAt:            row.AddedAt,
		})
	}
	return out, nil
}

// ─── Node Permissions ─────────────────────────────────────────────────────────

type nodePermissionRow struct {
	ID           string    `db:"id"`
	UserID       string    `db:"user_id"`
	ProjectID    *string   `db:"project_id"`
	ProjectName  string    `db:"project_name"`
	NodeID       string    `db:"node_id"`
	NodeHostname string    `db:"node_hostname"`
	RolePreset   string    `db:"role_preset"`
	Permissions  []byte    `db:"permissions"`
	GrantedBy    *string   `db:"granted_by"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

func rowToNodePermission(row *nodePermissionRow) *identity.NodePermissionGrant {
	var perms []string
	if len(row.Permissions) > 0 {
		_ = json.Unmarshal(row.Permissions, &perms)
	}
	if perms == nil {
		perms = []string{}
	}
	return &identity.NodePermissionGrant{
		ID:           row.ID,
		UserID:       row.UserID,
		ProjectID:    row.ProjectID,
		ProjectName:  row.ProjectName,
		NodeID:       row.NodeID,
		NodeHostname: row.NodeHostname,
		RolePreset:   row.RolePreset,
		Permissions:  perms,
		GrantedBy:    row.GrantedBy,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func (r *RBACRepository) ListUserNodePermissions(ctx context.Context, userID string) ([]*identity.NodePermissionGrant, error) {
	var rows []nodePermissionRow
	err := r.db.SelectContext(ctx, &rows, `
		SELECT unp.id, unp.user_id, COALESCE(unp.project_id, n.project_id) AS project_id,
		       COALESCE(p.name, '') AS project_name, unp.node_id, COALESCE(n.hostname, '') AS node_hostname,
		       unp.role_preset, unp.permissions, unp.granted_by, unp.created_at, unp.updated_at
		FROM user_node_permissions unp
		JOIN nodes n ON n.id = unp.node_id
		LEFT JOIN projects p ON p.id = COALESCE(unp.project_id, n.project_id)
		WHERE unp.user_id = $1
		ORDER BY n.hostname ASC`, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*identity.NodePermissionGrant, len(rows))
	for i := range rows {
		out[i] = rowToNodePermission(&rows[i])
	}
	return out, nil
}

func (r *RBACRepository) GetUserNodePermission(ctx context.Context, userID, nodeID string) (*identity.NodePermissionGrant, error) {
	var row nodePermissionRow
	err := r.db.GetContext(ctx, &row, `
		SELECT unp.id, unp.user_id, COALESCE(unp.project_id, n.project_id) AS project_id,
		       COALESCE(p.name, '') AS project_name, unp.node_id, COALESCE(n.hostname, '') AS node_hostname,
		       unp.role_preset, unp.permissions, unp.granted_by, unp.created_at, unp.updated_at
		FROM user_node_permissions unp
		JOIN nodes n ON n.id = unp.node_id
		LEFT JOIN projects p ON p.id = COALESCE(unp.project_id, n.project_id)
		WHERE unp.user_id = $1 AND unp.node_id = $2`, userID, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rowToNodePermission(&row), nil
}

func (r *RBACRepository) UpsertUserNodePermission(g *identity.NodePermissionGrant) (*identity.NodePermissionGrant, error) {
	permsJSON, err := json.Marshal(g.Permissions)
	if err != nil {
		permsJSON = []byte("[]")
	}
	var row nodePermissionRow
	err = r.db.GetContext(context.Background(), &row, `
		INSERT INTO user_node_permissions (user_id, project_id, node_id, role_preset, permissions, granted_by)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT (user_id, node_id) DO UPDATE
		SET project_id = COALESCE(EXCLUDED.project_id, user_node_permissions.project_id),
		    role_preset = EXCLUDED.role_preset,
		    permissions = EXCLUDED.permissions,
		    granted_by = COALESCE(EXCLUDED.granted_by, user_node_permissions.granted_by),
		    updated_at = NOW()
		RETURNING id, user_id, project_id, '' AS project_name, node_id, '' AS node_hostname, role_preset, permissions, granted_by, created_at, updated_at`,
		g.UserID, g.ProjectID, g.NodeID, g.RolePreset, string(permsJSON), g.GrantedBy)
	if err != nil {
		return nil, err
	}
	return r.GetUserNodePermission(context.Background(), g.UserID, g.NodeID)
}

func (r *RBACRepository) DeleteUserNodePermission(userID, nodeID string) error {
	_, err := r.db.ExecContext(context.Background(), `
		DELETE FROM user_node_permissions WHERE user_id = $1 AND node_id = $2`, userID, nodeID)
	return err
}

func (r *RBACRepository) DeleteUserNodePermissionsByProject(userID, projectID string) error {
	_, err := r.db.ExecContext(context.Background(), `
		DELETE FROM user_node_permissions
		WHERE user_id = $1 AND (project_id = $2 OR node_id IN (SELECT id FROM nodes WHERE project_id = $2))`,
		userID, projectID)
	return err
}

func (r *RBACRepository) ReplaceUserNodePermissions(userID string, grants []*identity.NodePermissionGrant) error {
	tx, err := r.db.BeginTxx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM user_node_permissions WHERE user_id = $1`, userID); err != nil {
		return err
	}

	for _, g := range grants {
		permsJSON, _ := json.Marshal(g.Permissions)
		if len(permsJSON) == 0 {
			permsJSON = []byte("[]")
		}
		if _, err := tx.Exec(`
			INSERT INTO user_node_permissions (user_id, project_id, node_id, role_preset, permissions, granted_by)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6)
			ON CONFLICT (user_id, node_id) DO UPDATE
			SET project_id = COALESCE(EXCLUDED.project_id, user_node_permissions.project_id),
			    role_preset = EXCLUDED.role_preset,
			    permissions = EXCLUDED.permissions,
			    granted_by = COALESCE(EXCLUDED.granted_by, user_node_permissions.granted_by),
			    updated_at = NOW()`,
			userID, g.ProjectID, g.NodeID, g.RolePreset, string(permsJSON), g.GrantedBy); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *RBACRepository) GetNodeProjectAndOwner(ctx context.Context, nodeID string) (*string, *string, bool, error) {
	var row struct {
		ProjectID *string `db:"project_id"`
		OwnerID   *string `db:"owner_id"`
	}
	err := r.db.GetContext(ctx, &row, `
		SELECT project_id, owner_id FROM nodes WHERE id = $1`, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	return row.ProjectID, row.OwnerID, true, nil
}
