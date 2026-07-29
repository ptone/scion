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

// Client-supplied broker identifiers (ptone/scion#591).
//
// Every other identity id in this hub is minted by the hub. A broker id is not:
// it is client-supplied, so it is the one identifier the hub does not choose.
// INVARIANT: an identifier admitted here must have exactly one spelling and must
// not already denote a principal of another kind, so that it cannot stand for
// anything but the broker it names. These tests pin those two properties —
// well-formed, and not already taken in another namespace — together with the
// registrations that must keep working.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type brokerIDFixture struct {
	srv   *Server
	svc   *BrokerAuthService
	store store.Store

	user    *store.User
	agent   *store.Agent
	group   *store.Group
	project *store.Project
}

func brokerIDSetup(t *testing.T) *brokerIDFixture {
	t.Helper()
	ctx := context.Background()

	s, err := newTestStore(":memory:")
	if err != nil {
		t.Skipf("skipping: test store unavailable (%v)", err)
	}
	require.NoError(t, s.Migrate(ctx))

	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	cfg.DevUserConfig = DevUserConfig{
		Username: "dev", DisplayName: "Development User", Email: "dev@localhost",
	}
	cfg.BrokerAuthConfig = DefaultBrokerAuthConfig()
	srv, err := New(cfg, s)
	require.NoError(t, err)
	srv.SetHubID("test-hub-id")
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	require.NotNil(t, srv.brokerAuthService)

	f := &brokerIDFixture{srv: srv, svc: srv.brokerAuthService, store: s}

	f.user = &store.User{
		ID: tid("bid-user"), Email: "bid-user@f3b.test", DisplayName: "bid-user",
		Role: store.UserRoleMember, Status: "active", Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.user))

	f.project = &store.Project{
		ID: tid("bid-project"), Name: "BID Project", Slug: "bid-project",
		OwnerID: f.user.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.project))

	f.agent = &store.Agent{
		ID: tid("bid-agent"), Slug: "bid-agent", Name: "BID Agent",
		ProjectID: f.project.ID, Phase: string(state.PhaseRunning),
		CreatedBy: f.user.ID, OwnerID: f.user.ID,
	}
	require.NoError(t, s.CreateAgent(ctx, f.agent))

	f.group = &store.Group{
		ID: tid("bid-group"), Name: "BID Group", Slug: "bid-group", GroupType: "explicit",
	}
	require.NoError(t, s.CreateGroup(ctx, f.group))

	return f
}

// register calls the service directly, which is where the refusal is decided.
func (f *brokerIDFixture) register(t *testing.T, name, brokerID string) (*CreateBrokerRegistrationResponse, error) {
	t.Helper()
	return f.svc.CreateBrokerRegistration(context.Background(),
		CreateBrokerRegistrationRequest{Name: name, BrokerID: brokerID}, f.user.ID)
}

// registerHTTP goes through POST /api/v1/brokers as an authenticated caller,
// so the refusal is observed where a client would observe it.
func (f *brokerIDFixture) registerHTTP(t *testing.T, name, brokerID string) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, f.srv, http.MethodPost, "/api/v1/brokers",
		CreateBrokerRegistrationRequest{Name: name, BrokerID: brokerID})
}

// join completes the two-phase registration, as a real broker does between one
// registration and the next. It matters here because a join token's primary key
// IS the broker id, so an outstanding token blocks the next registration; the
// production sequence is register, join, later re-register.
func (f *brokerIDFixture) join(t *testing.T, resp *CreateBrokerRegistrationResponse) {
	t.Helper()
	out, err := f.svc.CompleteBrokerJoin(context.Background(), BrokerJoinRequest{
		BrokerID: resp.BrokerID, JoinToken: resp.JoinToken,
		Hostname: "f3b-host", Version: "test",
	}, "https://hub.f3b.test")
	require.NoError(t, err)
	require.NotEmpty(t, out.SecretKey)
}

func (f *brokerIDFixture) brokerExists(t *testing.T, id string) bool {
	t.Helper()
	b, err := f.store.GetRuntimeBroker(context.Background(), id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		require.NoError(t, err)
	}
	return b != nil
}

// ============================================================================
// Format
// ============================================================================

// TestBrokerID_MalformedRejected pins that a client-supplied broker id must be
// a UUID in canonical form.
//
// What this check does and does not contribute, measured against the ent/sqlite
// store rather than assumed:
//
//   - Ids that are not UUIDs at all are ALREADY refused one layer down, by
//     parseUUID in the ent store, as an unclassified store.ErrInvalidInput.
//     For those rows this check contributes CLASSIFICATION, not refusal: the
//     caller gets a named, mappable error instead of an internal one whose text
//     is echoed back verbatim. That is worth having, and it is not a security
//     claim.
//   - Ids that PARSE but are not canonical — braced, URN, uppercase,
//     unhyphenated — are accepted by the store, which silently NORMALIZES them
//     on write. Those rows are refused by this check and by nothing else. They
//     are why the rule is canonical form rather than uuid.Parse: otherwise the
//     identifier the client keeps and resends is not the identifier the hub
//     stored, and every id comparison in this hub is a string comparison.
//   - The store's validation is an implementation detail of one adapter. The
//     store.Store interface promises nothing about id shape, so the service
//     must not rely on it.
//
// alsoAbsent carries the normalized spelling for the parseable cases, because
// "no row under the string I sent" would pass even if the store had written a
// row under the normalized form.
func TestBrokerID_MalformedRejected(t *testing.T) {
	canonical := uuid.New().String()

	cases := []struct {
		name       string
		brokerID   string
		alsoAbsent []string
		why        string
	}{
		{name: "not a uuid at all", brokerID: "not-a-uuid",
			why: "arbitrary strings are not identifiers"},
		{name: "empty-ish", brokerID: "   ",
			why: "whitespace is not an identifier"},
		{name: "truncated", brokerID: canonical[:len(canonical)-1],
			why: "a near-miss must not be rounded up"},
		{name: "path traversal shaped", brokerID: "../../admin",
			why: "an id is not a path segment"},
		{name: "sql shaped", brokerID: "' OR 1=1 --",
			why: "an id is not an expression"},
		{name: "unhyphenated", brokerID: strings.ReplaceAll(canonical, "-", ""),
			alsoAbsent: []string{canonical},
			why:        "parses, and the store would normalize it to a different string"},
		{name: "braced", brokerID: "{" + canonical + "}",
			alsoAbsent: []string{canonical},
			why:        "parses, and the store would normalize it to a different string"},
		{name: "urn", brokerID: "urn:uuid:" + canonical,
			alsoAbsent: []string{canonical},
			why:        "parses, and the store would normalize it to a different string"},
		{name: "uppercase", brokerID: uppercaseUUID(canonical),
			alsoAbsent: []string{canonical},
			why:        "parses, and the store would normalize it to a different string"},
	}

	// The parseable-but-non-canonical rows are the ones attributable to this
	// check alone. If the table ever loses them it stops testing anything the
	// store does not already do.
	attributable := 0
	for _, tc := range cases {
		if len(tc.alsoAbsent) > 0 {
			attributable++
		}
	}
	require.GreaterOrEqual(t, attributable, 4,
		"the table must keep the parseable-but-non-canonical spellings; the "+
			"remaining rows are refused by the store one layer down and would pass "+
			"without this check")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := brokerIDSetup(t)

			resp, err := f.register(t, "broker-"+tc.name, tc.brokerID)
			require.Error(t, err, "%s: %s", tc.name, tc.why)
			assert.ErrorIsf(t, err, ErrBrokerIDRejected,
				"%s must be refused as a rejected broker id, not as some incidental "+
					"failure; a caller cannot act on an unclassified error", tc.name)
			assert.Nil(t, resp)

			for _, id := range append([]string{tc.brokerID}, tc.alsoAbsent...) {
				assert.Falsef(t, f.brokerExists(t, id),
					"%s was refused but a broker row exists under %q; a refusal that "+
						"leaves the row behind is not a refusal", tc.name, id)
			}
		})
	}
}

// uppercaseUUID returns the canonical UUID with its hex digits upper-cased.
func uppercaseUUID(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'f' {
			out[i] = r - 32
		}
	}
	return string(out)
}

// TestBrokerID_CanonicalUUIDAccepted is the control for the format table above.
// Without it, a validator that refused every id would satisfy every row there.
func TestBrokerID_CanonicalUUIDAccepted(t *testing.T) {
	f := brokerIDSetup(t)
	id := uuid.New().String()

	resp, err := f.register(t, "legitimate-broker", id)
	require.NoError(t, err, "a fresh canonical UUID is exactly what the broker CLI sends")
	require.NotNil(t, resp)
	assert.Equal(t, id, resp.BrokerID, "the supplied identifier must be honoured")
	assert.NotEmpty(t, resp.JoinToken)
	assert.True(t, f.brokerExists(t, id), "the broker row must be created")
}

// TestBrokerID_OmittedIDStillGenerated pins the other legitimate shape: a
// client that supplies no id at all is given one.
func TestBrokerID_OmittedIDStillGenerated(t *testing.T) {
	f := brokerIDSetup(t)

	resp, err := f.register(t, "generated-broker", "")
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.BrokerID)
	assert.NoError(t, validateBrokerIDFormat(resp.BrokerID),
		"an identifier the hub generates must itself satisfy the rule the hub enforces")
	assert.True(t, f.brokerExists(t, resp.BrokerID))
}

// TestBrokerID_SharedEntryPointsDivideFormatFromNamespace pins the contract of
// the two shared entry points, which every path that creates a broker row or
// issues a broker credential is expected to call rather than reimplement.
//
// The division is the whole point of having two of them, so it is pinned
// rather than left to the doc comments: a second path that called the wrong one
// would either re-litigate an existing broker's spelling and break its
// re-registration, or skip the format rule on a brand new id.
func TestBrokerID_SharedEntryPointsDivideFormatFromNamespace(t *testing.T) {
	f := brokerIDSetup(t)
	ctx := context.Background()
	canonical := uuid.New().String()
	nonCanonical := uppercaseUUID(canonical)

	// A NEW client-supplied id is held to both rules.
	assert.NoError(t, validateNewBrokerID(ctx, f.store, canonical),
		"a fresh canonical id in no reserved namespace is acceptable")
	assert.ErrorIs(t, validateNewBrokerID(ctx, f.store, nonCanonical), ErrBrokerIDRejected,
		"a new id must be canonical")
	assert.ErrorIs(t, validateNewBrokerID(ctx, f.store, f.user.ID), ErrBrokerIDRejected,
		"a new id must not be drawn from a reserved namespace")

	// An id that is about to be CREDENTIALED is held to the namespace rule only.
	assert.NoError(t, validateCredentialedBrokerID(ctx, f.store, canonical))
	assert.NoError(t, validateCredentialedBrokerID(ctx, f.store, nonCanonical),
		"an existing broker's spelling is not re-litigated at credential time; "+
			"applying the format rule here would strand brokers whose rows predate it")
	assert.ErrorIs(t, validateCredentialedBrokerID(ctx, f.store, f.user.ID), ErrBrokerIDRejected,
		"the namespace rule is not skippable for an id that already has a row")
}

// ============================================================================
// Namespace
// ============================================================================

// brokerIDNamespaceVictims maps each reserved namespace to an id that already
// exists in it. TestBrokerID_NamespaceTableCoversEveryReservedNamespace asserts
// this covers brokerIDReservedNamespaces() exactly, so reserving a fifth
// namespace without giving it a row here fails.
func brokerIDNamespaceVictims() map[string]func(*brokerIDFixture) string {
	return map[string]func(*brokerIDFixture) string{
		"user":    func(f *brokerIDFixture) string { return f.user.ID },
		"agent":   func(f *brokerIDFixture) string { return f.agent.ID },
		"group":   func(f *brokerIDFixture) string { return f.group.ID },
		"project": func(f *brokerIDFixture) string { return f.project.ID },
	}
}

// TestBrokerID_CrossNamespaceRejected pins that a broker may not register under
// an identifier that already denotes another kind of principal.
func TestBrokerID_CrossNamespaceRejected(t *testing.T) {
	for ns, victim := range brokerIDNamespaceVictims() {
		t.Run(ns, func(t *testing.T) {
			f := brokerIDSetup(t)
			id := victim(f)

			// Precondition: the collision is with a WELL-FORMED id, so this row
			// is testing the namespace rule and not the format rule.
			require.NoError(t, validateBrokerIDFormat(id),
				"the %s fixture id is not canonical; this row would be refused by "+
					"the format check and would not exercise the namespace check", ns)

			resp, err := f.register(t, "broker-shadowing-"+ns, id)
			require.Error(t, err,
				"a broker must not be registered under an id that already denotes a %s", ns)
			assert.ErrorIs(t, err, ErrBrokerIDRejected)
			assert.Nil(t, resp)
			assert.False(t, f.brokerExists(t, id),
				"the colliding broker row must not be persisted")

			// INVARIANT: every rejection reason collapses to one generic
			// refusal, so the namespace that matched must not appear in it. See
			// the ErrBrokerIDRejected doc, which this row is the test for.
			assert.NotContains(t, err.Error(), ns,
				"the refusal names the namespace it matched, so refusals are "+
					"distinguishable by reason")
		})
	}
}

// TestBrokerID_CrossNamespaceRejectedOverHTTP observes the same refusal where a
// client observes it, and pins that nothing is persisted and no credential is
// handed out.
//
// The SECURITY assertions here deliberately do not depend on the status code:
// the guarantee is that the request fails and issues nothing, and that must
// hold however the error is mapped. The status code is pinned separately, in
// TestBrokerID_RejectionMapsToClientError, as API hygiene on top.
func TestBrokerID_CrossNamespaceRejectedOverHTTP(t *testing.T) {
	f := brokerIDSetup(t)

	rec := f.registerHTTP(t, "http-shadow-broker", f.user.ID)
	assert.GreaterOrEqual(t, rec.Code, 400,
		"registering under a user's id must fail; body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), JoinTokenPrefix,
		"a refused registration must not return a join token")
	assert.False(t, f.brokerExists(t, f.user.ID),
		"a refused registration must not leave a broker row behind")

	// Control on the same route: an ordinary registration through the same
	// handler still succeeds, so the assertions above are about the collision
	// and not about the route being broken.
	fresh := uuid.New().String()
	ok := f.registerHTTP(t, "http-ordinary-broker", fresh)
	require.Equal(t, http.StatusCreated, ok.Code, ok.Body.String())
	var resp CreateBrokerRegistrationResponse
	require.NoError(t, json.Unmarshal(ok.Body.Bytes(), &resp))
	assert.Equal(t, fresh, resp.BrokerID)
	assert.Contains(t, resp.JoinToken, JoinTokenPrefix)
	assert.True(t, f.brokerExists(t, fresh))
}

// TestBrokerID_RejectionMapsToClientError is API hygiene, kept separate from
// the security pins above on purpose: a rejected brokerId is the caller's
// input, so it must not be reported as an internal hub fault. The registration
// handler otherwise maps every service error to 500, which tells a client its
// own malformed input was the hub's problem.
//
// Nothing about the refusal depends on this test passing — see
// TestBrokerID_CrossNamespaceRejectedOverHTTP, which asserts the refusal and
// the absence of the persisted row without reference to the status code.
func TestBrokerID_RejectionMapsToClientError(t *testing.T) {
	f := brokerIDSetup(t)

	for _, tc := range []struct {
		name     string
		brokerID string
	}{
		{"malformed", "not-a-uuid"},
		{"cross-namespace", f.user.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.registerHTTP(t, "hygiene-"+tc.name, tc.brokerID)
			assert.Equal(t, http.StatusBadRequest, rec.Code,
				"a rejected brokerId is client input, not an internal error; body: %s",
				rec.Body.String())
		})
	}

	// Control: a service error that is NOT a rejected brokerId must still map
	// to 500, so the new arm is specific and has not swallowed the error class.
	//
	// The failure used here is a real one and is NOT introduced by this change:
	// a join token's primary key is the broker id, so registering the same
	// broker twice without completing the join in between fails with
	// "already exists". That is a pre-existing wart in the two-phase
	// registration flow, out of scope here, and it is what makes a convenient
	// non-sentinel error. If it is ever fixed this control will need a
	// different one.
	ok := f.registerHTTP(t, "double-register", uuid.New().String())
	require.Equal(t, http.StatusCreated, ok.Code, ok.Body.String())
	again := f.registerHTTP(t, "double-register", "")
	assert.Equal(t, http.StatusInternalServerError, again.Code,
		"a non-brokerId service failure must still be reported as an internal "+
			"error; body: %s", again.Body.String())
}

// TestBrokerID_NamespaceTableCoversEveryReservedNamespace closes the loop
// between the reserved set and the table that exercises it.
func TestBrokerID_NamespaceTableCoversEveryReservedNamespace(t *testing.T) {
	reserved := brokerIDReservedNamespaces()
	require.NotEmpty(t, reserved)

	var names, tested []string
	seen := map[string]bool{}
	for _, ns := range reserved {
		require.Falsef(t, seen[ns.name], "namespace %q is listed twice", ns.name)
		seen[ns.name] = true
		names = append(names, ns.name)
	}
	for name := range brokerIDNamespaceVictims() {
		tested = append(tested, name)
	}
	sort.Strings(names)
	sort.Strings(tested)
	assert.Equal(t, names, tested,
		"every reserved namespace needs a row in TestBrokerID_CrossNamespaceRejected. "+
			"A namespace reserved in production and absent from the table is an "+
			"unverified reservation; a namespace in the table and not reserved is a "+
			"test asserting a rule that is not enforced.")
}

// TestBrokerID_ReservedNamespacesCoverPolicyPrincipalTypes pins the reserved
// set against the principal types the policy layer binds against.
//
// Those are the ids the authorization layer compares a caller's id to, so any
// principal type the policy layer gains must be reserved here too; otherwise a
// broker could be registered under an identifier that the policy layer will
// later treat as that principal's.
func TestBrokerID_ReservedNamespacesCoverPolicyPrincipalTypes(t *testing.T) {
	reserved := map[string]bool{}
	for _, ns := range brokerIDReservedNamespaces() {
		reserved[ns.name] = true
	}
	for _, pt := range []string{
		store.PolicyPrincipalTypeUser,
		store.PolicyPrincipalTypeGroup,
		store.PolicyPrincipalTypeAgent,
	} {
		assert.Truef(t, reserved[pt],
			"policy bindings accept principalType %q, but a broker may still be "+
				"registered under an identifier from that namespace", pt)
	}
	// Projects are reserved in addition to the policy principal types, because
	// ownership and scope checks key on a project id. Stated so the extra entry
	// is a decision rather than an accident.
	assert.True(t, reserved["project"],
		"project ids are keyed on by ownership and scope checks and must be reserved")
}

// ============================================================================
// Registrations that must keep working
// ============================================================================

// TestBrokerID_ReRegistrationByNameStillWorks pins the ordinary restart case: a
// broker that comes back under the same name keeps its id and gets a new join
// token.
func TestBrokerID_ReRegistrationByNameStillWorks(t *testing.T) {
	f := brokerIDSetup(t)

	first, err := f.register(t, "restarting-broker", uuid.New().String())
	require.NoError(t, err)
	f.join(t, first)

	second, err := f.register(t, "restarting-broker", "")
	require.NoError(t, err, "re-registration by name must not be affected")
	assert.Equal(t, first.BrokerID, second.BrokerID, "the broker keeps its id")
	assert.True(t, second.Reregistered)
	assert.NotEmpty(t, second.JoinToken)
}

// TestBrokerID_ReRegistrationByIDStillWorks pins the case the broker CLI
// actually produces: it persists the id the hub gave it and sends it back.
func TestBrokerID_ReRegistrationByIDStillWorks(t *testing.T) {
	f := brokerIDSetup(t)

	first, err := f.register(t, "stable-broker", uuid.New().String())
	require.NoError(t, err)
	f.join(t, first)

	second, err := f.register(t, "stable-broker-renamed", first.BrokerID)
	require.NoError(t, err, "re-registration by id must not be affected")
	assert.Equal(t, first.BrokerID, second.BrokerID)
	assert.True(t, second.Reregistered)
}

// TestBrokerID_FormatRuleDoesNotApplyToExistingBrokers is the regression the
// format check would cause if it were applied to the wrong thing.
//
// The format rule governs what a client may ASK FOR when creating a broker, not
// what an existing broker already is. An existing broker must still be able to
// come back even when the spelling it sends is one the rule would refuse for a
// new registration — its row already exists, so the id is resolved by lookup
// and never allocated.
//
// The non-canonical spelling used here is an uppercase UUID. Measured on the
// ent/sqlite store: a truly non-UUID id cannot be planted at all (parseUUID
// refuses it on write), and a parseable non-canonical id is normalized on
// write, so the client's spelling and the stored spelling genuinely differ —
// which is the case this test needs.
func TestBrokerID_FormatRuleDoesNotApplyToExistingBrokers(t *testing.T) {
	f := brokerIDSetup(t)
	ctx := context.Background()

	canonical := uuid.New().String()
	clientSpelling := uppercaseUUID(canonical)
	require.Error(t, validateBrokerIDFormat(clientSpelling),
		"precondition: the spelling must be one the format rule refuses for a new broker")

	require.NoError(t, f.store.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: clientSpelling, Name: "existing-broker", Slug: "existing-broker",
		Status: store.BrokerStatusOffline, Created: time.Now(), Updated: time.Now(),
	}))
	stored, err := f.store.GetRuntimeBroker(ctx, clientSpelling)
	require.NoError(t, err)
	require.Equal(t, canonical, stored.ID,
		"precondition: the store normalizes on write, so the two spellings differ")

	byName, err := f.register(t, "existing-broker", "")
	require.NoError(t, err, "an existing broker must still re-register by name")
	assert.Equal(t, canonical, byName.BrokerID)
	f.join(t, byName)

	byID, err := f.register(t, "existing-broker-renamed", clientSpelling)
	require.NoError(t, err,
		"an existing broker must still re-register under the spelling it holds, "+
			"even though that spelling would be refused for a NEW broker")
	assert.Equal(t, canonical, byID.BrokerID)
	assert.True(t, byID.Reregistered)
}

// TestBrokerID_ReRegistrationOfCollidingRowRefused closes the route around the
// creation-time check.
//
// The namespace check is on the identifier being credentialed, not on the row
// being created, and this is why. A broker row is not proof that its id was
// ever checked — rows are created by more than one code path, and rows outlive
// the code that created them — while a re-registration issues a brand new join
// token. If the check ran only when creating a row, an id planted by any other
// route would be credentialed here on the re-registration branch.
func TestBrokerID_ReRegistrationOfCollidingRowRefused(t *testing.T) {
	f := brokerIDSetup(t)
	ctx := context.Background()

	// A pre-existing broker row whose id is a live user's id.
	require.NoError(t, f.store.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: f.user.ID, Name: "planted-broker", Slug: "planted-broker",
		Status: store.BrokerStatusOffline, Created: time.Now(), Updated: time.Now(),
	}))

	byName, err := f.register(t, "planted-broker", "")
	require.Error(t, err, "re-registration by name must not credential a colliding id")
	assert.ErrorIs(t, err, ErrBrokerIDRejected)
	assert.Nil(t, byName)

	byID, err := f.register(t, "planted-broker-renamed", f.user.ID)
	require.Error(t, err, "re-registration by id must not credential a colliding id")
	assert.ErrorIs(t, err, ErrBrokerIDRejected)
	assert.Nil(t, byID)

	// Control: an uncontaminated pre-existing row re-registers normally, so the
	// refusals above are about the collision and not about pre-existing rows.
	cleanID := uuid.New().String()
	require.NoError(t, f.store.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: cleanID, Name: "clean-broker", Slug: "clean-broker",
		Status: store.BrokerStatusOffline, Created: time.Now(), Updated: time.Now(),
	}))
	ok, err := f.register(t, "clean-broker", "")
	require.NoError(t, err)
	assert.Equal(t, cleanID, ok.BrokerID)
	assert.NotEmpty(t, ok.JoinToken)
}
