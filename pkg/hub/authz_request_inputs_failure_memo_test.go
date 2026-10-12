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
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const principalFailureWarning = "failed to resolve authorization principals"

// principalWarningRecorder is a slog.Handler that keeps the attributes of
// every record whose message is msg (principalFailureWarning when empty).
type principalWarningRecorder struct {
	msg     string
	mu      sync.Mutex
	records []map[string]string
}

func (r *principalWarningRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *principalWarningRecorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *principalWarningRecorder) WithGroup(string) slog.Handler            { return r }

func (r *principalWarningRecorder) Handle(_ context.Context, rec slog.Record) error {
	want := r.msg
	if want == "" {
		want = principalFailureWarning
	}
	if rec.Message != want {
		return nil
	}
	attrs := map[string]string{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	r.mu.Lock()
	r.records = append(r.records, attrs)
	r.mu.Unlock()
	return nil
}

func (r *principalWarningRecorder) snapshot() []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]string(nil), r.records...)
}

// perUserGroupsStore fails GetEffectiveGroups for failUser only and counts
// calls per user.
type perUserGroupsStore struct {
	*memoTestStore
	failUser string

	callsMu sync.Mutex
	calls   map[string]int
}

func (p *perUserGroupsStore) GetEffectiveGroups(ctx context.Context, userID string) ([]string, error) {
	p.callsMu.Lock()
	p.calls[userID]++
	p.callsMu.Unlock()
	if userID == p.failUser {
		return nil, fmt.Errorf("injected fault for %s: %w", userID, errInjected)
	}
	return p.memoTestStore.GetEffectiveGroups(ctx, userID)
}

func (p *perUserGroupsStore) callsFor(userID string) int {
	p.callsMu.Lock()
	defer p.callsMu.Unlock()
	return p.calls[userID]
}

// failureMemoAgents creates n agents in f's project and returns them as
// list-row resources.
func failureMemoAgents(t *testing.T, s store.Store, f *p1Fixture, name string, n int) []Resource {
	t.Helper()
	ctx := context.Background()
	out := make([]Resource, n)
	for i := range out {
		id := tid(fmt.Sprintf("%s-agent-%d", name, i))
		slug := fmt.Sprintf("%s-agent-%d", name, i)
		require.NoError(t, s.CreateAgent(ctx, &store.Agent{ID: id, Slug: slug, Name: slug, ProjectID: f.projectID, Phase: "running", OwnerID: f.agentRes.OwnerID, Ancestry: []string{f.agentRes.OwnerID}}))
		out[i] = Resource{Type: "agent", ID: id, ParentType: "project", ParentID: f.projectID, OwnerID: f.agentRes.OwnerID}
	}
	return out
}

// TestAuthzFailureMemo_ListBatchResolvesOnce: N list rows for a principal
// whose closure cannot be resolved cost one closure load and one warning,
// and every row is denied.
func TestAuthzFailureMemo_ListBatchResolvesOnce(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "fmemo-list")
	const n = 25
	resources := failureMemoAgents(t, s, f, "fmemo-list", n)

	ms := newMemoTestStore(s)
	ms.fault = alwaysFault("GetEffectiveGroups")
	rec := &principalWarningRecorder{}
	authz := NewAuthzService(ms, slog.New(rec))

	allowed, err := authz.AuthorizeListReadBatch(context.Background(), f.user, resources)
	require.NoError(t, err)
	require.Len(t, allowed, n)
	for i, ok := range allowed {
		assert.False(t, ok, "row %d must be denied", i)
	}
	assert.Equal(t, 1, ms.countOf("GetEffectiveGroups"), "one closure load per request")
	warnings := rec.snapshot()
	require.Len(t, warnings, 1, "one warning per principal per request")
	assert.Equal(t, "list", warnings[0]["operation"])
	assert.Equal(t, f.userID, warnings[0]["principal_id"])
}

// TestAuthzFailureMemo_PrincipalsIndependent: within one memo phase, a
// failed closure for one principal does not affect another principal, and
// the other principal's successful closure is still loaded once.
func TestAuthzFailureMemo_PrincipalsIndependent(t *testing.T) {
	_, s := authzTestSetup(t)
	fa := newP1Fixture(t, s, "fmemo-ind-a")
	fb := newP1Fixture(t, s, "fmemo-ind-b")

	ps := &perUserGroupsStore{memoTestStore: newMemoTestStore(s), failUser: fa.userID, calls: map[string]int{}}
	rec := &principalWarningRecorder{}
	authz := NewAuthzService(ps, slog.New(rec))

	mctx := withAuthzInputMemo(context.Background())
	const rounds = 5
	for i := 0; i < rounds; i++ {
		da := authz.CheckAccess(mctx, fa.user, fa.agentRes, ActionRead)
		assert.False(t, da.Allowed, "round %d: principal A must be denied", i)
		assert.Equal(t, DenyCauseResolutionError, da.DenyCause, "round %d: principal A", i)
		db := authz.CheckAccess(mctx, fb.user, fb.agentRes, ActionRead)
		assert.True(t, db.Allowed, "round %d: principal B must be allowed: %s", i, db.Reason)
	}
	assert.Equal(t, 1, ps.callsFor(fa.userID), "principal A: one closure load")
	assert.Equal(t, 1, ps.callsFor(fb.userID), "principal B: one closure load")
	warnings := rec.snapshot()
	require.Len(t, warnings, 1)
	assert.Equal(t, fa.userID, warnings[0]["principal_id"])
}

// TestAuthzFailureMemo_NewRequestRetries: each request reloads the closure
// and logs its own warning.
func TestAuthzFailureMemo_NewRequestRetries(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "fmemo-retry")
	const n = 4
	resources := failureMemoAgents(t, s, f, "fmemo-retry", n)

	ms := newMemoTestStore(s)
	ms.fault = alwaysFault("GetEffectiveGroups")
	rec := &principalWarningRecorder{}
	authz := NewAuthzService(ms, slog.New(rec))

	for req := 1; req <= 2; req++ {
		allowed, err := authz.AuthorizeReadBatch(context.Background(), f.user, resources)
		require.NoError(t, err)
		for i, ok := range allowed {
			assert.False(t, ok, "request %d row %d must be denied", req, i)
		}
		assert.Equal(t, req, ms.countOf("GetEffectiveGroups"), "request %d reloads the closure", req)
	}
	warnings := rec.snapshot()
	require.Len(t, warnings, 2, "one warning per request")
	for _, w := range warnings {
		assert.Equal(t, string(ActionRead), w["operation"])
	}
}

// TestAuthzFailureMemo_CtxErrorNotMemoised: a context cancellation or
// deadline error from the store is not kept for later decisions, so each row
// reloads.
func TestAuthzFailureMemo_CtxErrorNotMemoised(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"canceled", context.Canceled},
		{"deadline_exceeded", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := authzTestSetup(t)
			f := newP1Fixture(t, s, "fmemo-ctx-"+tc.name)
			const n = 6
			resources := failureMemoAgents(t, s, f, "fmemo-ctx-"+tc.name, n)

			ms := newMemoTestStore(s)
			ms.fault = func(m string, _ int, _ context.Context) error {
				if m == "GetEffectiveGroups" {
					return fmt.Errorf("store call: %w", tc.err)
				}
				return nil
			}
			rec := &principalWarningRecorder{}
			authz := NewAuthzService(ms, slog.New(rec))

			allowed, err := authz.AuthorizeListReadBatch(context.Background(), f.user, resources)
			require.NoError(t, err)
			for i, ok := range allowed {
				assert.False(t, ok, "row %d must be denied", i)
			}
			assert.Equal(t, n, ms.countOf("GetEffectiveGroups"), "a ctx error is reloaded on every row")
			assert.Len(t, rec.snapshot(), n, "each unmemoised failure is logged")
		})
	}
}

// TestAuthzFailureMemo_CtxDoneDuringLoadNotMemoised: when the decision's
// ctx is done by the time the closure load returns, the failure is not kept
// even though the error itself is not a context error, so a later decision
// on a live ctx sharing the same memo reloads.
func TestAuthzFailureMemo_CtxDoneDuringLoadNotMemoised(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "fmemo-ctxdone")

	mctx := withAuthzInputMemo(context.Background())
	child, cancel := context.WithCancel(mctx)
	defer cancel()

	ms := newMemoTestStore(s)
	ms.fault = func(m string, n int, _ context.Context) error {
		if m == "GetEffectiveGroups" && n == 1 {
			cancel()
			return fmt.Errorf("plain store fault: %w", errInjected)
		}
		return nil
	}
	authz := NewAuthzService(ms, slog.New(&principalWarningRecorder{}))

	d1 := authz.CheckAccess(child, f.user, f.agentRes, ActionRead)
	assert.False(t, d1.Allowed, "the faulted decision is denied")
	require.Error(t, child.Err(), "the decision ctx is done after the load")

	d2 := authz.CheckAccess(mctx, f.user, f.agentRes, ActionRead)
	assert.True(t, d2.Allowed, "a later decision on a live ctx reloads: %s", d2.Reason)
	assert.Equal(t, 2, ms.countOf("GetEffectiveGroups"), "the failure from a done ctx is not kept")
}

// TestAuthzFailureMemo_ListScopesWarningNamesOperation: the list-scope
// principal-resolution warning names the list operation.
func TestAuthzFailureMemo_ListScopesWarningNamesOperation(t *testing.T) {
	_, s := authzTestSetup(t)
	f := newP1Fixture(t, s, "fmemo-scopes")

	ms := newMemoTestStore(s)
	ms.fault = alwaysFault("GetEffectiveGroups")
	rec := &principalWarningRecorder{msg: "ResolveListScopes: failed to resolve principals"}
	authz := NewAuthzService(ms, slog.New(rec))

	_, err := authz.ResolveListScopes(withAuthzInputMemo(context.Background()), f.user, "agent.list")
	require.Error(t, err)
	warnings := rec.snapshot()
	require.Len(t, warnings, 1)
	assert.Equal(t, "list", warnings[0]["operation"])
}
