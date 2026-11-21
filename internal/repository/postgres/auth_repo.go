// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/jmoiron/sqlx"
)

// AuthRepository implementa identity.AuthRepository usando PostgreSQL
type AuthRepository struct {
	db *sqlx.DB
}

// NewAuthRepository crea una nueva instancia del repositorio
func NewAuthRepository(db *sqlx.DB) *AuthRepository {
	return &AuthRepository{db: db}
}

// CreateUser crea un nuevo usuario. El primer usuario de la instancia recibe automáticamente el rol 'admin'.
func (r *AuthRepository) CreateUser(email, name, passwordHash string) (string, error) {
	var id string
	err := r.db.QueryRowContext(context.Background(), `
		INSERT INTO users (email, name, password_hash, role, is_active)
		VALUES (
			$1,
			$2,
			$3,
			CASE WHEN (SELECT COUNT(*) FROM users) = 0 THEN 'admin' ELSE 'member' END,
			true
		)
		RETURNING id`, email, name, passwordHash).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("auth_repo.CreateUser: %w", err)
	}

	return id, nil
}

// FindUserByEmail busca un usuario por email
func (r *AuthRepository) FindUserByEmail(email string) (*identity.User, error) {
	var model UserModel
	err := r.db.Get(&model, `
		SELECT id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at 
		FROM users 
		WHERE email = $1`, email)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return userModelToDomain(&model), nil
}

// StoreRefreshToken almacena un token de refresco
func (r *AuthRepository) StoreRefreshToken(userID, tokenHash, userAgent, ip string) error {
	var ipVal interface{}
	if ip != "" {
		ipVal = ip
	}
	expiresAt := time.Now().UTC().Add(platform.RefreshTokenTTL())
	if _, err := r.db.Exec(`
		INSERT INTO refresh_tokens (user_id, token_hash, user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, userID, tokenHash, userAgent, ipVal, expiresAt); err != nil {
		return err
	}
	// Housekeeping is best effort: a failure must not block the login that just succeeded.
	if err := r.enforceSessionLimit(userID, platform.MaxSessionsPerUser()); err != nil {
		log.Printf("[AUTH] session housekeeping for user %s failed: %v", userID, err)
	}
	return nil
}

// enforceSessionLimit revokes the user's oldest active sessions beyond maxSessions (0 = no cap) and deletes the
// user's refresh tokens that expired or were revoked more than a day ago. Every refresh rotates the token, so
// without pruning the table would grow by roughly 96 rows per open session per day.
func (r *AuthRepository) enforceSessionLimit(userID string, maxSessions int) error {
	if maxSessions > 0 {
		if _, err := r.db.Exec(`
			UPDATE refresh_tokens
			SET revoked_at = now()
			WHERE user_id = $1 AND revoked_at IS NULL
			  AND id NOT IN (
				SELECT id FROM refresh_tokens
				WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
				ORDER BY created_at DESC
				LIMIT $2
			  )`, userID, maxSessions); err != nil {
			return err
		}
	}
	_, err := r.db.Exec(`
		DELETE FROM refresh_tokens
		WHERE user_id = $1
		  AND (expires_at < now() - interval '1 day' OR revoked_at < now() - interval '1 day')`, userID)
	return err
}

// ConsumeRefreshToken atomically revokes an active, unexpired refresh token and returns its user ID.
// Because the check and the revocation happen in a single UPDATE, a token can only be used once even
// under concurrent refresh requests.
func (r *AuthRepository) ConsumeRefreshToken(tokenHash string) (string, error) {
	var userID string
	err := r.db.QueryRowContext(context.Background(), `
		UPDATE refresh_tokens
		SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		RETURNING user_id
	`, tokenHash).Scan(&userID)
	if err != nil {
		return "", err
	}
	return userID, nil
}

// RevokeRefreshToken revoca un token de refresco
func (r *AuthRepository) RevokeRefreshToken(userID, tokenHash string) error {
	_, err := r.db.Exec(`
		UPDATE refresh_tokens 
		SET revoked_at = now()
		WHERE user_id = $1 AND token_hash = $2 AND revoked_at IS NULL`,
		userID, tokenHash)
	return err
}

// FindUserByID busca un usuario por ID
func (r *AuthRepository) FindUserByID(id string) (*identity.User, error) {
	var model UserModel
	err := r.db.Get(&model, `
		SELECT id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at 
		FROM users 
		WHERE id = $1`, id)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return userModelToDomain(&model), nil
}

// UpdateUserProfile actualiza el nombre y avatar_url del usuario
func (r *AuthRepository) UpdateUserProfile(userID string, name, avatarURL *string) (*identity.User, error) {
	var model UserModel
	err := r.db.Get(&model, `
		UPDATE users
		SET name = COALESCE($1, name),
		    avatar_url = COALESCE($2, avatar_url),
		    updated_at = NOW()
		WHERE id = $3
		RETURNING id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at`,
		name, avatarURL, userID)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return userModelToDomain(&model), nil
}

// FindOrCreateOAuthUser busca un usuario por proveedor OAuth o lo crea
func (r *AuthRepository) FindOrCreateOAuthUser(email, name, provider, providerID string) (*identity.User, error) {
	// 1. Buscar por provider + provider_id
	var model UserModel
	err := r.db.Get(&model, `
		SELECT id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at
		FROM users
		WHERE oauth_provider = $1 AND oauth_provider_id = $2`, provider, providerID)

	if err == nil {
		return userModelToDomain(&model), nil
	}

	// 2. Buscar por email — vincular OAuth a cuenta existente
	err = r.db.Get(&model, `
		SELECT id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at
		FROM users
		WHERE email = $1`, email)

	if err == nil {
		if model.OAuthProvider == nil {
			_, _ = r.db.Exec(`
				UPDATE users SET oauth_provider = $1, oauth_provider_id = $2, updated_at = now()
				WHERE id = $3`, provider, providerID, model.ID)
			model.OAuthProvider = &provider
			model.OAuthProviderID = &providerID
		}
		return userModelToDomain(&model), nil
	}

	// 3. Crear nuevo usuario (sin password)
	var id string
	var role string
	err = r.db.QueryRowContext(context.Background(), `
		INSERT INTO users (email, name, oauth_provider, oauth_provider_id, role, is_active)
		VALUES (
			$1,
			$2,
			$3,
			$4,
			CASE WHEN (SELECT COUNT(*) FROM users) = 0 THEN 'admin' ELSE 'member' END,
			true
		)
		RETURNING id, role`, email, name, provider, providerID).Scan(&id, &role)
	if err != nil {
		return nil, fmt.Errorf("auth_repo.FindOrCreateOAuthUser: %w", err)
	}

	return &identity.User{
		ID:              id,
		Email:           email,
		Name:            &name,
		Role:            role,
		IsActive:        true,
		OAuthProvider:   &provider,
		OAuthProviderID: &providerID,
	}, nil
}

// FindRefreshToken busca un refresh token activo y retorna el userID
func (r *AuthRepository) FindRefreshToken(tokenHash string) (string, error) {
	var userID string
	err := r.db.QueryRowContext(context.Background(), `
		SELECT user_id FROM refresh_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
	`, tokenHash).Scan(&userID)

	if err == sql.ErrNoRows {
		return "", sql.ErrNoRows
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

// CountUsers retorna el número total de usuarios registrados.
func (r *AuthRepository) CountUsers() (int, error) {
	var count int
	err := r.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

// FindFirstUser retorna el primer usuario registrado en la instancia (administrador principal).
func (r *AuthRepository) FindFirstUser() (*identity.User, error) {
	var model UserModel
	err := r.db.Get(&model, `
		SELECT id, email, name, avatar_url, role, is_active, created_by, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at
		FROM users
		ORDER BY created_at ASC
		LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return userModelToDomain(&model), nil
}

func userModelToDomain(m *UserModel) *identity.User {
	return &identity.User{
		ID:              m.ID,
		Email:           m.Email,
		Name:            m.Name,
		AvatarURL:       m.AvatarURL,
		Role:            m.Role,
		IsActive:        m.IsActive,
		CreatedBy:       m.CreatedBy,
		PasswordHash:    m.PasswordHash,
		OAuthProvider:   m.OAuthProvider,
		OAuthProviderID: m.OAuthProviderID,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}
