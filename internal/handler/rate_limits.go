// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"time"

	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// Rate limits for unauthenticated, credential-checking endpoints. Per-IP limits are generous because many
// users can share an IP (NAT, or the frontend when it does not forward the client IP); the per-account
// and per-share limits are what actually stop password guessing against a single target.
var (
	authIPLimiter       = middleware.NewRateLimiter(30, time.Minute)    // login/register/oauth per client IP
	loginAccountLimiter = middleware.NewRateLimiter(10, 15*time.Minute) // login attempts per email
	refreshIPLimiter    = middleware.NewRateLimiter(120, time.Minute)   // token refreshes per client IP
	enrollIPLimiter     = middleware.NewRateLimiter(20, time.Minute)    // node enrollments per client IP
	shareUnlockIPLim    = middleware.NewRateLimiter(30, time.Minute)    // share password attempts per IP
	shareUnlockShareLim = middleware.NewRateLimiter(20, 15*time.Minute) // share password attempts per share
)
