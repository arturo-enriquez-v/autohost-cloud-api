package auth

import "time"

// TokenPair holds the access and refresh tokens issued after authentication.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// User representa un usuario del sistema
type User struct {
	ID              string    `db:"id"`
	Email           string    `db:"email"`
	Name            *string   `db:"name"`
	AvatarURL       *string   `db:"avatar_url"`
	PasswordHash    *string   `db:"password_hash"`
	OAuthProvider   *string   `db:"oauth_provider"`
	OAuthProviderID *string   `db:"oauth_provider_id"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
}

// Repository define las operaciones de persistencia para usuarios
type Repository interface {
	CreateUser(email, name, passwordHash string) (string, error)
	FindUserByEmail(email string) (*User, error)
	FindUserByID(id string) (*User, error)
	FindOrCreateOAuthUser(email, name, provider, providerID string) (*User, error)
	UpdateUserProfile(userID string, name, avatarURL *string) (*User, error)
	StoreRefreshToken(userID, tokenHash, userAgent, ip string) error
	RevokeRefreshToken(userID, tokenHash string) error
	FindRefreshToken(tokenHash string) (userID string, err error)
}
