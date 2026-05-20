package grpcserver

// profile_manager.go — manages one `go tool pprof -http` subprocess per node.
//
// Architecture note on iframe embedding
// ──────────────────────────────────────
// `go tool pprof -http` generates a web SPA that uses absolute URL paths
// (/flamegraph, /top, /static/pprof.js, …).  Reverse-proxying it at a
// sub-path (/v1/nodes/{id}/profile/ui) would require rewriting every
// absolute path reference in HTML *and* JavaScript fetch() calls.
//
// Strategy implemented here:
//   1. pprof binds to 127.0.0.1:{random port} — not exposed directly.
//   2. The HTTP handler mounts the pprof proxy at a TOP-LEVEL path
//      /v1/pprof/{sessionID} (not a node sub-path), then strips only the
//      /v1/pprof/{sessionID} prefix before forwarding.
//   3. HTML responses are rewritten to inject a <base> tag and fix
//      absolute src=/href= attributes so the SPA's static assets load.
//   4. The pprof JS fetch() calls (/flamegraph, /top, etc.) are also
//      patched in text/javascript responses.
//
// For the iframe, the Dashboard should set:
//   <iframe src="/v1/pprof/{sessionID}/ui/" …>
//
// Sessions expire automatically after 30 minutes.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const pprofSessionTTL = 30 * time.Minute
const pprofReadyTimeout = 8 * time.Second

// pprofSession holds a running `go tool pprof -http` process for one node.
type pprofSession struct {
	nodeID  string
	tmpFile string
	port    int
	cancel  context.CancelFunc // kills the pprof process
}

// ProfileManager tracks one pprof session per node.
// Thread-safe.
type ProfileManager struct {
	mu       sync.Mutex
	sessions map[string]*pprofSession // nodeID → session
}

func NewProfileManager() *ProfileManager {
	return &ProfileManager{sessions: make(map[string]*pprofSession)}
}

// StartSession writes the raw pprof bytes to a temp file, starts a
// `go tool pprof -http` server on a free localhost port, waits for it to
// become ready, and stores the session.  Any previous session for the same
// node is terminated first.
func (m *ProfileManager) StartSession(nodeID string, profileBytes []byte) (*pprofSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Terminate existing session for this node.
	if old, ok := m.sessions[nodeID]; ok {
		m.stopLocked(old)
	}

	// Write raw pprof bytes to a temp file (in-memory FS where possible).
	f, err := os.CreateTemp("", "autohost-pprof-"+nodeID+"-*.pb.gz")
	if err != nil {
		return nil, fmt.Errorf("create temp profile file: %w", err)
	}
	tmpName := f.Name()
	if _, err := f.Write(profileBytes); err != nil {
		f.Close()
		os.Remove(tmpName)
		return nil, fmt.Errorf("write profile to temp file: %w", err)
	}
	f.Close()

	// Allocate a free port on loopback.
	port, err := freePort()
	if err != nil {
		os.Remove(tmpName)
		return nil, fmt.Errorf("find free port: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cmd := exec.CommandContext(ctx, "go", "tool", "pprof",
		"-http="+addr,
		"-no_browser",
		tmpName,
	)
	// Suppress pprof's own log output.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpName)
		return nil, fmt.Errorf("start pprof process: %w", err)
	}

	// Wait until pprof's HTTP server is ready.
	if err := waitReady(ctx, addr, pprofReadyTimeout); err != nil {
		cancel()
		_ = cmd.Wait()
		os.Remove(tmpName)
		return nil, fmt.Errorf("pprof server did not become ready: %w", err)
	}

	sess := &pprofSession{
		nodeID:  nodeID,
		tmpFile: tmpName,
		port:    port,
		cancel:  cancel,
	}
	m.sessions[nodeID] = sess

	// Auto-expire after TTL.
	go func() {
		timer := time.NewTimer(pprofSessionTTL)
		defer timer.Stop()
		select {
		case <-timer.C:
			log.Printf("[profile] TTL expired for node %s — stopping pprof session", nodeID)
			m.Stop(nodeID)
		case <-ctx.Done():
		}
	}()

	log.Printf("[profile] pprof session started for node %s at %s (tmp=%s)", nodeID, addr, tmpName)
	return sess, nil
}

// Get returns the active session for nodeID (nil, false if none).
func (m *ProfileManager) Get(nodeID string) (*pprofSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[nodeID]
	return sess, ok
}

// Stop terminates the pprof process and cleans up temp files.
func (m *ProfileManager) Stop(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[nodeID]; ok {
		m.stopLocked(sess)
	}
}

func (m *ProfileManager) stopLocked(sess *pprofSession) {
	sess.cancel()
	os.Remove(sess.tmpFile)
	delete(m.sessions, sess.nodeID)
	log.Printf("[profile] pprof session stopped for node %s", sess.nodeID)
}

// NewReverseProxy returns an httputil.ReverseProxy that forwards to the pprof
// server and rewrites absolute URL references in HTML/JS responses so the SPA
// works when accessed via the API's /v1/pprof/{sessionID} prefix.
func (sess *pprofSession) NewReverseProxy(sessionPrefix string) *httputil.ReverseProxy {
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", sess.port))

	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			// Strip the /v1/pprof/{sessionID} prefix before forwarding.
			req.URL.Path = strings.TrimPrefix(req.URL.Path, sessionPrefix)
			if req.URL.Path == "" || req.URL.Path[0] != '/' {
				req.URL.Path = "/" + req.URL.Path
			}
			req.Host = target.Host
			// Remove headers that could confuse the local pprof server.
			req.Header.Del("X-Forwarded-For")
		},
		ModifyResponse: func(resp *http.Response) error {
			return rewriteAbsolutePaths(resp, sessionPrefix)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[profile] proxy error for session %s: %v", sessionPrefix, err)
			http.Error(w, "pprof session unavailable", http.StatusBadGateway)
		},
	}
}

// rewriteAbsolutePaths patches HTML and JS response bodies so that pprof's
// absolute-path references (/static/pprof.js, /flamegraph, etc.) become
// relative to the proxy prefix.
func rewriteAbsolutePaths(resp *http.Response, prefix string) error {
	ct := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(ct, "text/html")
	isJS := strings.Contains(ct, "javascript")
	if !isHTML && !isJS {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}

	if isHTML {
		// Inject <base> so relative links resolve correctly.
		body = bytes.ReplaceAll(body,
			[]byte("<head>"),
			[]byte(`<head><base href="`+prefix+`/">`),
		)
		// Rewrite absolute src= / href= attributes.
		body = rewriteAttr(body, `src="/`, `src="`+prefix+`/`)
		body = rewriteAttr(body, `href="/`, `href="`+prefix+`/`)
	}

	if isJS {
		// Rewrite string literals used in fetch() and location assignments:
		//   fetch('/flamegraph')  →  fetch('/v1/pprof/{id}/flamegraph')
		body = bytes.ReplaceAll(body, []byte(`('/`), []byte(`('`+prefix+`/`))
		body = bytes.ReplaceAll(body, []byte(`("/`), []byte(`("`+prefix+`/`))
		body = bytes.ReplaceAll(body, []byte(`url: '/`), []byte(`url: '`+prefix+`/`))
		body = bytes.ReplaceAll(body, []byte(`url:'/`), []byte(`url:'`+prefix+`/`))
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
	// Remove Content-Security-Policy that may block inline scripts.
	resp.Header.Del("Content-Security-Policy")
	return nil
}

func rewriteAttr(body []byte, old, new string) []byte {
	return bytes.ReplaceAll(body, []byte(old), []byte(new))
}

// freePort asks the OS for an available TCP port on loopback.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// waitReady probes GET http://{addr}/ui/ until it responds or timeout.
func waitReady(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get("http://" + addr + "/ui/")
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for pprof server at %s", addr)
}
