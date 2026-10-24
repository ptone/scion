// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// The env finalize writes the names of the user-supplied env keys ahead of
// the broker send, as a union with the stored names (ptone/scion#6101,
// design r16 §4.4.2, H-18).

// namesWriteFaultStore fails every AddAgentUserSuppliedEnvKeys call.
type namesWriteFaultStore struct {
	store.Store
	err error
}

func (s namesWriteFaultStore) AddAgentUserSuppliedEnvKeys(context.Context, string, []string) ([]string, error) {
	return nil, s.err
}

// H-18 (i): a failed names write fails the finalize with the fixed keyless
// 503; nothing is minted or sent.
func TestDispatchFinalizeEnv_NamesWriteFaultIs503AndSendsNothing(t *testing.T) {
	f := newRunDispatchFixture(t)
	before, err := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, err)
	d := NewHTTPAgentDispatcherWithClient(namesWriteFaultStore{Store: f.store, err: errors.New("FAKE-names-write-detail")}, f.client, false, slog.Default())
	d.SetTokenGenerator(runTokenGenerator{svc: f.svc})

	_, err = d.DispatchFinalizeEnv(context.Background(), f.agent, map[string]string{"FAKE_USER_KEY": "FAKE-value"})
	require.ErrorIs(t, err, errUserSuppliedKeysUnrecorded)
	assert.NotContains(t, err.Error(), "FAKE-names-write-detail", "no store text in the error")
	assert.False(t, f.client.createCalled, "nothing sent")
	after, err := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, err)
	assert.Equal(t, before.RunID, after.RunID, "no run minted")

	rec := httptest.NewRecorder()
	require.True(t, writeUserSuppliedKeysUnrecordedError(rec, err))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), errUserSuppliedKeysUnrecorded.Error())
	assert.NotContains(t, rec.Body.String(), "FAKE_USER_KEY", "the body names no key")
	assert.NotContains(t, rec.Body.String(), "FAKE-names-write-detail")
	assert.True(t, isConfirmedStartNotActedOnError(err), "nothing was sent: not acted on")
}

// H-18 (ii): a broker send that fails after the names write leaves the
// names (names only), and a later finalize with another key set adds to
// them.
func TestDispatchFinalizeEnv_SendFailureKeepsNamesAndNextFinalizeAdds(t *testing.T) {
	f := newRunDispatchFixture(t)
	f.client.returnErr = errors.New("FAKE broker unreachable")
	_, err := f.d.DispatchFinalizeEnv(context.Background(), f.agent, map[string]string{"FAKE_KEY_A": "FAKE-value-a"})
	require.Error(t, err)
	require.True(t, f.client.createCalled, "the send happened after the write")
	stored := reloadAgent(t, f.store, f.agent.ID)
	assert.Equal(t, []string{"FAKE_KEY_A"}, stored.AppliedConfig.UserSuppliedEnvKeys)

	f.client.returnErr = nil
	_, err = f.d.DispatchFinalizeEnv(context.Background(), stored, map[string]string{"FAKE_KEY_B": "FAKE-value-b"})
	require.NoError(t, err)
	assert.Equal(t, []string{"FAKE_KEY_A", "FAKE_KEY_B"}, reloadAgent(t, f.store, f.agent.ID).AppliedConfig.UserSuppliedEnvKeys)
}

// H-18 (ii): a recorded name whose key the broker no longer requires has no
// effect at start.
func TestStartGather_RecordedNameNotRequiredHasNoEffect(t *testing.T) {
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return startedAnswer(true), nil
	}, nil)
	_, err := f.store.AddAgentUserSuppliedEnvKeys(context.Background(), f.agent.ID, []string{"FAKE_NO_LONGER_REQUIRED_KEY"})
	require.NoError(t, err)
	agent := reloadAgent(t, f.store, f.agent.ID)
	require.NoError(t, f.d.DispatchAgentStart(withRestartStartLeg(context.Background()), agent, "", false))
	assert.Len(t, gc.calls, 1, "started on the first send")
}

// H-18 (iii): two partial finalizes (keys A, then B, each from a copy read
// before either) record both names, with the other applied-config keys
// intact, and a restart with either key unmet as a marker-only need fails
// with the keyless 422.
func TestDispatchFinalizeEnv_PartialFinalizesRecordTheUnion(t *testing.T) {
	for _, unmet := range []string{"FAKE_PARTIAL_KEY_A", "FAKE_PARTIAL_KEY_B"} {
		t.Run(unmet, func(t *testing.T) {
			f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
				return needsAnswer([]string{unmet}, unmet), nil
			}, nil)
			first := reloadAgent(t, f.store, f.agent.ID)
			second := reloadAgent(t, f.store, f.agent.ID)
			_, err := f.d.DispatchFinalizeEnv(context.Background(), first, map[string]string{"FAKE_PARTIAL_KEY_A": "FAKE-value-a"})
			require.NoError(t, err)
			_, err = f.d.DispatchFinalizeEnv(context.Background(), second, map[string]string{"FAKE_PARTIAL_KEY_B": "FAKE-value-b"})
			require.NoError(t, err)
			assert.Equal(t, []string{"FAKE_PARTIAL_KEY_A", "FAKE_PARTIAL_KEY_B"}, second.AppliedConfig.UserSuppliedEnvKeys,
				"the second finalize's copy mirrors the stored union")

			stored := reloadAgent(t, f.store, f.agent.ID)
			assert.Equal(t, []string{"FAKE_PARTIAL_KEY_A", "FAKE_PARTIAL_KEY_B"}, stored.AppliedConfig.UserSuppliedEnvKeys)
			assert.Equal(t, "claude", stored.AppliedConfig.HarnessConfig, "other keys intact")
			assert.Equal(t, "task", stored.AppliedConfig.Task, "other keys intact")

			calls := len(gc.calls)
			err = f.d.DispatchAgentStart(withRestartStartLeg(context.Background()), stored, "", false)
			require.ErrorIs(t, err, errStartEnvUnmet)
			assert.Len(t, gc.calls, calls+1, "no replay")
			rec := httptest.NewRecorder()
			require.True(t, writeStartEnvGatherError(rec, err, api.AgentActionRestart))
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
			assert.NotContains(t, rec.Body.String(), unmet, "the 422 body names no key")
		})
	}
}

// H-18 (v) and (vi): the requester-path finalize's whole-row write
// (updateAgentAfterDispatch) keeps the names, and so does a concurrent
// whole-row writer holding a copy read before the finalize.
func TestDispatchFinalizeEnv_WholeRowWritersKeepTheNames(t *testing.T) {
	f := newRunDispatchFixture(t)
	stale := reloadAgent(t, f.store, f.agent.ID)
	_, err := f.d.DispatchFinalizeEnv(context.Background(), f.agent, map[string]string{"FAKE_REQUESTER_KEY": "FAKE-value"})
	require.NoError(t, err)

	srv := &Server{store: f.store}
	require.NoError(t, srv.updateAgentAfterDispatch(context.Background(), f.agent))
	assert.Equal(t, []string{"FAKE_REQUESTER_KEY"}, reloadAgent(t, f.store, f.agent.ID).AppliedConfig.UserSuppliedEnvKeys)

	// The stale copy predates the finalize; refresh only its version, as a
	// writer that re-read state_version but not the applied config would.
	stale.StateVersion = reloadAgent(t, f.store, f.agent.ID).StateVersion
	stale.Message = "FAKE stale writer"
	require.NoError(t, f.store.UpdateAgent(context.Background(), stale))
	assert.Equal(t, []string{"FAKE_REQUESTER_KEY"}, reloadAgent(t, f.store, f.agent.ID).AppliedConfig.UserSuppliedEnvKeys)
	_, err = f.store.UpdateAgentConfig(context.Background(), f.agent.ID, reloadAgent(t, f.store, f.agent.ID).StateVersion,
		&store.AgentAppliedConfig{HarnessConfig: "claude"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"FAKE_REQUESTER_KEY"}, reloadAgent(t, f.store, f.agent.ID).AppliedConfig.UserSuppliedEnvKeys)
}

// H-18 (v): a finalize the requester hands to the owner node records the
// names before the hand-off, and the requester's in-memory copy and a later
// whole-row write keep them.
func TestDispatchFinalizeEnv_OwnerHandOffKeepsTheNames(t *testing.T) {
	f := newCrossNodeFixture(t, nil, true)
	f.requester.client = &mockRuntimeBrokerClient{returnErr: ErrLifecycleDeferred}
	stale := reloadAgent(t, f.store, f.agent.ID)

	_, err := f.requester.DispatchFinalizeEnv(crossNodeCtx(t), f.agent, map[string]string{"FAKE_HANDOFF_KEY": "FAKE-value"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"FAKE_HANDOFF_KEY": "FAKE-value"}, f.ownerDisp.lastFinalizeEnv, "the owner ran the finalize")
	require.NotNil(t, f.agent.AppliedConfig)
	assert.Equal(t, []string{"FAKE_HANDOFF_KEY"}, f.agent.AppliedConfig.UserSuppliedEnvKeys, "the requester's copy mirrors the store")
	assert.Equal(t, []string{"FAKE_HANDOFF_KEY"}, reloadAgent(t, f.store, f.agent.ID).AppliedConfig.UserSuppliedEnvKeys)

	stale.StateVersion = reloadAgent(t, f.store, f.agent.ID).StateVersion
	require.NoError(t, f.store.UpdateAgent(context.Background(), stale))
	assert.Equal(t, []string{"FAKE_HANDOFF_KEY"}, reloadAgent(t, f.store, f.agent.ID).AppliedConfig.UserSuppliedEnvKeys)
}

// H-18 (i), HTTP: the env submit handler answers a failed names write with
// the fixed keyless 503.
func TestSubmitAgentEnv_NamesWriteFaultIs503(t *testing.T) {
	disp := &finalizeEnvErrDispatcher{err: fmt.Errorf("finalize: %w", &userSuppliedKeysWriteError{err: errors.New("FAKE-names-write-detail")})}
	srv, s, project := setupCreateAgentServer(t, disp)
	agent := &store.Agent{
		ID: tid("agent-env-names-503"), Name: "env-names-503", Slug: "env-names-503",
		ProjectID: project.ID, RuntimeBrokerID: project.DefaultRuntimeBrokerID, Phase: string(state.PhaseProvisioning),
	}
	require.NoError(t, s.CreateAgent(context.Background(), agent))
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/projects/"+project.ID+"/agents/"+agent.Slug+"/env",
		SubmitEnvRequest{Env: map[string]string{"FAKE_USER_KEY": "FAKE-value"}})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), errUserSuppliedKeysUnrecorded.Error())
	assert.NotContains(t, rec.Body.String(), "FAKE-names-write-detail", "no store text in the body")
	assert.NotContains(t, rec.Body.String(), "FAKE_USER_KEY", "the body names no key")
}
