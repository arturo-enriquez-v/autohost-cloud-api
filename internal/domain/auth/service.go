// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package auth

import (
	"errors"

	"github.com/arturo/autohost-cloud-api/internal/platform"
)

var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrUserAlreadyExists   = errors.New("user already exists")
	ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")
)

// Service encapsula la lógica de negocio de autenticación
type Service struct {
	repo Repository
}

// NewService crea una nueva instancia del servicio de autenticación
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// issueTokenPair firma un access token y genera un refresh token para el usuario dado.
func (s *Service) issueTokenPair(user *User, ua, ip string) (TokenPair, error) {
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

// Register registra un nuevo usuario
func (s *Service) Register(email, name, password string) (userID string, err error) {
	existing, _ := s.repo.FindUserByEmail(email)
	if existing != nil {
		return "", ErrUserAlreadyExists
	}

	hash, err := platform.HashPassword(password)
	if err != nil {
		return "", err
	}

	return s.repo.CreateUser(email, name, hash)
}

// RegisterAndIssue registra un nuevo usuario y emite tokens.
func (s *Service) RegisterAndIssue(email, name, password, ua, ip string) (TokenPair, error) {
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

// Login autentica a un usuario con email y contraseña
func (s *Service) Login(email, password string) (*User, error) {
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
func (s *Service) LoginAndIssue(email, password, ua, ip string) (TokenPair, error) {
	user, err := s.Login(email, password)
	if err != nil {
		return TokenPair{}, err
	}
	return s.issueTokenPair(user, ua, ip)
}

// RefreshTokens rota el refresh token y emite un nuevo par.
func (s *Service) RefreshTokens(oldPlain, ua, ip string) (TokenPair, error) {
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
func (s *Service) RevokeToken(plainToken string) {
	hash := platform.HashRefreshToken(plainToken)
	userID, err := s.repo.FindRefreshToken(hash)
	if err != nil {
		return
	}
	_ = s.repo.RevokeRefreshToken(userID, hash)
}

// LoginOAuth busca o crea un usuario mediante proveedor OAuth y emite tokens.
func (s *Service) LoginOAuth(email, name, provider, providerID string) (*User, error) {
	return s.repo.FindOrCreateOAuthUser(email, name, provider, providerID)
}

// OAuthLoginAndIssue verifica un ID token de Google y emite tokens.
func (s *Service) OAuthLoginAndIssue(idToken, ua, ip string) (TokenPair, error) {
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
func (s *Service) GetByID(id string) (*User, error) {
	return s.repo.FindUserByID(id)
}

// UpdateProfile updates the user's name and/or avatar_url.
func (s *Service) UpdateProfile(userID string, name, avatarURL *string) (*User, error) {
	return s.repo.UpdateUserProfile(userID, name, avatarURL)
}
