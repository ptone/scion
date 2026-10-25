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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
)

// Broker request-policy tests for the hub's seen-request record: a request
// is recorded only after its signature verifies, under a key derived from
// the verified HMAC, on both the in-memory and the DB-backed paths.

// seenRecordMode is one way a BrokerAuthService records served requests.
type seenRecordMode struct {
	name string
	// count returns how many records the service holds.
	count func(t *testing.T) int
}

// seenRecordFixture is a broker auth service with one broker and its
// secret.
type seenRecordFixture struct {
	svc      *BrokerAuthService
	store    store.Store
	brokerID string
	secret   []byte
}

func newSeenRecordStore(t *testing.T) store.Store {
	t.Helper()
	s, err := newTestStore(t, ":memory:")
	require.NoError(t, err)
	return s
}

// newSeenRecordService returns a broker auth service on s. With a non-nil
// nonceStore, the service records in that DB store; otherwise in memory.
func newSeenRecordService(t *testing.T, s store.Store, nonceStore *NonceCacheStore) *BrokerAuthService {
	t.Helper()
	svc := NewBrokerAuthService(DefaultBrokerAuthConfig(), s)
	t.Cleanup(svc.Close)
	if nonceStore != nil {
		svc.SetNonceCacheStore(nonceStore)
	}
	return svc
}

func seedSeenRecordBroker(t *testing.T, s store.Store, status string) (string, []byte) {
	t.Helper()
	ctx := context.Background()
	brokerID := uuid.New().String()
	require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: brokerID, Name: "seen-" + brokerID[:8], Slug: "seen-" + brokerID[:8], Status: store.BrokerStatusOnline,
		Created: time.Now(), Updated: time.Now(),
	}))
	secret := []byte("seen-record-secret-32-bytes-long")
	require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID: brokerID, SecretKey: secret,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256, Status: status,
	}))
	return brokerID, secret
}

func memoryRecordCount(svc *BrokerAuthService) func(t *testing.T) int {
	return func(t *testing.T) int {
		t.Helper()
		require.NotNil(t, svc.nonces)
		svc.nonces.mu.Lock()
		defer svc.nonces.mu.Unlock()
		return len(svc.nonces.nonces)
	}
}

func dbRecordCount(ns *NonceCacheStore) func(t *testing.T) int {
	return func(t *testing.T) int {
		t.Helper()
		n, err := ns.client.NonceCache.Query().Count(context.Background())
		require.NoError(t, err)
		return n
	}
}

// seenRecordCase is a fixture with the mode it records in.
type seenRecordCase struct {
	mode seenRecordMode
	fx   seenRecordFixture
}

// seenRecordFixtures returns one fixture per record mode (memory, DB).
func seenRecordFixtures(t *testing.T) []seenRecordCase {
	t.Helper()
	var out []seenRecordCase

	s := newSeenRecordStore(t)
	svc := newSeenRecordService(t, s, nil)
	brokerID, secret := seedSeenRecordBroker(t, s, store.BrokerSecretStatusActive)
	out = append(out, seenRecordCase{seenRecordMode{"memory", memoryRecordCount(svc)}, seenRecordFixture{svc, s, brokerID, secret}})

	s2 := newSeenRecordStore(t)
	ns := NewNonceCacheStore(enttest.NewClient(t))
	svc2 := newSeenRecordService(t, s2, ns)
	brokerID2, secret2 := seedSeenRecordBroker(t, s2, store.BrokerSecretStatusActive)
	out = append(out, seenRecordCase{seenRecordMode{"db", dbRecordCount(ns)}, seenRecordFixture{svc2, s2, brokerID2, secret2}})
	return out
}

// signSeenRecordRequest returns a POST signed with secret. signedHeaders
// maps header names to values that are set and listed in the signed-header
// list (in the order given by names).
func signSeenRecordRequest(svc *BrokerAuthService, brokerID string, secret []byte, ts time.Time, nonce string, names []string, values map[string]string) *http.Request {
	body := []byte(`{"seen":"record"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime-brokers/heartbeat", bytes.NewReader(body))
	timestamp := strconv.FormatInt(ts.Unix(), 10)
	req.Header.Set(HeaderBrokerID, brokerID)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	for _, n := range names {
		req.Header.Set(n, values[n])
	}
	if len(names) > 0 {
		req.Header.Set(HeaderSignedHeaders, strings.Join(names, ";"))
	}
	canonical := svc.buildCanonicalString(req, timestamp, nonce)
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(computeHMAC(secret, canonical)))
	return req
}

// cloneSeenRecordRequest copies req with a fresh body reader, so it can be
// presented again.
func cloneSeenRecordRequest(t *testing.T, req *http.Request) *http.Request {
	t.Helper()
	c := req.Clone(context.Background())
	if req.Body != nil {
		var buf bytes.Buffer
		_, err := buf.ReadFrom(req.Body)
		require.NoError(t, err)
		req.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))
		c.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))
		c.ContentLength = int64(buf.Len())
	}
	return c
}

// validators are the two hub entry points that verify a broker signature.
var seenRecordValidators = []struct {
	name     string
	validate func(svc *BrokerAuthService, r *http.Request) (BrokerIdentity, error)
}{
	{"single", func(svc *BrokerAuthService, r *http.Request) (BrokerIdentity, error) {
		return svc.ValidateBrokerSignature(context.Background(), r)
	}},
	{"rotation", func(svc *BrokerAuthService, r *http.Request) (BrokerIdentity, error) {
		return svc.ValidateBrokerSignatureWithRotation(context.Background(), r)
	}},
}

// TestBrokerSeenRecord_UnverifiedRequestCreatesNoRecord: a request that
// does not pass the signature check (wrong signature, unknown broker,
// revoked secret) leaves no record in memory or in the DB, and does not
// use up the request's later first use.
func TestBrokerSeenRecord_UnverifiedRequestCreatesNoRecord(t *testing.T) {
	for _, m := range seenRecordFixtures(t) {
		for _, v := range seenRecordValidators {
			t.Run(m.mode.name+"/"+v.name, func(t *testing.T) {
				fx := m.fx
				start := m.mode.count(t)
				revokedID, revokedSecret := seedSeenRecordBroker(t, fx.store, store.BrokerSecretStatusRevoked)
				now := time.Now()

				good := signSeenRecordRequest(fx.svc, fx.brokerID, fx.secret, now, "unverified-"+v.name, nil, nil)
				forged := cloneSeenRecordRequest(t, good)
				forged.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(computeHMAC([]byte("wrong-secret"), []byte("x"))))
				unknown := signSeenRecordRequest(fx.svc, uuid.New().String(), fx.secret, now, "unverified-unknown-"+v.name, nil, nil)
				revoked := signSeenRecordRequest(fx.svc, revokedID, revokedSecret, now, "unverified-revoked-"+v.name, nil, nil)

				for name, r := range map[string]*http.Request{"forged": forged, "unknown broker": unknown, "revoked secret": revoked} {
					_, err := v.validate(fx.svc, r)
					require.Error(t, err, name)
					require.Equal(t, start, m.mode.count(t), "%s: an unverified request must not be recorded", name)
				}

				// The forged copy did not use up the genuine request.
				_, err := v.validate(fx.svc, good)
				require.NoError(t, err)
				require.Equal(t, start+1, m.mode.count(t))
			})
		}
	}
}

// TestBrokerSeenRecord_ExactRepeatRefused: the same signed request is
// served once.
func TestBrokerSeenRecord_ExactRepeatRefused(t *testing.T) {
	for _, m := range seenRecordFixtures(t) {
		for _, v := range seenRecordValidators {
			t.Run(m.mode.name+"/"+v.name, func(t *testing.T) {
				fx := m.fx
				first := signSeenRecordRequest(fx.svc, fx.brokerID, fx.secret, time.Now(), "repeat-"+v.name, nil, nil)
				again := cloneSeenRecordRequest(t, first)

				_, err := v.validate(fx.svc, first)
				require.NoError(t, err)
				_, err = v.validate(fx.svc, again)
				require.True(t, errors.Is(err, errBrokerAuthReplay), "want a repeat refusal, got %v", err)

				// A freshly signed request for the same call is served.
				fresh := signSeenRecordRequest(fx.svc, fx.brokerID, fx.secret, time.Now(), "repeat-fresh-"+v.name, nil, nil)
				_, err = v.validate(fx.svc, fresh)
				require.NoError(t, err)
			})
		}
	}
}

// reframeSeenRecordRequest returns a copy of req (signed with nonce and
// one signed header, name: value) whose headers are arranged differently
// but whose canonical bytes, and so its signature, are those of req.
func reframeSeenRecordRequest(t *testing.T, svc *BrokerAuthService, req *http.Request, nonce, name, value string) *http.Request {
	t.Helper()
	timestamp := req.Header.Get(HeaderTimestamp)
	orig := cloneSeenRecordRequest(t, req)
	origCanonical := svc.buildCanonicalString(orig, timestamp, nonce)

	c := cloneSeenRecordRequest(t, req)
	reframedNonce := nonce + "\n" + strings.ToLower(name) + ":" + value
	c.Header.Set(HeaderNonce, reframedNonce)
	c.Header.Del(HeaderSignedHeaders)
	c.Header.Del(name)
	check := cloneSeenRecordRequest(t, c)
	require.Equal(t, string(origCanonical), string(svc.buildCanonicalString(check, timestamp, reframedNonce)),
		"the reframed request must have the original's canonical bytes")
	return c
}

// TestBrokerSeenRecord_ReframedRequestRefused: a copy of a served request
// with differently arranged headers but the same canonical bytes and
// signature is refused as a repeat.
func TestBrokerSeenRecord_ReframedRequestRefused(t *testing.T) {
	const hdr = "X-Scion-Seen-Test"
	for _, m := range seenRecordFixtures(t) {
		for _, v := range seenRecordValidators {
			t.Run(m.mode.name+"/"+v.name, func(t *testing.T) {
				fx := m.fx
				nonce := "reframe-" + v.name
				first := signSeenRecordRequest(fx.svc, fx.brokerID, fx.secret, time.Now(), nonce, []string{hdr}, map[string]string{hdr: "v1"})
				reframed := reframeSeenRecordRequest(t, fx.svc, first, nonce, hdr, "v1")

				_, err := v.validate(fx.svc, first)
				require.NoError(t, err)
				before := m.mode.count(t)
				_, err = v.validate(fx.svc, reframed)
				require.True(t, errors.Is(err, errBrokerAuthReplay), "want a repeat refusal, got %v", err)
				require.Equal(t, before, m.mode.count(t))
			})
		}
	}
}

// respellSignature returns another base64 spelling of sig's bytes (the
// unused low bits of the last data character set differently).
func respellSignature(t *testing.T, sig string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	require.True(t, strings.HasSuffix(sig, "=") && !strings.HasSuffix(sig, "=="), "32-byte HMAC has one pad character: %q", sig)
	i := len(sig) - 2
	idx := strings.IndexByte(alphabet, sig[i])
	require.GreaterOrEqual(t, idx, 0)
	out := sig[:i] + string(alphabet[idx^1]) + sig[i+1:]
	want, err := base64.StdEncoding.DecodeString(sig)
	require.NoError(t, err)
	got, err := base64.StdEncoding.DecodeString(out)
	require.NoError(t, err)
	require.Equal(t, want, got, "the re-spelled signature must decode to the same bytes")
	require.NotEqual(t, sig, out)
	return out
}

// TestBrokerSeenRecord_RespelledSignatureRefused: another spelling of a
// served request's signature is refused, records nothing, and does not
// use up the genuine request when presented first.
func TestBrokerSeenRecord_RespelledSignatureRefused(t *testing.T) {
	for _, m := range seenRecordFixtures(t) {
		for _, v := range seenRecordValidators {
			t.Run(m.mode.name+"/"+v.name, func(t *testing.T) {
				fx := m.fx
				req := signSeenRecordRequest(fx.svc, fx.brokerID, fx.secret, time.Now(), "respell-"+v.name, nil, nil)
				respelled := cloneSeenRecordRequest(t, req)
				respelled.Header.Set(HeaderSignature, respellSignature(t, req.Header.Get(HeaderSignature)))
				respelledAgain := cloneSeenRecordRequest(t, respelled)
				start := m.mode.count(t)

				_, err := v.validate(fx.svc, respelled)
				require.Error(t, err)
				require.Equal(t, start, m.mode.count(t))

				_, err = v.validate(fx.svc, req)
				require.NoError(t, err)
				require.Equal(t, start+1, m.mode.count(t))

				_, err = v.validate(fx.svc, respelledAgain)
				require.Error(t, err)
				require.Equal(t, start+1, m.mode.count(t))
			})
		}
	}
}

// TestBrokerSeenRecord_SharedStoreAcrossInstances: two hub instances that
// share one DB-backed store serve a signed request once between them,
// including a reframed copy.
func TestBrokerSeenRecord_SharedStoreAcrossInstances(t *testing.T) {
	const hdr = "X-Scion-Seen-Test"
	s := newSeenRecordStore(t)
	shared := NewNonceCacheStore(enttest.NewClient(t))
	a := newSeenRecordService(t, s, shared)
	b := newSeenRecordService(t, s, shared)
	brokerID, secret := seedSeenRecordBroker(t, s, store.BrokerSecretStatusActive)

	for _, v := range seenRecordValidators {
		t.Run(v.name, func(t *testing.T) {
			nonce := "shared-" + v.name
			first := signSeenRecordRequest(a, brokerID, secret, time.Now(), nonce, []string{hdr}, map[string]string{hdr: "v1"})
			exact := cloneSeenRecordRequest(t, first)
			reframed := reframeSeenRecordRequest(t, a, first, nonce, hdr, "v1")

			_, err := v.validate(a, first)
			require.NoError(t, err)
			_, err = v.validate(b, exact)
			require.True(t, errors.Is(err, errBrokerAuthReplay), "exact copy on the other instance: got %v", err)
			_, err = v.validate(b, reframed)
			require.True(t, errors.Is(err, errBrokerAuthReplay), "reframed copy on the other instance: got %v", err)

			// An unverified request on either instance writes no row.
			before := dbRecordCount(shared)(t)
			forged := signSeenRecordRequest(a, brokerID, []byte("wrong-secret"), time.Now(), nonce+"-forged", nil, nil)
			_, err = v.validate(b, forged)
			require.Error(t, err)
			require.Equal(t, before, dbRecordCount(shared)(t))
		})
	}
}

// TestBrokerSeenRecord_DistinctRequestSameNonceServed: the record is keyed
// on the verified signature, not the nonce. A different request (another
// body) that carries a served request's nonce has its own canonical bytes
// and needs its own valid signature from the broker's secret; it is served
// once, and its own exact repeat is refused.
func TestBrokerSeenRecord_DistinctRequestSameNonceServed(t *testing.T) {
	for _, m := range seenRecordFixtures(t) {
		for _, v := range seenRecordValidators {
			t.Run(m.mode.name+"/"+v.name, func(t *testing.T) {
				fx := m.fx
				nonce := "shared-nonce-" + v.name
				now := time.Now()
				first := signSeenRecordRequest(fx.svc, fx.brokerID, fx.secret, now, nonce, nil, nil)
				other := signSeenRecordRequestBody(fx.svc, fx.brokerID, fx.secret, now, nonce, []byte(`{"seen":"other"}`))
				require.NotEqual(t, first.Header.Get(HeaderSignature), other.Header.Get(HeaderSignature))
				otherAgain := cloneSeenRecordRequest(t, other)

				_, err := v.validate(fx.svc, first)
				require.NoError(t, err)
				before := m.mode.count(t)
				_, err = v.validate(fx.svc, other)
				require.NoError(t, err, "a different signed request with the same nonce is served")
				require.Equal(t, before+1, m.mode.count(t))
				_, err = v.validate(fx.svc, otherAgain)
				require.True(t, errors.Is(err, errBrokerAuthReplay), "its exact repeat is refused, got %v", err)
			})
		}
	}
}

// signSeenRecordRequestBody is signSeenRecordRequest with a given body and
// no signed headers.
func signSeenRecordRequestBody(svc *BrokerAuthService, brokerID string, secret []byte, ts time.Time, nonce string, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime-brokers/heartbeat", bytes.NewReader(body))
	timestamp := strconv.FormatInt(ts.Unix(), 10)
	req.Header.Set(HeaderBrokerID, brokerID)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	canonical := svc.buildCanonicalString(req, timestamp, nonce)
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(computeHMAC(secret, canonical)))
	return req
}
