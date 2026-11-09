// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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
	"strings"
	"sync"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
)

const (
	defaultTunnelName     = "autohost-gateway"
	internalProxyBindAddr = "127.0.0.1:18080"
)

// CloudflareTunnelRoute represents a reverse-proxy route exposed through the Master Cloudflare Tunnel
// and routed over Tailscale (or local loopback) to a container on any node.
type CloudflareTunnelRoute struct {
	Domain         string    `json:"domain"`
	Subdomain      string    `json:"subdomain"`
	BaseDomain     string    `json:"base_domain"`
	TargetNodeID   string    `json:"target_node_id"`
	TargetNodeName string    `json:"target_node_name"`
	TargetIP       string    `json:"target_ip"`
	Port           int       `json:"port"`
	Upstream       string    `json:"upstream"`
	ContainerID    string    `json:"container_id,omitempty"`
	ContainerName  string    `json:"container_name,omitempty"`
	ContainerImage string    `json:"container_image,omitempty"`
	DNSConfigured  bool      `json:"dns_configured"`
	DNSMessage     string    `json:"dns_message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CloudflareTunnelState is persisted to /var/lib/autohost/cloudflared/state.json.
type CloudflareTunnelState struct {
	TunnelID       string                   `json:"tunnel_id"`
	TunnelName     string                   `json:"tunnel_name"`
	BaseDomain     string                   `json:"base_domain"`
	Mode           string                   `json:"mode"` // "managed" (1-click login) | "quick" (trycloudflare) | "token"
	QuickTunnelURL string                   `json:"quick_tunnel_url,omitempty"`
	Routes         []*CloudflareTunnelRoute `json:"routes"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

// CloudflareTunnelStatusResponse is returned by GET /v1/cloudflare-tunnel/status.
type CloudflareTunnelStatusResponse struct {
	Installed      bool                     `json:"installed"`
	Authenticated  bool                     `json:"authenticated"`
	Running        bool                     `json:"running"`
	LoginInProcess bool                     `json:"login_in_process"`
	LoginURL       string                   `json:"login_url,omitempty"`
	LoginError     string                   `json:"login_error,omitempty"`
	Mode           string                   `json:"mode"`
	TunnelID       string                   `json:"tunnel_id"`
	TunnelName     string                   `json:"tunnel_name"`
	BaseDomain     string                   `json:"base_domain"`
	QuickTunnelURL string                   `json:"quick_tunnel_url,omitempty"`
	MasterNodeID   string                   `json:"master_node_id,omitempty"`
	MasterHostname string                   `json:"master_hostname,omitempty"`
	Routes         []*CloudflareTunnelRoute `json:"routes"`
	RecentLogs     []string                 `json:"recent_logs,omitempty"`
}

type CloudflareTunnelHandler struct {
	nodeSvc      *node.NodeService
	caddySvc     *apps.CaddyRouteService
	jobService   *jobs.JobService
	dispatcher   dispatcher.ParamDispatcher
	baseDir      string
	cfHomeDir    string
	statePath    string
	configPath   string
	mu           sync.RWMutex
	state        *CloudflareTunnelState
	loginCmd     *exec.Cmd
	loginURL     string
	loginErr     string
	loginRunning bool
	tunnelCmd    *exec.Cmd
	tunnelLogs   []string
	proxyServer  *http.Server
}

func NewCloudflareTunnelHandler(
	nodeSvc *node.NodeService,
	caddySvc *apps.CaddyRouteService,
	jobService *jobs.JobService,
	disp dispatcher.ParamDispatcher,
) *CloudflareTunnelHandler {
	baseDir := filepath.Join(platform.StorageDir(), "cloudflared")
	cfHomeDir := filepath.Join(baseDir, ".cloudflared")
	_ = os.MkdirAll(cfHomeDir, 0700)

	h := &CloudflareTunnelHandler{
		nodeSvc:    nodeSvc,
		caddySvc:   caddySvc,
		jobService: jobService,
		dispatcher: disp,
		baseDir:    baseDir,
		cfHomeDir:  cfHomeDir,
		statePath:  filepath.Join(baseDir, "state.json"),
		configPath: filepath.Join(baseDir, "config.yml"),
		state: &CloudflareTunnelState{
			TunnelName: defaultTunnelName,
			Mode:       "managed",
			Routes:     []*CloudflareTunnelRoute{},
		},
	}

	h.loadState()
	h.startInternalReverseProxy()

	// Auto-resume tunnel on startup if already authenticated or configured
	go func() {
		time.Sleep(2 * time.Second)
		if h.isCertPresent() {
			h.mu.RLock()
			baseDomain := h.state.BaseDomain
			h.mu.RUnlock()
			if baseDomain == "" {
				if detected := h.detectZoneFromCert(); detected != "" {
					h.mu.Lock()
					h.state.BaseDomain = detected
					h.saveStateLocked()
					h.mu.Unlock()
				}
			}
			if err := h.ensureTunnelAndRun(); err != nil {
				log.Printf("[WARN] cloudflared auto-resume failed: %v", err)
			}
		}
	}()

	return h
}

func (h *CloudflareTunnelHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth)
	r.Get("/status", h.GetStatus)
	r.Post("/login", h.StartLogin)
	r.Post("/quick-tunnel", h.StartQuickTunnel)
	r.Post("/config", h.UpdateConfig)
	r.Post("/disconnect", h.Disconnect)
	r.Post("/routes", h.UpsertRoute)
	r.Delete("/routes/{domain}", h.DeleteRoute)
	return r
}

// startInternalReverseProxy runs a lightweight dynamic reverse proxy on 127.0.0.1:18080
// so incoming traffic from cloudflared is dynamically routed by Host header (or default route
// for Quick Tunnels) to the target node's Tailscale IP and container port.
func (h *CloudflareTunnelHandler) startInternalReverseProxy() {
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			targetUpstream := h.resolveUpstreamForHost(req.Host)
			if targetUpstream == "" {
				req.URL.Scheme = "http"
				req.URL.Host = "127.0.0.1:9" // triggers ErrorHandler cleanly
				return
			}
			if !strings.HasPrefix(targetUpstream, "http://") && !strings.HasPrefix(targetUpstream, "https://") {
				targetUpstream = "http://" + targetUpstream
			}
			u, err := url.Parse(targetUpstream)
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
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 0, // support long streaming / WebSockets / video playback
		},
		FlushInterval: -1, // flush immediately for SSE, WebSockets, and video streams
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>AutoHost Gateway — 502 Bad Gateway</title>
<style>body{background:#090d16;color:#e2e8f0;font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.card{background:#111827;border:1px solid #1e293b;border-radius:12px;padding:32px;max-width:480px;text-align:center}
h1{font-size:20px;margin:0 0 12px;color:#38bdf8}p{color:#94a3b8;font-size:14px;line-height:1.5}code{background:#1e293b;padding:2px 6px;border-radius:4px;color:#f8fafc}</style></head>
<body><div class="card"><h1>AutoHost Cloud Gateway</h1><p>No se pudo conectar con el contenedor destino para <code>%s</code>.</p><p style="font-size:12px;color:#64748b">%v</p></div></body></html>`, r.Host, err)
		},
	}

	h.proxyServer = &http.Server{
		Addr:    internalProxyBindAddr,
		Handler: rp,
	}

	go func() {
		log.Printf("[CLOUDFLARE-GATEWAY] Dynamic multi-node reverse proxy listening on http://%s", internalProxyBindAddr)
		if err := h.proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[WARN] internal reverse proxy server exited: %v", err)
		}
	}()
}

func (h *CloudflareTunnelHandler) resolveUpstreamForHost(host string) string {
	cleanHost := strings.ToLower(strings.TrimSpace(host))
	if idx := strings.IndexByte(cleanHost, ':'); idx != -1 {
		cleanHost = cleanHost[:idx]
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, rt := range h.state.Routes {
		if strings.EqualFold(rt.Domain, cleanHost) {
			return rt.Upstream
		}
	}
	// If accessed via *.trycloudflare.com Quick Tunnel and there is at least one route, route to the most recently updated route
	if strings.HasSuffix(cleanHost, ".trycloudflare.com") && len(h.state.Routes) > 0 {
		return h.state.Routes[len(h.state.Routes)-1].Upstream
	}
	return ""
}

func (h *CloudflareTunnelHandler) loadState() {
	h.mu.Lock()
	defer h.mu.Unlock()

	data, err := os.ReadFile(h.statePath)
	if err != nil {
		return
	}
	var st CloudflareTunnelState
	if err := json.Unmarshal(data, &st); err == nil {
		if st.TunnelName == "" {
			st.TunnelName = defaultTunnelName
		}
		if st.Mode == "" {
			st.Mode = "managed"
		}
		if st.Routes == nil {
			st.Routes = []*CloudflareTunnelRoute{}
		}
		h.state = &st
	}
}

func (h *CloudflareTunnelHandler) saveStateLocked() {
	h.state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(h.state, "", "  ")
	if err != nil {
		log.Printf("[ERROR] marshal cloudflared state: %v", err)
		return
	}
	_ = os.MkdirAll(h.baseDir, 0700)
	if err := os.WriteFile(h.statePath, data, 0600); err != nil {
		log.Printf("[ERROR] write cloudflared state: %v", err)
	}
}

func (h *CloudflareTunnelHandler) appendLog(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tunnelLogs = append(h.tunnelLogs, line)
	if len(h.tunnelLogs) > 40 {
		h.tunnelLogs = h.tunnelLogs[len(h.tunnelLogs)-40:]
	}
}

func (h *CloudflareTunnelHandler) certPath() string {
	return filepath.Join(h.cfHomeDir, "cert.pem")
}

func (h *CloudflareTunnelHandler) isCertPresent() bool {
	info, err := os.Stat(h.certPath())
	return err == nil && info.Size() > 0
}

// ensureBinary locates cloudflared or downloads the official static binary if missing.
func (h *CloudflareTunnelHandler) ensureBinary() (string, error) {
	if p, err := exec.LookPath("cloudflared"); err == nil && p != "" {
		return p, nil
	}
	for _, candidate := range []string{
		"/usr/local/bin/cloudflared",
		"/usr/bin/cloudflared",
		filepath.Join(h.baseDir, "bin", "cloudflared"),
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	// Auto-download official cloudflared binary to /var/lib/autohost/cloudflared/bin/cloudflared
	arch := "amd64"
	switch runtime.GOARCH {
	case "arm64":
		arch = "arm64"
	case "arm":
		arch = "arm"
	}
	dlURL := fmt.Sprintf("https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-%s", arch)
	binDir := filepath.Join(h.baseDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", err
	}
	destPath := filepath.Join(binDir, "cloudflared")
	tmpPath := destPath + ".tmp"

	log.Printf("[CLOUDFLARE-GATEWAY] Downloading cloudflared binary from %s ...", dlURL)
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Get(dlURL)
	if err != nil {
		return "", fmt.Errorf("download cloudflared: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download cloudflared returned HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", err
	}
	log.Printf("[CLOUDFLARE-GATEWAY] Installed cloudflared at %s", destPath)
	return destPath, nil
}

func (h *CloudflareTunnelHandler) commandEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "TUNNEL_ORIGIN_CERT=") {
			filtered = append(filtered, kv)
		}
	}
	filtered = append(filtered,
		"HOME="+h.baseDir,
		"TUNNEL_ORIGIN_CERT="+h.certPath(),
	)
	return filtered
}

// detectZoneFromCert decodes the ARGO TUNNEL TOKEN PEM block inside cert.pem
// and queries Cloudflare's Zone API to auto-discover the authorized domain name.
func (h *CloudflareTunnelHandler) detectZoneFromCert() string {
	raw, err := os.ReadFile(h.certPath())
	if err != nil {
		return ""
	}
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "ARGO TUNNEL TOKEN") || len(block.Bytes) > 0 {
			var tokenPayload struct {
				ZoneID    string `json:"zoneID"`
				AccountID string `json:"accountID"`
				APIToken  string `json:"apiToken"`
			}
			// block.Bytes in pem.Decode is already base64-decoded
			if err := json.Unmarshal(block.Bytes, &tokenPayload); err != nil {
				// Fallback if double-encoded
				if decoded, err2 := base64.StdEncoding.DecodeString(string(block.Bytes)); err2 == nil {
					_ = json.Unmarshal(decoded, &tokenPayload)
				}
			}
			if tokenPayload.ZoneID != "" && tokenPayload.APIToken != "" {
				req, err := http.NewRequest("GET", "https://api.cloudflare.com/client/v4/zones/"+tokenPayload.ZoneID, nil)
				if err != nil {
					continue
				}
				req.Header.Set("Authorization", "Bearer "+tokenPayload.APIToken)
				client := &http.Client{Timeout: 8 * time.Second}
				resp, err := client.Do(req)
				if err != nil {
					continue
				}
				defer resp.Body.Close()
				var zoneResp struct {
					Success bool `json:"success"`
					Result  struct {
						Name string `json:"name"`
					} `json:"result"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&zoneResp); err == nil && zoneResp.Result.Name != "" {
					log.Printf("[CLOUDFLARE-GATEWAY] Auto-detected Cloudflare zone domain: %s", zoneResp.Result.Name)
					return strings.ToLower(strings.TrimSpace(zoneResp.Result.Name))
				}
			}
		}
	}
	return ""
}

// GetStatus returns the current state of the Master Cloudflare Tunnel and all multi-node routes.
// GET /v1/cloudflare-tunnel/status
func (h *CloudflareTunnelHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	_, binErr := exec.LookPath("cloudflared")
	if binErr != nil {
		_, binErr = os.Stat(filepath.Join(h.baseDir, "bin", "cloudflared"))
	}

	var masterID, masterHost string
	if h.nodeSvc != nil {
		if localNode, err := h.nodeSvc.FindLocalNode(); err == nil && localNode != nil {
			masterID = localNode.ID
			masterHost = localNode.Hostname
		}
	}

	h.mu.RLock()
	running := h.tunnelCmd != nil && h.tunnelCmd.Process != nil
	resp := CloudflareTunnelStatusResponse{
		Installed:      binErr == nil,
		Authenticated:  h.isCertPresent(),
		Running:        running,
		LoginInProcess: h.loginRunning,
		LoginURL:       h.loginURL,
		LoginError:     h.loginErr,
		Mode:           h.state.Mode,
		TunnelID:       h.state.TunnelID,
		TunnelName:     h.state.TunnelName,
		BaseDomain:     h.state.BaseDomain,
		QuickTunnelURL: h.state.QuickTunnelURL,
		MasterNodeID:   masterID,
		MasterHostname: masterHost,
		Routes:         h.state.Routes,
		RecentLogs:     append([]string{}, h.tunnelLogs...),
	}
	h.mu.RUnlock()

	if resp.Routes == nil {
		resp.Routes = []*CloudflareTunnelRoute{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

var cfAuthURLRegex = regexp.MustCompile(`https://dash\.cloudflare\.com/[^\s"']+`)
var tryCloudflareURLRegex = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)
var uuidRegex = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// StartLogin initiates `cloudflared tunnel login` and returns the 1-Click browser authorization URL.
// POST /v1/cloudflare-tunnel/login
func (h *CloudflareTunnelHandler) StartLogin(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	binPath, err := h.ensureBinary()
	if err != nil {
		http.Error(w, fmt.Sprintf("could not prepare cloudflared: %v", err), http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	if h.loginCmd != nil && h.loginCmd.Process != nil {
		_ = h.loginCmd.Process.Kill()
	}
	// Remove stale cert.pem if re-authenticating so cloudflared prompts for a fresh login
	_ = os.Remove(h.certPath())
	h.loginURL = ""
	h.loginErr = ""
	h.loginRunning = true
	h.mu.Unlock()

	cmd := exec.Command(binPath, "tunnel", "login")
	cmd.Env = h.commandEnv()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "failed to create stdout pipe", http.StatusInternalServerError)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		http.Error(w, "failed to create stderr pipe", http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		h.mu.Lock()
		h.loginRunning = false
		h.loginErr = err.Error()
		h.mu.Unlock()
		http.Error(w, fmt.Sprintf("failed to start cloudflared login: %v", err), http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	h.loginCmd = cmd
	h.mu.Unlock()

	urlFoundCh := make(chan string, 1)

	scanOutput := func(rc io.ReadCloser) {
		scanner := bufio.NewScanner(rc)
		for scanner.Scan() {
			line := scanner.Text()
			h.appendLog("[login] " + line)
			if match := cfAuthURLRegex.FindString(line); match != "" {
				h.mu.Lock()
				if h.loginURL == "" {
					h.loginURL = match
					select {
					case urlFoundCh <- match:
					default:
					}
				}
				h.mu.Unlock()
			}
		}
	}

	go scanOutput(stdoutPipe)
	go scanOutput(stderrPipe)

	// Wait in background for user to complete OAuth in browser and cert.pem to be written
	go func() {
		waitErr := cmd.Wait()
		h.mu.Lock()
		h.loginRunning = false
		h.loginCmd = nil
		h.mu.Unlock()

		if h.isCertPresent() {
			h.appendLog("[login] Cloudflare cert.pem received successfully!")
			if detectedDomain := h.detectZoneFromCert(); detectedDomain != "" {
				h.mu.Lock()
				h.state.BaseDomain = detectedDomain
				h.state.Mode = "managed"
				h.saveStateLocked()
				h.mu.Unlock()
			}
			if err := h.ensureTunnelAndRun(); err != nil {
				h.mu.Lock()
				h.loginErr = err.Error()
				h.mu.Unlock()
				h.appendLog("[error] " + err.Error())
			}
		} else if waitErr != nil {
			h.mu.Lock()
			h.loginErr = fmt.Sprintf("login exited before authorization completed: %v", waitErr)
			h.mu.Unlock()
		}
	}()

	// Wait up to 6 seconds to capture the URL immediately for the response
	var capturedURL string
	select {
	case capturedURL = <-urlFoundCh:
	case <-time.After(6 * time.Second):
		h.mu.RLock()
		capturedURL = h.loginURL
		h.mu.RUnlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":    "waiting_auth",
		"login_url": capturedURL,
	})
}

// ensureTunnelAndRun creates or reuses the `autohost-gateway` Cloudflare tunnel,
// generates config.yml for all routes, and starts `cloudflared tunnel run`.
func (h *CloudflareTunnelHandler) ensureTunnelAndRun() error {
	binPath, err := h.ensureBinary()
	if err != nil {
		return err
	}

	h.mu.RLock()
	tunnelID := h.state.TunnelID
	tunnelName := h.state.TunnelName
	if tunnelName == "" {
		tunnelName = defaultTunnelName
	}
	h.mu.RUnlock()

	// Verify if credentials file exists for current tunnelID
	if tunnelID != "" {
		credPath := filepath.Join(h.cfHomeDir, tunnelID+".json")
		if _, err := os.Stat(credPath); err != nil {
			tunnelID = ""
		}
	}

	// If no local tunnel credentials exist, create a new tunnel (with a timestamp suffix if name collides)
	if tunnelID == "" {
		createName := tunnelName
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, binPath, "tunnel", "create", createName)
		cmd.Env = h.commandEnv()
		out, err := cmd.CombinedOutput()
		outStr := string(out)
		h.appendLog("[tunnel-create] " + outStr)

		if err != nil && strings.Contains(strings.ToLower(outStr), "already exists") {
			// Delete stale tunnel with no local credentials or create with unique host suffix
			createName = fmt.Sprintf("%s-%d", tunnelName, time.Now().Unix()%10000)
			cmd2 := exec.CommandContext(ctx, binPath, "tunnel", "create", createName)
			cmd2.Env = h.commandEnv()
			out2, err2 := cmd2.CombinedOutput()
			outStr = string(out2)
			h.appendLog("[tunnel-create] " + outStr)
			err = err2
		}

		if err != nil {
			return fmt.Errorf("create tunnel failed: %v (%s)", err, strings.TrimSpace(outStr))
		}

		// Extract UUID from output or look for <uuid>.json in .cloudflared
		if match := uuidRegex.FindString(outStr); match != "" {
			tunnelID = match
		} else {
			entries, _ := os.ReadDir(h.cfHomeDir)
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

		h.mu.Lock()
		h.state.TunnelID = tunnelID
		h.state.TunnelName = createName
		h.state.Mode = "managed"
		h.saveStateLocked()
		h.mu.Unlock()
	}

	if err := h.writeConfigYAML(); err != nil {
		return err
	}

	return h.restartManagedTunnelProcess()
}

func (h *CloudflareTunnelHandler) writeConfigYAML() error {
	h.mu.RLock()
	tunnelID := h.state.TunnelID
	routes := append([]*CloudflareTunnelRoute{}, h.state.Routes...)
	h.mu.RUnlock()

	if tunnelID == "" {
		return nil
	}

	credFile := filepath.Join(h.cfHomeDir, tunnelID+".json")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("tunnel: %s\n", tunnelID))
	sb.WriteString(fmt.Sprintf("credentials-file: %s\n", credFile))
	sb.WriteString("ingress:\n")

	for _, rt := range routes {
		if rt.Domain == "" || rt.Upstream == "" {
			continue
		}
		serviceURL := rt.Upstream
		if !strings.HasPrefix(serviceURL, "http://") && !strings.HasPrefix(serviceURL, "https://") {
			serviceURL = "http://" + serviceURL
		}
		sb.WriteString(fmt.Sprintf("  - hostname: %s\n", rt.Domain))
		sb.WriteString(fmt.Sprintf("    service: %s\n", serviceURL))
		sb.WriteString("    originRequest:\n")
		sb.WriteString("      noTLSVerify: true\n")
	}

	// Fallback catch-all forwards to our internal dynamic reverse proxy on 127.0.0.1:18080
	sb.WriteString(fmt.Sprintf("  - service: http://%s\n", internalProxyBindAddr))

	return os.WriteFile(h.configPath, []byte(sb.String()), 0600)
}

func (h *CloudflareTunnelHandler) restartManagedTunnelProcess() error {
	binPath, err := h.ensureBinary()
	if err != nil {
		return err
	}

	h.mu.Lock()
	if h.tunnelCmd != nil && h.tunnelCmd.Process != nil {
		_ = h.tunnelCmd.Process.Kill()
		h.tunnelCmd = nil
	}
	tunnelID := h.state.TunnelID
	h.mu.Unlock()

	if tunnelID == "" {
		return fmt.Errorf("missing tunnel ID")
	}

	cmd := exec.Command(binPath, "tunnel", "--config", h.configPath, "run", tunnelID)
	cmd.Env = h.commandEnv()

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start cloudflared tunnel run: %w", err)
	}

	h.mu.Lock()
	h.tunnelCmd = cmd
	h.mu.Unlock()

	scanLogs := func(rc io.ReadCloser) {
		if rc == nil {
			return
		}
		s := bufio.NewScanner(rc)
		for s.Scan() {
			h.appendLog(s.Text())
		}
	}
	go scanLogs(stdout)
	go scanLogs(stderr)

	go func(c *exec.Cmd) {
		_ = c.Wait()
		h.mu.Lock()
		if h.tunnelCmd == c {
			h.tunnelCmd = nil
		}
		h.mu.Unlock()
	}(cmd)

	return nil
}

// StartQuickTunnel starts a zero-account `*.trycloudflare.com` tunnel pointing to the internal reverse proxy.
// POST /v1/cloudflare-tunnel/quick-tunnel
func (h *CloudflareTunnelHandler) StartQuickTunnel(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	binPath, err := h.ensureBinary()
	if err != nil {
		http.Error(w, fmt.Sprintf("could not prepare cloudflared: %v", err), http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	if h.tunnelCmd != nil && h.tunnelCmd.Process != nil {
		_ = h.tunnelCmd.Process.Kill()
		h.tunnelCmd = nil
	}
	h.state.Mode = "quick"
	h.state.QuickTunnelURL = ""
	h.saveStateLocked()
	h.mu.Unlock()

	cmd := exec.Command(binPath, "tunnel", "--url", "http://"+internalProxyBindAddr)
	cmd.Env = h.commandEnv()

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("failed to start quick tunnel: %v", err), http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	h.tunnelCmd = cmd
	h.mu.Unlock()

	foundCh := make(chan string, 1)
	scanQuick := func(rc io.ReadCloser) {
		if rc == nil {
			return
		}
		s := bufio.NewScanner(rc)
		for s.Scan() {
			line := s.Text()
			h.appendLog("[quick] " + line)
			if match := tryCloudflareURLRegex.FindString(line); match != "" {
				h.mu.Lock()
				if h.state.QuickTunnelURL == "" {
					h.state.QuickTunnelURL = match
					h.saveStateLocked()
					select {
					case foundCh <- match:
					default:
					}
				}
				h.mu.Unlock()
			}
		}
	}
	go scanQuick(stdout)
	go scanQuick(stderr)

	go func(c *exec.Cmd) {
		_ = c.Wait()
		h.mu.Lock()
		if h.tunnelCmd == c {
			h.tunnelCmd = nil
		}
		h.mu.Unlock()
	}(cmd)

	var quickURL string
	select {
	case quickURL = <-foundCh:
	case <-time.After(8 * time.Second):
		h.mu.RLock()
		quickURL = h.state.QuickTunnelURL
		h.mu.RUnlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":           "running",
		"mode":             "quick",
		"quick_tunnel_url": quickURL,
	})
}

type updateTunnelConfigRequest struct {
	BaseDomain string `json:"base_domain"`
}

// UpdateConfig updates the base domain (e.g. "tudominio.com").
// POST /v1/cloudflare-tunnel/config
func (h *CloudflareTunnelHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req updateTunnelConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	cleanDomain := strings.ToLower(strings.TrimSpace(req.BaseDomain))
	cleanDomain = strings.TrimPrefix(cleanDomain, "https://")
	cleanDomain = strings.TrimPrefix(cleanDomain, "http://")
	cleanDomain = strings.TrimSuffix(cleanDomain, "/")

	h.mu.Lock()
	h.state.BaseDomain = cleanDomain
	h.saveStateLocked()
	h.mu.Unlock()

	if h.isCertPresent() {
		_ = h.ensureTunnelAndRun()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"base_domain": cleanDomain,
	})
}

// Disconnect stops the running tunnel and optionally clears authentication credentials.
// POST /v1/cloudflare-tunnel/disconnect
func (h *CloudflareTunnelHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	h.mu.Lock()
	if h.loginCmd != nil && h.loginCmd.Process != nil {
		_ = h.loginCmd.Process.Kill()
		h.loginCmd = nil
	}
	if h.tunnelCmd != nil && h.tunnelCmd.Process != nil {
		_ = h.tunnelCmd.Process.Kill()
		h.tunnelCmd = nil
	}
	h.loginRunning = false
	h.loginURL = ""
	h.state.QuickTunnelURL = ""
	_ = os.Remove(h.certPath())
	h.saveStateLocked()
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "disconnected"})
}

type upsertCloudflareRouteRequest struct {
	Subdomain      string `json:"subdomain"`
	BaseDomain     string `json:"base_domain"`
	Domain         string `json:"domain"`
	TargetNodeID   string `json:"target_node_id"`
	Port           int    `json:"port"`
	ContainerID    string `json:"container_id"`
	ContainerName  string `json:"container_name"`
	ContainerImage string `json:"container_image"`
}

// UpsertRoute creates or updates a multi-node reverse proxy route:
// 1. Resolves the target node's Tailscale IP (`IPVPN`, or local IP if Master)
// 2. Configures `<domain> -> http://<node-tailscale-ip>:<port>`
// 3. Registers the CNAME DNS record in Cloudflare via `cloudflared tunnel route dns`
// 4. Updates `config.yml` and reloads `cloudflared` + internal proxy
// POST /v1/cloudflare-tunnel/routes
func (h *CloudflareTunnelHandler) UpsertRoute(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req upsertCloudflareRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.TargetNodeID == "" || req.Port <= 0 || req.Port > 65535 {
		http.Error(w, "target_node_id and valid port (1-65535) are required", http.StatusBadRequest)
		return
	}

	targetNode, err := h.nodeSvc.GetByID(req.TargetNodeID)
	if err != nil || targetNode == nil {
		http.Error(w, "target node not found", http.StatusNotFound)
		return
	}
	if targetNode.OwnerID == nil || *targetNode.OwnerID != claims.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Determine target IP: prefer Tailscale VPN IP (100.x.x.x), fallback to loopback for local Master node or IPLocal
	targetIP := ""
	if targetNode.IPVPN != nil && strings.TrimSpace(*targetNode.IPVPN) != "" {
		targetIP = strings.TrimSpace(*targetNode.IPVPN)
	} else if targetNode.IsLocal {
		targetIP = "127.0.0.1"
	} else if strings.TrimSpace(targetNode.IPLocal) != "" {
		targetIP = strings.TrimSpace(targetNode.IPLocal)
	}
	// Strip CIDR suffix if present (e.g. "100.122.24.42/32" -> "100.122.24.42")
	if idx := strings.IndexByte(targetIP, '/'); idx != -1 {
		targetIP = targetIP[:idx]
	}
	if targetIP == "" {
		http.Error(w, "target node has no reachable Tailscale or local IP", http.StatusBadRequest)
		return
	}

	// Build full domain from subdomain + base_domain (or direct domain)
	sub := strings.ToLower(strings.TrimSpace(req.Subdomain))
	base := strings.ToLower(strings.TrimSpace(req.BaseDomain))
	if base == "" {
		h.mu.RLock()
		base = h.state.BaseDomain
		h.mu.RUnlock()
	}
	fullDomain := strings.ToLower(strings.TrimSpace(req.Domain))
	if fullDomain == "" && base != "" {
		if sub == "" || sub == "@" {
			fullDomain = base
		} else {
			fullDomain = fmt.Sprintf("%s.%s", sub, base)
		}
	}
	if fullDomain == "" {
		// Fallback for Quick Tunnel mode without custom domain
		if sub != "" && sub != "@" {
			fullDomain = sub + ".local"
		} else {
			fullDomain = fmt.Sprintf("%s-%d.local", strings.ToLower(targetNode.Hostname), req.Port)
		}
	}

	upstream := fmt.Sprintf("%s:%d", targetIP, req.Port)
	now := time.Now().UTC()

	route := &CloudflareTunnelRoute{
		Domain:         fullDomain,
		Subdomain:      sub,
		BaseDomain:     base,
		TargetNodeID:   targetNode.ID,
		TargetNodeName: targetNode.Hostname,
		TargetIP:       targetIP,
		Port:           req.Port,
		Upstream:       upstream,
		ContainerID:    strings.TrimSpace(req.ContainerID),
		ContainerName:  strings.TrimSpace(req.ContainerName),
		ContainerImage: strings.TrimSpace(req.ContainerImage),
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	// Save base_domain if provided and update route in state
	h.mu.Lock()
	if base != "" {
		h.state.BaseDomain = base
	}
	replaced := false
	for i, existing := range h.state.Routes {
		if strings.EqualFold(existing.Domain, fullDomain) {
			route.CreatedAt = existing.CreatedAt
			h.state.Routes[i] = route
			replaced = true
			break
		}
	}
	if !replaced {
		h.state.Routes = append(h.state.Routes, route)
	}
	tunnelID := h.state.TunnelID
	h.saveStateLocked()
	h.mu.Unlock()

	// Also sync to CaddyRoute table on Master node so existing Caddy views stay consistent
	if h.caddySvc != nil && h.nodeSvc != nil {
		if masterNode, err := h.nodeSvc.FindLocalNode(); err == nil && masterNode != nil {
			_, _ = h.caddySvc.Upsert(&apps.CaddyRoute{
				NodeID:   masterNode.ID,
				UserID:   claims.UserID,
				Domain:   fullDomain,
				Upstream: upstream,
				TLSMode:  "http_only",
			})
		}
	}

	// If authenticated with Cloudflare, automatically create DNS CNAME and reload tunnel
	if h.isCertPresent() {
		if tunnelID == "" {
			_ = h.ensureTunnelAndRun()
			h.mu.RLock()
			tunnelID = h.state.TunnelID
			h.mu.RUnlock()
		}
		if tunnelID != "" && !strings.HasSuffix(fullDomain, ".local") {
			if binPath, err := h.ensureBinary(); err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binPath, "tunnel", "route", "dns", "--overwrite-dns", tunnelID, fullDomain)
				cmd.Env = h.commandEnv()
				out, dnsErr := cmd.CombinedOutput()
				outStr := strings.TrimSpace(string(out))
				h.appendLog("[dns] " + outStr)

				h.mu.Lock()
				route.DNSConfigured = dnsErr == nil
				route.DNSMessage = outStr
				h.saveStateLocked()
				h.mu.Unlock()
			}
		}
		_ = h.writeConfigYAML()
		_ = h.restartManagedTunnelProcess()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(route)
}

// DeleteRoute removes a domain route from the tunnel configuration.
// DELETE /v1/cloudflare-tunnel/routes/{domain}
func (h *CloudflareTunnelHandler) DeleteRoute(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	domain := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "domain")))
	if domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	filtered := make([]*CloudflareTunnelRoute, 0, len(h.state.Routes))
	for _, rt := range h.state.Routes {
		if !strings.EqualFold(rt.Domain, domain) {
			filtered = append(filtered, rt)
		}
	}
	h.state.Routes = filtered
	h.saveStateLocked()
	h.mu.Unlock()

	if h.caddySvc != nil && h.nodeSvc != nil {
		if masterNode, err := h.nodeSvc.FindLocalNode(); err == nil && masterNode != nil {
			_ = h.caddySvc.Delete(masterNode.ID, domain)
		}
	}

	if h.isCertPresent() {
		_ = h.writeConfigYAML()
		_ = h.restartManagedTunnelProcess()
	}

	w.WriteHeader(http.StatusNoContent)
}
