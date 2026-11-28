// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hub

import (
	"context"
	"net"
	"net/http"
)

// ---------------------------------------------------------------------------
// GitHub App setup callback rate limiter — per-client-IP token bucket
// guarding GET /github-app/setup (handlers_github_app_webhook.go).
//
// Because GET /github-app/setup is a public pre-auth browser redirect that
// triggers outbound GitHub App API calls and a project scan, an unthrottled
// caller could amplify traffic into GitHub App quota and hub store load.
// Reuses geExchangeRateLimiter's bounded bucket map and trustedClientIP's
// trusted-proxy IP extraction (client_ip.go), matching externalBearerRateLimiter.
// ---------------------------------------------------------------------------

const (
	// githubAppSetupRatePerSecond is the sustained per-IP request budget for
	// GET /github-app/setup callbacks: 5 requests/minute (~0.083/s).
	githubAppSetupRatePerSecond = 5.0 / 60.0

	// githubAppSetupBurst is the maximum per-IP burst before the sustained
	// rate applies.
	githubAppSetupBurst = 3
)

// githubAppSetupRateLimiter is a per-client-IP token bucket rate limiter for
// GET /github-app/setup.
type githubAppSetupRateLimiter struct {
	buckets     *geExchangeRateLimiter
	trustedNets []*net.IPNet
}

// newGitHubAppSetupRateLimiter creates a rate limiter with the default budget
// (5 req/min, burst 3 per IP). trustedProxies is parsed once at construction.
func newGitHubAppSetupRateLimiter(trustedProxies []string) *githubAppSetupRateLimiter {
	l := newGEExchangeRateLimiter()
	l.rate = githubAppSetupRatePerSecond
	l.burst = githubAppSetupBurst
	return &githubAppSetupRateLimiter{
		buckets:     l,
		trustedNets: parseTrustedProxies(trustedProxies),
	}
}

// Allow reports whether r's client IP is within budget. retryAfterSeconds is
// meaningful only when allowed is false.
func (l *githubAppSetupRateLimiter) Allow(r *http.Request) (allowed bool, retryAfterSeconds int) {
	clientIP := trustedClientIP(r, l.trustedNets)
	return l.buckets.Allow(clientIP)
}

// StartCleanup runs the background goroutine that evicts stale per-IP
// buckets, exiting when ctx is cancelled.
func (l *githubAppSetupRateLimiter) StartCleanup(ctx context.Context) {
	l.buckets.StartCleanup(ctx)
}
