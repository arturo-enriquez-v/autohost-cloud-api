package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arturo/autohost-cloud-api/internal/domain/auth"
	"github.com/jmoiron/sqlx"
)

// AuthRepository implementa auth.Repository usando PostgreSQL
type AuthRepository struct {
	db *sqlx.DB
}

// NewAuthRepository crea una nueva instancia del repositorio
func NewAuthRepository(db *sqlx.DB) *AuthRepository {
	return &AuthRepository{db: db}
}

// CreateUser crea un nuevo usuario y le asigna el plan starter de forma atómica.
func (r *AuthRepository) CreateUser(email, name, passwordHash string) (string, error) {
	tx, err := r.db.BeginTxx(context.Background(), nil)
	if err != nil {
		return "", fmt.Errorf("auth_repo.CreateUser: begin tx: %w", err)
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRowContext(context.Background(), `
		INSERT INTO users (email, name, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id`, email, name, passwordHash).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("auth_repo.CreateUser: %w", err)
	}

	_, err = tx.ExecContext(context.Background(), `
		INSERT INTO user_subscriptions (user_id, plan_id) VALUES ($1, 'free')`, id)
	if err != nil {
		return "", fmt.Errorf("auth_repo.CreateUser: assign starter plan: %w", err)
	}

	return id, tx.Commit()
}

// FindUserByEmail busca un usuario por email
func (r *AuthRepository) FindUserByEmail(email string) (*auth.User, error) {
	var model UserModel
	err := r.db.Get(&model, `
		SELECT id, email, name, avatar_url, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at 
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
	_, err := r.db.Exec(`
		INSERT INTO refresh_tokens (user_id, token_hash, user_agent, ip)
		VALUES ($1, $2, $3, $4)`, userID, tokenHash, userAgent, ipVal)
	return err
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
func (r *AuthRepository) FindUserByID(id string) (*auth.User, error) {
	var model UserModel
	err := r.db.Get(&model, `
		SELECT id, email, name, avatar_url, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at 
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
func (r *AuthRepository) UpdateUserProfile(userID string, name, avatarURL *string) (*auth.User, error) {
	var model UserModel
	err := r.db.Get(&model, `
		UPDATE users
		SET name = COALESCE($1, name),
		    avatar_url = COALESCE($2, avatar_url),
		    updated_at = NOW()
		WHERE id = $3
		RETURNING id, email, name, avatar_url, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at`,
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
func (r *AuthRepository) FindOrCreateOAuthUser(email, name, provider, providerID string) (*auth.User, error) {
	// 1. Buscar por provider + provider_id
	var model UserModel
	err := r.db.Get(&model, `
		SELECT id, email, name, avatar_url, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at
		FROM users
		WHERE oauth_provider = $1 AND oauth_provider_id = $2`, provider, providerID)

	if err == nil {
		return userModelToDomain(&model), nil
	}

	// 2. Buscar por email — vincular OAuth a cuenta existente
	err = r.db.Get(&model, `
		SELECT id, email, name, avatar_url, password_hash, oauth_provider, oauth_provider_id, created_at, updated_at
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

	// 3. Crear nuevo usuario (sin password) + asignar plan starter
	tx, err := r.db.BeginTxx(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("auth_repo.FindOrCreateOAuthUser: begin tx: %w", err)
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRowContext(context.Background(), `
		INSERT INTO users (email, name, oauth_provider, oauth_provider_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id`, email, name, provider, providerID).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("auth_repo.FindOrCreateOAuthUser: %w", err)
	}

	_, err = tx.ExecContext(context.Background(), `
		INSERT INTO user_subscriptions (user_id, plan_id) VALUES ($1, 'free')`, id)
	if err != nil {
		return nil, fmt.Errorf("auth_repo.FindOrCreateOAuthUser: assign starter plan: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("auth_repo.FindOrCreateOAuthUser: commit: %w", err)
	}

	return &auth.User{
		ID:              id,
		Email:           email,
		Name:            &name,
		OAuthProvider:   &provider,
		OAuthProviderID: &providerID,
	}, nil
}

// FindRefreshToken busca un refresh token activo y retorna el userID
func (r *AuthRepository) FindRefreshToken(tokenHash string) (string, error) {
	var userID string
	err := r.db.QueryRowContext(context.Background(), `
		SELECT user_id FROM refresh_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, tokenHash).Scan(&userID)

	if err == sql.ErrNoRows {
		return "", sql.ErrNoRows
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

func userModelToDomain(m *UserModel) *auth.User {
	return &auth.User{
		ID:              m.ID,
		Email:           m.Email,
		Name:            m.Name,
		AvatarURL:       m.AvatarURL,
		PasswordHash:    m.PasswordHash,
		OAuthProvider:   m.OAuthProvider,
		OAuthProviderID: m.OAuthProviderID,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}
