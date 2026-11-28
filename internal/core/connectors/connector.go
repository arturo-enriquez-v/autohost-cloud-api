// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

// Package connectors holds connectors: HTTP requests to external APIs that users define from the web
// panel (URL, method, headers, body, parameters and credentials) and run from it, without new Go code.
// A connector belongs to a project and is shared with its members. "query" connectors read data for
// widgets and can be cached; "action" connectors trigger changes and every run is recorded.
package connectors

import (
	"context"
	"errors"
	"time"
)

// Kinds of connector.
const (
	KindQuery  = "query"
	KindAction = "action"
)

// Body types.
const (
	BodyNone = "none"
	BodyJSON = "json"
	BodyForm = "form"
	BodyText = "text"
)

// Parameter types.
const (
	ParamString  = "string"
	ParamNumber  = "number"
	ParamBoolean = "boolean"
)

// Auth types. The secret (token, API key or password) is stored encrypted and never returned.
const (
	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthAPIKey = "api_key"
	AuthBasic  = "basic"
)

// Where an API key goes.
const (
	APIKeyInHeader = "header"
	APIKeyInQuery  = "query"
)

// Response views: a hint for the web panel on how to show the mapped data.
const (
	ViewJSON  = "json"
	ViewTable = "table"
	ViewValue = "value"
	ViewList  = "list"
)

var (
	ErrNotFound      = errors.New("connector not found")
	ErrNameExists    = errors.New("a connector with this name already exists in the project")
	ErrInvalid       = errors.New("invalid connector")
	ErrInvalidParams = errors.New("invalid parameters")
	// ErrSecretRequired: the connector uses authentication but has no credentials stored.
	ErrSecretRequired = errors.New("the connector has no credentials set")
	// ErrSecretOriginChanged: the URL's scheme or host changed while credentials are stored. They must be
	// entered again, so whoever edits a connector cannot send credentials they do not know elsewhere.
	ErrSecretOriginChanged = errors.New("credentials must be entered again when the URL scheme or host changes")
	// ErrSecretsNotConfigured: SECRETS_ENCRYPTION_KEY is not set, so credentials cannot be stored.
	ErrSecretsNotConfigured = errors.New("credential storage is not configured on the server")
	// ErrUpstream wraps failures to reach the external API (DNS, connection, timeout, blocked address).
	ErrUpstream = errors.New("could not reach the external API")

	ErrVariableNotFound = errors.New("variable not found")
	ErrVariableExists   = errors.New("a variable with this name already exists in the project")
	// ErrVariableInUse: deleting or renaming a variable that connectors reference would break them.
	ErrVariableInUse = errors.New("the variable is used by connectors")
	// ErrVariableValueRequired: the value must be entered again (adding allowed origins to a secret
	// variable, or switching between secret and plain), so whoever edits it cannot send or reveal a
	// secret they do not know.
	ErrVariableValueRequired = errors.New("the value must be entered again")
	// ErrVariableOrigin: a secret variable would be sent to an origin it is not allowed for.
	ErrVariableOrigin = errors.New("a secret variable cannot be sent to this origin")
)

// KeyValue is a header. Values may contain {{param}} and {{vars.NAME}} placeholders.
type KeyValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Param is a value the caller fills when running the connector, referenced as {{name}}.
type Param struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
	// Sensitive values are masked in the run history.
	Sensitive bool `json:"sensitive,omitempty"`
}

// Auth describes how the stored secret is sent.
type Auth struct {
	Type string `json:"type"`
	// Name of the header or query parameter for api_key.
	Name string `json:"name,omitempty"`
	// In is "header" or "query" for api_key.
	In string `json:"in,omitempty"`
	// Username for basic auth (the password is the secret).
	Username string `json:"username,omitempty"`
}

// FieldMapping picks one value out of each item of the response.
type FieldMapping struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// ResponseMapping turns a JSON response into rows: Root selects the item list (or a single object or
// value) and Fields the columns, with paths like "data.items[].name" or "results[0].id".
type ResponseMapping struct {
	Root   string         `json:"root,omitempty"`
	Fields []FieldMapping `json:"fields,omitempty"`
	View   string         `json:"view,omitempty"`
}

// Definition is the part of a connector a user edits.
type Definition struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Folder groups connectors in the panel; "/" separates levels ("Cloudflare/DNS"), "" is the top.
	Folder          string          `json:"folder"`
	Kind            string          `json:"kind"`
	Method          string          `json:"method"`
	URL             string          `json:"url"`
	Headers         []KeyValue      `json:"headers"`
	BodyType        string          `json:"body_type"`
	Body            string          `json:"body"`
	Params          []Param         `json:"params"`
	Auth            Auth            `json:"auth"`
	Response        ResponseMapping `json:"response"`
	CacheTTLSeconds int             `json:"cache_ttl_seconds"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
}

// Connector is a stored connector. The encrypted secret is never serialized.
type Connector struct {
	ID string `json:"id"`
	Definition
	HasSecret        bool      `json:"has_secret"`
	SecretCiphertext string    `json:"-"`
	CreatedBy        string    `json:"created_by,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Run is one recorded execution (action connectors).
type Run struct {
	ID          string            `json:"id"`
	ConnectorID string            `json:"connector_id"`
	UserID      string            `json:"user_id,omitempty"`
	Params      map[string]string `json:"params"`
	StatusCode  int               `json:"status_code"`
	OK          bool              `json:"ok"`
	Error       string            `json:"error,omitempty"`
	DurationMS  int64             `json:"duration_ms"`
	CreatedAt   time.Time         `json:"created_at"`
}

// Repository persists connectors and their run history.
type Repository interface {
	Create(ctx context.Context, c *Connector) (*Connector, error)
	Get(ctx context.Context, id string) (*Connector, error)
	ListByProjects(ctx context.Context, projectIDs []string) ([]*Connector, error)
	ListAll(ctx context.Context) ([]*Connector, error)
	Update(ctx context.Context, c *Connector) (*Connector, error)
	Delete(ctx context.Context, id string) error
	// InsertRun stores a run and keeps at most keep runs per connector.
	InsertRun(ctx context.Context, r *Run, keep int) error
	ListRuns(ctx context.Context, connectorID string, limit int) ([]*Run, error)
}
