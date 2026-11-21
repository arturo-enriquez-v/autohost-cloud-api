// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package network

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/platform"
)

var (
	ErrShareNotFound       = errors.New("share not found")
	ErrWrongSharePassword  = errors.New("wrong share password")
	ErrInvalidSharePasswd  = errors.New("invalid share password")
	ErrFunnelRouteNotFound = errors.New("funnel route not found")
	// ErrFunnelPathTaken is returned when the public port/path is already routed and the caller did not
	// prove it may replace that route.
	ErrFunnelPathTaken = errors.New("this public path is already routed to a node you cannot manage")
	// ErrGatewayPersist wraps storage failures; the in-memory state is left unchanged.
	ErrGatewayPersist = errors.New("could not persist gateway state")
)

// FunnelPortBindings maps the public Funnel HTTPS ports to the local reverse-proxy listeners.
var FunnelPortBindings = map[int]string{
	443:   "127.0.0.1:18443",
	8443:  "127.0.0.1:18444",
	10000: "127.0.0.1:18445",
}

// MinSharePasswordLen is the minimum length for custom share passwords (generated ones are 12 chars).
const MinSharePasswordLen = 8

// shareTokenTTL is how long a visitor's share session stays valid after unlocking.
const shareTokenTTL = 12 * time.Hour

// ShareGatewayConfig wires the gateway's dependencies.
type ShareGatewayConfig struct {
	Store GatewayStateStore
	// LegacyStatePath is the old state.json, imported once into the store and then renamed.
	LegacyStatePath string
	// CommandsDir is where funnel_sync.sh is written for the master agent to execute.
	CommandsDir string
	// RunOnMaster dispatches a script in CommandsDir to the master node's agent (script.exec).
	RunOnMaster func(scriptPath string) error
	// DetectDNSName returns the master's Tailscale DNS name ("" if unknown).
	DetectDNSName func() string
	// SigningSecret derives the HMAC key for visitor share tokens.
	SigningSecret string
}

// ShareGateway owns password-protected folder shares and Tailscale Funnel routes: their state and
// persistence, visitor share tokens, the Funnel reverse proxies and the host `tailscale funnel` sync.
// HTTP handlers only authenticate/authorize callers and translate requests.
//
// Mutations are applied to a copy of the state and only published after it is persisted; readers always
// receive copies, so callers can never mutate gateway state outside the lock.
type ShareGateway struct {
	cfg        ShareGatewayConfig
	signingKey []byte

	mu    sync.RWMutex
	state *ShareFunnelState
}

func NewShareGateway(cfg ShareGatewayConfig) *ShareGateway {
	secret := cfg.SigningSecret
	if secret == "" {
		secret = "autohost-default-share-secret-key"
	}
	// Separate domain key so a share token is cryptographically distinct from user JWTs.
	sum := sha256.Sum256([]byte("autohost-share-scope-v1:" + secret))
	g := &ShareGateway{
		cfg:        cfg,
		signingKey: sum[:],
		state:      &ShareFunnelState{Shares: []*SharedFolderLink{}, FunnelRoutes: []*TailscaleFunnelRoute{}},
	}
	g.Load()
	return g
}

// ─── State, persistence and legacy import ────────────────────────────────────

func cloneState(st *ShareFunnelState) *ShareFunnelState {
	out := &ShareFunnelState{
		FunnelDNSName: st.FunnelDNSName,
		Shares:        make([]*SharedFolderLink, 0, len(st.Shares)),
		FunnelRoutes:  make([]*TailscaleFunnelRoute, 0, len(st.FunnelRoutes)),
	}
	for _, s := range st.Shares {
		c := *s
		out.Shares = append(out.Shares, &c)
	}
	for _, r := range st.FunnelRoutes {
		c := *r
		out.FunnelRoutes = append(out.FunnelRoutes, &c)
	}
	return out
}

// Load (re)reads the state from the store; on the first start after the DB migration it imports the legacy
// state.json (hashing plaintext share passwords) and renames it.
func (g *ShareGateway) Load() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cfg.Store == nil {
		log.Printf("[WARN] public shares: no state store configured; shares will not persist")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	st, err := g.cfg.Store.LoadShareFunnelState(ctx)
	if err != nil {
		log.Printf("[ERROR] public shares: load state from database: %v", err)
		return
	}
	g.state = st
	if len(st.Shares) == 0 && len(st.FunnelRoutes) == 0 {
		g.importLegacyStateLocked(ctx)
	}
}

// legacyShareState mirrors the old state.json layout, where share passwords were stored in plaintext.
type legacyShareState struct {
	FunnelDNSName string `json:"funnel_dns_name"`
	Shares        []struct {
		SharedFolderLink
		Password string `json:"password"`
	} `json:"shares"`
	FunnelRoutes []*TailscaleFunnelRoute `json:"funnel_routes"`
}

func (g *ShareGateway) importLegacyStateLocked(ctx context.Context) {
	if g.cfg.LegacyStatePath == "" {
		return
	}
	data, err := os.ReadFile(g.cfg.LegacyStatePath)
	if err != nil {
		return
	}
	var legacy legacyShareState
	if err := json.Unmarshal(data, &legacy); err != nil {
		log.Printf("[WARN] public shares: cannot parse legacy %s: %v", g.cfg.LegacyStatePath, err)
		return
	}

	st := &ShareFunnelState{FunnelDNSName: legacy.FunnelDNSName, Shares: []*SharedFolderLink{}, FunnelRoutes: []*TailscaleFunnelRoute{}}
	if st.FunnelDNSName == "" {
		st.FunnelDNSName = g.state.FunnelDNSName
	}
	for _, ls := range legacy.Shares {
		share := ls.SharedFolderLink
		if ls.Password == "" {
			log.Printf("[WARN] public shares: skipping legacy share %s without a usable password", share.ID)
			continue
		}
		hash, err := platform.HashPassword(ls.Password)
		if err != nil {
			log.Printf("[WARN] public shares: skipping legacy share %s: %v", share.ID, err)
			continue
		}
		share.PasswordHash = hash
		st.Shares = append(st.Shares, &share)
	}
	if legacy.FunnelRoutes != nil {
		st.FunnelRoutes = legacy.FunnelRoutes
	}

	if err := g.cfg.Store.SaveShareFunnelState(ctx, st); err != nil {
		log.Printf("[ERROR] public shares: import legacy state into database: %v", err)
		return
	}
	// Reload so rows dropped by foreign keys (deleted nodes/users) are reflected in memory.
	if reloaded, err := g.cfg.Store.LoadShareFunnelState(ctx); err == nil {
		g.state = reloaded
	} else {
		g.state = st
	}
	if err := os.Rename(g.cfg.LegacyStatePath, g.cfg.LegacyStatePath+".migrated"); err != nil {
		log.Printf("[WARN] public shares: imported legacy state but could not rename %s: %v", g.cfg.LegacyStatePath, err)
	}
	log.Printf("[INFO] public shares: imported %d share(s) and %d funnel route(s) from legacy %s",
		len(g.state.Shares), len(g.state.FunnelRoutes), g.cfg.LegacyStatePath)
}

// mutate applies fn to a copy of the state, persists it, and publishes it only if the save succeeded.
func (g *ShareGateway) mutate(fn func(st *ShareFunnelState) error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	next := cloneState(g.state)
	if err := fn(next); err != nil {
		return err
	}
	if g.cfg.Store == nil {
		return fmt.Errorf("%w: no state store configured", ErrGatewayPersist)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := g.cfg.Store.SaveShareFunnelState(ctx, next); err != nil {
		log.Printf("[ERROR] public shares: persist state: %v", err)
		return fmt.Errorf("%w: %v", ErrGatewayPersist, err)
	}
	g.state = next
	return nil
}

// ─── Shares ──────────────────────────────────────────────────────────────────

// Shares returns copies of all shares.
func (g *ShareGateway) Shares() []*SharedFolderLink {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return cloneState(g.state).Shares
}

// Share returns a copy of the share with the given ID.
func (g *ShareGateway) Share(id string) (*SharedFolderLink, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, s := range g.state.Shares {
		if s.ID == id {
			c := *s
			return &c, true
		}
	}
	return nil, false
}

// NewShareInput describes a share to create. Authorization is the caller's responsibility.
type NewShareInput struct {
	OwnerID       string
	NodeID        string
	NodeName      string
	FolderPath    string
	Title         string
	Permission    SharePermission
	Password      string // empty: generate one
	FunnelEnabled bool
}

// CreateShare creates a share and returns it with the plaintext password (shown once; only its hash is kept).
func (g *ShareGateway) CreateShare(in NewShareInput) (*SharedFolderLink, string, error) {
	cleanPath := filepath.Clean("/" + strings.TrimSpace(in.FolderPath))
	perm := in.Permission
	if !IsValidSharePermission(perm) {
		perm = SharePermRead
	}
	pwd := strings.TrimSpace(in.Password)
	if pwd == "" {
		pwd = GenerateSharePassword()
	} else if err := ValidateSharePassword(pwd); err != nil {
		return nil, "", err
	}
	pwdHash, err := platform.HashPassword(pwd) // before taking the lock: bcrypt is slow
	if err != nil {
		return nil, "", fmt.Errorf("hash share password: %w", err)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		if cleanPath == "/" {
			title = fmt.Sprintf("Archivos de %s", in.NodeName)
		} else {
			title = filepath.Base(cleanPath)
		}
	}

	now := time.Now().UTC()
	share := &SharedFolderLink{
		ID: generateShortID("sh_"), OwnerID: in.OwnerID, NodeID: in.NodeID, NodeName: in.NodeName,
		FolderPath: cleanPath, Title: title, Permission: perm, PasswordHash: pwdHash,
		FunnelEnabled: in.FunnelEnabled, CreatedAt: now, UpdatedAt: now,
	}
	err = g.mutate(func(st *ShareFunnelState) error {
		if share.FunnelEnabled && st.FunnelDNSName != "" {
			share.FunnelURL = shareFunnelURL(st.FunnelDNSName, share.ID)
		}
		c := *share
		st.Shares = append(st.Shares, &c)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if share.FunnelEnabled {
		go g.SyncHostFunnel()
	}
	return share, pwd, nil
}

// ShareUpdate lists the fields to change (nil = unchanged).
type ShareUpdate struct {
	Title              *string
	Permission         *SharePermission
	Password           *string
	RegeneratePassword bool
	FunnelEnabled      *bool
}

// UpdateShare applies upd and returns the updated share plus the new plaintext password, if one was set.
func (g *ShareGateway) UpdateShare(id string, upd ShareUpdate) (*SharedFolderLink, string, error) {
	newPassword := ""
	if upd.RegeneratePassword {
		newPassword = GenerateSharePassword()
	} else if upd.Password != nil && strings.TrimSpace(*upd.Password) != "" {
		newPassword = strings.TrimSpace(*upd.Password)
		if err := ValidateSharePassword(newPassword); err != nil {
			return nil, "", err
		}
	}
	newHash := ""
	if newPassword != "" {
		h, err := platform.HashPassword(newPassword)
		if err != nil {
			return nil, "", fmt.Errorf("hash share password: %w", err)
		}
		newHash = h
	}

	var updated SharedFolderLink
	err := g.mutate(func(st *ShareFunnelState) error {
		for _, s := range st.Shares {
			if s.ID != id {
				continue
			}
			if upd.Title != nil && strings.TrimSpace(*upd.Title) != "" {
				s.Title = strings.TrimSpace(*upd.Title)
			}
			if upd.Permission != nil && IsValidSharePermission(*upd.Permission) {
				s.Permission = *upd.Permission
			}
			if newHash != "" {
				s.PasswordHash = newHash
			}
			if upd.FunnelEnabled != nil {
				s.FunnelEnabled = *upd.FunnelEnabled
				if s.FunnelEnabled && st.FunnelDNSName != "" {
					s.FunnelURL = shareFunnelURL(st.FunnelDNSName, s.ID)
				} else {
					s.FunnelURL = ""
				}
			}
			s.UpdatedAt = time.Now().UTC()
			updated = *s
			return nil
		}
		return ErrShareNotFound
	})
	if err != nil {
		return nil, "", err
	}
	go g.SyncHostFunnel()
	return &updated, newPassword, nil
}

// DeleteShare removes a share (its visitor tokens stop working immediately).
func (g *ShareGateway) DeleteShare(id string) error {
	return g.mutate(func(st *ShareFunnelState) error {
		for i, s := range st.Shares {
			if s.ID == id {
				st.Shares = append(st.Shares[:i], st.Shares[i+1:]...)
				return nil
			}
		}
		return ErrShareNotFound
	})
}

// Unlock checks a visitor's password and returns a share session token.
func (g *ShareGateway) Unlock(id, password string) (string, *SharedFolderLink, error) {
	share, ok := g.Share(id)
	if !ok {
		return "", nil, ErrShareNotFound
	}
	pwd := strings.TrimSpace(password)
	if pwd == "" || share.PasswordHash == "" || platform.CheckPassword(share.PasswordHash, pwd) != nil {
		return "", nil, ErrWrongSharePassword
	}
	token, err := g.issueToken(share, time.Now().Add(shareTokenTTL))
	if err != nil {
		return "", nil, err
	}
	return token, share, nil
}

type shareTokenClaims struct {
	ShareID    string          `json:"sid"`
	NodeID     string          `json:"nid"`
	FolderPath string          `json:"fp"`
	Permission SharePermission `json:"perm"`
	ExpiresAt  int64           `json:"exp"`
}

func (g *ShareGateway) issueToken(share *SharedFolderLink, expiresAt time.Time) (string, error) {
	payload, err := json.Marshal(shareTokenClaims{
		ShareID: share.ID, NodeID: share.NodeID, FolderPath: share.FolderPath,
		Permission: share.Permission, ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		return "", err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, g.signingKey)
	mac.Write([]byte(payloadB64))
	return payloadB64 + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyToken validates a visitor token for shareID and returns the share's current state (so a revoked
// share or a downgraded permission takes effect immediately).
func (g *ShareGateway) VerifyToken(token, shareID string) (*SharedFolderLink, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid share token format")
	}
	mac := hmac.New(sha256.New, g.signingKey)
	mac.Write([]byte(parts[0]))
	actualSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || subtle.ConstantTimeCompare(mac.Sum(nil), actualSig) != 1 {
		return nil, fmt.Errorf("invalid share token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid share token payload")
	}
	var claims shareTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("malformed share token claims")
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, fmt.Errorf("share session expired, please re-enter password")
	}
	if claims.ShareID != shareID {
		return nil, fmt.Errorf("share token mismatch")
	}
	share, ok := g.Share(shareID)
	if !ok {
		return nil, fmt.Errorf("share link has been revoked")
	}
	return share, nil
}

// ResolveJailedPath maps a visitor's path inside a shared folder to the real node path, guaranteeing it never
// escapes above shareRoot. It returns the node path and the cleaned visitor path.
func ResolveJailedPath(shareRoot, requestedSubPath string) (string, string, error) {
	cleanRoot := filepath.Clean("/" + strings.TrimSpace(shareRoot))
	cleanSub := filepath.Clean("/" + strings.TrimSpace(requestedSubPath))
	if cleanRoot == "/" {
		return cleanSub, cleanSub, nil
	}
	full := filepath.Clean(filepath.Join(cleanRoot, cleanSub))
	if full != cleanRoot && !strings.HasPrefix(full, cleanRoot+"/") {
		return "", "", fmt.Errorf("access denied: path outside shared folder")
	}
	return full, cleanSub, nil
}

// ─── Tailscale Funnel ────────────────────────────────────────────────────────

// FunnelStatus returns the Funnel DNS name (detecting and persisting it if unset) and copies of all routes.
func (g *ShareGateway) FunnelStatus() (string, []*TailscaleFunnelRoute) {
	g.mu.RLock()
	snap := cloneState(g.state)
	g.mu.RUnlock()
	if snap.FunnelDNSName == "" {
		snap.FunnelDNSName = g.EnsureDNSName()
	}
	return snap.FunnelDNSName, snap.FunnelRoutes
}

// EnsureDNSName detects the master's Tailscale DNS name and stores it if none is configured yet.
func (g *ShareGateway) EnsureDNSName() string {
	g.mu.RLock()
	current := g.state.FunnelDNSName
	g.mu.RUnlock()
	if current != "" || g.cfg.DetectDNSName == nil {
		return current
	}
	detected := g.cfg.DetectDNSName()
	if detected == "" {
		return ""
	}
	_ = g.mutate(func(st *ShareFunnelState) error {
		if st.FunnelDNSName == "" {
			st.FunnelDNSName = detected
		}
		return nil
	})
	return detected
}

// SetFunnelDNSName sets the platform-wide Funnel DNS name and refreshes derived public URLs.
func (g *ShareGateway) SetFunnelDNSName(raw string) (string, error) {
	clean := strings.ToLower(strings.TrimSpace(raw))
	clean = strings.TrimPrefix(clean, "https://")
	clean = strings.TrimPrefix(clean, "http://")
	clean = strings.TrimSuffix(clean, "/")
	clean = strings.TrimSuffix(clean, ".")
	err := g.mutate(func(st *ShareFunnelState) error {
		st.FunnelDNSName = clean
		for _, s := range st.Shares {
			if s.FunnelEnabled && clean != "" {
				s.FunnelURL = shareFunnelURL(clean, s.ID)
			}
		}
		for _, rt := range st.FunnelRoutes {
			rt.PublicURL = FunnelPublicURL(clean, rt.FunnelPort, rt.PathPrefix)
		}
		return nil
	})
	return clean, err
}

// FunnelRoute returns a copy of the route with the given ID.
func (g *ShareGateway) FunnelRoute(id string) (*TailscaleFunnelRoute, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, rt := range g.state.FunnelRoutes {
		if rt.ID == id {
			c := *rt
			return &c, true
		}
	}
	return nil, false
}

// FunnelRouteAt returns a copy of the route currently bound to a public port + path prefix, if any.
func (g *ShareGateway) FunnelRouteAt(funnelPort int, pathPrefix string) (*TailscaleFunnelRoute, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, rt := range g.state.FunnelRoutes {
		if rt.FunnelPort == funnelPort && rt.PathPrefix == pathPrefix {
			c := *rt
			return &c, true
		}
	}
	return nil, false
}

// FunnelRouteInput describes a container exposed through Funnel. TargetIP must be a valid IP of the node.
type FunnelRouteInput struct {
	FunnelPort     int
	PathPrefix     string
	TargetNodeID   string
	TargetNodeName string
	TargetIP       string
	ContainerPort  int
	ContainerID    string
	ContainerName  string
	ContainerImage string
}

// UpsertFunnelRoute creates or replaces the route for the input's port + path. If a route already occupies that
// public path, it is only replaced when its ID equals replaceID (the caller verified it may manage it);
// otherwise ErrFunnelPathTaken is returned.
func (g *ShareGateway) UpsertFunnelRoute(in FunnelRouteInput, replaceID string) (*TailscaleFunnelRoute, error) {
	if _, ok := FunnelPortBindings[in.FunnelPort]; !ok {
		in.FunnelPort = 443
	}
	if in.ContainerPort <= 0 || in.ContainerPort > 65535 {
		return nil, fmt.Errorf("invalid container_port")
	}
	if net.ParseIP(in.TargetIP) == nil {
		return nil, fmt.Errorf("target node has no valid IP address")
	}
	prefix, err := NormalizeFunnelPathPrefix(in.PathPrefix)
	if err != nil {
		return nil, err
	}

	var route TailscaleFunnelRoute
	err = g.mutate(func(st *ShareFunnelState) error {
		route = TailscaleFunnelRoute{
			ID: generateShortID("fn_"), FunnelPort: in.FunnelPort, PathPrefix: prefix,
			PublicURL:    FunnelPublicURL(st.FunnelDNSName, in.FunnelPort, prefix),
			TargetNodeID: in.TargetNodeID, TargetNodeName: in.TargetNodeName, TargetIP: in.TargetIP,
			ContainerPort: in.ContainerPort, Upstream: net.JoinHostPort(in.TargetIP, strconv.Itoa(in.ContainerPort)),
			ContainerID: strings.TrimSpace(in.ContainerID), ContainerName: strings.TrimSpace(in.ContainerName),
			ContainerImage: strings.TrimSpace(in.ContainerImage), CreatedAt: time.Now().UTC(),
		}
		for i, existing := range st.FunnelRoutes {
			if existing.FunnelPort == route.FunnelPort && existing.PathPrefix == prefix {
				if existing.ID != replaceID {
					return ErrFunnelPathTaken
				}
				route.ID = existing.ID
				c := route
				st.FunnelRoutes[i] = &c
				return nil
			}
		}
		c := route
		st.FunnelRoutes = append(st.FunnelRoutes, &c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	go g.SyncHostFunnel()
	return &route, nil
}

// DeleteFunnelRoute removes a route.
func (g *ShareGateway) DeleteFunnelRoute(id string) error {
	err := g.mutate(func(st *ShareFunnelState) error {
		for i, rt := range st.FunnelRoutes {
			if rt.ID == id {
				st.FunnelRoutes = append(st.FunnelRoutes[:i], st.FunnelRoutes[i+1:]...)
				return nil
			}
		}
		return ErrFunnelRouteNotFound
	})
	if err == nil {
		go g.SyncHostFunnel()
	}
	return err
}

// ResolveUpstream returns the upstream for a request on a Funnel port: the most specific matching path prefix
// wins, then the "/" catch-all.
func (g *ShareGateway) ResolveUpstream(funnelPort int, reqPath string) string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var rootMatch string
	for _, rt := range g.state.FunnelRoutes {
		if rt.FunnelPort != funnelPort {
			continue
		}
		if rt.PathPrefix != "" && rt.PathPrefix != "/" && strings.HasPrefix(reqPath, rt.PathPrefix) {
			return rt.Upstream
		}
		if rt.PathPrefix == "" || rt.PathPrefix == "/" {
			rootMatch = rt.Upstream
		}
	}
	return rootMatch
}

// StartFunnelProxies starts the local reverse proxies Tailscale Funnel forwards to.
func (g *ShareGateway) StartFunnelProxies() {
	for funnelPort, bindAddr := range FunnelPortBindings {
		fp, addr := funnelPort, bindAddr
		rp := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				upstream := g.ResolveUpstream(fp, req.URL.Path)
				if upstream == "" {
					req.URL.Scheme = "http"
					req.URL.Host = "127.0.0.1:9"
					return
				}
				if !strings.HasPrefix(upstream, "http://") && !strings.HasPrefix(upstream, "https://") {
					upstream = "http://" + upstream
				}
				u, err := url.Parse(upstream)
				if err != nil {
					return
				}
				req.URL.Scheme = u.Scheme
				req.URL.Host = u.Host
				req.Header.Set("X-Forwarded-Proto", "https")
			},
			Transport: &http.Transport{
				DialContext:     (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:    100,
				IdleConnTimeout: 90 * time.Second,
			},
			FlushInterval: -1,
		}
		srv := &http.Server{Addr: addr, Handler: rp}
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[WARN] funnel proxy on %s exited: %v", addr, err)
			}
		}()
	}
}

// BuildFunnelSyncScript renders the bash script that configures `tailscale funnel` on the master host. It runs
// as root, so every value is re-validated (state may come from older versions) and shell-quoted.
func BuildFunnelSyncScript(shares []*SharedFolderLink, routes []*TailscaleFunnelRoute) string {
	var sb strings.Builder
	sb.WriteString("#!/usr/bin/env bash\nset -e\n")
	sb.WriteString("TS_BIN=$(command -v tailscale || echo /usr/bin/tailscale)\n")
	sb.WriteString("if [ ! -x \"$TS_BIN\" ]; then exit 0; fi\n")

	for _, s := range shares {
		if s.FunnelEnabled {
			// Expose the share portal of the web frontend (port 3000) on 443.
			sb.WriteString("$TS_BIN funnel --bg --https=443 --set-path=/share http://127.0.0.1:3000/share || true\n")
			sb.WriteString("$TS_BIN funnel --bg --https=443 --set-path=/api/public-shares http://127.0.0.1:3000/api/public-shares || true\n")
			break
		}
	}

	for _, rt := range routes {
		if _, ok := FunnelPortBindings[rt.FunnelPort]; !ok {
			log.Printf("[WARN] funnel sync: skipping route %s with invalid port %d", rt.ID, rt.FunnelPort)
			continue
		}
		host, port, err := net.SplitHostPort(rt.Upstream)
		portNum, perr := strconv.Atoi(port)
		if err != nil || net.ParseIP(host) == nil || perr != nil || portNum <= 0 || portNum > 65535 {
			log.Printf("[WARN] funnel sync: skipping route %s with invalid upstream %q", rt.ID, rt.Upstream)
			continue
		}
		pathArg := ""
		if rt.PathPrefix != "" && rt.PathPrefix != "/" {
			clean, err := NormalizeFunnelPathPrefix(rt.PathPrefix)
			if err != nil {
				log.Printf("[WARN] funnel sync: skipping route %s with invalid path prefix %q", rt.ID, rt.PathPrefix)
				continue
			}
			pathArg = ShellQuote("--set-path="+clean) + " "
		}
		sb.WriteString(fmt.Sprintf("\"$TS_BIN\" funnel --bg --https=%d %s%s || true\n",
			rt.FunnelPort, pathArg, ShellQuote("http://"+net.JoinHostPort(host, strconv.Itoa(portNum)))))
	}
	return sb.String()
}

// SyncHostFunnel writes funnel_sync.sh and dispatches it to the master agent, so `tailscale funnel` runs on
// the host OS.
func (g *ShareGateway) SyncHostFunnel() {
	g.mu.RLock()
	snap := cloneState(g.state)
	g.mu.RUnlock()

	if g.cfg.CommandsDir == "" {
		return
	}
	_ = os.MkdirAll(g.cfg.CommandsDir, 0755)
	scriptPath := filepath.Join(g.cfg.CommandsDir, "funnel_sync.sh")
	if err := os.WriteFile(scriptPath, []byte(BuildFunnelSyncScript(snap.Shares, snap.FunnelRoutes)), 0700); err != nil {
		log.Printf("[WARN] write funnel_sync.sh: %v", err)
		return
	}
	if g.cfg.RunOnMaster != nil {
		if err := g.cfg.RunOnMaster(scriptPath); err != nil {
			log.Printf("[WARN] dispatch funnel_sync.sh: %v", err)
		}
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func shareFunnelURL(dnsName, shareID string) string {
	return fmt.Sprintf("https://%s/share/%s", dnsName, shareID)
}

// FunnelPublicURL builds the public URL of a Funnel route.
func FunnelPublicURL(dnsName string, funnelPort int, pathPrefix string) string {
	host := dnsName
	if host == "" {
		host = "tu-nodo.tailnet.ts.net"
	}
	portSuffix := ""
	if funnelPort != 443 {
		portSuffix = fmt.Sprintf(":%d", funnelPort)
	}
	cleanPrefix := "/" + strings.TrimPrefix(strings.TrimSpace(pathPrefix), "/")
	if cleanPrefix == "/" {
		cleanPrefix = ""
	}
	return fmt.Sprintf("https://%s%s%s", host, portSuffix, cleanPrefix)
}

// IsValidSharePermission reports whether p is a known share permission.
func IsValidSharePermission(p SharePermission) bool {
	return p == SharePermRead || p == SharePermWrite || p == SharePermEditor
}

// ValidateSharePassword enforces the length limits for custom share passwords (bcrypt uses at most 72 bytes).
func ValidateSharePassword(p string) error {
	if len(p) < MinSharePasswordLen {
		return fmt.Errorf("%w: must be at least %d characters", ErrInvalidSharePasswd, MinSharePasswordLen)
	}
	if len(p) > 72 {
		return fmt.Errorf("%w: must be at most 72 characters", ErrInvalidSharePasswd)
	}
	return nil
}

// GenerateSharePassword returns a random password like "AH-7KQ2-M9XP".
func GenerateSharePassword() string {
	const chars = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	var sb strings.Builder
	sb.WriteString("AH-")
	for i := 0; i < 8; i++ {
		if i == 4 {
			sb.WriteByte('-')
		}
		sb.WriteByte(chars[int(b[i])%len(chars)])
	}
	return sb.String()
}

func generateShortID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// safeFunnelPathRe matches URL path prefixes exposed through Tailscale Funnel.
var safeFunnelPathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,200}$`)

// NormalizeFunnelPathPrefix returns a clean "/"-prefixed path, or an error if it contains anything other than
// URL-safe path characters (it ends up in a root shell script).
func NormalizeFunnelPathPrefix(raw string) (string, error) {
	p := path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(raw), "/"))
	if !safeFunnelPathRe.MatchString(p) {
		return "", fmt.Errorf("invalid path_prefix %q", raw)
	}
	return p, nil
}

// ShellQuote wraps s in single quotes for safe use as one POSIX shell word.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
