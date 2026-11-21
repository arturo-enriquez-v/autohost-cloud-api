// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package middleware

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/middleware"
)

// sensitiveQueryParams are redacted from request logs (JWTs, share tokens, transfer tickets...).
var sensitiveQueryParams = map[string]bool{
	"token":        true,
	"access_token": true,
	"st":           true,
	"id_token":     true,
	"code":         true,
	"password":     true,
}

// RedactedRequestURI returns the request path with sensitive query parameter values replaced.
func RedactedRequestURI(u *url.URL) string {
	if u.RawQuery == "" {
		return u.Path
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return u.Path + "?<unparsable-query>"
	}
	for k := range q {
		if sensitiveQueryParams[strings.ToLower(k)] {
			q[k] = []string{"REDACTED"}
		}
	}
	return u.Path + "?" + q.Encode()
}

// RequestLogger logs one line per request like chi's Logger, but never writes credentials that are
// passed in the query string (WebSocket/SSE auth, share tokens) to the logs.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			log.Printf("%q from %s - %d %dB in %s",
				r.Method+" "+RedactedRequestURI(r.URL)+" "+r.Proto,
				r.RemoteAddr, status, ww.BytesWritten(), time.Since(start))
		}()
		next.ServeHTTP(ww, r)
	})
}
