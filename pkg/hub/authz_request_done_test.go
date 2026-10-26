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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Authorization checks after the request context ended (ptone/scion#6012).

// requestDoneLogRecord is one captured log record.
type requestDoneLogRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// requestDoneLogCapture is a slog handler that keeps every record.
type requestDoneLogCapture struct {
	mu      sync.Mutex
	records []requestDoneLogRecord
}

func (c *requestDoneLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *requestDoneLogCapture) Handle(_ context.Context, r slog.Record) error {
	rec := requestDoneLogRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, rec)
	c.mu.Unlock()
	return nil
}

func (c *requestDoneLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *requestDoneLogCapture) WithGroup(string) slog.Handler      { return c }

// count returns the records at level whose message is one of msgs (any
// message when msgs is empty).
func (c *requestDoneLogCapture) count(level slog.Level, msgs ...string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.records {
		if r.level != level {
			continue
		}
		if len(msgs) == 0 {
			n++
			continue
		}
		for _, m := range msgs {
			if r.msg == m {
				n++
				break
			}
		}
	}
	return n
}

func (c *requestDoneLogCapture) find(msg string) []requestDoneLogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []requestDoneLogRecord
	for _, r := range c.records {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

// captureRequestDoneDefaultLog routes slog.Default to a capture for the test.
func captureRequestDoneDefaultLog(t *testing.T) *requestDoneLogCapture {
	t.Helper()
	c := &requestDoneLogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// The per-item WARN messages the change quiets for a request that ended.
var requestDoneWarnMsgs = []string{
	"failed to resolve authorization principals",
	"failed to load role bindings",
	"failed to load role definitions",
	"agent standing lookup failed",
}

const requestDoneSummaryMsg = "authorization checks stopped after the request context ended"

// requestDoneStore counts reads and can run a hook on GetAgent.
type requestDoneStore struct {
	store.Store
	mu     sync.Mutex
	counts map[string]int
	// onGetAgent, when set, runs before GetAgent and may return an error
	// instead of reading.
	onGetAgent func(ctx context.Context) error
}

func (s *requestDoneStore) note(op string) {
	s.mu.Lock()
	s.counts[op]++
	s.mu.Unlock()
}

func (s *requestDoneStore) countOf(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[op]
}

func (s *requestDoneStore) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.counts {
		n += c
	}
	return n
}

func (s *requestDoneStore) GetAgent(ctx context.Context, id string) (*store.Agent, error) {
	s.note("GetAgent")
	if s.onGetAgent != nil {
		if err := s.onGetAgent(ctx); err != nil {
			return nil, err
		}
	}
	return s.Store.GetAgent(ctx, id)
}

func (s *requestDoneStore) HasActiveAgentHold(ctx context.Context, id string) (bool, error) {
	s.note("HasActiveAgentHold")
	return s.Store.HasActiveAgentHold(ctx, id)
}

func (s *requestDoneStore) GetUser(ctx context.Context, id string) (*store.User, error) {
	s.note("GetUser")
	return s.Store.GetUser(ctx, id)
}

func (s *requestDoneStore) GetDelegationEdgesForDelegate(ctx context.Context, t, id string) ([]*store.DelegationEdge, error) {
	s.note("GetDelegationEdgesForDelegate")
	return s.Store.GetDelegationEdgesForDelegate(ctx, t, id)
}

func (s *requestDoneStore) GetEffectiveGroups(ctx context.Context, id string) ([]string, error) {
	s.note("GetEffectiveGroups")
	return s.Store.GetEffectiveGroups(ctx, id)
}

func (s *requestDoneStore) GetEffectiveGroupsForAgent(ctx context.Context, id string) ([]string, error) {
	s.note("GetEffectiveGroupsForAgent")
	return s.Store.GetEffectiveGroupsForAgent(ctx, id)
}

func (s *requestDoneStore) ListRoleBindingsForPrincipals(ctx context.Context, p []store.PrincipalRef, st []string, sid []string) ([]*store.RoleBinding, error) {
	s.note("ListRoleBindingsForPrincipals")
	return s.Store.ListRoleBindingsForPrincipals(ctx, p, st, sid)
}

// installRequestDoneStore routes the server's and the authz service's
// reads through a counting store for the test.
func installRequestDoneStore(t *testing.T, srv *Server, inner store.Store) *requestDoneStore {
	t.Helper()
	rs := &requestDoneStore{Store: inner, counts: map[string]int{}}
	origSrv, origAuthz := srv.store, srv.authzService.store
	srv.store = rs
	srv.authzService.store = rs
	t.Cleanup(func() {
		srv.store = origSrv
		srv.authzService.store = origAuthz
	})
	return rs
}

// Once the request context has ended, no decision resolves the caller's
// principals (or, for a user, its status) again: the decisions after the
// cancellation make no such lookup, all deny, log no WARN, and are each
// counted once on the request's note. Run for a user (stopped at the
// status gate) and an agent (stopped at principal resolution), with and
// without the per-phase input memo.
func TestAuthzRequestDone_PrincipalsNotResolvedAfterCancel(t *testing.T) {
	lookups := []string{"GetUser", "GetEffectiveGroups", "GetEffectiveGroupsForAgent", "ListRoleBindingsForPrincipals"}
	for _, withMemo := range []bool{false, true} {
		for _, who := range []string{"user", "agent"} {
			name := who
			if withMemo {
				name += "/memo"
			}
			t.Run(name, func(t *testing.T) {
				_, s := authzTestSetup(t)
				var (
					identity Identity
					tuples   []rawTuple
					site     string
				)
				if who == "user" {
					f := newP1Fixture(t, s, "reqdone-u")
					identity, tuples, site = f.user, agentResourceTuples(f.agentRes), "principal_status"
				} else {
					f := newA1Fixture(t, s, "reqdone-a")
					identity, tuples, site = f.agent, agentResourceTuples(f.resource), "principals"
				}
				require.Greater(t, len(tuples), 2)
				st := newMemoTestStore(s)
				logs := &requestDoneLogCapture{}
				authz := NewAuthzService(st, slog.New(logs))

				base := context.Background()
				if withMemo {
					base = withAuthzInputMemo(base)
				}
				// The note is installed on the request context, as
				// serveAfterAuth does, so it sees the request end.
				reqCtx, cancel := context.WithCancel(base)
				defer cancel()
				ctx, note := withAuthzStoppedNote(reqCtx)
				require.NotNil(t, note)

				for _, tp := range tuples {
					authz.CheckAccess(ctx, identity, tp.resource, tp.action)
				}
				live := map[string]int{}
				liveTotal := 0
				for _, m := range lookups {
					live[m] = st.countOf(m)
					liveTotal += live[m]
				}
				require.Positive(t, liveTotal, "the live decisions read the caller's inputs")
				require.Zero(t, note.count, "nothing is counted while the request is live")

				cancel()
				for i, tp := range tuples {
					d := authz.CheckAccess(ctx, identity, tp.resource, tp.action)
					assert.False(t, d.Allowed, "decision %d after the cancellation denies", i)
					assert.Equal(t, DenyCauseResolutionError, d.DenyCause, "decision %d", i)
				}
				for _, m := range lookups {
					assert.Equal(t, live[m], st.countOf(m), "no %s after the cancellation", m)
				}
				assert.Zero(t, logs.count(slog.LevelWarn), "no WARN for decisions after the cancellation")
				assert.Equal(t, len(tuples), note.count, "each stopped decision is counted once")
				assert.Equal(t, site, note.firstSite)
				assert.Equal(t, "canceled", note.class)
			})
		}
	}
}

// requestDoneCancelEmitter records decisions and cancels the request
// after the given number of them, so a loop is cut mid-way.
type requestDoneCancelEmitter struct {
	mu     sync.Mutex
	n      int
	after  int
	cancel context.CancelFunc
}

func (e *requestDoneCancelEmitter) EmitDecisionAudit(context.Context, *store.DecisionAuditRecord) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n++
	if e.cancel != nil && e.after > 0 && e.n == e.after {
		e.cancel()
	}
}

func (e *requestDoneCancelEmitter) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}

// Capability batches, for a plain user and a scoped user token, on both
// ComputeCapabilitiesBatch and ComputeCapabilitiesForActions: on an ended
// request no decision runs and every item gets no action; when the request
// ends mid-loop, the items decided before keep their real result and the
// rest get no action. No WARN either way.
func TestAuthzRequestDone_CapabilityBatchesStopAndDeny(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "reqdone-caps")
	st := newMemoTestStore(s)
	logs := &requestDoneLogCapture{}
	authz := NewAuthzService(st, slog.New(logs))
	em := &requestDoneCancelEmitter{}
	authz.SetDecisionAuditEmitter(em)
	authz.DecisionAuditSampleRate = 1.0

	const n = 12
	resources := make([]Resource, n)
	for i := range resources {
		resources[i] = f.agentRes
	}
	actions := ResourceActions["agent"]
	run := map[string]func(context.Context, Identity) []*Capabilities{
		"batch": func(ctx context.Context, id Identity) []*Capabilities {
			return authz.ComputeCapabilitiesBatch(ctx, id, resources, "agent")
		},
		"for-actions": func(ctx context.Context, id Identity) []*Capabilities {
			return authz.ComputeCapabilitiesForActions(ctx, id, resources, actions)
		},
	}
	identities := map[string]Identity{
		"user":       f.user,
		"scoped-uat": NewScopedUserIdentity(f.user, f.projectID, []string{"agent:read"}),
	}
	for idName, id := range identities {
		for fnName, fn := range run {
			t.Run(idName+"/"+fnName, func(t *testing.T) {
				require.Equal(t, idName == "scoped-uat", IsScopedUserIdentity(id), "the branch under test")
				em.mu.Lock()
				em.n, em.after, em.cancel = 0, 0, nil
				em.mu.Unlock()

				live := fn(context.Background(), id)
				require.Len(t, live, n)
				require.NotEmpty(t, live[0].Actions, "allowed something while the request is live")
				require.Zero(t, em.count()%n, "every item costs the same decisions")
				perItem := em.count() / n
				require.Positive(t, perItem, "each item runs decisions")

				// Ended before the loop: no decision, no action.
				em.mu.Lock()
				em.n = 0
				em.mu.Unlock()
				before := st.countOf("GetEffectiveGroups")
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				for i, c := range fn(ctx, id) {
					require.NotNil(t, c, "item %d", i)
					assert.Empty(t, c.Actions, "item %d is denied every action", i)
				}
				assert.Zero(t, em.count(), "no decision runs on an ended request")
				assert.Equal(t, before, st.countOf("GetEffectiveGroups"), "no principal lookup on an ended request")

				// Ended mid-loop, after the decisions of the first k items.
				const k = 3
				ctx, cancel = context.WithCancel(context.Background())
				defer cancel()
				em.mu.Lock()
				em.n, em.after, em.cancel = 0, k*perItem, cancel
				em.mu.Unlock()
				got := fn(ctx, id)
				require.Len(t, got, n)
				for i := 0; i < k; i++ {
					assert.Equal(t, live[i].Actions, got[i].Actions, "item %d, decided before the end, keeps its result", i)
				}
				for i := k; i < n; i++ {
					assert.Empty(t, got[i].Actions, "item %d, after the end, is denied every action", i)
				}
				assert.Equal(t, k*perItem, em.count(), "no decision runs after the request ends")
				assert.Zero(t, logs.count(slog.LevelWarn))
			})
		}
	}
}

// A standing lookup cut short by the request ending is remembered for the
// request: later checks of the same agent refuse without a read and log
// no WARN; a live ctx sharing the memo evaluates again.
func TestAuthzRequestDone_StandingRememberedAndDenies(t *testing.T) {
	f := newMSFixture(t, "reqdone-standing")
	logs := captureRequestDoneDefaultLog(t)
	rs := installRequestDoneStore(t, f.srv, f.s)

	parent := withStandingMemo(context.Background())
	reqCtx, cancel := context.WithCancel(parent)
	defer cancel()
	ctx, note := withAuthzStoppedNote(reqCtx)
	require.NotNil(t, note)
	// The request ends while the agent row is read, as when a client gives
	// up during a slow read.
	rs.onGetAgent = func(c context.Context) error {
		cancel()
		return fmt.Errorf("agent read: %w", c.Err())
	}

	const n = 50
	for i := 0; i < n; i++ {
		err := f.srv.agentStanding(ctx, f.agentA.ID)
		require.Error(t, err, "check %d refuses", i)
		assert.True(t, errors.Is(err, context.Canceled), "check %d: %v", i, err)
		assert.False(t, errors.Is(err, errAgentNotInStanding), "a cut-short check is not a policy refusal")
	}
	assert.Equal(t, 1, rs.countOf("GetAgent"), "only the first check reads; the rest are remembered")
	assert.Zero(t, logs.count(slog.LevelWarn, requestDoneWarnMsgs...), "no WARN for a check cut short by the request ending")
	assert.Equal(t, 1, note.count)
	assert.Equal(t, "agent_standing", note.firstSite)

	// A different agent on the ended request refuses without a read.
	err := f.srv.agentStanding(ctx, f.childC.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Equal(t, 1, rs.countOf("GetAgent"))

	// A live ctx sharing the memo is not served the remembered refusal.
	rs.onGetAgent = nil
	require.NoError(t, f.srv.agentStanding(parent, f.agentA.ID))
	assert.Equal(t, 2, rs.countOf("GetAgent"))
}

// A store fault on a live request is still a WARN and still recomputed.
func TestAuthzRequestDone_LiveFaultStillWarns(t *testing.T) {
	f := newMSFixture(t, "reqdone-fault")
	logs := captureRequestDoneDefaultLog(t)
	rs := installRequestDoneStore(t, f.srv, f.s)
	rs.onGetAgent = func(context.Context) error { return context.DeadlineExceeded }

	ctx := withStandingMemo(context.Background())
	for i := 0; i < 3; i++ {
		require.Error(t, f.srv.agentStanding(ctx, f.agentA.ID))
	}
	assert.Equal(t, 3, rs.countOf("GetAgent"), "a fault on a live request is not remembered")
	assert.Equal(t, 3, logs.count(slog.LevelWarn, "agent standing lookup failed"))
}

// countReachableAgents stops on an ended request: no check runs and no
// agent is counted.
func TestAuthzRequestDone_CountReachableAgentsStops(t *testing.T) {
	f := newMSFixture(t, "reqdone-reach")
	for i := 0; i < 10; i++ {
		f.userAgent(fmt.Sprintf("reach-%d", i), f.userID)
	}
	agents, err := f.s.ListAgents(context.Background(), store.AgentFilter{ProjectID: f.projectID}, store.ListOptions{})
	require.NoError(t, err)
	require.Greater(t, len(agents.Items), 10)
	// Project mode on both sides, so every target reaches the sender's
	// standing check.
	sender := *f.agentA
	sender.MessageMode = store.MessageModeProject
	for i := range agents.Items {
		agents.Items[i].MessageMode = store.MessageModeProject
	}
	logs := captureRequestDoneDefaultLog(t)
	rs := installRequestDoneStore(t, f.srv, f.s)

	require.Positive(t, f.srv.countReachableAgents(withStandingMemo(context.Background()), &sender, agents.Items),
		"the sender reaches agents while the request is live")
	require.Positive(t, rs.countOf("GetAgent"), "the live loop checks the sender's standing")

	before := rs.total()
	reqCtx, cancel := context.WithCancel(withStandingMemo(context.Background()))
	cancel()
	ctx, note := withAuthzStoppedNote(reqCtx)
	assert.Zero(t, f.srv.countReachableAgents(ctx, &sender, agents.Items))
	assert.Equal(t, before, rs.total(), "no read on an ended request")
	// The loop itself stopped: no per-target decision ran, so not even the
	// sender's standing check was reached and counted.
	assert.Zero(t, note.count, "no per-target decision runs on an ended request")
	// Probe: one decision on the same ended request does reach the
	// standing check and is counted, so the zero above is the loop's break.
	other := agents.Items[0]
	if other.ID == sender.ID {
		other = agents.Items[1]
	}
	assert.False(t, f.srv.evaluateAgentMessageForSender(ctx, agentIdentityFromAgent(&sender), &sender, &other).Allowed)
	assert.Equal(t, 1, note.count, "a decision on the ended request is counted")
	assert.Zero(t, logs.count(slog.LevelWarn))
}

// A check that runs past its OWN caller-set bound while the request is
// still live is an ordinary fault: it keeps its per-check WARN and is not
// counted. Without a request note, a deadline keeps its WARN and only a
// cancellation is quieted.
func TestAuthzRequestDone_OwnDeadlineOnLiveRequestWarns(t *testing.T) {
	f := newMSFixture(t, "reqdone-own")
	logs := captureRequestDoneDefaultLog(t)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	ctx, note := withAuthzStoppedNote(reqCtx)
	require.NotNil(t, note)
	expired := func(parent context.Context) context.Context {
		c, cancel := context.WithTimeout(parent, time.Nanosecond)
		t.Cleanup(cancel)
		<-c.Done()
		return c
	}

	// Agent standing under its own expired bound.
	err := f.srv.agentStanding(withStandingMemo(expired(ctx)), f.agentA.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.Equal(t, 1, logs.count(slog.LevelWarn, "agent standing lookup failed"), "an own deadline keeps the WARN")

	// A user decision under its own expired bound: the status gate WARN.
	_, s := authzTestSetup(t)
	p1 := newP1Fixture(t, s, "reqdone-own-u")
	authzLogs := &requestDoneLogCapture{}
	authz := NewAuthzService(newMemoTestStore(s), slog.New(authzLogs))
	d := authz.CheckAccess(expired(ctx), p1.user, p1.agentRes, ActionRead)
	assert.False(t, d.Allowed)
	assert.Equal(t, 1, authzLogs.count(slog.LevelWarn, principalStatusWarnMsg), "an own deadline keeps the WARN")

	// An agent decision under its own expired bound: the principals WARN.
	a1 := newA1Fixture(t, s, "reqdone-own-a")
	d = authz.CheckAccess(expired(ctx), a1.agent, a1.resource, ActionRead)
	assert.False(t, d.Allowed)
	assert.Equal(t, 1, authzLogs.count(slog.LevelWarn, "failed to resolve authorization principals"), "an own deadline keeps the WARN")

	assert.Zero(t, note.count, "nothing is counted while the request is live")

	// Without a note: a deadline WARNs, a cancellation does not.
	before := logs.count(slog.LevelWarn, "agent standing lookup failed")
	require.Error(t, f.srv.agentStanding(expired(context.Background()), f.agentA.ID))
	assert.Equal(t, before+1, logs.count(slog.LevelWarn, "agent standing lookup failed"), "no note: a deadline keeps the WARN")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, f.srv.agentStanding(canceled, f.agentA.ID))
	assert.Equal(t, before+1, logs.count(slog.LevelWarn, "agent standing lookup failed"), "no note: a cancellation is DEBUG")
}

// serveAfterAuth writes one INFO summary for a request with stopped
// checks, none for a request without, exactly once on panic, and once for
// nested installs.
func TestAuthzRequestDone_OneSummaryPerRequest(t *testing.T) {
	logs := captureRequestDoneDefaultLog(t)
	serve := func(h http.HandlerFunc) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/p1/agents", nil)
		req = req.WithContext(ContextWithRoute(req.Context(), "projects.agents.list"))
		serveAfterAuth(httptest.NewRecorder(), h, req)
	}
	stop := func(r *http.Request, n int) {
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		for i := 0; i < n; i++ {
			noteAuthzStopped(ctx, nil, "principals", fmt.Errorf("x: %w", ctx.Err()))
		}
	}

	serve(func(w http.ResponseWriter, r *http.Request) { stop(r, 300) })
	sums := logs.find(requestDoneSummaryMsg)
	require.Len(t, sums, 1)
	assert.Equal(t, slog.LevelInfo, sums[0].level)
	assert.Equal(t, "300", sums[0].attrs["count"])
	assert.Equal(t, "principals", sums[0].attrs["first_site"])
	assert.Equal(t, "canceled", sums[0].attrs["class"])
	assert.Equal(t, http.MethodGet, sums[0].attrs["method"])
	assert.Equal(t, "projects.agents.list", sums[0].attrs["route"])
	for k, v := range sums[0].attrs {
		assert.False(t, strings.Contains(v, "/p1/"), "attr %s carries no raw path", k)
	}
	assert.Zero(t, logs.count(slog.LevelWarn))

	serve(func(w http.ResponseWriter, r *http.Request) {})
	assert.Len(t, logs.find(requestDoneSummaryMsg), 1, "no summary for a request with no stopped check")

	func() {
		defer func() { _ = recover() }()
		serve(func(w http.ResponseWriter, r *http.Request) {
			stop(r, 2)
			panic("handler panic")
		})
	}()
	assert.Len(t, logs.find(requestDoneSummaryMsg), 2, "one summary on panic")

	serve(func(w http.ResponseWriter, r *http.Request) {
		serveAfterAuth(w, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { stop(r, 3) }), r)
	})
	sums = logs.find(requestDoneSummaryMsg)
	require.Len(t, sums, 3, "one summary for nested installs")
	assert.Equal(t, "3", sums[2].attrs["count"])
}
