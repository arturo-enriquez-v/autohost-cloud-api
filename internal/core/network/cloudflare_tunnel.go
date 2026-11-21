// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package network

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultCFTunnelName = "autohost-gateway"
	// CloudflareProxyAddr is the local reverse proxy cloudflared forwards unmatched traffic to.
	CloudflareProxyAddr = "127.0.0.1:18080"
	maxTunnelLogLines   = 40
)

var (
	ErrCFRouteNotFound = errors.New("route not found")
	ErrCFDomainTaken   = errors.New("domain is already routed to a node you cannot manage")
	ErrCFInvalidDomain = errors.New("invalid domain")
	ErrCFPersist       = errors.New("could not persist tunnel state")
	cfHostnameRe       = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	cfSubdomainRe      = regexp.MustCompile(`^(?:@|[a-z0-9](?:[a-z0-9.-]{0,200}[a-z0-9])?)$`)
	cfLabelSanitizer   = regexp.MustCompile(`[^a-z0-9-]+`)
	cfAuthURLRe        = regexp.MustCompile(`https://dash\.cloudflare\.com/[^\s"']+`)
	tryCloudflareURLRe = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)
	uuidRe             = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
)

// IsValidHostname reports whether s is a DNS hostname accepted for tunnel routes and base domains.
func IsValidHostname(s string) bool { return cfHostnameRe.MatchString(s) }

// CloudflareRouteMirror keeps another view of tunnel routes in sync (the Caddy routes of the master node).
type CloudflareRouteMirror interface {
	Upsert(domain, upstream, userID string)
	Delete(domain string)
}

// CloudflareTunnelConfig wires the tunnel manager's dependencies.
type CloudflareTunnelConfig struct {
	Store GatewayStateStore
	// BaseDir holds config.yml, the downloaded binary and the cloudflared home (.cloudflared/ with cert.pem
	// and tunnel credentials).
	BaseDir string
	// LegacyStatePath is the old state.json, imported once into the store and then renamed.
	LegacyStatePath string
	Mirror          CloudflareRouteMirror
}

// CloudflareTunnelStatus is a snapshot of the tunnel for the API.
type CloudflareTunnelStatus struct {
	Installed      bool
	Authenticated  bool
	Running        bool
	LoginInProcess bool
	LoginURL       string
	LoginError     string
	State          CloudflareTunnelState // copy, including routes
	RecentLogs     []string
}

// CloudflareTunnel manages the master's Cloudflare Tunnel: persisted configuration and routes, the
// cloudflared login/run processes, config.yml, DNS records and the internal reverse proxy. HTTP handlers only
// authenticate/authorize callers.
type CloudflareTunnel struct {
	cfg        CloudflareTunnelConfig
	homeDir    string
	configPath string

	// mu guards the persisted state; mutations go through mutate (copy, persist, publish).
	mu    sync.RWMutex
	state *CloudflareTunnelState

	// procMu guards the cloudflared processes and their transient output.
	procMu       sync.Mutex
	loginCmd     *exec.Cmd
	loginURL     string
	loginErr     string
	loginRunning bool
	tunnelCmd    *exec.Cmd
	logs         []string
}

func NewCloudflareTunnel(cfg CloudflareTunnelConfig) *CloudflareTunnel {
	t := &CloudflareTunnel{
		cfg:        cfg,
		homeDir:    filepath.Join(cfg.BaseDir, ".cloudflared"),
		configPath: filepath.Join(cfg.BaseDir, "config.yml"),
		state:      &CloudflareTunnelState{TunnelName: defaultCFTunnelName, Mode: "managed", Routes: []*CloudflareTunnelRoute{}},
	}
	_ = os.MkdirAll(t.homeDir, 0700)
	t.load()
	return t
}

// Start launches the internal reverse proxy and, if a Cloudflare account is connected, resumes the tunnel.
func (t *CloudflareTunnel) Start() {
	t.startInternalProxy()
	go func() {
		time.Sleep(2 * time.Second)
		if !t.isCertPresent() {
			return
		}
		if t.BaseDomain() == "" {
			if detected := t.detectZoneFromCert(); detected != "" {
				_ = t.mutate(func(st *CloudflareTunnelState) error { st.BaseDomain = detected; return nil })
			}
		}
		if err := t.ensureTunnelAndRun(); err != nil {
			log.Printf("[WARN] cloudflared auto-resume failed: %v", err)
		}
	}()
}

// ─── State and persistence ───────────────────────────────────────────────────

func cloneCFState(st *CloudflareTunnelState) *CloudflareTunnelState {
	out := *st
	out.Routes = make([]*CloudflareTunnelRoute, 0, len(st.Routes))
	for _, r := range st.Routes {
		c := *r
		out.Routes = append(out.Routes, &c)
	}
	return &out
}

func (t *CloudflareTunnel) load() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cfg.Store == nil {
		log.Printf("[WARN] cloudflared: no state store configured; tunnel routes will not persist")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	st, err := t.cfg.Store.LoadCloudflareTunnelState(ctx)
	if err != nil {
		log.Printf("[ERROR] cloudflared: load state from database: %v", err)
		return
	}
	if st.TunnelID == "" && st.BaseDomain == "" && len(st.Routes) == 0 {
		if imported := t.importLegacyState(ctx); imported != nil {
			st = imported
		}
	}
	if st.TunnelName == "" {
		st.TunnelName = defaultCFTunnelName
	}
	if st.Mode == "" {
		st.Mode = "managed"
	}
	if st.Routes == nil {
		st.Routes = []*CloudflareTunnelRoute{}
	}
	t.state = st
}

func (t *CloudflareTunnel) importLegacyState(ctx context.Context) *CloudflareTunnelState {
	if t.cfg.LegacyStatePath == "" {
		return nil
	}
	data, err := os.ReadFile(t.cfg.LegacyStatePath)
	if err != nil {
		return nil
	}
	var legacy CloudflareTunnelState
	if err := json.Unmarshal(data, &legacy); err != nil {
		log.Printf("[WARN] cloudflared: cannot parse legacy %s: %v", t.cfg.LegacyStatePath, err)
		return nil
	}
	if err := t.cfg.Store.SaveCloudflareTunnelState(ctx, &legacy); err != nil {
		log.Printf("[ERROR] cloudflared: import legacy state into database: %v", err)
		return nil
	}
	reloaded, err := t.cfg.Store.LoadCloudflareTunnelState(ctx)
	if err != nil {
		reloaded = &legacy
	}
	if err := os.Rename(t.cfg.LegacyStatePath, t.cfg.LegacyStatePath+".migrated"); err != nil {
		log.Printf("[WARN] cloudflared: imported legacy state but could not rename %s: %v", t.cfg.LegacyStatePath, err)
	}
	log.Printf("[INFO] cloudflared: imported tunnel state with %d route(s) from legacy %s", len(reloaded.Routes), t.cfg.LegacyStatePath)
	return reloaded
}

// mutate applies fn to a copy of the state, persists it, and publishes it only if the save succeeded.
func (t *CloudflareTunnel) mutate(fn func(st *CloudflareTunnelState) error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	next := cloneCFState(t.state)
	if err := fn(next); err != nil {
		return err
	}
	if t.cfg.Store == nil {
		return fmt.Errorf("%w: no state store configured", ErrCFPersist)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := t.cfg.Store.SaveCloudflareTunnelState(ctx, next); err != nil {
		log.Printf("[ERROR] cloudflared: persist state: %v", err)
		return fmt.Errorf("%w: %v", ErrCFPersist, err)
	}
	t.state = next
	return nil
}

func (t *CloudflareTunnel) snapshot() *CloudflareTunnelState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return cloneCFState(t.state)
}

// BaseDomain returns the configured base domain.
func (t *CloudflareTunnel) BaseDomain() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state.BaseDomain
}

// Route returns a copy of the route for domain.
func (t *CloudflareTunnel) Route(domain string) (*CloudflareTunnelRoute, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, rt := range t.state.Routes {
		if strings.EqualFold(rt.Domain, domain) {
			c := *rt
			return &c, true
		}
	}
	return nil, false
}

func (t *CloudflareTunnel) appendLog(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	t.procMu.Lock()
	defer t.procMu.Unlock()
	t.logs = append(t.logs, line)
	if len(t.logs) > maxTunnelLogLines {
		t.logs = t.logs[len(t.logs)-maxTunnelLogLines:]
	}
}

// Status returns a snapshot of the tunnel.
func (t *CloudflareTunnel) Status() CloudflareTunnelStatus {
	_, binErr := exec.LookPath("cloudflared")
	if binErr != nil {
		_, binErr = os.Stat(filepath.Join(t.cfg.BaseDir, "bin", "cloudflared"))
	}
	st := t.snapshot()
	t.procMu.Lock()
	defer t.procMu.Unlock()
	return CloudflareTunnelStatus{
		Installed:      binErr == nil,
		Authenticated:  t.isCertPresent(),
		Running:        t.tunnelCmd != nil && t.tunnelCmd.Process != nil,
		LoginInProcess: t.loginRunning,
		LoginURL:       t.loginURL,
		LoginError:     t.loginErr,
		State:          *st,
		RecentLogs:     append([]string{}, t.logs...),
	}
}

// ─── Configuration ───────────────────────────────────────────────────────────

// SetBaseDomain validates and stores the platform-wide base domain, then reloads the tunnel if connected.
func (t *CloudflareTunnel) SetBaseDomain(raw string) (string, error) {
	clean := strings.ToLower(strings.TrimSpace(raw))
	clean = strings.TrimPrefix(clean, "https://")
	clean = strings.TrimPrefix(clean, "http://")
	clean = strings.TrimSuffix(clean, "/")
	if clean != "" && !IsValidHostname(clean) {
		return "", fmt.Errorf("%w: base_domain", ErrCFInvalidDomain)
	}
	if err := t.mutate(func(st *CloudflareTunnelState) error { st.BaseDomain = clean; return nil }); err != nil {
		return "", err
	}
	if t.isCertPresent() {
		_ = t.ensureTunnelAndRun()
	}
	return clean, nil
}

// ResolveRouteDomain computes the full hostname of a route from an explicit domain, or subdomain + base
// (defaulting to the configured base domain), falling back to "<label>.local" for quick tunnels. It returns
// the full domain and the normalized subdomain and base.
func (t *CloudflareTunnel) ResolveRouteDomain(subdomain, baseDomain, domain, nodeHostname string, port int) (string, string, string, error) {
	sub := strings.ToLower(strings.TrimSpace(subdomain))
	base := strings.ToLower(strings.TrimSpace(baseDomain))
	if base == "" {
		base = t.BaseDomain()
	}
	full := strings.ToLower(strings.TrimSpace(domain))
	if full == "" && base != "" {
		if sub == "" || sub == "@" {
			full = base
		} else {
			full = sub + "." + base
		}
	}
	if full == "" {
		if sub != "" && sub != "@" {
			full = sub + ".local"
		} else {
			label := strings.Trim(cfLabelSanitizer.ReplaceAllString(strings.ToLower(nodeHostname), "-"), "-")
			if label == "" {
				label = "node"
			}
			full = fmt.Sprintf("%s-%d.local", label, port)
		}
	}
	// The domain ends up in cloudflared's YAML config and in `cloudflared tunnel route dns --overwrite-dns`.
	if sub != "" && !cfSubdomainRe.MatchString(sub) {
		return "", "", "", fmt.Errorf("%w: subdomain", ErrCFInvalidDomain)
	}
	if base != "" && !IsValidHostname(base) {
		return "", "", "", fmt.Errorf("%w: base_domain", ErrCFInvalidDomain)
	}
	if !IsValidHostname(full) {
		return "", "", "", ErrCFInvalidDomain
	}
	return full, sub, base, nil
}

// CloudflareRouteInput describes a route; Domain must come from ResolveRouteDomain and TargetIP must be an IP.
type CloudflareRouteInput struct {
	Domain         string
	Subdomain      string
	BaseDomain     string
	TargetNodeID   string
	TargetNodeName string
	TargetIP       string
	Port           int
	ContainerID    string
	ContainerName  string
	ContainerImage string
	UserID         string
}

// UpsertRoute stores the route (and the base domain, if given), mirrors it to Caddy and, when a Cloudflare
// account is connected, creates the DNS record and reloads cloudflared. An existing route for the same domain
// is only replaced when allowReplace is true (the caller verified it may manage it); otherwise
// ErrCFDomainTaken is returned.
func (t *CloudflareTunnel) UpsertRoute(in CloudflareRouteInput, allowReplace bool) (*CloudflareTunnelRoute, error) {
	if !IsValidHostname(in.Domain) {
		return nil, ErrCFInvalidDomain
	}
	if net.ParseIP(in.TargetIP) == nil || in.Port <= 0 || in.Port > 65535 {
		return nil, fmt.Errorf("invalid target address")
	}
	now := time.Now().UTC()
	route := CloudflareTunnelRoute{
		Domain: in.Domain, Subdomain: in.Subdomain, BaseDomain: in.BaseDomain,
		TargetNodeID: in.TargetNodeID, TargetNodeName: in.TargetNodeName, TargetIP: in.TargetIP, Port: in.Port,
		Upstream:    net.JoinHostPort(in.TargetIP, strconv.Itoa(in.Port)),
		ContainerID: strings.TrimSpace(in.ContainerID), ContainerName: strings.TrimSpace(in.ContainerName),
		ContainerImage: strings.TrimSpace(in.ContainerImage), CreatedAt: now, UpdatedAt: now,
	}

	var tunnelID string
	err := t.mutate(func(st *CloudflareTunnelState) error {
		if in.BaseDomain != "" {
			st.BaseDomain = in.BaseDomain
		}
		tunnelID = st.TunnelID
		for i, existing := range st.Routes {
			if strings.EqualFold(existing.Domain, route.Domain) {
				if !allowReplace {
					return ErrCFDomainTaken
				}
				route.CreatedAt = existing.CreatedAt
				c := route
				st.Routes[i] = &c
				return nil
			}
		}
		c := route
		st.Routes = append(st.Routes, &c)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if t.cfg.Mirror != nil {
		t.cfg.Mirror.Upsert(route.Domain, route.Upstream, in.UserID)
	}

	if t.isCertPresent() {
		if tunnelID == "" {
			_ = t.ensureTunnelAndRun()
			t.mu.RLock()
			tunnelID = t.state.TunnelID
			t.mu.RUnlock()
		}
		if tunnelID != "" && !strings.HasSuffix(route.Domain, ".local") {
			if configured, msg, ok := t.routeDNS(tunnelID, route.Domain); ok {
				route.DNSConfigured, route.DNSMessage = configured, msg
				_ = t.mutate(func(st *CloudflareTunnelState) error {
					for _, rt := range st.Routes {
						if strings.EqualFold(rt.Domain, route.Domain) {
							rt.DNSConfigured, rt.DNSMessage = configured, msg
						}
					}
					return nil
				})
			}
		}
		_ = t.writeConfig()
		_ = t.restartTunnel()
	}
	return &route, nil
}

// routeDNS points domain at the tunnel (overwriting an existing record). ok is false if cloudflared is missing.
func (t *CloudflareTunnel) routeDNS(tunnelID, domain string) (configured bool, message string, ok bool) {
	binPath, err := t.ensureBinary()
	if err != nil {
		return false, "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "tunnel", "route", "dns", "--overwrite-dns", tunnelID, domain)
	cmd.Env = t.commandEnv()
	out, dnsErr := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	t.appendLog("[dns] " + msg)
	return dnsErr == nil, msg, true
}

// DeleteRoute removes a route, its Caddy mirror, and reloads cloudflared.
func (t *CloudflareTunnel) DeleteRoute(domain string) error {
	err := t.mutate(func(st *CloudflareTunnelState) error {
		for i, rt := range st.Routes {
			if strings.EqualFold(rt.Domain, domain) {
				st.Routes = append(st.Routes[:i], st.Routes[i+1:]...)
				return nil
			}
		}
		return ErrCFRouteNotFound
	})
	if err != nil {
		return err
	}
	if t.cfg.Mirror != nil {
		t.cfg.Mirror.Delete(domain)
	}
	if t.isCertPresent() {
		_ = t.writeConfig()
		_ = t.restartTunnel()
	}
	return nil
}

// ─── Reverse proxy ───────────────────────────────────────────────────────────

// ResolveUpstream returns the upstream for a request Host: an exact domain route, or for *.trycloudflare.com
// quick tunnels the most recently added route.
func (t *CloudflareTunnel) ResolveUpstream(host string) string {
	clean := strings.ToLower(strings.TrimSpace(host))
	if idx := strings.IndexByte(clean, ':'); idx != -1 {
		clean = clean[:idx]
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, rt := range t.state.Routes {
		if strings.EqualFold(rt.Domain, clean) {
			return rt.Upstream
		}
	}
	if strings.HasSuffix(clean, ".trycloudflare.com") && len(t.state.Routes) > 0 {
		return t.state.Routes[len(t.state.Routes)-1].Upstream
	}
	return ""
}

func (t *CloudflareTunnel) startInternalProxy() {
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			upstream := t.ResolveUpstream(req.Host)
			if upstream == "" {
				req.URL.Scheme = "http"
				req.URL.Host = "127.0.0.1:9" // triggers ErrorHandler cleanly
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
			if req.Header.Get("X-Forwarded-Host") == "" {
				req.Header.Set("X-Forwarded-Host", req.Host)
			}
			if req.Header.Get("X-Forwarded-Proto") == "" {
				req.Header.Set("X-Forwarded-Proto", "https")
			}
		},
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		FlushInterval: -1, // flush immediately for SSE, WebSockets, and video streams
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>AutoHost Gateway — 502 Bad Gateway</title>
<style>body{background:#090d16;color:#e2e8f0;font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.card{background:#111827;border:1px solid #1e293b;border-radius:12px;padding:32px;max-width:480px;text-align:center}
h1{font-size:20px;margin:0 0 12px;color:#38bdf8}p{color:#94a3b8;font-size:14px;line-height:1.5}code{background:#1e293b;padding:2px 6px;border-radius:4px;color:#f8fafc}</style></head>
<body><div class="card"><h1>AutoHost Cloud Gateway</h1><p>No se pudo conectar con el contenedor destino para <code>%s</code>.</p></div></body></html>`,
				htmlEscape(r.Host))
		},
	}
	srv := &http.Server{Addr: CloudflareProxyAddr, Handler: rp}
	go func() {
		log.Printf("[CLOUDFLARE-GATEWAY] Dynamic multi-node reverse proxy listening on http://%s", CloudflareProxyAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[WARN] internal reverse proxy server exited: %v", err)
		}
	}()
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(s)
}

// ─── cloudflared processes ───────────────────────────────────────────────────

func (t *CloudflareTunnel) certPath() string { return filepath.Join(t.homeDir, "cert.pem") }

func (t *CloudflareTunnel) isCertPresent() bool {
	info, err := os.Stat(t.certPath())
	return err == nil && info.Size() > 0
}

func (t *CloudflareTunnel) commandEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "TUNNEL_ORIGIN_CERT=") {
			out = append(out, kv)
		}
	}
	return append(out, "HOME="+t.cfg.BaseDir, "TUNNEL_ORIGIN_CERT="+t.certPath())
}

// Pinned cloudflared release downloaded when no system binary is installed. The SHA-256 values come from the
// release notes and were verified against the downloaded assets. To use another release set both
// CLOUDFLARED_VERSION and CLOUDFLARED_SHA256 (hash of the binary for this host's architecture).
const pinnedCloudflaredVersion = "2026.10.7"

var pinnedCloudflaredSHA256 = map[string]string{
	"amd64": "1f99fb518b83dec2ba8d870a7cc991d82a77e6196295a3d2754ba17ab9f91872",
	"arm64": "3df43aedc9ff291d1735b5cb576cb97e0b157488796db2e32b28aecabb445775",
	"arm":   "74f690f6707182839b63e0465726c7776dd63f0a174390b911d5d71f35cab94e",
}

var (
	sha256HexRe          = regexp.MustCompile(`^[0-9a-f]{64}$`)
	cloudflaredVersionRe = regexp.MustCompile(`^[0-9A-Za-z._-]{1,40}$`)
)

// cloudflaredDownloadBase is the release download URL prefix (var for tests).
var cloudflaredDownloadBase = "https://github.com/cloudflare/cloudflared/releases/download"

// maxCloudflaredSize caps the download (current binaries are ~40 MB).
const maxCloudflaredSize = 200 << 20

// cloudflaredRelease returns the version, architecture and expected SHA-256 of the binary to install.
func cloudflaredRelease() (version, arch, sum string, err error) {
	switch runtime.GOARCH {
	case "amd64", "arm64", "arm":
		arch = runtime.GOARCH
	default:
		return "", "", "", fmt.Errorf("no cloudflared binary available for architecture %s; install cloudflared manually", runtime.GOARCH)
	}
	envVersion := strings.TrimSpace(os.Getenv("CLOUDFLARED_VERSION"))
	envSum := strings.ToLower(strings.TrimSpace(os.Getenv("CLOUDFLARED_SHA256")))
	switch {
	case envVersion == "" && envSum == "":
		return pinnedCloudflaredVersion, arch, pinnedCloudflaredSHA256[arch], nil
	case envVersion == "" || envSum == "":
		return "", "", "", errors.New("CLOUDFLARED_VERSION and CLOUDFLARED_SHA256 must be set together")
	case !sha256HexRe.MatchString(envSum):
		return "", "", "", errors.New("CLOUDFLARED_SHA256 must be a hex-encoded SHA-256")
	case !cloudflaredVersionRe.MatchString(envVersion):
		return "", "", "", errors.New("invalid CLOUDFLARED_VERSION")
	}
	return envVersion, arch, envSum, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ensureBinary returns a cloudflared binary to run. A system-installed binary (PATH, /usr/local/bin,
// /usr/bin) is trusted as installed by the administrator. Otherwise the pinned release is downloaded into
// BaseDir/bin and its SHA-256 verified before it is ever executed; a previously downloaded binary that does not
// match the expected hash (e.g. an unverified "latest" download from older versions) is replaced.
func (t *CloudflareTunnel) ensureBinary() (string, error) {
	if p, err := exec.LookPath("cloudflared"); err == nil && p != "" {
		return p, nil
	}
	for _, candidate := range []string{"/usr/local/bin/cloudflared", "/usr/bin/cloudflared"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	version, arch, wantSum, err := cloudflaredRelease()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(t.cfg.BaseDir, "bin")
	destPath := filepath.Join(binDir, "cloudflared")
	if got, err := fileSHA256(destPath); err == nil {
		if got == wantSum {
			return destPath, nil
		}
		log.Printf("[CLOUDFLARE-GATEWAY] %s does not match the expected SHA-256 for cloudflared %s; replacing it", destPath, version)
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", err
	}

	dlURL := fmt.Sprintf("%s/%s/cloudflared-linux-%s", strings.TrimRight(cloudflaredDownloadBase, "/"), version, arch)
	log.Printf("[CLOUDFLARE-GATEWAY] Downloading cloudflared %s from %s ...", version, dlURL)
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Get(dlURL)
	if err != nil {
		return "", fmt.Errorf("download cloudflared: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download cloudflared returned HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(binDir, ".cloudflared-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxCloudflaredSize+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("download cloudflared: %w", err)
	}
	if n > maxCloudflaredSize {
		return "", fmt.Errorf("download cloudflared: file exceeds %d bytes", maxCloudflaredSize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSum {
		return "", fmt.Errorf("cloudflared %s checksum mismatch: expected %s, got %s; refusing to install", version, wantSum, got)
	}
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", err
	}
	log.Printf("[CLOUDFLARE-GATEWAY] Installed cloudflared %s at %s (SHA-256 verified)", version, destPath)
	return destPath, nil
}

// StartLogin runs `cloudflared tunnel login` and returns the browser authorization URL (if captured within a
// few seconds). Completion is handled in the background: on success the zone is detected and the tunnel starts.
func (t *CloudflareTunnel) StartLogin() (string, error) {
	binPath, err := t.ensureBinary()
	if err != nil {
		return "", fmt.Errorf("could not prepare cloudflared: %w", err)
	}

	t.procMu.Lock()
	if t.loginCmd != nil && t.loginCmd.Process != nil {
		_ = t.loginCmd.Process.Kill()
	}
	_ = os.Remove(t.certPath()) // force a fresh login
	t.loginURL, t.loginErr, t.loginRunning = "", "", true
	t.procMu.Unlock()

	cmd := exec.Command(binPath, "tunnel", "login")
	cmd.Env = t.commandEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		t.procMu.Lock()
		t.loginRunning, t.loginErr = false, err.Error()
		t.procMu.Unlock()
		return "", fmt.Errorf("failed to start cloudflared login: %w", err)
	}
	t.procMu.Lock()
	t.loginCmd = cmd
	t.procMu.Unlock()

	urlFound := make(chan string, 1)
	scan := func(rc io.ReadCloser) {
		s := bufio.NewScanner(rc)
		for s.Scan() {
			line := s.Text()
			t.appendLog("[login] " + line)
			if match := cfAuthURLRe.FindString(line); match != "" {
				t.procMu.Lock()
				if t.loginURL == "" {
					t.loginURL = match
					select {
					case urlFound <- match:
					default:
					}
				}
				t.procMu.Unlock()
			}
		}
	}
	go scan(stdout)
	go scan(stderr)

	go func() {
		waitErr := cmd.Wait()
		t.procMu.Lock()
		t.loginRunning, t.loginCmd = false, nil
		t.procMu.Unlock()

		if t.isCertPresent() {
			t.appendLog("[login] Cloudflare cert.pem received successfully!")
			if detected := t.detectZoneFromCert(); detected != "" {
				_ = t.mutate(func(st *CloudflareTunnelState) error {
					st.BaseDomain, st.Mode = detected, "managed"
					return nil
				})
			}
			if err := t.ensureTunnelAndRun(); err != nil {
				t.procMu.Lock()
				t.loginErr = err.Error()
				t.procMu.Unlock()
				t.appendLog("[error] " + err.Error())
			}
		} else if waitErr != nil {
			t.procMu.Lock()
			t.loginErr = fmt.Sprintf("login exited before authorization completed: %v", waitErr)
			t.procMu.Unlock()
		}
	}()

	select {
	case u := <-urlFound:
		return u, nil
	case <-time.After(6 * time.Second):
		t.procMu.Lock()
		defer t.procMu.Unlock()
		return t.loginURL, nil
	}
}

// ensureTunnelAndRun creates (or reuses) the named tunnel, writes config.yml and starts `cloudflared tunnel run`.
func (t *CloudflareTunnel) ensureTunnelAndRun() error {
	binPath, err := t.ensureBinary()
	if err != nil {
		return err
	}
	st := t.snapshot()
	tunnelID, tunnelName := st.TunnelID, st.TunnelName
	if tunnelName == "" {
		tunnelName = defaultCFTunnelName
	}
	if tunnelID != "" {
		if _, err := os.Stat(filepath.Join(t.homeDir, tunnelID+".json")); err != nil {
			tunnelID = ""
		}
	}

	if tunnelID == "" {
		createName := tunnelName
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		out, err := t.runCloudflared(ctx, binPath, "tunnel", "create", createName)
		if err != nil && strings.Contains(strings.ToLower(out), "already exists") {
			createName = fmt.Sprintf("%s-%d", tunnelName, time.Now().Unix()%10000)
			out, err = t.runCloudflared(ctx, binPath, "tunnel", "create", createName)
		}
		if err != nil {
			return fmt.Errorf("create tunnel failed: %v (%s)", err, strings.TrimSpace(out))
		}
		if match := uuidRe.FindString(out); match != "" {
			tunnelID = match
		} else if entries, _ := os.ReadDir(t.homeDir); entries != nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					tunnelID = strings.TrimSuffix(e.Name(), ".json")
					break
				}
			}
		}
		if tunnelID == "" {
			return fmt.Errorf("could not determine created tunnel UUID")
		}
		_ = t.mutate(func(st *CloudflareTunnelState) error {
			st.TunnelID, st.TunnelName, st.Mode = tunnelID, createName, "managed"
			return nil
		})
	}

	if err := t.writeConfig(); err != nil {
		return err
	}
	return t.restartTunnel()
}

func (t *CloudflareTunnel) runCloudflared(ctx context.Context, binPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Env = t.commandEnv()
	out, err := cmd.CombinedOutput()
	t.appendLog("[" + strings.Join(args[:min(2, len(args))], "-") + "] " + string(out))
	return string(out), err
}

// BuildCloudflaredConfig renders config.yml. Every route is re-validated (state may come from older versions
// or be edited in the database): rows with an invalid hostname or upstream are skipped, since the values are
// written into YAML that cloudflared parses.
func BuildCloudflaredConfig(tunnelID, credFile string, routes []*CloudflareTunnelRoute, fallbackAddr string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "tunnel: %s\n", tunnelID)
	fmt.Fprintf(&sb, "credentials-file: %s\n", credFile)
	sb.WriteString("ingress:\n")
	for _, rt := range routes {
		host, port, err := net.SplitHostPort(rt.Upstream)
		portNum, perr := strconv.Atoi(port)
		if !IsValidHostname(rt.Domain) || err != nil || net.ParseIP(host) == nil || perr != nil || portNum <= 0 || portNum > 65535 {
			log.Printf("[WARN] cloudflared config: skipping invalid route %q -> %q", rt.Domain, rt.Upstream)
			continue
		}
		fmt.Fprintf(&sb, "  - hostname: %s\n", rt.Domain)
		fmt.Fprintf(&sb, "    service: http://%s\n", net.JoinHostPort(host, strconv.Itoa(portNum)))
		sb.WriteString("    originRequest:\n")
		sb.WriteString("      noTLSVerify: true\n")
	}
	// Catch-all forwards to the internal dynamic reverse proxy.
	fmt.Fprintf(&sb, "  - service: http://%s\n", fallbackAddr)
	return sb.String()
}

func (t *CloudflareTunnel) writeConfig() error {
	st := t.snapshot()
	if st.TunnelID == "" || !uuidRe.MatchString(st.TunnelID) {
		return nil
	}
	credFile := filepath.Join(t.homeDir, st.TunnelID+".json")
	return os.WriteFile(t.configPath, []byte(BuildCloudflaredConfig(st.TunnelID, credFile, st.Routes, CloudflareProxyAddr)), 0600)
}

// startProcess starts cmd as the tunnel process (replacing any previous one), streaming its output to the
// tunnel log through onLine.
func (t *CloudflareTunnel) startProcess(cmd *exec.Cmd, onLine func(string)) error {
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	t.procMu.Lock()
	if t.tunnelCmd != nil && t.tunnelCmd.Process != nil {
		_ = t.tunnelCmd.Process.Kill()
		t.tunnelCmd = nil
	}
	t.procMu.Unlock()

	if err := cmd.Start(); err != nil {
		return err
	}
	t.procMu.Lock()
	t.tunnelCmd = cmd
	t.procMu.Unlock()

	scan := func(rc io.ReadCloser) {
		if rc == nil {
			return
		}
		s := bufio.NewScanner(rc)
		for s.Scan() {
			onLine(s.Text())
		}
	}
	go scan(stdout)
	go scan(stderr)
	go func() {
		_ = cmd.Wait()
		t.procMu.Lock()
		if t.tunnelCmd == cmd {
			t.tunnelCmd = nil
		}
		t.procMu.Unlock()
	}()
	return nil
}

func (t *CloudflareTunnel) restartTunnel() error {
	binPath, err := t.ensureBinary()
	if err != nil {
		return err
	}
	tunnelID := t.snapshot().TunnelID
	if tunnelID == "" {
		return fmt.Errorf("missing tunnel ID")
	}
	cmd := exec.Command(binPath, "tunnel", "--config", t.configPath, "run", tunnelID)
	cmd.Env = t.commandEnv()
	if err := t.startProcess(cmd, t.appendLog); err != nil {
		return fmt.Errorf("start cloudflared tunnel run: %w", err)
	}
	return nil
}

// StartQuickTunnel starts a zero-account *.trycloudflare.com tunnel to the internal proxy and returns its URL
// (if printed within a few seconds).
func (t *CloudflareTunnel) StartQuickTunnel() (string, error) {
	binPath, err := t.ensureBinary()
	if err != nil {
		return "", fmt.Errorf("could not prepare cloudflared: %w", err)
	}
	_ = t.mutate(func(st *CloudflareTunnelState) error {
		st.Mode, st.QuickTunnelURL = "quick", ""
		return nil
	})

	found := make(chan string, 1)
	var once sync.Once
	cmd := exec.Command(binPath, "tunnel", "--url", "http://"+CloudflareProxyAddr)
	cmd.Env = t.commandEnv()
	err = t.startProcess(cmd, func(line string) {
		t.appendLog("[quick] " + line)
		if match := tryCloudflareURLRe.FindString(line); match != "" {
			once.Do(func() {
				_ = t.mutate(func(st *CloudflareTunnelState) error { st.QuickTunnelURL = match; return nil })
				found <- match
			})
		}
	})
	if err != nil {
		return "", fmt.Errorf("failed to start quick tunnel: %w", err)
	}

	select {
	case u := <-found:
		return u, nil
	case <-time.After(8 * time.Second):
		return t.snapshot().QuickTunnelURL, nil
	}
}

// Disconnect stops login and tunnel processes and removes the Cloudflare account certificate.
func (t *CloudflareTunnel) Disconnect() error {
	t.procMu.Lock()
	if t.loginCmd != nil && t.loginCmd.Process != nil {
		_ = t.loginCmd.Process.Kill()
		t.loginCmd = nil
	}
	if t.tunnelCmd != nil && t.tunnelCmd.Process != nil {
		_ = t.tunnelCmd.Process.Kill()
		t.tunnelCmd = nil
	}
	t.loginRunning, t.loginURL = false, ""
	t.procMu.Unlock()

	_ = os.Remove(t.certPath())
	return t.mutate(func(st *CloudflareTunnelState) error { st.QuickTunnelURL = ""; return nil })
}

// detectZoneFromCert decodes the ARGO TUNNEL TOKEN in cert.pem and asks Cloudflare's API for the zone name.
func (t *CloudflareTunnel) detectZoneFromCert() string {
	raw, err := os.ReadFile(t.certPath())
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: 8 * time.Second}
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return ""
		}
		var token struct {
			ZoneID   string `json:"zoneID"`
			APIToken string `json:"apiToken"`
		}
		if err := json.Unmarshal(block.Bytes, &token); err != nil {
			if decoded, err2 := base64.StdEncoding.DecodeString(string(block.Bytes)); err2 == nil {
				_ = json.Unmarshal(decoded, &token)
			}
		}
		if token.ZoneID == "" || token.APIToken == "" {
			continue
		}
		if name := fetchZoneName(client, token.ZoneID, token.APIToken); name != "" {
			log.Printf("[CLOUDFLARE-GATEWAY] Auto-detected Cloudflare zone domain: %s", name)
			return name
		}
	}
}

func fetchZoneName(client *http.Client, zoneID, apiToken string) string {
	req, err := http.NewRequest(http.MethodGet, "https://api.cloudflare.com/client/v4/zones/"+url.PathEscape(zoneID), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var zone struct {
		Result struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&zone); err != nil {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(zone.Result.Name))
	if !IsValidHostname(name) {
		return ""
	}
	return name
}
