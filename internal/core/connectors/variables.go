// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package connectors

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Limits on variables.
const (
	maxVariableValueLen   = 4096
	maxAllowedOrigins     = 10
	maxVariablesInProject = 100
)

// Variable is a value shared by the connectors of a project, referenced as {{vars.NAME}} in the URL
// (also as its base: "{{vars.API}}/users"), headers and body. A secret variable is stored encrypted,
// never returned, hidden in responses and only sent to its allowed origins.
type Variable struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Secret      bool   `json:"secret"`
	// Value of a plain variable; always empty for secret ones.
	Value          string    `json:"value"`
	HasValue       bool      `json:"has_value"`
	ValueCipher    string    `json:"-"`
	AllowedOrigins []string  `json:"allowed_origins"`
	CreatedBy      string    `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// VariableInput is a variable as sent by the panel. Value: nil keeps the stored one (updates only).
type VariableInput struct {
	ProjectID      string   `json:"project_id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Secret         bool     `json:"secret"`
	Value          *string  `json:"value,omitempty"`
	AllowedOrigins []string `json:"allowed_origins"`
}

// VariableRepository persists variables.
type VariableRepository interface {
	ListVariables(ctx context.Context, projectID string) ([]*Variable, error)
	GetVariable(ctx context.Context, id string) (*Variable, error)
	CreateVariable(ctx context.Context, v *Variable) (*Variable, error)
	UpdateVariable(ctx context.Context, v *Variable) (*Variable, error)
	DeleteVariable(ctx context.Context, id string) error
}

// normalizeOrigin turns "https://API.example.com/" into "https://api.example.com"; only a scheme, host
// and optional port are allowed.
func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not an origin like https://api.example.com", raw)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%q must be only scheme and host, like https://api.example.com", raw)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

func (in *VariableInput) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if !paramNameRe.MatchString(in.Name) {
		return invalid("variable name must be letters, digits and _ (max 64), starting with a letter or _")
	}
	if len(in.Description) > maxDescriptionLen {
		return invalid("description is too long (max %d characters)", maxDescriptionLen)
	}
	if in.Value != nil {
		if len(*in.Value) > maxVariableValueLen || strings.ContainsAny(*in.Value, "\r\n\x00") {
			return invalid("the value must be at most %d characters, without line breaks", maxVariableValueLen)
		}
		if in.Secret && *in.Value == "" {
			return invalid("a secret variable needs a value")
		}
	}
	if !in.Secret {
		in.AllowedOrigins = []string{}
		return nil
	}
	if len(in.AllowedOrigins) == 0 {
		return invalid("a secret variable needs at least one allowed origin (where it may be sent)")
	}
	if len(in.AllowedOrigins) > maxAllowedOrigins {
		return invalid("too many allowed origins (max %d)", maxAllowedOrigins)
	}
	seen := map[string]bool{}
	origins := make([]string, 0, len(in.AllowedOrigins))
	for _, o := range in.AllowedOrigins {
		n, err := normalizeOrigin(o)
		if err != nil {
			return invalid("allowed origin %v", err)
		}
		if !seen[n] {
			seen[n] = true
			origins = append(origins, n)
		}
	}
	sort.Strings(origins)
	in.AllowedOrigins = origins
	return nil
}

func (s *Service) setVariableValue(v *Variable, value string) error {
	if !v.Secret {
		v.Value, v.ValueCipher, v.HasValue = value, "", true
		return nil
	}
	if !s.box.Configured() {
		return ErrSecretsNotConfigured
	}
	sealed, err := s.box.Seal(value, v.ID)
	if err != nil {
		return err
	}
	v.Value, v.ValueCipher, v.HasValue = "", sealed, true
	return nil
}

// ListVariables returns the variables of a project.
func (s *Service) ListVariables(ctx context.Context, projectID string) ([]*Variable, error) {
	return s.vars.ListVariables(ctx, projectID)
}

// GetVariable returns a variable.
func (s *Service) GetVariable(ctx context.Context, id string) (*Variable, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrVariableNotFound
	}
	return s.vars.GetVariable(ctx, id)
}

// CreateVariable validates and stores a variable.
func (s *Service) CreateVariable(ctx context.Context, in VariableInput, createdBy string) (*Variable, error) {
	if in.ProjectID == "" {
		return nil, invalid("project_id is required")
	}
	if err := in.normalize(); err != nil {
		return nil, err
	}
	existing, err := s.vars.ListVariables(ctx, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(existing) >= maxVariablesInProject {
		return nil, invalid("a project can have at most %d variables", maxVariablesInProject)
	}
	v := &Variable{
		ID: uuid.NewString(), ProjectID: in.ProjectID, Name: in.Name, Description: in.Description,
		Secret: in.Secret, AllowedOrigins: in.AllowedOrigins, CreatedBy: createdBy,
	}
	value := ""
	if in.Value != nil {
		value = *in.Value
	} else if in.Secret {
		return nil, invalid("a secret variable needs a value")
	}
	if err := s.setVariableValue(v, value); err != nil {
		return nil, err
	}
	return s.vars.CreateVariable(ctx, v)
}

// UpdateVariable changes a variable. The project cannot change. The value must be sent again when the
// variable switches between secret and plain, or when a secret one gains an allowed origin.
func (s *Service) UpdateVariable(ctx context.Context, existing *Variable, in VariableInput) (*Variable, error) {
	in.ProjectID = existing.ProjectID
	if err := in.normalize(); err != nil {
		return nil, err
	}
	if in.Name != existing.Name {
		if err := s.checkVariableUnused(ctx, existing); err != nil {
			return nil, err
		}
	}
	if in.Value == nil {
		if in.Secret != existing.Secret {
			return nil, fmt.Errorf("%w: switching between secret and plain", ErrVariableValueRequired)
		}
		if in.Secret {
			for _, o := range in.AllowedOrigins {
				if !slices.Contains(existing.AllowedOrigins, o) {
					return nil, fmt.Errorf("%w: adding allowed origin %s", ErrVariableValueRequired, o)
				}
			}
		}
	}
	v := *existing
	v.Name, v.Description, v.Secret, v.AllowedOrigins = in.Name, in.Description, in.Secret, in.AllowedOrigins
	if in.Value != nil {
		if err := s.setVariableValue(&v, *in.Value); err != nil {
			return nil, err
		}
	}
	return s.vars.UpdateVariable(ctx, &v)
}

// DeleteVariable removes a variable that no connector uses.
func (s *Service) DeleteVariable(ctx context.Context, v *Variable) error {
	if err := s.checkVariableUnused(ctx, v); err != nil {
		return err
	}
	return s.vars.DeleteVariable(ctx, v.ID)
}

func (s *Service) checkVariableUnused(ctx context.Context, v *Variable) error {
	list, err := s.repo.ListByProjects(ctx, []string{v.ProjectID})
	if err != nil {
		return err
	}
	var users []string
	for _, c := range list {
		if slices.Contains(c.VariableRefs(), v.Name) {
			users = append(users, c.Name)
		}
	}
	if len(users) > 0 {
		sort.Strings(users)
		return fmt.Errorf("%w: %s", ErrVariableInUse, strings.Join(users, ", "))
	}
	return nil
}

// resolvedVar is a variable ready to be sent: the plain text of its value.
type resolvedVar struct {
	value   string
	secret  bool
	origins []string
}

// resolveVariables loads the variables a definition uses from its project, decrypting secret ones.
func (s *Service) resolveVariables(ctx context.Context, d *Definition) (map[string]resolvedVar, error) {
	refs := d.VariableRefs()
	if len(refs) == 0 {
		return nil, nil
	}
	if s.vars == nil {
		return nil, fmt.Errorf("%w: variables are not available", ErrInvalidParams)
	}
	list, err := s.vars.ListVariables(ctx, d.ProjectID)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*Variable, len(list))
	for _, v := range list {
		byName[v.Name] = v
	}
	out := make(map[string]resolvedVar, len(refs))
	for _, name := range refs {
		v, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w: variable %q is not defined in the project", ErrInvalidParams, name)
		}
		rv := resolvedVar{value: v.Value, secret: v.Secret, origins: v.AllowedOrigins}
		if v.Secret {
			if !s.box.Configured() {
				return nil, ErrSecretsNotConfigured
			}
			if rv.value, err = s.box.Open(v.ValueCipher, v.ID); err != nil {
				return nil, err
			}
		}
		out[name] = rv
	}
	return out, nil
}

// checkVariableUse checks at save time what can be known before running: every variable exists, a base
// URL variable is plain and gives a valid URL, and secret variables are allowed for a fixed URL's origin.
func (s *Service) checkVariableUse(ctx context.Context, d *Definition) error {
	vars, err := s.resolveVariables(ctx, d)
	if err != nil {
		return err
	}
	_, err = effectiveURL(d.URL, vars)
	return err
}

// effectiveURL is the URL template with its base variable (if any) replaced, after checking that every
// secret variable is allowed for the resulting origin. Placeholders in the path and query stay.
func effectiveURL(rawURL string, vars map[string]resolvedVar) (string, error) {
	target := rawURL
	if base, ok := BaseVariable(rawURL); ok {
		if vars[base].secret {
			return "", invalid("the base url variable %q cannot be secret", base)
		}
		var err error
		if target, err = withBase(rawURL, vars[base].value); err != nil {
			return "", err
		}
	}
	o := origin(target)
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if v := vars[name]; v.secret && !slices.Contains(v.origins, o) {
			return "", fmt.Errorf("%w: variable %q is not allowed for %s", ErrVariableOrigin, name, o)
		}
	}
	return target, nil
}
