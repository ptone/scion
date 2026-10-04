//go:build !no_sqlite

package entadapter

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordTestAgent seeds an agent in phase and records a launch of kind on it.
func recordTestAgent(t *testing.T, ctx context.Context, s *AgentStore, projectID, slug, phase, kind string) (*store.Agent, string) {
	t.Helper()
	a := makeAgent(projectID, slug)
	a.Phase = phase
	require.NoError(t, s.CreateAgent(ctx, a))
	id, err := s.RecordLaunch(ctx, a.ID, kind)
	require.NoError(t, err)
	return a, id
}

func TestRecordLaunch(t *testing.T) {
	tests := []struct {
		name  string
		phase string
		kind  string
	}{
		{"create", "created", store.LaunchKindCreate},
		{"start", "stopped", store.LaunchKindStart},
		{"restart", "running", store.LaunchKindRestart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, projectID := newTestAgentStore(t)
			a, id := recordTestAgent(t, ctx, s, projectID, "rec-"+tt.name, tt.phase, tt.kind)
			before := a.StateVersion

			got, err := s.GetAgent(ctx, a.ID)
			require.NoError(t, err)
			_, err = uuid.Parse(id)
			require.NoError(t, err)
			assert.Equal(t, id, got.LaunchID)
			assert.Equal(t, tt.kind, got.LaunchKind)
			assert.Equal(t, store.LaunchStateEnded, got.LaunchState)
			assert.Equal(t, store.LaunchEndReasonRecordOnly, got.LaunchEndReason)
			assert.True(t, got.LaunchDeadline.IsZero())
			assert.Equal(t, tt.phase, got.Phase, "RecordLaunch must not change the phase")
			assert.Equal(t, before, got.StateVersion, "RecordLaunch must not bump state_version")
			assert.False(t, got.IsInFlight())
			assert.False(t, got.IsIncompleteCreate())
			assert.Nil(t, store.ComputeAgentLaunch(got, time.Now()), "a record-only launch is not shown to clients")
		})
	}
}

func TestRecordLaunchRejectsUnknownKind(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)
	a := createLaunchableAgent(t, ctx, s, projectID, "rec-kind")
	_, err := s.RecordLaunch(ctx, a.ID, "resume")
	assert.ErrorIs(t, err, store.ErrInvalidInput)
	_, err = s.RecordLaunch(ctx, uuid.NewString(), store.LaunchKindStart)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestRecordLaunchSupersedesActiveLaunch(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)
	a := createLaunchableAgent(t, ctx, s, projectID, "rec-supersede")
	old, err := s.BeginLaunch(ctx, a.ID, store.LaunchKindCreate, time.Hour)
	require.NoError(t, err)
	setAgentLaunchError(t, ctx, s, a.ID, store.LaunchErrorAgentError)

	id, err := s.RecordLaunch(ctx, a.ID, store.LaunchKindCreate)
	require.NoError(t, err)
	assert.NotEqual(t, old, id)

	got, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, id, got.LaunchID)
	assert.Empty(t, got.LaunchError)
	assert.Equal(t, store.LaunchStateEnded, got.LaunchState)

	ans, _, err := s.ApplyLaunchReport(ctx, a.ID, a.RuntimeBrokerID, store.LaunchReport{
		LaunchID: old, InstanceID: "i1", State: store.LaunchReportStateProgress,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, ans.HTTPStatus, "the superseded async launch is refused")
}

// TestRecordLaunchIsNeverReaped: with no launch reports at all, a recorded
// launch is left alone by every reaper rule and keeps its id.
func TestRecordLaunchIsNeverReaped(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)
	a, id := recordTestAgent(t, ctx, s, projectID, "rec-reaper", "stopped", store.LaunchKindStart)
	setAgentLastReportAt(t, ctx, s, a.ID, time.Now().Add(-time.Hour))
	setAgentLaunchDeadline(t, ctx, s, a.ID, time.Now().Add(-time.Hour))

	for range 2 {
		result, err := s.RunLaunchReaperTick(ctx, testReaperParams)
		require.NoError(t, err)
		assert.Empty(t, result.Reaped)
	}
	got, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, id, got.LaunchID)
	assert.Equal(t, store.LaunchEndReasonRecordOnly, got.LaunchEndReason)
	assert.Equal(t, "stopped", got.Phase)
}

func TestRecordLaunchSurvivesStatusWrites(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)
	a, id := recordTestAgent(t, ctx, s, projectID, "rec-status", "created", store.LaunchKindCreate)

	for _, phase := range []string{"running", "stopped"} {
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		got.Phase = phase
		require.NoError(t, s.UpdateAgent(ctx, got))
		after, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, id, after.LaunchID, "phase %s must keep launch_id", phase)
		assert.Equal(t, store.LaunchEndReasonRecordOnly, after.LaunchEndReason)
	}
}

func TestAdoptLaunchID(t *testing.T) {
	const other = "11111111-2222-3333-4444-555555555555"
	tests := []struct {
		name      string
		proposed  string // "" means the recorded id
		effective string // "" stays ""; "recorded" means the recorded id
		wantOK    bool
		wantRow   string // "recorded", or a literal
	}{
		{name: "reused labelled container", effective: other, wantOK: true, wantRow: other},
		{name: "reused unlabelled container", effective: "", wantOK: true, wantRow: ""},
		{name: "new container", effective: "recorded", wantOK: false, wantRow: "recorded"},
		{name: "superseded proposal", proposed: "stale", effective: other, wantOK: false, wantRow: "recorded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, projectID := newTestAgentStore(t)
			a, id := recordTestAgent(t, ctx, s, projectID, "adopt", "stopped", store.LaunchKindStart)
			resolve := func(v string) string {
				if v == "recorded" {
					return id
				}
				return v
			}
			proposed := tt.proposed
			if proposed == "" {
				proposed = id
			}
			ok, err := s.AdoptLaunchID(ctx, a.ID, proposed, resolve(tt.effective))
			require.NoError(t, err)
			assert.Equal(t, tt.wantOK, ok)
			got, err := s.GetAgent(ctx, a.ID)
			require.NoError(t, err)
			assert.Equal(t, resolve(tt.wantRow), got.LaunchID)
			assert.Equal(t, a.StateVersion, got.StateVersion, "AdoptLaunchID must not bump state_version")
		})
	}
}

func TestAdoptLaunchIDNotFound(t *testing.T) {
	s, _ := newTestAgentStore(t)
	_, err := s.AdoptLaunchID(context.Background(), uuid.NewString(), "a", "b")
	assert.ErrorIs(t, err, store.ErrNotFound)
}
