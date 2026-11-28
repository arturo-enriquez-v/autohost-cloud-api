// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package connectors

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/arturo/autohost-cloud-api/internal/platform/safehttp"
	"github.com/arturo/autohost-cloud-api/internal/platform/secretbox"
)

const (
	// runsKept is how many runs are kept per connector.
	runsKept = 200
	// maxCacheEntries bounds the in-memory query cache.
	maxCacheEntries = 1000
	// EnvAllowPrivate lets connectors reach private networks (LAN, VPN) from the API.
	EnvAllowPrivate = "CONNECTORS_ALLOW_PRIVATE_NETWORKS"
)

// Input is a definition plus the secret, as sent by the panel. Secret: nil keeps the stored one, ""
// clears it, any other value replaces it.
type Input struct {
	Definition
	Secret *string `json:"secret,omitempty"`
}

// Service holds the rules for connectors. Authorization is done by the handler.
type Service struct {
	repo   Repository
	vars   VariableRepository
	box    *secretbox.Box
	client Doer
	cache  *resultCache
	now    func() time.Time
}

// NewService builds the service from the environment: SECRETS_ENCRYPTION_KEY (an invalid key stops the
// API from starting; a missing one only disables storing credentials) and
// CONNECTORS_ALLOW_PRIVATE_NETWORKS.
func NewService(repo Repository, vars VariableRepository) (*Service, error) {
	box, err := secretbox.FromEnv()
	if err != nil {
		return nil, err
	}
	if !box.Configured() {
		log.Printf("[WARN] connectors: %s is not set, connectors cannot store credentials", secretbox.EnvKey)
	}
	allowPrivate := strings.EqualFold(strings.TrimSpace(os.Getenv(EnvAllowPrivate)), "true")
	client := safehttp.NewClient(safehttp.Options{AllowPrivate: allowPrivate, Timeout: maxTimeout * time.Second})
	return NewServiceWith(repo, box, client).WithVariables(vars), nil
}

// NewServiceWith builds the service with explicit dependencies (tests).
func NewServiceWith(repo Repository, box *secretbox.Box, client Doer) *Service {
	return &Service{repo: repo, box: box, client: client, cache: newResultCache(), now: time.Now}
}

// WithVariables enables project variables ({{vars.NAME}}); without them a connector using one fails.
func (s *Service) WithVariables(vars VariableRepository) *Service {
	s.vars = vars
	return s
}

// SecretsConfigured reports whether credentials can be stored.
func (s *Service) SecretsConfigured() bool { return s.box.Configured() }

// Get returns a connector.
func (s *Service) Get(ctx context.Context, id string) (*Connector, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	return s.repo.Get(ctx, id)
}

// ListByProjects returns the connectors of the given projects.
func (s *Service) ListByProjects(ctx context.Context, projectIDs []string) ([]*Connector, error) {
	if len(projectIDs) == 0 {
		return []*Connector{}, nil
	}
	return s.repo.ListByProjects(ctx, projectIDs)
}

// ListAll returns every connector (admins).
func (s *Service) ListAll(ctx context.Context) ([]*Connector, error) {
	return s.repo.ListAll(ctx)
}

// Create validates and stores a new connector.
func (s *Service) Create(ctx context.Context, in Input, createdBy string) (*Connector, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if err := s.checkVariableUse(ctx, &in.Definition); err != nil {
		return nil, err
	}
	c := &Connector{ID: uuid.NewString(), Definition: in.Definition, CreatedBy: createdBy}
	if in.Secret != nil && *in.Secret != "" && in.Auth.Type != AuthNone {
		if err := s.setSecret(c, *in.Secret); err != nil {
			return nil, err
		}
	}
	return s.repo.Create(ctx, c)
}

// Update replaces a connector's definition. The project cannot change. Stored credentials are kept
// unless replaced or cleared, but not when the URL's scheme or host changes (ErrSecretOriginChanged)
// or the auth type becomes "none".
func (s *Service) Update(ctx context.Context, existing *Connector, in Input) (*Connector, error) {
	in.ProjectID = existing.ProjectID
	in.Normalize()
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if err := s.checkVariableUse(ctx, &in.Definition); err != nil {
		return nil, err
	}
	c := *existing
	c.Definition = in.Definition
	switch {
	case in.Auth.Type == AuthNone:
		c.SecretCiphertext = ""
	case in.Secret != nil && *in.Secret == "":
		c.SecretCiphertext = ""
	case in.Secret != nil:
		if err := s.setSecret(&c, *in.Secret); err != nil {
			return nil, err
		}
	case existing.SecretCiphertext != "" && origin(existing.URL) != origin(in.URL):
		return nil, ErrSecretOriginChanged
	}
	c.HasSecret = c.SecretCiphertext != ""
	return s.repo.Update(ctx, &c)
}

// Delete removes a connector and its run history.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

func (s *Service) setSecret(c *Connector, secret string) error {
	if !s.box.Configured() {
		return ErrSecretsNotConfigured
	}
	if len(secret) > maxSecretLen || strings.ContainsAny(secret, "\r\n\x00") {
		return fmt.Errorf("%w: the secret is not valid", ErrInvalid)
	}
	sealed, err := s.box.Seal(secret, c.ID)
	if err != nil {
		return err
	}
	c.SecretCiphertext, c.HasSecret = sealed, true
	return nil
}

func (s *Service) openSecret(c *Connector) (string, error) {
	if c.SecretCiphertext == "" {
		return "", nil
	}
	if !s.box.Configured() {
		return "", ErrSecretsNotConfigured
	}
	return s.box.Open(c.SecretCiphertext, c.ID)
}

// Run executes a stored connector. Query results are cached for cache_ttl_seconds per parameter values;
// action runs are always executed and recorded, including failures.
func (s *Service) Run(ctx context.Context, c *Connector, params map[string]any, userID string) (*Result, error) {
	values, err := ResolveParams(c.Params, params)
	if err != nil {
		return nil, err
	}
	vars, err := s.resolveVariables(ctx, &c.Definition)
	if err != nil {
		return nil, err
	}
	key := cacheKey(c, values, vars)
	if c.Kind == KindQuery && c.CacheTTLSeconds > 0 {
		if res, ok := s.cache.get(key, s.now()); ok {
			return res, nil
		}
	}
	secret, err := s.openSecret(c)
	if err != nil {
		return nil, err
	}
	res, err := execute(ctx, s.client, &c.Definition, values, secret, vars, s.now())
	if c.Kind == KindAction && !errors.Is(err, ErrInvalidParams) && !errors.Is(err, ErrSecretRequired) &&
		!errors.Is(err, ErrVariableOrigin) {
		s.recordRun(c, userID, values, res, err)
	}
	if err != nil {
		return nil, err
	}
	if c.Kind == KindQuery && c.CacheTTLSeconds > 0 && res.OK {
		s.cache.put(key, res, s.now().Add(time.Duration(c.CacheTTLSeconds)*time.Second))
	}
	return res, nil
}

// Test executes an unsaved definition without caching or recording it. With an existing connector and
// no new secret, the stored secret is used, but only if the URL keeps its scheme and host.
func (s *Service) Test(ctx context.Context, in Input, existing *Connector, params map[string]any) (*Result, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return nil, err
	}
	values, err := ResolveParams(in.Params, params)
	if err != nil {
		return nil, err
	}
	vars, err := s.resolveVariables(ctx, &in.Definition)
	if err != nil {
		return nil, err
	}
	secret := ""
	switch {
	case in.Auth.Type == AuthNone:
	case in.Secret != nil:
		secret = *in.Secret
	case existing != nil && existing.SecretCiphertext != "":
		if origin(existing.URL) != origin(in.URL) {
			return nil, ErrSecretOriginChanged
		}
		if secret, err = s.openSecret(existing); err != nil {
			return nil, err
		}
	}
	return execute(ctx, s.client, &in.Definition, values, secret, vars, s.now())
}

// ListRuns returns the latest runs of a connector.
func (s *Service) ListRuns(ctx context.Context, connectorID string, limit int) ([]*Run, error) {
	if limit <= 0 || limit > runsKept {
		limit = 50
	}
	return s.repo.ListRuns(ctx, connectorID, limit)
}

func (s *Service) recordRun(c *Connector, userID string, values map[string]string, res *Result, runErr error) {
	r := &Run{ConnectorID: c.ID, UserID: userID, Params: loggableParams(c.Params, values)}
	if res != nil {
		r.StatusCode, r.OK, r.DurationMS = res.StatusCode, res.OK, res.DurationMS
	}
	if runErr != nil {
		r.Error = runErr.Error()
	}
	// Recorded even if the client went away mid-request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.repo.InsertRun(ctx, r, runsKept); err != nil {
		log.Printf("[ERROR] connectors: record run of %s: %v", c.ID, err)
	}
}

// resultCache keeps query results in memory. Each API instance has its own; that only means a few
// extra calls to the external API.
type resultCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	res     *Result
	expires time.Time
}

func newResultCache() *resultCache { return &resultCache{entries: map[string]cacheEntry{}} }

func (c *resultCache) get(key string, now time.Time) (*Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.After(e.expires) {
		return nil, false
	}
	cp := *e.res
	cp.Cached = true
	return &cp, true
}

func (c *resultCache) put(key string, res *Result, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCacheEntries {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxCacheEntries {
			c.entries = map[string]cacheEntry{}
		}
	}
	c.entries[key] = cacheEntry{res: res, expires: expires}
}
