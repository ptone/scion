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
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bapClock is a settable clock for the request policy.
type bapClock struct{ t time.Time }

func (c *bapClock) now() time.Time          { return c.t }
func (c *bapClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newBAPClockedPolicy(t *testing.T) (*brokerAuthRequestPolicy, *bapClock, *bytes.Buffer) {
	t.Helper()
	clock := &bapClock{t: time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)}
	p := newBrokerAuthRequestPolicy()
	p.setNow(clock.now)
	var buf bytes.Buffer
	p.failures.log = slog.New(slog.NewJSONHandler(&buf, nil))
	return p, clock, &buf
}

func bapRequest(remoteAddr string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/runtime-brokers", nil)
	r.RemoteAddr = remoteAddr
	return r
}

func TestGEExchangeRateLimiter_RefundReturnsToken(t *testing.T) {
	l := newGEExchangeRateLimiter()
	l.rate = 0.0001
	l.burst = 2
	clock := &bapClock{t: time.Unix(1_800_000_000, 0)}
	l.nowFunc = clock.now

	ok, _ := l.Allow("a")
	require.True(t, ok)
	ok, _ = l.Allow("a")
	require.True(t, ok)
	ok, _ = l.Allow("a")
	require.False(t, ok, "the burst is spent")

	l.Refund("a")
	ok, _ = l.Allow("a")
	assert.True(t, ok, "a refunded token can be taken again")

	l.Refund("a")
	l.Refund("a")
	l.Refund("a")
	ok, _ = l.Allow("a")
	require.True(t, ok)
	ok, _ = l.Allow("a")
	require.True(t, ok)
	ok, _ = l.Allow("a")
	assert.False(t, ok, "refunds never raise a bucket above its burst")

	l.Refund("unknown") // no bucket: no-op, no entry created
	assert.Equal(t, 1, l.Len())
}

func TestBrokerAuthRequestPolicy_RefundOnValidRequest(t *testing.T) {
	p, _, _ := newBAPClockedPolicy(t)
	r := bapRequest("192.0.2.1:1000")
	// Far more valid requests than any burst: each one is refunded.
	for i := 0; i < 10*brokerAuthGlobalFailureBurst; i++ {
		res, ok, _ := p.reserve(r, "broker-a")
		require.True(t, ok, "valid request %d", i)
		p.complete(res, true)
	}
	// Failures are charged: the per-key burst is spent, then refused.
	r2 := bapRequest("192.0.2.2:1000")
	for i := 0; i < brokerAuthKeyFailureBurst; i++ {
		res, ok, _ := p.reserve(r2, "broker-x")
		require.True(t, ok, "failure %d", i)
		p.complete(res, false)
	}
	_, ok, retry := p.reserve(r2, "broker-x")
	assert.False(t, ok)
	assert.Equal(t, 5, retry, "Retry-After is the time for one token at 0.2/s")
}

func TestBrokerAuthRequestPolicy_GlobalRefusalReturnsPerKeyToken(t *testing.T) {
	p, _, _ := newBAPClockedPolicy(t)
	for i := 0; i < brokerAuthGlobalFailureBurst; i++ {
		res, ok, _ := p.reserve(bapRequest(fmt.Sprintf("198.51.100.%d:1", i%200+1)), "b")
		require.True(t, ok)
		p.complete(res, false)
	}
	r := bapRequest("192.0.2.77:1")
	for i := 0; i < 3*brokerAuthKeyFailureBurst; i++ {
		_, ok, _ := p.reserve(r, "b")
		require.False(t, ok, "the global budget is spent")
	}
	// The per-key bucket kept its tokens: refusals by the global bucket do
	// not drain it.
	ok, _ := p.perKey.Allow(p.clientKey(r))
	assert.True(t, ok)
}

func TestBrokerAuthRequestPolicy_EstablishedPairExpires(t *testing.T) {
	p, clock, _ := newBAPClockedPolicy(t)
	r := bapRequest("192.0.2.5:1")
	res, ok, _ := p.reserve(r, "broker-a")
	require.True(t, ok)
	require.False(t, res.viaPair)
	p.complete(res, true)

	res, _, _ = p.reserve(r, "broker-a")
	assert.True(t, res.viaPair, "a valid request establishes the pair")
	p.complete(res, true)

	res, _, _ = p.reserve(r, "broker-b")
	assert.False(t, res.viaPair, "the pair is per broker ID")
	p.complete(res, false)

	clock.advance(brokerAuthEstablishedTTL)
	res, _, _ = p.reserve(r, "broker-a")
	assert.False(t, res.viaPair, "a pair expires after the TTL")
}

func TestBrokerAuthRequestPolicy_ClientKey(t *testing.T) {
	p, _, _ := newBAPClockedPolicy(t)

	r := bapRequest("192.0.2.9:4444")
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	r.Header.Set("X-Real-IP", "203.0.113.2")
	assert.Equal(t, "192.0.2.9", p.clientKey(r), "forwarded headers from an unlisted peer are ignored")
	assert.Equal(t, "192.0.2.9", p.clientIP(r))

	r6 := bapRequest("[2001:db8:1:2:3:4:5:6]:4444")
	assert.Equal(t, "2001:db8:1:2::/64", p.clientKey(r6), "IPv6 sources are keyed by /64")
	assert.Equal(t, "2001:db8:1:2:3:4:5:6", p.clientIP(r6), "the audit address is the full peer")

	mapped := bapRequest("[::ffff:192.0.2.9]:4444")
	assert.Equal(t, "192.0.2.9", p.clientKey(mapped))

	relay := bapRequest(conduitRelayAdmitRemoteAddr)
	relay = relay.WithContext(withBrokerAuthRelaySource(relay.Context(), "relay-7"))
	assert.Equal(t, "relay:relay-7", p.clientKey(relay), "a relay admission is keyed by its relay instance")
}

func TestBrokerAuthFailureLog_DedupCapAndSummary(t *testing.T) {
	p, clock, buf := newBAPClockedPolicy(t)
	f := p.failures

	assert.True(t, f.admit("192.0.2.1", "broker-a", "invalid_signature"), "the first failure is written")
	for i := 0; i < 99; i++ {
		assert.False(t, f.admit("192.0.2.1", "broker-a", "invalid_signature"))
	}
	assert.True(t, f.admit("192.0.2.1", "broker-a", "clock_skew"), "another failure code is written")
	assert.True(t, f.admit("192.0.2.1", "broker-b", "invalid_signature"), "another broker ID is written")
	for i := 0; i < 7; i++ {
		f.noteRateLimited("192.0.2.3")
	}
	assert.Zero(t, buf.Len(), "no summary inside the window")

	clock.advance(brokerAuthFailureLogWindow)
	f.flush(clock.now())

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec), buf.String())
	assert.Equal(t, "broker auth failures suppressed", rec["msg"])
	assert.Equal(t, "WARN", rec["level"])
	assert.EqualValues(t, 60, rec["window_seconds"])
	assert.EqualValues(t, 99, rec["suppressed_total"])
	assert.EqualValues(t, 7, rec["rate_limited_total"])
	assert.Equal(t, map[string]any{"invalid_signature": float64(99)}, rec["by_failure_code"])
	assert.EqualValues(t, 2, rec["distinct_sources"])
	assert.Equal(t, map[string]any{"192.0.2.1": float64(99), "192.0.2.3": float64(7)}, rec["top_sources"])

	// The window reset: the same failure is written again, and a quiet
	// window writes no summary.
	buf.Reset()
	assert.True(t, f.admit("192.0.2.1", "broker-a", "invalid_signature"))
	clock.advance(brokerAuthFailureLogWindow)
	f.flush(clock.now())
	assert.Zero(t, buf.Len(), "no summary when nothing was suppressed")
}

func TestBrokerAuthFailureLog_HubWideCap(t *testing.T) {
	p, _, _ := newBAPClockedPolicy(t)
	written := 0
	for i := 0; i < 1000; i++ {
		if p.failures.admit(fmt.Sprintf("src-%d", i), "b", "invalid_signature") {
			written++
		}
	}
	assert.Equal(t, brokerAuthFailureLogBurst, written, "distinct sources still share the hub-wide cap")
	assert.EqualValues(t, 1000-brokerAuthFailureLogBurst, p.failures.suppressed)
}

func TestBrokerAuthRequestPolicy_BoundedUnderChurn(t *testing.T) {
	p, clock, _ := newBAPClockedPolicy(t)
	const maxN = 50
	p.perKey.maxEntries = maxN
	p.pairs.maxEntries = maxN
	p.maxEstablished = maxN
	p.failures.maxKeys = maxN
	p.global.burst = 1 << 30 // isolate the per-key and set bounds

	for i := 0; i < 20*maxN; i++ {
		r := bapRequest(fmt.Sprintf("10.%d.%d.%d:1", i/65536, (i/256)%256, i%256))
		res, ok, _ := p.reserve(r, fmt.Sprintf("b-%d", i))
		if ok {
			p.complete(res, i%2 == 0)
		}
		p.failures.admit(res.key, "b", "invalid_signature")
		p.failures.noteRateLimited(res.key)
		clock.advance(time.Millisecond)
	}
	assert.LessOrEqual(t, p.perKey.Len(), maxN)
	assert.LessOrEqual(t, p.pairs.Len(), maxN)
	p.mu.Lock()
	assert.LessOrEqual(t, len(p.established), maxN)
	p.mu.Unlock()
	p.failures.mu.Lock()
	assert.LessOrEqual(t, len(p.failures.seen), maxN)
	assert.LessOrEqual(t, len(p.failures.bySource), maxN)
	assert.Positive(t, p.failures.sourcesOverflow, "sources past the bound are counted")
	p.failures.mu.Unlock()

	// A sweep after the TTLs frees everything.
	clock.advance(brokerAuthBucketMaxAge + brokerAuthEstablishedTTL)
	p.sweep(clock.now())
	assert.Zero(t, p.perKey.Len())
	p.mu.Lock()
	assert.Zero(t, len(p.established))
	p.mu.Unlock()
}
