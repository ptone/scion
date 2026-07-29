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

// Package hub provides the Scion Hub API server.
package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
)

// BrokerAuthConfig holds broker authentication configuration.
type BrokerAuthConfig struct {
	// Enabled controls whether broker authentication is active.
	Enabled bool
	// MaxClockSkew is the maximum allowed time difference between client and server.
	MaxClockSkew time.Duration
	// EnableNonceCache enables replay attack prevention via nonce caching.
	EnableNonceCache bool
	// NonceCacheTTL is how long nonces are cached (should be > MaxClockSkew).
	NonceCacheTTL time.Duration
	// JoinTokenExpiry is how long join tokens remain valid.
	JoinTokenExpiry time.Duration
	// JoinTokenLength is the length of generated join tokens in bytes.
	JoinTokenLength int
	// SecretKeyLength is the length of generated secret keys in bytes.
	SecretKeyLength int
}

// DefaultBrokerAuthConfig returns the default broker authentication configuration.
func DefaultBrokerAuthConfig() BrokerAuthConfig {
	return BrokerAuthConfig{
		Enabled:          true,
		MaxClockSkew:     5 * time.Minute,
		EnableNonceCache: true, // Enabled by default for replay attack prevention
		NonceCacheTTL:    10 * time.Minute,
		JoinTokenExpiry:  1 * time.Hour,
		JoinTokenLength:  32,
		SecretKeyLength:  32, // 256 bits
	}
}

// BrokerAuthService handles broker registration and HMAC-based authentication.
type BrokerAuthService struct {
	config BrokerAuthConfig
	store  store.Store
	nonces *NonceCache
}

// NonceCache provides replay attack prevention by caching used nonces.
type NonceCache struct {
	mu     sync.RWMutex
	nonces map[string]time.Time
	ttl    time.Duration
	done   chan struct{}
}

// NewNonceCache creates a new nonce cache.
func NewNonceCache(ttl time.Duration) *NonceCache {
	nc := &NonceCache{
		nonces: make(map[string]time.Time),
		ttl:    ttl,
		done:   make(chan struct{}),
	}
	// Start cleanup goroutine
	go nc.cleanup()
	return nc
}

// Stop shuts down the cleanup goroutine. It is safe to call multiple times.
func (nc *NonceCache) Stop() {
	select {
	case <-nc.done:
		// Already stopped.
	default:
		close(nc.done)
	}
}

// Add adds a nonce to the cache. Returns false if nonce already exists.
func (nc *NonceCache) Add(nonce string) bool {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if _, exists := nc.nonces[nonce]; exists {
		return false
	}
	nc.nonces[nonce] = time.Now()
	return true
}

// cleanup periodically removes expired nonces.
func (nc *NonceCache) cleanup() {
	ticker := time.NewTicker(nc.ttl / 2)
	defer ticker.Stop()

	for {
		select {
		case <-nc.done:
			return
		case <-ticker.C:
			nc.mu.Lock()
			cutoff := time.Now().Add(-nc.ttl)
			for nonce, addedAt := range nc.nonces {
				if addedAt.Before(cutoff) {
					delete(nc.nonces, nonce)
				}
			}
			nc.mu.Unlock()
		}
	}
}

// NewBrokerAuthService creates a new broker authentication service.
func NewBrokerAuthService(config BrokerAuthConfig, s store.Store) *BrokerAuthService {
	svc := &BrokerAuthService{
		config: config,
		store:  s,
	}
	if config.EnableNonceCache {
		svc.nonces = NewNonceCache(config.NonceCacheTTL)
	}
	return svc
}

// Close releases resources held by the BrokerAuthService, including stopping the
// nonce cache cleanup goroutine.
func (bas *BrokerAuthService) Close() {
	if bas.nonces != nil {
		bas.nonces.Stop()
	}
}

// =============================================================================
// Broker Registration
// =============================================================================

// CreateBrokerRegistrationRequest is the request body for POST /api/v1/brokers.
type CreateBrokerRegistrationRequest struct {
	BrokerID     string            `json:"brokerId,omitempty"` // Optional stable broker UUID supplied by the client
	Name         string            `json:"name"`
	AutoProvide  bool              `json:"autoProvide,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// CreateBrokerRegistrationResponse is the response for POST /api/v1/brokers.
type CreateBrokerRegistrationResponse struct {
	BrokerID     string    `json:"brokerId"`
	JoinToken    string    `json:"joinToken"` // scion_join_<base64>
	ExpiresAt    time.Time `json:"expiresAt"`
	Reregistered bool      `json:"reregistered,omitempty"`
}

// BrokerJoinRequest is the request body for POST /api/v1/brokers/join.
type BrokerJoinRequest struct {
	BrokerID     string                `json:"brokerId"`
	JoinToken    string                `json:"joinToken"`
	Hostname     string                `json:"hostname"`
	Version      string                `json:"version"`
	Capabilities []string              `json:"capabilities,omitempty"`
	Profiles     []store.BrokerProfile `json:"profiles,omitempty"`
}

// BrokerJoinResponse is the response for POST /api/v1/brokers/join.
type BrokerJoinResponse struct {
	SecretKey   string `json:"secretKey"` // Base64-encoded 256-bit key
	HubEndpoint string `json:"hubEndpoint"`
	BrokerID    string `json:"brokerId"`
}

// JoinTokenPrefix is the prefix for join tokens.
const JoinTokenPrefix = "scion_join_"

// ErrBrokerIDRejected is returned when a client-supplied broker identifier is
// refused. It is a sentinel so a handler can map the refusal to a client error
// rather than an internal one.
//
// Every rejection reason — bad format and already-taken alike — collapses to
// this one generic error carrying no distinguishing detail, so the broker
// registration endpoint cannot be used as a membership oracle for existing
// principals. Keep new reject paths uniform: return this sentinel, do not wrap
// it with a reason the caller can tell apart. See #591.
var ErrBrokerIDRejected = errors.New("brokerId rejected")

// ErrBrokerReregisterForbidden is returned when a caller who is not the broker's
// recorded owner attempts to re-register an existing broker. Re-registration
// overwrites broker settings (e.g. AutoProvide) and mints a fresh join token, so
// where an owner is recorded it is restricted to that owner. The N59/#149
// empty-guard means an empty recorded owner never matches a caller id; a broker
// with no recorded owner is the register-by-name divergence (#221) and is left to
// its existing restart behaviour, so this sentinel closes only the recorded-owner
// mismatch (#591).
var ErrBrokerReregisterForbidden = errors.New("not authorized to re-register this broker")

// validateBrokerIDFormat requires a client-supplied broker identifier to be a
// UUID in canonical form.
//
// Canonical form, not merely "parses as a UUID": uuid.Parse also accepts
// braced, URN and unhyphenated spellings, and one identifier must have one
// spelling so it cannot be registered under one and looked up under another.
//
// This regresses nothing that is produced today: the only client that supplies
// a broker id is the broker itself, which sends either a uuid.New().String()
// or the value the hub previously handed it, both canonical.
func validateBrokerIDFormat(brokerID string) error {
	parsed, err := uuid.Parse(brokerID)
	if err != nil {
		return ErrBrokerIDRejected
	}
	// Broker ids are pinned to canonical UUID form to prevent id-collision /
	// spoofing across principal namespaces: a non-canonical spelling could be
	// registered under one form and resolved under another. See #591. The
	// refusal is deliberately generic — see ErrBrokerIDRejected.
	if parsed.String() != brokerID {
		return ErrBrokerIDRejected
	}
	return nil
}

// brokerIDReservedNamespace is one principal namespace a broker identifier must
// not be drawn from, and the lookup that decides whether a given identifier is
// already taken in it.
type brokerIDReservedNamespace struct {
	// name is for the enumeration test, not for the error message.
	name  string
	taken func(context.Context, store.Store, string) (bool, error)
}

// brokerIDReservedNamespaces enumerates the principal namespaces whose
// identifiers a broker may not adopt.
//
// The set is the principal types the policy layer binds against — see
// AddPolicyBindingRequest, whose principalType is "user", "group" or "agent" —
// plus projects, which are the other id that ownership and scope checks key on.
// The hub compares principal ids as opaque strings across all of these, so an
// identifier that exists in one of them does not uniquely denote a broker, and
// a credential issued for it is a credential for an ambiguous name.
//
// TestBrokerID_ReservedNamespacesCoverPolicyPrincipalTypes pins this against
// the policy layer's principal types, so a principal type added there without
// being reserved here fails rather than passing silently.
func brokerIDReservedNamespaces() []brokerIDReservedNamespace {
	return []brokerIDReservedNamespace{
		{"user", func(ctx context.Context, s store.Store, id string) (bool, error) {
			v, err := s.GetUser(ctx, id)
			return v != nil, err
		}},
		{"agent", func(ctx context.Context, s store.Store, id string) (bool, error) {
			v, err := s.GetAgent(ctx, id)
			return v != nil, err
		}},
		{"group", func(ctx context.Context, s store.Store, id string) (bool, error) {
			v, err := s.GetGroup(ctx, id)
			return v != nil, err
		}},
		{"project", func(ctx context.Context, s store.Store, id string) (bool, error) {
			v, err := s.GetProject(ctx, id)
			return v != nil, err
		}},
	}
}

// checkBrokerIDNamespace refuses a broker identifier that already denotes some
// other kind of principal.
//
// A lookup error that is not "not found" is propagated rather than read as
// "free": the question being asked is whether the identifier is available, and
// an unanswered question is not a yes.
func checkBrokerIDNamespace(ctx context.Context, s store.Store, brokerID string) error {
	for _, ns := range brokerIDReservedNamespaces() {
		taken, err := ns.taken(ctx, s, brokerID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("failed to check brokerId availability: %w", err)
		}
		if taken {
			// The same generic refusal as a bad-format id: the two reasons
			// must stay indistinguishable to the caller. See #591 and
			// ErrBrokerIDRejected.
			return ErrBrokerIDRejected
		}
	}
	return nil
}

// validateNewBrokerID is THE entry point for any code path about to create a
// broker row under an identifier the CLIENT chose. It takes a store rather than
// hanging off BrokerAuthService precisely so that every such path can call it:
// broker rows are created from more than one handler, and two paths each with
// their own idea of a valid broker id is the drift this work exists to remove.
// Mirror this by CALLING it, not by copying it.
//
// It does not need to be called for an id the hub generates itself.
func validateNewBrokerID(ctx context.Context, s store.Store, brokerID string) error {
	if err := validateBrokerIDFormat(brokerID); err != nil {
		return err
	}
	return checkBrokerIDNamespace(ctx, s, brokerID)
}

// validateCredentialedBrokerID is the entry point for a path about to issue a
// credential for a broker identifier that ALREADY has a row.
//
// It is the namespace half only. The format half is deliberately absent: an
// existing row's id is resolved by lookup rather than allocated, and rows
// created before any format rule existed must still be able to re-register.
// The namespace half is NOT skippable here — an existing row is not evidence
// that its id was ever checked, because rows are created by more than one path
// and outlive the code that created them, and re-registration issues a fresh
// credential.
func validateCredentialedBrokerID(ctx context.Context, s store.Store, brokerID string) error {
	return checkBrokerIDNamespace(ctx, s, brokerID)
}

// CreateBrokerRegistration creates a new broker with a join token.
// Requires authentication.
func (s *BrokerAuthService) CreateBrokerRegistration(ctx context.Context, req CreateBrokerRegistrationRequest, createdBy string) (*CreateBrokerRegistrationResponse, error) {
	if req.Name == "" {
		return nil, errors.New("name is required")
	}

	// Default broker-type label to "external" if not provided
	if req.Labels == nil {
		req.Labels = make(map[string]string)
	}
	if _, exists := req.Labels["scion.io/broker-type"]; !exists {
		req.Labels["scion.io/broker-type"] = "external"
	}

	// Before generating a new broker ID, check for an existing broker with same name
	var brokerID string
	var reregistered bool

	existingBroker, err := s.store.GetRuntimeBrokerByName(ctx, req.Name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("failed to check existing broker: %w", err)
	}

	// Also check for re-registration by client-supplied BrokerID
	if existingBroker == nil && req.BrokerID != "" {
		existingByID, err := s.store.GetRuntimeBroker(ctx, req.BrokerID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("failed to check existing broker by ID: %w", err)
		}
		if existingByID != nil {
			existingBroker = existingByID
		}
	}

	// Validate the broker identifier through the shared helper before anything is
	// written or any credential is issued, in both the new-broker and the
	// re-registration case. See the two entry points for why they differ.
	if existingBroker != nil {
		if err := validateCredentialedBrokerID(ctx, s.store, existingBroker.ID); err != nil {
			return nil, err
		}
	} else if req.BrokerID != "" {
		if err := validateNewBrokerID(ctx, s.store, req.BrokerID); err != nil {
			return nil, err
		}
	}

	if existingBroker != nil {
		// Re-registration is an ownership-control path: it overwrites broker
		// settings (AutoProvide below) and mints a fresh join token, so where an
		// owner is recorded only that owner may perform it. The `!= ""` guard
		// (N59/#149) keeps an empty recorded owner from matching an absent/empty
		// caller id; a broker with no recorded owner is the register-by-name
		// divergence (#221), left untouched here (deferred), so its restart flow
		// still works — this commit only closes the recorded-owner mismatch (#591).
		if existingBroker.CreatedBy != "" && existingBroker.CreatedBy != createdBy {
			return nil, ErrBrokerReregisterForbidden
		}
		// Reuse existing broker - update its metadata
		brokerID = existingBroker.ID
		reregistered = true
		existingBroker.AutoProvide = req.AutoProvide
		// Merge request labels into existing labels to preserve any
		// user-set labels while updating registration-provided ones.
		if len(req.Labels) > 0 {
			if existingBroker.Labels == nil {
				existingBroker.Labels = make(map[string]string, len(req.Labels))
			}
			for k, v := range req.Labels {
				existingBroker.Labels[k] = v
			}
		}
		existingBroker.Updated = time.Now()
		if err := s.store.UpdateRuntimeBroker(ctx, existingBroker); err != nil {
			return nil, fmt.Errorf("failed to update existing broker: %w", err)
		}
	} else {
		// Create new broker - use client-supplied ID if provided, otherwise generate
		if req.BrokerID != "" {
			brokerID = req.BrokerID
		} else {
			brokerID = uuid.New().String()
		}

		broker := &store.RuntimeBroker{
			ID:          brokerID,
			Name:        req.Name,
			Slug:        slugify(req.Name),
			Status:      store.BrokerStatusOffline,
			AutoProvide: req.AutoProvide,
			Labels:      req.Labels,
			Created:     time.Now(),
			Updated:     time.Now(),
			CreatedBy:   createdBy,
		}

		if err := s.store.CreateRuntimeBroker(ctx, broker); err != nil {
			return nil, fmt.Errorf("failed to create runtime broker: %w", err)
		}
	}

	// Generate join token
	tokenBytes := make([]byte, s.config.JoinTokenLength)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("failed to generate join token: %w", err)
	}
	joinToken := JoinTokenPrefix + base64.URLEncoding.EncodeToString(tokenBytes)

	// Hash the token for storage
	tokenHash := sha256Hash(joinToken)

	// Calculate expiry
	expiresAt := time.Now().Add(s.config.JoinTokenExpiry)

	// Store the join token
	joinTokenRecord := &store.BrokerJoinToken{
		BrokerID:  brokerID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
		CreatedBy: createdBy,
	}

	if err := s.store.CreateJoinToken(ctx, joinTokenRecord); err != nil {
		// Clean up the broker record on failure (only if we just created it)
		if !reregistered {
			_ = s.store.DeleteRuntimeBroker(ctx, brokerID)
		}
		return nil, fmt.Errorf("failed to create join token: %w", err)
	}

	return &CreateBrokerRegistrationResponse{
		BrokerID:     brokerID,
		JoinToken:    joinToken,
		ExpiresAt:    expiresAt,
		Reregistered: reregistered,
	}, nil
}

// CompleteBrokerJoin completes broker registration with join token exchange.
// Returns the shared secret for HMAC authentication.
func (s *BrokerAuthService) CompleteBrokerJoin(ctx context.Context, req BrokerJoinRequest, hubEndpoint string) (*BrokerJoinResponse, error) {
	if req.BrokerID == "" {
		return nil, errors.New("brokerId is required")
	}
	if req.JoinToken == "" {
		return nil, errors.New("joinToken is required")
	}

	// Hash the provided token
	tokenHash := sha256Hash(req.JoinToken)

	// Look up the join token
	joinToken, err := s.store.GetJoinToken(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("invalid join token")
		}
		return nil, fmt.Errorf("failed to validate join token: %w", err)
	}

	// Verify broker ID matches
	if joinToken.BrokerID != req.BrokerID {
		return nil, fmt.Errorf("join token does not match broker")
	}

	// Check expiry
	if time.Now().After(joinToken.ExpiresAt) {
		// Delete expired token
		_ = s.store.DeleteJoinToken(ctx, joinToken.BrokerID)
		return nil, fmt.Errorf("join token has expired")
	}

	// Generate shared secret
	secretKey := make([]byte, s.config.SecretKeyLength)
	if _, err := rand.Read(secretKey); err != nil {
		return nil, fmt.Errorf("failed to generate secret key: %w", err)
	}

	// Delete any existing secret for this broker (re-registration case)
	_ = s.store.DeleteBrokerSecret(ctx, req.BrokerID)

	// Store the broker secret
	brokerSecret := &store.BrokerSecret{
		BrokerID:  req.BrokerID,
		SecretKey: secretKey,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		CreatedAt: time.Now(),
		Status:    store.BrokerSecretStatusActive,
	}

	if err := s.store.CreateBrokerSecret(ctx, brokerSecret); err != nil {
		return nil, fmt.Errorf("failed to store broker secret: %w", err)
	}

	// Update the runtime broker with connection info
	broker, err := s.store.GetRuntimeBroker(ctx, req.BrokerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get runtime broker: %w", err)
	}

	broker.Version = req.Version
	broker.Status = store.BrokerStatusOnline
	broker.ConnectionState = "connected"
	broker.LastHeartbeat = time.Now()
	broker.Updated = time.Now()

	// Update profiles if provided in the join request
	if len(req.Profiles) > 0 {
		broker.Profiles = req.Profiles
	}

	if err := s.store.UpdateRuntimeBroker(ctx, broker); err != nil {
		return nil, fmt.Errorf("failed to update runtime broker: %w", err)
	}

	// Delete the used join token
	_ = s.store.DeleteJoinToken(ctx, joinToken.BrokerID)

	return &BrokerJoinResponse{
		SecretKey:   base64.StdEncoding.EncodeToString(secretKey),
		HubEndpoint: hubEndpoint,
		BrokerID:    req.BrokerID,
	}, nil
}

// GenerateAndStoreSecret generates a new HMAC secret for an existing broker.
// This is used for simplified registration flows where a join token is not required.
// Returns the base64-encoded secret key.
func (s *BrokerAuthService) GenerateAndStoreSecret(ctx context.Context, brokerID string) (string, error) {
	if brokerID == "" {
		return "", errors.New("brokerId is required")
	}

	// Check if broker already has a secret
	existingSecret, err := s.store.GetBrokerSecret(ctx, brokerID)
	if err == nil && existingSecret != nil {
		// Broker already has a secret - return it (re-registration case)
		return base64.StdEncoding.EncodeToString(existingSecret.SecretKey), nil
	}

	// Generate shared secret
	secretKey := make([]byte, s.config.SecretKeyLength)
	if _, err := rand.Read(secretKey); err != nil {
		return "", fmt.Errorf("failed to generate secret key: %w", err)
	}

	// Store the broker secret
	brokerSecret := &store.BrokerSecret{
		BrokerID:  brokerID,
		SecretKey: secretKey,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		CreatedAt: time.Now(),
		Status:    store.BrokerSecretStatusActive,
	}

	if err := s.store.CreateBrokerSecret(ctx, brokerSecret); err != nil {
		return "", fmt.Errorf("failed to store broker secret: %w", err)
	}

	return base64.StdEncoding.EncodeToString(secretKey), nil
}

// =============================================================================
// HMAC Signature Validation
// =============================================================================

// HMAC authentication headers as per runtime-broker-auth.md
const (
	HeaderBrokerID      = "X-Scion-Broker-ID"
	HeaderTimestamp     = "X-Scion-Timestamp"
	HeaderNonce         = "X-Scion-Nonce"
	HeaderSignature     = "X-Scion-Signature"
	HeaderSignedHeaders = "X-Scion-Signed-Headers"
)

// ValidateBrokerSignature validates an HMAC-signed request from a Runtime Broker.
func (s *BrokerAuthService) ValidateBrokerSignature(ctx context.Context, r *http.Request) (BrokerIdentity, error) {
	// Extract required headers
	brokerID := r.Header.Get(HeaderBrokerID)
	if brokerID == "" {
		return nil, errors.New("missing X-Scion-Broker-ID header")
	}

	timestamp := r.Header.Get(HeaderTimestamp)
	if timestamp == "" {
		return nil, errors.New("missing X-Scion-Timestamp header")
	}

	signature := r.Header.Get(HeaderSignature)
	if signature == "" {
		return nil, errors.New("missing X-Scion-Signature header")
	}

	nonce := r.Header.Get(HeaderNonce)
	if nonce == "" {
		return nil, errors.New("missing X-Scion-Nonce header")
	}

	// Parse and validate timestamp
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp format: %w", err)
	}

	requestTime := time.Unix(ts, 0)
	clockSkew := time.Since(requestTime)
	if clockSkew < 0 {
		clockSkew = -clockSkew
	}
	if clockSkew > s.config.MaxClockSkew {
		return nil, fmt.Errorf("timestamp outside acceptable range (skew: %v)", clockSkew)
	}

	// Validate nonce if enabled
	if s.nonces != nil && nonce != "" {
		if !s.nonces.Add(nonce) {
			return nil, errors.New("nonce already used (possible replay attack)")
		}
	}

	// Get the broker's secret
	brokerSecret, err := s.store.GetBrokerSecret(ctx, brokerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("unknown broker: %s", brokerID)
		}
		return nil, fmt.Errorf("failed to get broker secret: %w", err)
	}

	// Check if secret is active
	if brokerSecret.Status != store.BrokerSecretStatusActive {
		return nil, fmt.Errorf("broker secret is %s", brokerSecret.Status)
	}

	// Check expiry
	if !brokerSecret.ExpiresAt.IsZero() && time.Now().After(brokerSecret.ExpiresAt) {
		return nil, errors.New("broker secret has expired")
	}

	// Build canonical string and verify signature
	canonicalString := s.buildCanonicalString(r, timestamp, nonce)
	expectedSig := computeHMAC(brokerSecret.SecretKey, canonicalString)
	expectedSigB64 := base64.StdEncoding.EncodeToString(expectedSig)

	if !hmac.Equal([]byte(signature), []byte(expectedSigB64)) {
		return nil, errors.New("invalid signature")
	}

	return NewBrokerIdentity(brokerID), nil
}

// buildCanonicalString builds the canonical string for HMAC signing.
// Format: METHOD\nPATH\nQUERY\nTIMESTAMP\nNONCE\nSIGNED_HEADERS\nBODY_HASH
func (s *BrokerAuthService) buildCanonicalString(r *http.Request, timestamp, nonce string) []byte {
	var buf bytes.Buffer

	// HTTP method
	buf.WriteString(r.Method)
	buf.WriteByte('\n')

	// Request path
	buf.WriteString(r.URL.Path)
	buf.WriteByte('\n')

	// Query string (sorted)
	buf.WriteString(r.URL.RawQuery)
	buf.WriteByte('\n')

	// Timestamp
	buf.WriteString(timestamp)
	buf.WriteByte('\n')

	// Nonce
	buf.WriteString(nonce)
	buf.WriteByte('\n')

	// Signed headers (if specified)
	signedHeaders := r.Header.Get(HeaderSignedHeaders)
	if signedHeaders != "" {
		// Headers are listed as semicolon-separated names
		headerNames := strings.Split(signedHeaders, ";")
		for _, name := range headerNames {
			name = strings.TrimSpace(name)
			value := r.Header.Get(name)
			buf.WriteString(strings.ToLower(name))
			buf.WriteByte(':')
			buf.WriteString(strings.TrimSpace(value))
			buf.WriteByte('\n')
		}
	}

	// Body hash (SHA-256 of request body)
	if r.Body != nil && r.ContentLength > 0 {
		// We need to read and restore the body
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			bodyHash := sha256.Sum256(bodyBytes)
			buf.WriteString(base64.StdEncoding.EncodeToString(bodyHash[:]))
		}
	}

	return buf.Bytes()
}

// SignRequest signs an outgoing HTTP request with HMAC.
// Used by Runtime Brokers when calling the Hub API.
func (s *BrokerAuthService) SignRequest(r *http.Request, brokerID string, secret []byte) error {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	// Generate nonce
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("failed to generate nonce: %w", err)
	}
	nonce := base64.URLEncoding.EncodeToString(nonceBytes)

	// Set headers
	r.Header.Set(HeaderBrokerID, brokerID)
	r.Header.Set(HeaderTimestamp, timestamp)
	r.Header.Set(HeaderNonce, nonce)

	// Build canonical string and compute signature
	canonicalString := s.buildCanonicalString(r, timestamp, nonce)
	sig := computeHMAC(secret, canonicalString)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	r.Header.Set(HeaderSignature, sigB64)

	return nil
}

// =============================================================================
// Secret Rotation
// =============================================================================

// RotateSecretRequest is the request body for POST /api/v1/brokers/{id}/rotate-secret.
type RotateSecretRequest struct {
	// GracePeriod is how long the old secret remains valid after rotation.
	// Defaults to 5 minutes if not specified.
	GracePeriod time.Duration `json:"gracePeriod,omitempty"`
}

// RotateSecretResponse is the response for POST /api/v1/brokers/{id}/rotate-secret.
type RotateSecretResponse struct {
	SecretKey   string    `json:"secretKey"` // Base64-encoded new secret
	RotatedAt   time.Time `json:"rotatedAt"`
	GracePeriod string    `json:"gracePeriod"` // Duration string
}

// RotateBrokerSecret generates a new secret for a broker.
// The old secret is marked as deprecated and remains valid for the grace period.
// Note: Current schema only supports one secret per broker, so this replaces immediately.
// TODO: Add schema migration to support multiple secrets per broker for true dual-secret rotation.
func (s *BrokerAuthService) RotateBrokerSecret(ctx context.Context, brokerID string, gracePeriod time.Duration) (*RotateSecretResponse, error) {
	if gracePeriod <= 0 {
		gracePeriod = 5 * time.Minute
	}

	// Get existing secret
	existingSecret, err := s.store.GetBrokerSecret(ctx, brokerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing secret: %w", err)
	}

	// Generate new secret
	newSecretKey := make([]byte, s.config.SecretKeyLength)
	if _, err := rand.Read(newSecretKey); err != nil {
		return nil, fmt.Errorf("failed to generate new secret: %w", err)
	}

	now := time.Now()

	// Update the secret with new key
	// Note: In a full implementation with multi-secret support, we would:
	// 1. Mark old secret as deprecated with expiry = now + gracePeriod
	// 2. Create new secret with status active
	existingSecret.SecretKey = newSecretKey
	existingSecret.RotatedAt = now
	existingSecret.Status = store.BrokerSecretStatusActive

	if err := s.store.UpdateBrokerSecret(ctx, existingSecret); err != nil {
		return nil, fmt.Errorf("failed to update secret: %w", err)
	}

	return &RotateSecretResponse{
		SecretKey:   base64.StdEncoding.EncodeToString(newSecretKey),
		RotatedAt:   now,
		GracePeriod: gracePeriod.String(),
	}, nil
}

// ValidateBrokerSignatureWithRotation validates a request trying multiple secrets.
// This supports the grace period during secret rotation where both old and new
// secrets are valid.
func (s *BrokerAuthService) ValidateBrokerSignatureWithRotation(ctx context.Context, r *http.Request) (BrokerIdentity, error) {
	// Extract required headers
	brokerID := r.Header.Get(HeaderBrokerID)
	if brokerID == "" {
		return nil, errors.New("missing X-Scion-Broker-ID header")
	}

	// Get all active secrets for this broker
	secrets, err := s.store.GetActiveSecrets(ctx, brokerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get broker secrets: %w", err)
	}

	if len(secrets) == 0 {
		return nil, fmt.Errorf("unknown broker: %s", brokerID)
	}

	// Try each secret until one validates
	var lastErr error
	for _, secret := range secrets {
		// Skip expired secrets
		if !secret.ExpiresAt.IsZero() && time.Now().After(secret.ExpiresAt) {
			continue
		}

		identity, err := s.validateWithSecret(ctx, r, brokerID, secret.SecretKey)
		if err == nil {
			return identity, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no valid secrets found")
}

// validateWithSecret validates a request using a specific secret key.
func (s *BrokerAuthService) validateWithSecret(ctx context.Context, r *http.Request, brokerID string, secretKey []byte) (BrokerIdentity, error) {
	timestamp := r.Header.Get(HeaderTimestamp)
	if timestamp == "" {
		return nil, errors.New("missing X-Scion-Timestamp header")
	}

	signature := r.Header.Get(HeaderSignature)
	if signature == "" {
		return nil, errors.New("missing X-Scion-Signature header")
	}

	nonce := r.Header.Get(HeaderNonce)
	if nonce == "" {
		return nil, errors.New("missing X-Scion-Nonce header")
	}

	// Parse and validate timestamp
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp format: %w", err)
	}

	requestTime := time.Unix(ts, 0)
	clockSkew := time.Since(requestTime)
	if clockSkew < 0 {
		clockSkew = -clockSkew
	}
	if clockSkew > s.config.MaxClockSkew {
		return nil, fmt.Errorf("timestamp outside acceptable range (skew: %v)", clockSkew)
	}

	// Build canonical string and verify signature
	canonicalString := s.buildCanonicalString(r, timestamp, nonce)
	expectedSig := computeHMAC(secretKey, canonicalString)
	expectedSigB64 := base64.StdEncoding.EncodeToString(expectedSig)

	if !hmac.Equal([]byte(signature), []byte(expectedSigB64)) {
		return nil, errors.New("invalid signature")
	}

	// Only add nonce to cache after successful validation
	if s.nonces != nil && nonce != "" {
		if !s.nonces.Add(nonce) {
			return nil, errors.New("nonce already used (possible replay attack)")
		}
	}

	return NewBrokerIdentity(brokerID), nil
}

// =============================================================================
// Helper Functions
// =============================================================================

// computeHMAC computes HMAC-SHA256.
func computeHMAC(secret, data []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(data)
	return h.Sum(nil)
}

// sha256Hash returns the hex-encoded SHA-256 hash of a string.
func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.StdEncoding.EncodeToString(h[:])
}

// slugify converts a name to a URL-safe slug.
func slugify(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	// Remove non-alphanumeric characters except hyphens
	var result strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// =============================================================================
// Middleware
// =============================================================================

// BrokerAuthMiddleware creates middleware for HMAC-based broker authentication.
// This runs AFTER UnifiedAuthMiddleware and checks for X-Scion-Broker-ID header.
func BrokerAuthMiddleware(svc *BrokerAuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip if broker auth service is not configured
			if svc == nil || !svc.config.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			// Skip if not a broker-authenticated request
			brokerID := r.Header.Get(HeaderBrokerID)
			if brokerID == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Validate HMAC signature
			identity, err := svc.ValidateBrokerSignature(r.Context(), r)
			if err != nil {
				writeBrokerAuthError(w, err.Error())
				return
			}

			// Set both broker-specific and generic identity contexts
			ctx := contextWithBrokerIdentity(r.Context(), identity)
			ctx = contextWithIdentity(ctx, identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeBrokerAuthError writes a broker authentication error response.
func writeBrokerAuthError(w http.ResponseWriter, message string) {
	writeError(w, http.StatusUnauthorized, ErrCodeBrokerAuthFailed, message, nil)
}
