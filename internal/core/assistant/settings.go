// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

var (
	ErrNotConfigured = errors.New("the assistant is not configured")
	ErrInvalid       = errors.New("invalid request")
)

// Settings is the assistant's configuration, set by an admin. The API key is stored encrypted and
// never returned.
type Settings struct {
	Enabled          bool      `json:"enabled"`
	BaseURL          string    `json:"base_url"`
	HasAPIKey        bool      `json:"has_api_key"`
	APIKeyCiphertext string    `json:"-"`
	DefaultModel     string    `json:"default_model"`
	Models           []string  `json:"models"`
	ReasoningEffort  string    `json:"reasoning_effort"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
	// FromEnv is true when no settings were saved and ASSISTANT_* variables are used.
	FromEnv bool `json:"from_env,omitempty"`
}

// SettingsInput is what an admin saves. APIKey: nil keeps the stored one, "" clears it.
type SettingsInput struct {
	Enabled         bool     `json:"enabled"`
	BaseURL         string   `json:"base_url"`
	APIKey          *string  `json:"api_key,omitempty"`
	DefaultModel    string   `json:"default_model"`
	Models          []string `json:"models"`
	ReasoningEffort string   `json:"reasoning_effort"`
}

// SettingsRepository stores the single settings row. Get returns nil, nil when nothing was saved.
type SettingsRepository interface {
	Get(ctx context.Context) (*Settings, error)
	Save(ctx context.Context, s *Settings, updatedBy string) (*Settings, error)
}

// settingsAAD binds the encrypted API key to the settings row.
const settingsAAD = "assistant-settings"

var reasoningEfforts = []string{"", "none", "low", "medium", "high"}

// envSettings is the configuration from the environment, used until an admin saves settings:
// ASSISTANT_BASE_URL (e.g. http://localhost:11434/v1), ASSISTANT_MODEL, ASSISTANT_API_KEY.
func envSettings() *Settings {
	base := strings.TrimSpace(os.Getenv("ASSISTANT_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("ASSISTANT_MODEL"))
	return &Settings{
		Enabled:      base != "" && model != "",
		BaseURL:      base,
		DefaultModel: model,
		HasAPIKey:    os.Getenv("ASSISTANT_API_KEY") != "",
		Models:       []string{},
		FromEnv:      true,
	}
}

func validateSettings(in *SettingsInput) error {
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.DefaultModel = strings.TrimSpace(in.DefaultModel)
	in.ReasoningEffort = strings.ToLower(strings.TrimSpace(in.ReasoningEffort))
	if in.BaseURL != "" {
		u, err := url.Parse(in.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: base_url must be an http(s) URL, e.g. http://localhost:11434/v1", ErrInvalid)
		}
		if u.User != nil {
			return fmt.Errorf("%w: put the API key in api_key, not in the URL", ErrInvalid)
		}
	}
	if in.Enabled && (in.BaseURL == "" || in.DefaultModel == "") {
		return fmt.Errorf("%w: base_url and default_model are required to enable the assistant", ErrInvalid)
	}
	if len(in.DefaultModel) > 200 || len(in.Models) > 30 {
		return fmt.Errorf("%w: too many or too long models", ErrInvalid)
	}
	models := []string{}
	for _, m := range in.Models {
		m = strings.TrimSpace(m)
		if m != "" && len(m) <= 200 && !slices.Contains(models, m) {
			models = append(models, m)
		}
	}
	in.Models = models
	if !slices.Contains(reasoningEfforts, in.ReasoningEffort) {
		return fmt.Errorf("%w: reasoning_effort must be empty, none, low, medium or high", ErrInvalid)
	}
	if in.APIKey != nil && (len(*in.APIKey) > 4096 || strings.ContainsAny(*in.APIKey, "\r\n")) {
		return fmt.Errorf("%w: api_key is not valid", ErrInvalid)
	}
	return nil
}

// allowedModels is the default model plus the extra ones users may pick.
func (s *Settings) allowedModels() []string {
	out := []string{s.DefaultModel}
	for _, m := range s.Models {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}
