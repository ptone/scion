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

//go:build !no_sqlite

package hub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Broker request policy: the failure budget in front of the broker
// signature check, and the bounded failed-auth log. These tests use only
// the middleware and service API, so they also build against a tree
// without the policy.

// secretReadCountingStore counts broker secret reads.
type secretReadCountingStore struct {
	store.Store
	reads atomic.Int64
}

func (c *secretReadCountingStore) GetBrokerSecret(ctx context.Context, brokerID string) (*store.BrokerSecret, error) {
	c.reads.Add(1)
	return c.Store.GetBrokerSecret(ctx, brokerID)
}

// policyTestBroker is a broker with an active secret that can sign
// requests from a chosen client address.
type policyTestBroker struct {
	id     string
	secret []byte
	svc    *BrokerAuthService
	nonce  atomic.Int64
}

func newPolicyTestBroker(t *testing.T, svc *BrokerAuthService, s store.Store) *policyTestBroker {
	t.Helper()
	ctx := context.Background()
	id := uuid.New().String()
	require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: id, Name: "policy-" + id[:8], Slug: "policy-" + id[:8], Status: store.BrokerStatusOnline,
		Created: time.Now(), Updated: time.Now(),
	}))
	secret := []byte("policy-test-secret-32-bytes-long")
	require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID: id, SecretKey: secret,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256, Status: store.BrokerSecretStatusActive,
	}))
	return &policyTestBroker{id: id, secret: secret, svc: svc}
}

// signed returns a validly signed request from remoteAddr.
func (b *policyTestBroker) signed(remoteAddr string) *http.Request {
	n := b.nonce.Add(1)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := fmt.Sprintf("policy-%s-%d", b.id[:8], n)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime-brokers", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set(HeaderBrokerID, b.id)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	h := hmac.New(sha256.New, b.secret)
	h.Write(b.svc.buildCanonicalString(req, timestamp, nonce))
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(h.Sum(nil)))
	return req
}

// badSignature returns a request for brokerID from remoteAddr whose
// signature does not verify. Every request has a fresh nonce, so without a
// budget each one reaches the secret read and the HMAC.
var badSignatureNonce atomic.Int64

func badSignature(brokerID, remoteAddr string) *http.Request {
	req := unsignedBrokerRequest(brokerID, strconv.FormatInt(time.Now().Unix(), 10),
		fmt.Sprintf("policy-bad-%d", badSignatureNonce.Add(1)))
	req.RemoteAddr = remoteAddr
	return req
}

func policyTestAddr(i int) string {
	return fmt.Sprintf("198.51.%d.%d:40000", (i/250)%250, i%250+1)
}

type policyTestServer struct {
	svc    *BrokerAuthService
	counts *secretReadCountingStore
	audit  *mockAuditLogger
	h      http.Handler
	served atomic.Int64
}

func newPolicyTestServer(t *testing.T) *policyTestServer {
	t.Helper()
	base, err := newTestStore(t, ":memory:")
	require.NoError(t, err)
	counts := &secretReadCountingStore{Store: base}
	svc := NewBrokerAuthService(DefaultBrokerAuthConfig(), counts)
	t.Cleanup(svc.Close)
	ts := &policyTestServer{svc: svc, counts: counts, audit: &mockAuditLogger{}}
	ts.h = AuditableBrokerAuthMiddleware(svc, ts.audit)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ts.served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	return ts
}

func (ts *policyTestServer) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ts.h.ServeHTTP(rec, req)
	return rec
}

func (ts *policyTestServer) failureEvents() int {
	n := 0
	for _, e := range ts.audit.brokerEvents {
		if e.EventType == BrokerAuthEventAuthFailure {
			n++
		}
	}
	return n
}

func TestBrokerAuthPolicy_SingleSourceBurstIsRefusedBeforeSecretRead(t *testing.T) {
	ts := newPolicyTestServer(t)
	broker := newPolicyTestBroker(t, ts.svc, ts.counts)
	const addr = "192.0.2.10:5555"

	var refused int
	var firstRefusal *httptest.ResponseRecorder
	for i := 0; i < 200; i++ {
		rec := ts.do(badSignature(broker.id, addr))
		switch rec.Code {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			refused++
			if firstRefusal == nil {
				firstRefusal = rec
			}
		default:
			t.Fatalf("request %d: status %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	require.NotNil(t, firstRefusal, "a burst of 200 failed signatures from one address must be refused")
	assert.Equal(t, http.StatusTooManyRequests, firstRefusal.Code)
	retry, err := strconv.Atoi(firstRefusal.Header().Get("Retry-After"))
	require.NoError(t, err, "a refusal carries Retry-After in seconds")
	assert.GreaterOrEqual(t, retry, 1)
	assert.Contains(t, firstRefusal.Body.String(), ErrCodeRateLimited)

	// The budget is checked before the secret read: refused requests do no
	// store work. About 30 failures fit the per-source burst.
	reads := ts.counts.reads.Load()
	assert.LessOrEqual(t, reads, int64(35), "secret reads must stop once the source is refused")
	assert.Equal(t, int64(200), reads+int64(refused), "every request is either checked or refused")

	before := ts.counts.reads.Load()
	ts.do(badSignature(broker.id, addr))
	assert.Equal(t, before, ts.counts.reads.Load(), "a refused request must not read the broker secret")
}

func TestBrokerAuthPolicy_DistributedBurstHitsGlobalLimit(t *testing.T) {
	ts := newPolicyTestServer(t)
	broker := newPolicyTestBroker(t, ts.svc, ts.counts)

	// 40 sources x 10 failures: each source stays inside its own burst.
	var refused int
	for round := 0; round < 10; round++ {
		for src := 0; src < 40; src++ {
			if ts.do(badSignature(broker.id, policyTestAddr(src))).Code == http.StatusTooManyRequests {
				refused++
			}
		}
	}
	assert.Positive(t, refused, "a distributed burst of 400 failures must reach the global limit")
	assert.Less(t, ts.counts.reads.Load(), int64(300), "the global limit must cap secret reads")
}

func TestBrokerAuthPolicy_EstablishedBrokerPassesDuringFlood(t *testing.T) {
	ts := newPolicyTestServer(t)
	established := newPolicyTestBroker(t, ts.svc, ts.counts)
	newcomer := newPolicyTestBroker(t, ts.svc, ts.counts)
	const brokerAddr = "203.0.113.20:7000"

	require.Equal(t, http.StatusOK, ts.do(established.signed(brokerAddr)).Code)

	// Flood from many sources (each inside its own burst) until the global
	// budget refuses, then probe at once, before it refills.
	drained := false
	for i := 0; i < 1000 && !drained; i++ {
		drained = ts.do(badSignature(established.id, policyTestAddr(i%80))).Code == http.StatusTooManyRequests
	}
	require.True(t, drained, "the flood must reach the global limit")
	require.Equal(t, http.StatusTooManyRequests, ts.do(newcomer.signed("203.0.113.99:7000")).Code,
		"the flood must have drained the global budget for sources without an established pair")

	for i := 0; i < 50; i++ {
		rec := ts.do(established.signed(brokerAddr))
		require.Equal(t, http.StatusOK, rec.Code, "request %d of an established broker: %s", i, rec.Body.String())
	}
}

func TestBrokerAuthPolicy_FailedAuthAuditVolumeIsBounded(t *testing.T) {
	t.Run("one source", func(t *testing.T) {
		ts := newPolicyTestServer(t)
		broker := newPolicyTestBroker(t, ts.svc, ts.counts)
		for i := 0; i < 1000; i++ {
			ts.do(badSignature(broker.id, "192.0.2.30:1000"))
		}
		// One full event per (source, broker, failure code) per window.
		assert.Equal(t, 1, ts.failureEvents())
	})
	t.Run("many sources", func(t *testing.T) {
		ts := newPolicyTestServer(t)
		broker := newPolicyTestBroker(t, ts.svc, ts.counts)
		start := time.Now()
		for src := 0; src < 300; src++ {
			ts.do(badSignature(broker.id, policyTestAddr(src)))
		}
		// The hub-wide cap: a burst of 50 plus 5/s while the test runs.
		limit := 50 + int(5*time.Since(start).Seconds()) + 1
		assert.LessOrEqual(t, ts.failureEvents(), limit)
		assert.Positive(t, ts.failureEvents())
	})
}

func TestBrokerAuthPolicy_LowRateFailureIsLogged(t *testing.T) {
	ts := newPolicyTestServer(t)
	broker := newPolicyTestBroker(t, ts.svc, ts.counts)
	rec := ts.do(badSignature(broker.id, "192.0.2.40:1000"))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, 1, ts.failureEvents())
	e := ts.audit.brokerEvents[0]
	assert.Equal(t, broker.id, e.BrokerID)
	assert.False(t, e.Success)
	assert.Equal(t, "192.0.2.40", e.IPAddress)
}

func TestBrokerAuthPolicy_ForwardedHeaderFromUnlistedPeerIsIgnored(t *testing.T) {
	t.Run("audit address", func(t *testing.T) {
		ts := newPolicyTestServer(t)
		broker := newPolicyTestBroker(t, ts.svc, ts.counts)
		req := badSignature(broker.id, "192.0.2.50:1000")
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		req.Header.Set("X-Real-IP", "203.0.113.8")
		ts.do(req)
		require.Equal(t, 1, ts.failureEvents())
		assert.Equal(t, "192.0.2.50", ts.audit.brokerEvents[0].IPAddress)
	})
	t.Run("budget key", func(t *testing.T) {
		ts := newPolicyTestServer(t)
		broker := newPolicyTestBroker(t, ts.svc, ts.counts)
		var refused int
		for i := 0; i < 100; i++ {
			req := badSignature(broker.id, "192.0.2.51:1000")
			req.Header.Set("X-Forwarded-For", policyTestAddr(i)[:len(policyTestAddr(i))-len(":40000")])
			if ts.do(req).Code == http.StatusTooManyRequests {
				refused++
			}
		}
		assert.Positive(t, refused, "a rotating X-Forwarded-For from one peer must not escape its budget")
	})
}

func TestBrokerAuthPolicy_UpgradeRefusedBeforeHandshake(t *testing.T) {
	ts := newPolicyTestServer(t)
	broker := newPolicyTestBroker(t, ts.svc, ts.counts)
	srv := &Server{brokerAuthService: ts.svc}
	const addr = "192.0.2.60:1000"
	var last *httptest.ResponseRecorder
	for i := 0; i < 100; i++ {
		last = httptest.NewRecorder()
		if _, ok := srv.authenticateBrokerUpgrade(last, badSignature(broker.id, addr)); ok {
			t.Fatal("a bad signature must not authenticate")
		}
	}
	assert.Equal(t, http.StatusTooManyRequests, last.Code)
	assert.NotEmpty(t, last.Header().Get("Retry-After"))
}

func TestBrokerAuthPolicy_LegitimateTrafficIsNotRefused(t *testing.T) {
	t.Run("steady load from one broker", func(t *testing.T) {
		ts := newPolicyTestServer(t)
		broker := newPolicyTestBroker(t, ts.svc, ts.counts)
		for i := 0; i < 2000; i++ {
			rec := ts.do(broker.signed("203.0.113.30:9000"))
			require.Equal(t, http.StatusOK, rec.Code, "request %d: %s", i, rec.Body.String())
		}
	})
	t.Run("reconnect wave, sequential", func(t *testing.T) {
		ts := newPolicyTestServer(t)
		// More brokers than the global burst: each valid request is
		// refunded, so none is refused.
		for i := 0; i < 400; i++ {
			b := newPolicyTestBroker(t, ts.svc, ts.counts)
			rec := ts.do(b.signed(policyTestAddr(i)))
			require.Equal(t, http.StatusOK, rec.Code, "broker %d: %s", i, rec.Body.String())
		}
	})
	// Concurrent waves: a valid request holds its reservation only while
	// its signature is checked, so a wave is never refused while fewer than
	// the burst (30 per source, 200 hub-wide) are checked at once.
	concurrentWave := func(t *testing.T, n int, addr func(i int) string) {
		ts := newPolicyTestServer(t)
		brokers := make([]*policyTestBroker, n)
		for i := range brokers {
			brokers[i] = newPolicyTestBroker(t, ts.svc, ts.counts)
		}
		var wg sync.WaitGroup
		var refused atomic.Int64
		for i, b := range brokers {
			wg.Add(1)
			go func(i int, b *policyTestBroker) {
				defer wg.Done()
				for j := 0; j < 5; j++ {
					rec := httptest.NewRecorder()
					ts.h.ServeHTTP(rec, b.signed(addr(i)))
					if rec.Code != http.StatusOK {
						refused.Add(1)
					}
				}
			}(i, b)
		}
		wg.Wait()
		assert.Zero(t, refused.Load())
	}
	t.Run("reconnect wave, concurrent, distinct addresses", func(t *testing.T) {
		concurrentWave(t, 150, policyTestAddr)
	})
	t.Run("reconnect wave, concurrent, brokers behind one address", func(t *testing.T) {
		concurrentWave(t, 25, func(int) string { return "203.0.113.40:9000" })
	})
}
