// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/platform"
)

var (
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrUserAlreadyExists    = errors.New("user already exists")
	ErrInvalidRefreshToken  = errors.New("invalid or expired refresh token")
	ErrRegistrationDisabled = errors.New("public registration is disabled")
)

// SetupStatus describes the onboarding and registration state of this self-hosted instance.
type SetupStatus struct {
	NeedsSetup         bool `json:"needs_setup"`
	AllowRegistration  bool `json:"allow_registration"`
	GoogleOAuthEnabled bool `json:"google_oauth_enabled"`
}

// TokenPair holds the access and refresh tokens issued after authentication.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// User representa un usuario del sistema.
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

// AuthRepository define las operaciones de persistencia para usuarios.
type AuthRepository interface {
	CreateUser(email, name, passwordHash string) (string, error)
	FindUserByEmail(email string) (*User, error)
	FindUserByID(id string) (*User, error)
	FindOrCreateOAuthUser(email, name, provider, providerID string) (*User, error)
	UpdateUserProfile(userID string, name, avatarURL *string) (*User, error)
	StoreRefreshToken(userID, tokenHash, userAgent, ip string) error
	RevokeRefreshToken(userID, tokenHash string) error
	FindRefreshToken(tokenHash string) (userID string, err error)
	CountUsers() (int, error)
	FindFirstUser() (*User, error)
}

// AuthService encapsula la lógica de negocio de autenticación.
type AuthService struct {
	repo               AuthRepository
	onFirstUserCreated func(userID string)
}

func NewAuthService(repo AuthRepository) *AuthService {
	return &AuthService{repo: repo}
}

// SetOnFirstUserCreated registers a callback invoked when the initial admin account is created.
func (s *AuthService) SetOnFirstUserCreated(fn func(userID string)) {
	s.onFirstUserCreated = fn
}

func isRegistrationExplicitlyAllowed() bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv("ALLOW_REGISTRATION")))
	return val == "true" || val == "1" || val == "yes"
}

// GetSetupStatus returns whether the instance requires initial admin setup and whether public signups are allowed.
func (s *AuthService) GetSetupStatus() (*SetupStatus, error) {
	count, err := s.repo.CountUsers()
	if err != nil {
		return nil, err
	}
	needsSetup := count == 0
	return &SetupStatus{
		NeedsSetup:         needsSetup,
		AllowRegistration:  needsSetup || isRegistrationExplicitlyAllowed(),
		GoogleOAuthEnabled: strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID")) != "",
	}, nil
}

// GetFirstUser returns the earliest registered user (typically the self-hosted admin).
func (s *AuthService) GetFirstUser() (*User, error) {
	return s.repo.FindFirstUser()
}

func (s *AuthService) issueTokenPair(user *User, ua, ip string) (TokenPair, error) {
	name := ""
	if user.Name != nil {
		name = *user.Name
	}
	access, err := platform.SignAccessToken(user.ID, user.Email, name)
	if err != nil {
		return TokenPair{}, err
	}
	rt, rtHash := platform.MakeRefreshPair()
	if err := s.repo.StoreRefreshToken(user.ID, rtHash, ua, ip); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: rt}, nil
}

// Register registra un nuevo usuario.
// En modo self-hosted, permite el registro libre del primer usuario (Setup Wizard)
// y bloquea registros posteriores a menos que ALLOW_REGISTRATION=true.
func (s *AuthService) Register(email, name, password string) (userID string, err error) {
	count, err := s.repo.CountUsers()
	if err != nil {
		return "", err
	}
	if count > 0 && !isRegistrationExplicitlyAllowed() {
		return "", ErrRegistrationDisabled
	}

	existing, _ := s.repo.FindUserByEmail(email)
	if existing != nil {
		return "", ErrUserAlreadyExists
	}

	hash, err := platform.HashPassword(password)
	if err != nil {
		return "", err
	}

	id, err := s.repo.CreateUser(email, name, hash)
	if err != nil {
		return "", err
	}

	if count == 0 && s.onFirstUserCreated != nil {
		s.onFirstUserCreated(id)
	}

	return id, nil
}

// RegisterAndIssue registra un nuevo usuario y emite tokens.
func (s *AuthService) RegisterAndIssue(email, name, password, ua, ip string) (TokenPair, error) {
	userID, err := s.Register(email, name, password)
	if err != nil {
		return TokenPair{}, err
	}
	access, err := platform.SignAccessToken(userID, email, name)
	if err != nil {
		return TokenPair{}, err
	}
	rt, rtHash := platform.MakeRefreshPair()
	if err := s.repo.StoreRefreshToken(userID, rtHash, ua, ip); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: rt}, nil
}

// Login autentica a un usuario con email y contraseña.
func (s *AuthService) Login(email, password string) (*User, error) {
	user, err := s.repo.FindUserByEmail(email)
	if err != nil || user == nil {
		return nil, ErrInvalidCredentials
	}

	if user.PasswordHash == nil {
		return nil, ErrInvalidCredentials
	}

	if err := platform.CheckPassword(*user.PasswordHash, password); err != nil {
		return nil, ErrInvalidCredentials
	}

	return user, nil
}

// LoginAndIssue autentica con email/password y emite tokens.
func (s *AuthService) LoginAndIssue(email, password, ua, ip string) (TokenPair, error) {
	user, err := s.Login(email, password)
	if err != nil {
		return TokenPair{}, err
	}
	return s.issueTokenPair(user, ua, ip)
}

// RefreshTokens rota el refresh token y emite un nuevo par.
func (s *AuthService) RefreshTokens(oldPlain, ua, ip string) (TokenPair, error) {
	oldHash := platform.HashRefreshToken(oldPlain)

	userID, err := s.repo.FindRefreshToken(oldHash)
	if err != nil {
		return TokenPair{}, ErrInvalidRefreshToken
	}

	user, err := s.repo.FindUserByID(userID)
	if err != nil || user == nil {
		return TokenPair{}, ErrInvalidRefreshToken
	}

	_ = s.repo.RevokeRefreshToken(userID, oldHash)

	return s.issueTokenPair(user, ua, ip)
}

// RevokeToken revoca un refresh token si existe.
func (s *AuthService) RevokeToken(plainToken string) {
	hash := platform.HashRefreshToken(plainToken)
	userID, err := s.repo.FindRefreshToken(hash)
	if err != nil {
		return
	}
	_ = s.repo.RevokeRefreshToken(userID, hash)
}

// LoginOAuth busca o crea un usuario mediante proveedor OAuth y emite tokens.
// Si ya existe un usuario administrador y ALLOW_REGISTRATION no está activo,
// permite iniciar sesión a cuentas existentes pero bloquea el alta de cuentas nuevas.
func (s *AuthService) LoginOAuth(email, name, provider, providerID string) (*User, error) {
	existing, _ := s.repo.FindUserByEmail(email)
	if existing != nil {
		return s.repo.FindOrCreateOAuthUser(email, name, provider, providerID)
	}

	count, err := s.repo.CountUsers()
	if err != nil {
		return nil, err
	}
	if count > 0 && !isRegistrationExplicitlyAllowed() {
		return nil, ErrRegistrationDisabled
	}

	user, err := s.repo.FindOrCreateOAuthUser(email, name, provider, providerID)
	if err != nil {
		return nil, err
	}

	if count == 0 && user != nil && s.onFirstUserCreated != nil {
		s.onFirstUserCreated(user.ID)
	}

	return user, nil
}

// OAuthLoginAndIssue verifica un ID token de Google y emite tokens.
func (s *AuthService) OAuthLoginAndIssue(idToken, ua, ip string) (TokenPair, error) {
	claims, err := platform.VerifyGoogleIDToken(idToken)
	if err != nil {
		return TokenPair{}, err
	}

	user, err := s.LoginOAuth(claims.Email, claims.Name, "google", claims.Sub)
	if err != nil {
		return TokenPair{}, err
	}

	return s.issueTokenPair(user, ua, ip)
}

// GetByID returns the user with the given ID.
func (s *AuthService) GetByID(id string) (*User, error) {
	return s.repo.FindUserByID(id)
}

// UpdateProfile updates the user's name and/or avatar_url.
func (s *AuthService) UpdateProfile(userID string, name, avatarURL *string) (*User, error) {
	return s.repo.UpdateUserProfile(userID, name, avatarURL)
}
