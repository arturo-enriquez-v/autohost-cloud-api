// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"errors"
	"os"
	"testing"
)

type mockAuthRepo struct {
	users map[string]*User
}

func newMockAuthRepo() *mockAuthRepo {
	return &mockAuthRepo{users: make(map[string]*User)}
}

func (m *mockAuthRepo) CreateUser(email, name, passwordHash string) (string, error) {
	id := "user-" + email
	m.users[email] = &User{
		ID:           id,
		Email:        email,
		Name:         &name,
		PasswordHash: &passwordHash,
	}
	return id, nil
}

func (m *mockAuthRepo) FindUserByEmail(email string) (*User, error) {
	return m.users[email], nil
}

func (m *mockAuthRepo) FindUserByID(id string) (*User, error) {
	for _, u := range m.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, nil
}

func (m *mockAuthRepo) FindOrCreateOAuthUser(email, name, provider, providerID string) (*User, error) {
	if u, ok := m.users[email]; ok {
		return u, nil
	}
	id := "oauth-" + email
	u := &User{
		ID:              id,
		Email:           email,
		Name:            &name,
		OAuthProvider:   &provider,
		OAuthProviderID: &providerID,
	}
	m.users[email] = u
	return u, nil
}

func (m *mockAuthRepo) UpdateUserProfile(userID string, name, avatarURL *string) (*User, error) {
	return nil, nil
}

func (m *mockAuthRepo) StoreRefreshToken(userID, tokenHash, userAgent, ip string) error {
	return nil
}

func (m *mockAuthRepo) RevokeRefreshToken(userID, tokenHash string) error {
	return nil
}

func (m *mockAuthRepo) FindRefreshToken(tokenHash string) (string, error) {
	return "", nil
}

func (m *mockAuthRepo) CountUsers() (int, error) {
	return len(m.users), nil
}

func (m *mockAuthRepo) FindFirstUser() (*User, error) {
	for _, u := range m.users {
		return u, nil
	}
	return nil, nil
}

func TestSelfHostedSetupAndRegistrationLock(t *testing.T) {
	_ = os.Unsetenv("ALLOW_REGISTRATION")
	repo := newMockAuthRepo()
	svc := NewAuthService(repo)

	var claimedByUserID string
	svc.SetOnFirstUserCreated(func(userID string) {
		claimedByUserID = userID
	})

	// 1. Fresh instance: NeedsSetup = true, AllowRegistration = true
	status, err := svc.GetSetupStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.NeedsSetup || !status.AllowRegistration {
		t.Fatalf("expected NeedsSetup=true and AllowRegistration=true on empty DB, got %+v", status)
	}

	// 2. First user registers -> succeeds and triggers onFirstUserCreated hook
	firstID, err := svc.Register("admin@homelab.local", "Admin", "StrongPass123!")
	if err != nil {
		t.Fatalf("expected first user registration to succeed, got: %v", err)
	}
	if claimedByUserID != firstID {
		t.Fatalf("expected onFirstUserCreated hook to fire with %q, got %q", firstID, claimedByUserID)
	}

	// 3. After first user exists: NeedsSetup = false, AllowRegistration = false
	status, err = svc.GetSetupStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.NeedsSetup || status.AllowRegistration {
		t.Fatalf("expected NeedsSetup=false and AllowRegistration=false after setup, got %+v", status)
	}

	// 4. Second user attempts public registration -> blocked with ErrRegistrationDisabled
	_, err = svc.Register("intruder@homelab.local", "Intruder", "Pass123!")
	if !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("expected ErrRegistrationDisabled for 2nd user, got: %v", err)
	}

	// 5. Unknown user attempts Google OAuth signup -> blocked with ErrRegistrationDisabled
	_, err = svc.LoginOAuth("stranger@gmail.com", "Stranger", "google", "g-123")
	if !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("expected ErrRegistrationDisabled for unknown OAuth user, got: %v", err)
	}

	// 6. Existing admin logs in with Google OAuth -> succeeds!
	u, err := svc.LoginOAuth("admin@homelab.local", "Admin", "google", "g-admin")
	if err != nil || u == nil || u.ID != firstID {
		t.Fatalf("expected existing admin to log in via Google OAuth, got user=%v err=%v", u, err)
	}
}

