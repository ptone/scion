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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

func TestPerfTraceNilReceiverIsNoOp(t *testing.T) {
	var t1 *PerfTrace
	// None of these must panic on a nil receiver -- this is exactly the
	// path every call site takes when tracing is disabled, since
	// PerfTraceFromContext returns nil and callers pass that straight into
	// these methods without a separate nil check.
	t1.AddPhase("db_fetch", 5*time.Millisecond)
	t1.IncStoreCall("GetEffectiveGroups")
	t1.RecordDecision(2 * time.Millisecond)

	phases, storeCalls, decisions := t1.HeaderValues()
	require.Empty(t, phases)
	require.Empty(t, storeCalls)
	require.Empty(t, decisions)
	require.Nil(t, t1.LogAttrs())
}

func TestPerfTraceAccumulatesAndSnapshots(t *testing.T) {
	tr := newPerfTrace()
	tr.AddPhase("db_fetch", 10*time.Millisecond)
	tr.AddPhase("db_fetch", 5*time.Millisecond) // same phase recorded twice: sums
	tr.AddPhase("enrich", 3*time.Millisecond)
	tr.IncStoreCall("GetEffectiveGroups")
	tr.IncStoreCall("GetEffectiveGroups")
	tr.IncStoreCall("GetRoleDefinitionsByIDs")
	tr.RecordDecision(1 * time.Millisecond)
	tr.RecordDecision(2 * time.Millisecond)

	phases, storeCalls, decisions := tr.HeaderValues()
	require.Equal(t, "db_fetch=15,enrich=3", phases)
	require.Equal(t, "GetEffectiveGroups=2,GetRoleDefinitionsByIDs=1", storeCalls)
	require.Equal(t, "count=2,ms=3", decisions)

	attrs := tr.LogAttrs()
	found := map[string]bool{}
	for _, a := range attrs {
		found[a.Key] = true
	}
	require.True(t, found["phase_db_fetch_ms"])
	require.True(t, found["phase_enrich_ms"])
	require.True(t, found["store_calls_GetEffectiveGroups"])
	require.True(t, found["store_calls_GetRoleDefinitionsByIDs"])
	require.True(t, found["decisions"])
	require.True(t, found["decisions_ms"])
}

func TestPerfTraceHeaderValuesAreSorted(t *testing.T) {
	tr := newPerfTrace()
	tr.AddPhase("serialize", time.Millisecond)
	tr.AddPhase("db_fetch", time.Millisecond)
	tr.AddPhase("capabilities_batch", time.Millisecond)

	phases, _, _ := tr.HeaderValues()
	// Alphabetical: capabilities_batch, db_fetch, serialize.
	require.Equal(t, "capabilities_batch=1,db_fetch=1,serialize=1", phases)
}

func TestPerfTraceConcurrentAccess(t *testing.T) {
	tr := newPerfTrace()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.AddPhase("db_fetch", time.Microsecond)
			tr.IncStoreCall("GetEffectiveGroups")
			tr.RecordDecision(time.Microsecond)
		}()
	}
	wg.Wait()

	_, storeCalls, decisions := tr.HeaderValues()
	require.Equal(t, "GetEffectiveGroups=50", storeCalls)
	require.Equal(t, "count=50,ms=0", decisions)
}

func TestContextWithPerfTraceRoundTrip(t *testing.T) {
	ctx := context.Background()
	require.Nil(t, PerfTraceFromContext(ctx))

	tr := newPerfTrace()
	ctx = ContextWithPerfTrace(ctx, tr)
	require.Same(t, tr, PerfTraceFromContext(ctx))
}

func TestStartPhaseNoTraceInContextIsNoop(t *testing.T) {
	// No trace installed (tracing disabled is the default state a bare
	// context.Background() models): StartPhase must return a usable,
	// side-effect-free stop function rather than nil or a panic.
	stop := StartPhase(context.Background(), "db_fetch")
	require.NotNil(t, stop)
	require.NotPanics(t, stop)
}

func TestStartPhaseRecordsElapsed(t *testing.T) {
	tr := newPerfTrace()
	ctx := ContextWithPerfTrace(context.Background(), tr)

	stop := StartPhase(ctx, "db_fetch")
	time.Sleep(2 * time.Millisecond)
	stop()

	phases, _, _ := tr.HeaderValues()
	require.Contains(t, phases, "db_fetch=")
	require.NotContains(t, phases, "db_fetch=0")
}

func TestRecordDecisionNoTraceInContextIsNoop(t *testing.T) {
	require.NotPanics(t, func() {
		RecordDecision(context.Background(), time.Millisecond)
	})
}

func TestRecordDecisionUpdatesContextTrace(t *testing.T) {
	tr := newPerfTrace()
	ctx := ContextWithPerfTrace(context.Background(), tr)
	RecordDecision(ctx, 5*time.Millisecond)
	_, _, decisions := tr.HeaderValues()
	require.Equal(t, "count=1,ms=5", decisions)
}

// --- perfCountingStore --------------------------------------------------

// stubStore is a minimal store.Store that only implements the methods
// perfCountingStore overrides plus whatever store.Store requires to embed;
// since perfCountingStore embeds store.Store, a stub that only defines the
// six wrapped methods (and panics on anything else) is enough to prove the
// counting/delegation behavior without a real database.
type stubStore struct {
	store.Store // nil embed: any unimplemented method panics if called, which no test below does
	calls       []string
}

func (s *stubStore) GetEffectiveGroups(ctx context.Context, userID string) ([]string, error) {
	s.calls = append(s.calls, "GetEffectiveGroups")
	return []string{"g1"}, nil
}

func (s *stubStore) GetEffectiveGroupsForAgent(ctx context.Context, agentID string) ([]string, error) {
	s.calls = append(s.calls, "GetEffectiveGroupsForAgent")
	return nil, nil
}

func (s *stubStore) ListRoleBindingsForPrincipals(ctx context.Context, principals []store.PrincipalRef, scopeTypes []string, scopeIDs []string) ([]*store.RoleBinding, error) {
	s.calls = append(s.calls, "ListRoleBindingsForPrincipals")
	return nil, nil
}

func (s *stubStore) GetRoleDefinitionsByIDs(ctx context.Context, ids []string) (map[string]*store.RoleDefinition, error) {
	s.calls = append(s.calls, "GetRoleDefinitionsByIDs")
	return nil, nil
}

func (s *stubStore) ListAccessConstraints(ctx context.Context, limit, offset int) ([]*store.AccessConstraint, error) {
	s.calls = append(s.calls, "ListAccessConstraints")
	return nil, nil
}

func (s *stubStore) GetDelegationEdgesForDelegate(ctx context.Context, delegateType, delegateID string) ([]*store.DelegationEdge, error) {
	s.calls = append(s.calls, "GetDelegationEdgesForDelegate")
	return nil, nil
}

func withPerfTraceEnabled(t *testing.T, enabled bool) {
	t.Helper()
	orig := perfTraceEnabled
	perfTraceEnabled = enabled
	t.Cleanup(func() { perfTraceEnabled = orig })
}

func TestWrapStoreForPerfTraceDisabledReturnsUnchanged(t *testing.T) {
	withPerfTraceEnabled(t, false)
	s := &stubStore{}
	wrapped := WrapStoreForPerfTrace(s)
	// Identity, not just interface equality: proves zero overhead (no
	// wrapper allocated at all) in the disabled case, not merely
	// "delegates transparently."
	require.Same(t, store.Store(s), wrapped)
}

func TestWrapStoreForPerfTraceEnabledCountsCalls(t *testing.T) {
	withPerfTraceEnabled(t, true)
	s := &stubStore{}
	wrapped := WrapStoreForPerfTrace(s)
	if _, ok := wrapped.(perfCountingStore); !ok {
		t.Fatalf("WrapStoreForPerfTrace(s) = %T, want perfCountingStore when enabled", wrapped)
	}

	tr := newPerfTrace()
	ctx := ContextWithPerfTrace(context.Background(), tr)

	_, _ = wrapped.GetEffectiveGroups(ctx, "u1")
	_, _ = wrapped.GetEffectiveGroups(ctx, "u1")
	_, _ = wrapped.GetEffectiveGroupsForAgent(ctx, "a1")
	_, _ = wrapped.ListRoleBindingsForPrincipals(ctx, nil, nil, nil)
	_, _ = wrapped.GetRoleDefinitionsByIDs(ctx, nil)
	_, _ = wrapped.ListAccessConstraints(ctx, 10, 0)
	_, _ = wrapped.GetDelegationEdgesForDelegate(ctx, "agent", "a1")

	_, storeCalls, _ := tr.HeaderValues()
	require.Equal(t,
		"GetDelegationEdgesForDelegate=1,GetEffectiveGroups=2,GetEffectiveGroupsForAgent=1,GetRoleDefinitionsByIDs=1,ListAccessConstraints=1,ListRoleBindingsForPrincipals=1",
		storeCalls,
	)

	// The underlying store's own calls still happened -- counting is
	// transparent, not a replacement for the real call.
	require.Len(t, s.calls, 7)
}

func TestWrapStoreForPerfTraceEnabledButNoTraceInContextDoesNotPanic(t *testing.T) {
	withPerfTraceEnabled(t, true)
	s := &stubStore{}
	wrapped := WrapStoreForPerfTrace(s)

	// No PerfTrace installed in this context (e.g. a background job, not a
	// traced HTTP request): the counting methods must still delegate
	// correctly rather than panicking on a nil trace.
	require.NotPanics(t, func() {
		_, err := wrapped.GetEffectiveGroups(context.Background(), "u1")
		require.NoError(t, err)
	})
}
