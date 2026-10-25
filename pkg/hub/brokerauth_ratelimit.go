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
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
)

// ---------------------------------------------------------------------------
// Broker request policy: a failure budget in front of broker signature
// checks, and a bounded failed-auth log.
//
// Every request with X-Scion-Broker-ID goes through a signature check
// (BrokerAuthMiddleware, AuditableBrokerAuthMiddleware,
// authenticateBrokerUpgrade). Before the HMAC, that check runs the broker
// admission hook, a nonce store write and a GetBrokerSecret read, and a
// failure writes an audit event. Brokers send most of their traffic through
// the same check, so the budget charges FAILURES only:
//
//   - A request reserves a token before any of that work, and gets it back
//     when its signature checks out (reserve, then refund). Concurrent
//     failures cannot overshoot, and a valid request costs nothing net.
//   - Requests from a (client key, broker ID) pair that passed the check in
//     the last brokerAuthEstablishedTTL use only that pair's own bucket, so
//     a flood that drains the per-key or global bucket does not refuse a
//     broker that is already connected.
//   - Any other request takes a token from its client key's bucket and
//     from the hub-wide bucket.
//   - A refused request gets 429 with Retry-After and writes no audit
//     event; it is counted in the periodic summary.
//
// Failed-auth events are written in full for the first failure per
// (client key, broker ID, failure code) in each brokerAuthFailureLogWindow,
// up to a hub-wide rate; the rest are counted, and a summary record goes to
// the hub.broker-auth logger once per window when any count is non-zero.
//
// Limits are per process: each hub replica keeps its own buckets.
// ---------------------------------------------------------------------------

const (
	// Per client key: 12 failures a minute after a burst of 30.
	brokerAuthKeyFailureRate  = 0.2
	brokerAuthKeyFailureBurst = 30

	// Hub-wide, for requests that are not from an established pair.
	brokerAuthGlobalFailureRate  = 20.0
	brokerAuthGlobalFailureBurst = 200

	// Per established (client key, broker ID) pair.
	brokerAuthPairFailureRate  = 0.2
	brokerAuthPairFailureBurst = 30

	// brokerAuthMaxEntries bounds every map the policy keeps: bucket maps,
	// the established set and the failure log's dedup and source maps.
	brokerAuthMaxEntries = 10000

	// brokerAuthBucketMaxAge is how long an idle bucket is kept. A drained
	// per-key bucket refills fully in burst/rate = 150 s.
	brokerAuthBucketMaxAge = 10 * time.Minute

	// brokerAuthEstablishedTTL is how long a pair counts as established
	// after its last valid request.
	brokerAuthEstablishedTTL = 10 * time.Minute

	// brokerAuthMaintenanceInterval is how often StartMaintenance sweeps
	// stale entries and flushes the failure summary.
	brokerAuthMaintenanceInterval = time.Minute

	// brokerAuthFailureLogWindow is the dedup and summary window.
	brokerAuthFailureLogWindow = 60 * time.Second

	// Hub-wide cap on failed-auth events written in full.
	brokerAuthFailureLogRate  = 5.0
	brokerAuthFailureLogBurst = 50

	// brokerAuthSummaryTopSources is how many client keys the summary
	// names, by count.
	brokerAuthSummaryTopSources = 5

	brokerAuthGlobalKey = "*"
)

// errBrokerAuthRateLimited reports a broker request refused by the failure
// budget before its signature was checked.
var errBrokerAuthRateLimited = errors.New("broker authentication: too many failed attempts")

// brokerAuthRateLimitError carries the Retry-After of a refused request.
// errors.Is(err, errBrokerAuthRateLimited) holds through Unwrap.
type brokerAuthRateLimitError struct {
	retryAfterSeconds int
}

func (e *brokerAuthRateLimitError) Error() string { return errBrokerAuthRateLimited.Error() }
func (e *brokerAuthRateLimitError) Unwrap() error { return errBrokerAuthRateLimited }

// brokerAuthRetryAfter reports whether err is a budget refusal, and its
// Retry-After in seconds.
func brokerAuthRetryAfter(err error) (int, bool) {
	var rl *brokerAuthRateLimitError
	if errors.As(err, &rl) {
		return rl.retryAfterSeconds, true
	}
	return 0, false
}

// writeBrokerAuthRateLimited answers a refused broker request.
func writeBrokerAuthRateLimited(w http.ResponseWriter, retryAfterSeconds int) {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	writeError(w, http.StatusTooManyRequests, ErrCodeRateLimited,
		"too many failed broker authentication attempts, try again later", nil)
}

// brokerAuthRelaySourceKey marks a request the hub built for a conduit
// relay admission (conduit_relay_admit.go). Its RemoteAddr is a fixed
// documentation address, so the relay instance is the client key instead.
type brokerAuthRelaySourceKey struct{}

// withBrokerAuthRelaySource returns ctx marked as a relay admission for
// relayInstanceID.
func withBrokerAuthRelaySource(ctx context.Context, relayInstanceID string) context.Context {
	return context.WithValue(ctx, brokerAuthRelaySourceKey{}, relayInstanceID)
}

// brokerAuthClientIP is the client address of a broker request, used both
// for the failure budget's key and for the ip_address of broker audit
// events. It is the immediate peer (RemoteAddr). A forwarded header is read
// only when the peer is inside trustedNets, walking X-Forwarded-For right
// to left (geExchangeClientIP); trustedNets is empty today, so a forwarded
// header from any caller changes neither the key nor the audit address.
func brokerAuthClientIP(r *http.Request, trustedNets []*net.IPNet) string {
	return geExchangeClientIP(r, trustedNets)
}

// brokerAuthRequestPolicy is the failure budget and failed-auth log of the
// broker signature check.
type brokerAuthRequestPolicy struct {
	perKey *geExchangeRateLimiter
	global *geExchangeRateLimiter
	pairs  *geExchangeRateLimiter

	// trustedNets are the peers whose forwarded client address is used.
	// Empty: the immediate peer is always the client.
	trustedNets []*net.IPNet

	mu             sync.Mutex
	established    map[string]time.Time // pair key -> last valid request
	maxEstablished int
	establishedTTL time.Duration
	nowFunc        func() time.Time

	failures *brokerAuthFailureLog
}

// newBrokerAuthRequestPolicy builds the policy with its production limits.
func newBrokerAuthRequestPolicy() *brokerAuthRequestPolicy {
	newBuckets := func(rate float64, burst int) *geExchangeRateLimiter {
		l := newGEExchangeRateLimiter()
		l.rate = rate
		l.burst = burst
		l.maxEntries = brokerAuthMaxEntries
		l.maxAge = brokerAuthBucketMaxAge
		l.cleanupInterval = brokerAuthMaintenanceInterval
		return l
	}
	return &brokerAuthRequestPolicy{
		perKey:         newBuckets(brokerAuthKeyFailureRate, brokerAuthKeyFailureBurst),
		global:         newBuckets(brokerAuthGlobalFailureRate, brokerAuthGlobalFailureBurst),
		pairs:          newBuckets(brokerAuthPairFailureRate, brokerAuthPairFailureBurst),
		established:    make(map[string]time.Time),
		maxEstablished: brokerAuthMaxEntries,
		establishedTTL: brokerAuthEstablishedTTL,
		nowFunc:        time.Now,
		failures:       newBrokerAuthFailureLog(),
	}
}

// setNow replaces the clock of the policy and everything it holds (tests).
func (p *brokerAuthRequestPolicy) setNow(now func() time.Time) {
	p.nowFunc = now
	p.perKey.nowFunc = now
	p.global.nowFunc = now
	p.pairs.nowFunc = now
	p.failures.nowFunc = now
	p.failures.fullEvents.nowFunc = now
}

// clientIP is brokerAuthClientIP with the policy's trusted peers.
func (p *brokerAuthRequestPolicy) clientIP(r *http.Request) string {
	return brokerAuthClientIP(r, p.trustedNets)
}

// clientKey is the bucket and dedup key of r: the relay instance for a
// relay admission, else the client IPv4 address or IPv6 /64.
func (p *brokerAuthRequestPolicy) clientKey(r *http.Request) string {
	if relay, ok := r.Context().Value(brokerAuthRelaySourceKey{}).(string); ok {
		return "relay:" + relay
	}
	return cspReportClientKey(p.clientIP(r))
}

// brokerAuthReservation is what reserve took, for complete to settle.
type brokerAuthReservation struct {
	key     string
	pair    string
	viaPair bool
}

func brokerAuthPairKey(key, brokerID string) string {
	return key + "\x00" + brokerID
}

// reserve takes the tokens r needs before its signature is checked. When
// it refuses, nothing is held and retryAfterSeconds is set.
func (p *brokerAuthRequestPolicy) reserve(r *http.Request, brokerID string) (res brokerAuthReservation, allowed bool, retryAfterSeconds int) {
	res.key = p.clientKey(r)
	res.pair = brokerAuthPairKey(res.key, brokerID)
	if p.isEstablished(res.pair) {
		res.viaPair = true
		ok, retry := p.pairs.Allow(res.pair)
		return res, ok, retry
	}
	ok, retry := p.perKey.Allow(res.key)
	if !ok {
		return res, false, retry
	}
	if ok, retry = p.global.Allow(brokerAuthGlobalKey); !ok {
		p.perKey.Refund(res.key)
		return res, false, retry
	}
	return res, true, 0
}

// complete settles a reservation: a valid request gets its tokens back and
// marks its pair established; a failed one stays charged.
func (p *brokerAuthRequestPolicy) complete(res brokerAuthReservation, valid bool) {
	if !valid {
		return
	}
	if res.viaPair {
		p.pairs.Refund(res.pair)
	} else {
		p.perKey.Refund(res.key)
		p.global.Refund(brokerAuthGlobalKey)
	}
	p.markEstablished(res.pair)
}

func (p *brokerAuthRequestPolicy) isEstablished(pair string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	last, ok := p.established[pair]
	return ok && p.nowFunc().Sub(last) < p.establishedTTL
}

// markEstablished records a valid request for pair. When the set is full a
// new pair is not added: it keeps using the per-key and global buckets.
func (p *brokerAuthRequestPolicy) markEstablished(pair string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.established[pair]; !ok && len(p.established) >= p.maxEstablished {
		return
	}
	p.established[pair] = p.nowFunc()
}

// sweep drops stale buckets and pairs and flushes a due summary.
func (p *brokerAuthRequestPolicy) sweep(now time.Time) {
	p.perKey.Cleanup(now)
	p.global.Cleanup(now)
	p.pairs.Cleanup(now)
	p.mu.Lock()
	for pair, last := range p.established {
		if now.Sub(last) >= p.establishedTTL {
			delete(p.established, pair)
		}
	}
	p.mu.Unlock()
	p.failures.flush(now)
}

// StartMaintenance sweeps and flushes every brokerAuthMaintenanceInterval
// until ctx is cancelled. Without it the bounded maps fill and the last
// window's summary waits for the next failure.
func (p *brokerAuthRequestPolicy) StartMaintenance(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(brokerAuthMaintenanceInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				p.sweep(t)
			}
		}
	}()
}

// ---------------------------------------------------------------------------
// Failed-auth log throttle.
// ---------------------------------------------------------------------------

// brokerAuthFailureLog decides which failed broker authentications are
// written in full, and counts the rest for a periodic summary.
type brokerAuthFailureLog struct {
	mu         sync.Mutex
	window     time.Duration
	maxKeys    int
	nowFunc    func() time.Time
	fullEvents *geExchangeRateLimiter
	log        *slog.Logger

	seen map[string]time.Time // dedup key -> time its full event was written

	// Counts since windowStart.
	windowStart     time.Time
	suppressed      int64
	rateLimited     int64
	byCode          map[string]int64
	bySource        map[string]int64
	sourcesOverflow int64
}

func newBrokerAuthFailureLog() *brokerAuthFailureLog {
	fullEvents := newGEExchangeRateLimiter()
	fullEvents.rate = brokerAuthFailureLogRate
	fullEvents.burst = brokerAuthFailureLogBurst
	return &brokerAuthFailureLog{
		window:     brokerAuthFailureLogWindow,
		maxKeys:    brokerAuthMaxEntries,
		nowFunc:    time.Now,
		fullEvents: fullEvents,
		log:        logging.Subsystem("hub.broker-auth"),
		seen:       make(map[string]time.Time),
		byCode:     make(map[string]int64),
		bySource:   make(map[string]int64),
	}
}

// admit reports whether a failure from source (a client key) for brokerID
// with failure code code is written in full. A false answer is counted.
func (f *brokerAuthFailureLog) admit(source, brokerID, code string) bool {
	now := f.nowFunc()
	f.flush(now)

	f.mu.Lock()
	dedup := source + "\x00" + brokerID + "\x00" + code
	first, seen := f.seen[dedup]
	fresh := !seen || now.Sub(first) >= f.window
	if fresh && !seen && len(f.seen) >= f.maxKeys {
		fresh = false // dedup state is full: count only
	}
	if fresh {
		if ok, _ := f.fullEvents.Allow(brokerAuthGlobalKey); ok {
			f.seen[dedup] = now
			f.mu.Unlock()
			return true
		}
	}
	f.suppressed++
	f.byCode[code]++
	f.countSourceLocked(source)
	f.mu.Unlock()
	return false
}

// noteRateLimited counts a request the failure budget refused.
func (f *brokerAuthFailureLog) noteRateLimited(source string) {
	now := f.nowFunc()
	f.flush(now)

	f.mu.Lock()
	f.rateLimited++
	f.countSourceLocked(source)
	f.mu.Unlock()
}

func (f *brokerAuthFailureLog) countSourceLocked(source string) {
	if _, ok := f.bySource[source]; !ok && len(f.bySource) >= f.maxKeys {
		f.sourcesOverflow++
		return
	}
	f.bySource[source]++
}

// flush writes the summary when a window has ended with non-zero counts,
// then starts a new window and drops expired dedup entries.
func (f *brokerAuthFailureLog) flush(now time.Time) {
	f.mu.Lock()
	if f.windowStart.IsZero() {
		f.windowStart = now
	}
	if now.Sub(f.windowStart) < f.window {
		f.mu.Unlock()
		return
	}
	var attrs []any
	if f.suppressed > 0 || f.rateLimited > 0 {
		attrs = f.summaryAttrsLocked(now)
	}
	f.windowStart = now
	f.suppressed = 0
	f.rateLimited = 0
	f.sourcesOverflow = 0
	clear(f.byCode)
	clear(f.bySource)
	for k, first := range f.seen {
		if now.Sub(first) >= f.window {
			delete(f.seen, k)
		}
	}
	f.mu.Unlock()

	if attrs != nil {
		f.log.Warn("broker auth failures suppressed", attrs...)
	}
}

func (f *brokerAuthFailureLog) summaryAttrsLocked(now time.Time) []any {
	codes := make([]string, 0, len(f.byCode))
	for c := range f.byCode {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	codeAttrs := make([]any, 0, len(codes))
	for _, c := range codes {
		codeAttrs = append(codeAttrs, slog.Int64(c, f.byCode[c]))
	}

	type sourceCount struct {
		source string
		n      int64
	}
	sources := make([]sourceCount, 0, len(f.bySource))
	for s, n := range f.bySource {
		sources = append(sources, sourceCount{s, n})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].n != sources[j].n {
			return sources[i].n > sources[j].n
		}
		return sources[i].source < sources[j].source
	})
	if len(sources) > brokerAuthSummaryTopSources {
		sources = sources[:brokerAuthSummaryTopSources]
	}
	topAttrs := make([]any, 0, len(sources))
	for _, s := range sources {
		topAttrs = append(topAttrs, slog.Int64(s.source, s.n))
	}

	return []any{
		slog.Int("window_seconds", int(math.Round(now.Sub(f.windowStart).Seconds()))),
		slog.Int64("suppressed_total", f.suppressed),
		slog.Int64("rate_limited_total", f.rateLimited),
		slog.Group("by_failure_code", codeAttrs...),
		slog.Int("distinct_sources", len(f.bySource)),
		slog.Int64("sources_not_tracked", f.sourcesOverflow),
		slog.Group("top_sources", topAttrs...),
	}
}

// ---------------------------------------------------------------------------
// BrokerAuthService integration.
// ---------------------------------------------------------------------------

// validateBrokerRequest is the signature check every broker verifier runs:
// the failure budget first, then ValidateBrokerSignature, then the
// outcome. A refused request returns a *brokerAuthRateLimitError and none
// of ValidateBrokerSignature's work is done.
func (s *BrokerAuthService) validateBrokerRequest(r *http.Request) (BrokerIdentity, error) {
	p := s.requestPolicy
	if p == nil {
		return s.ValidateBrokerSignature(r.Context(), r)
	}
	res, ok, retry := p.reserve(r, r.Header.Get(HeaderBrokerID))
	if !ok {
		p.failures.noteRateLimited(res.key)
		return nil, &brokerAuthRateLimitError{retryAfterSeconds: retry}
	}
	identity, err := s.ValidateBrokerSignature(r.Context(), r)
	p.complete(res, err == nil)
	return identity, err
}

// brokerClientIP is the client address recorded on r's broker audit events
// (brokerAuthClientIP).
func (s *BrokerAuthService) brokerClientIP(r *http.Request) string {
	if s != nil && s.requestPolicy != nil {
		return s.requestPolicy.clientIP(r)
	}
	return brokerAuthClientIP(r, nil)
}

// admitFailureEvent reports whether a failed signature check of r, with
// failure code code, is logged in full.
func (s *BrokerAuthService) admitFailureEvent(r *http.Request, code string) bool {
	if s == nil || s.requestPolicy == nil {
		return true
	}
	p := s.requestPolicy
	return p.failures.admit(p.clientKey(r), r.Header.Get(HeaderBrokerID), code)
}

// StartRequestPolicy runs the request policy's maintenance until ctx is
// cancelled.
func (s *BrokerAuthService) StartRequestPolicy(ctx context.Context) {
	if s != nil && s.requestPolicy != nil {
		s.requestPolicy.StartMaintenance(ctx)
	}
}
