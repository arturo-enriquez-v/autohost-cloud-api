// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter is a fixed-window counter keyed by an arbitrary string (client IP, email, share ID...).
// State is in-memory and per-process: enough for a single API instance; a multi-instance deployment
// should move it to Redis.
type RateLimiter struct {
	limit  int
	window time.Duration

	mu          sync.Mutex
	buckets     map[string]*rateBucket
	lastCleanup time.Time
	now         func() time.Time
}

type rateBucket struct {
	count int
	start time.Time
}

// NewRateLimiter allows up to limit events per key in each window.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]*rateBucket),
		now:     time.Now,
	}
}

// Allow records one event for key and reports whether it is within the limit. When it is not, it also
// returns how long until the window resets.
func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastCleanup) > l.window {
		for k, b := range l.buckets {
			if now.Sub(b.start) >= l.window {
				delete(l.buckets, k)
			}
		}
		l.lastCleanup = now
	}

	b, ok := l.buckets[key]
	if !ok || now.Sub(b.start) >= l.window {
		l.buckets[key] = &rateBucket{count: 1, start: now}
		return true, 0
	}
	if b.count >= l.limit {
		return false, b.start.Add(l.window).Sub(now)
	}
	b.count++
	return true, 0
}

// WriteRateLimited writes a 429 response with a Retry-After header.
func WriteRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int(retryAfter.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = fmt.Fprintf(w, `{"error":"too many requests, retry in %d seconds"}`, secs)
}

// ClientIP returns the request's client IP (RemoteAddr, already normalized by TrustedRealIP).
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RateLimitByIP rejects requests once the client IP exceeds the limiter's budget.
func RateLimitByIP(l *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ok, retry := l.Allow("ip:" + ClientIP(r)); !ok {
				WriteRateLimited(w, retry)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
