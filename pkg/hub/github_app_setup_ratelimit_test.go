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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestSetupCallbackRequest(remoteAddr, xff string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/github-app/setup?installation_id=12345&setup_action=install", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	return req
}

func TestGitHubAppSetupRateLimiter_PerIPBudgetAndTrustedProxy(t *testing.T) {
	now := time.Now()
	limiter := newGitHubAppSetupRateLimiter(nil)
	limiter.buckets.nowFunc = func() time.Time { return now }

	// Burst of 3 allowed for IP A.
	for i := range githubAppSetupBurst {
		allowed, _ := limiter.Allow(newTestSetupCallbackRequest("198.51.100.1:1000", ""))
		if !allowed {
			t.Fatalf("request %d for IP A should be allowed within burst %d", i+1, githubAppSetupBurst)
		}
	}

	// 4th request from IP A is rejected with a positive Retry-After.
	allowed, retryAfter := limiter.Allow(newTestSetupCallbackRequest("198.51.100.1:1001", ""))
	if allowed {
		t.Fatal("request over burst for IP A should be rejected")
	}
	if retryAfter < 1 {
		t.Fatalf("expected retryAfter >= 1, got %d", retryAfter)
	}

	// Spoofing X-Forwarded-For from an untrusted peer must NOT bypass IP A's budget.
	if allowed, _ := limiter.Allow(newTestSetupCallbackRequest("198.51.100.1:1002", "203.0.113.99")); allowed {
		t.Fatal("spoofed X-Forwarded-For from untrusted peer must not bypass rate limit")
	}

	// Distinct IP B has its own independent budget.
	if allowed, _ := limiter.Allow(newTestSetupCallbackRequest("198.51.100.2:2000", "")); !allowed {
		t.Fatal("distinct IP B should be allowed independently of IP A")
	}

	// When a trusted proxy CIDR is configured, distinct XFF clients behind it get separate budgets.
	proxyLimiter := newGitHubAppSetupRateLimiter([]string{"10.0.0.0/8"})
	proxyLimiter.buckets.nowFunc = func() time.Time { return now }
	for i := range githubAppSetupBurst {
		if allowed, _ := proxyLimiter.Allow(newTestSetupCallbackRequest("10.0.0.5:443", "203.0.113.10")); !allowed {
			t.Fatalf("proxied client A request %d should be allowed", i+1)
		}
	}
	if allowed, _ := proxyLimiter.Allow(newTestSetupCallbackRequest("10.0.0.5:443", "203.0.113.10")); allowed {
		t.Fatal("proxied client A over burst should be rejected")
	}
	if allowed, _ := proxyLimiter.Allow(newTestSetupCallbackRequest("10.0.0.5:443", "203.0.113.11")); !allowed {
		t.Fatal("proxied client B behind same trusted proxy should have its own budget")
	}
}

func TestGitHubAppSetupRateLimiter_BoundedCapacityAndCleanup(t *testing.T) {
	now := time.Now()
	limiter := newGitHubAppSetupRateLimiter(nil)
	limiter.buckets.maxEntries = 2
	limiter.buckets.nowFunc = func() time.Time { return now }

	if allowed, _ := limiter.Allow(newTestSetupCallbackRequest("198.51.100.1:1", "")); !allowed {
		t.Fatal("first IP should be allowed")
	}
	if allowed, _ := limiter.Allow(newTestSetupCallbackRequest("198.51.100.2:1", "")); !allowed {
		t.Fatal("second IP should be allowed")
	}
	if got := limiter.buckets.Len(); got != 2 {
		t.Fatalf("bucket count = %d, want 2", got)
	}

	// At capacity, a third unseen IP is refused without growing the map.
	if allowed, _ := limiter.Allow(newTestSetupCallbackRequest("198.51.100.3:1", "")); allowed {
		t.Fatal("third IP should be refused when limiter is at maxEntries")
	}
	if got := limiter.buckets.Len(); got != 2 {
		t.Fatalf("bucket count grew past maxEntries: got %d, want 2", got)
	}

	// Cleanup past maxAge evicts stale entries and frees capacity for new IPs.
	limiter.buckets.Cleanup(now.Add(limiter.buckets.maxAge + time.Second))
	if got := limiter.buckets.Len(); got != 0 {
		t.Fatalf("bucket count after cleanup = %d, want 0", got)
	}
	if allowed, _ := limiter.Allow(newTestSetupCallbackRequest("198.51.100.3:1", "")); !allowed {
		t.Fatal("third IP should be admitted after cleanup evicts stale entries")
	}
}

func TestTruncateSetupAction(t *testing.T) {
	if got := truncateSetupAction(""); got != "" {
		t.Errorf("truncateSetupAction(\"\") = %q, want empty", got)
	}
	if got := truncateSetupAction("install"); got != "install" {
		t.Errorf("truncateSetupAction(\"install\") = %q, want \"install\"", got)
	}
	longASCII := strings.Repeat("a", maxLoggedSetupActionRunes+20)
	if got := truncateSetupAction(longASCII); len(got) != maxLoggedSetupActionRunes {
		t.Errorf("len(truncateSetupAction(longASCII)) = %d, want %d", len(got), maxLoggedSetupActionRunes)
	}
	longUTF8 := strings.Repeat("é", maxLoggedSetupActionRunes+5)
	gotUTF8 := truncateSetupAction(longUTF8)
	if len([]rune(gotUTF8)) != maxLoggedSetupActionRunes {
		t.Errorf("rune count = %d, want %d", len([]rune(gotUTF8)), maxLoggedSetupActionRunes)
	}
}
