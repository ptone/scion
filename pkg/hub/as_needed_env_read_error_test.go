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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// resolveAsNeededForKeys propagates a ListEnvVars error from its stored env
// vars pass as a *secretResolutionError with the env settings source and the
// fixed message (ptone/scion#6089). The first-pass read of the same settings
// (resolveEnvFromStorage) is covered by the TestEnvReadError_* tests.

const fakeNeededKey = "FAKE_NEEDED_KEY"

// newAsNeededEnvReadFixture returns an env read fixture whose stored env
// reads succeed until the broker's first create-with-gather call, which
// reports fakeNeededKey as needed and makes every later ListEnvVars fail.
// It returns the broker call counter.
func newAsNeededEnvReadFixture(t *testing.T) (*envReadFixture, *int) {
	t.Helper()
	var es *envReadErrStore
	f := newEnvReadFixture(t, func(s store.Store) store.Store {
		es = &envReadErrStore{Store: s, scopesOK: true}
		return es
	})
	calls := 0
	f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
		calls++
		es.scopesOK = false
		return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{fakeNeededKey}}, nil
	}
	return f, &calls
}

func TestStoredEnvReadError_Classification(t *testing.T) {
	cause := errors.New(fakeEnvReadBackendError)
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{cause, "store_error"},
		{fmt.Errorf("wrapped: %w", context.Canceled), "canceled"},
		{context.DeadlineExceeded, "deadline_exceeded"},
	} {
		e := newStoredEnvReadError(store.ScopeProject, tc.err)
		assert.Equal(t, tc.kind, e.Kind)
		assert.Equal(t, "list project env vars failed ("+tc.kind+")", e.Error())
		assert.ErrorIs(t, e, tc.err, "the store error stays reachable")
		assertNoEnvReadDetail(t, e.Error(), "the error text")
	}
}

// Create with env gather and finalize env return the fixed env settings
// message when the as-needed pass cannot read the stored env vars, and send
// no replay.
func TestAsNeededEnvRead_GatherPathsReturnFixedMessage(t *testing.T) {
	cases := []struct {
		name     string
		dispatch func(f *envReadFixture) error
	}{
		{"create with env gather", func(f *envReadFixture) error {
			_, err := f.d.DispatchAgentCreateWithGather(context.Background(), f.agent)
			return err
		}},
		{"finalize env", func(f *envReadFixture) error {
			_, err := f.d.DispatchFinalizeEnv(context.Background(), f.agent, nil)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, calls := newAsNeededEnvReadFixture(t)

			err := tc.dispatch(f)
			require.Error(t, err)
			var resErr *secretResolutionError
			require.ErrorAs(t, err, &resErr)
			assert.Equal(t, resolutionSourceEnvSettings, resErr.Source)
			var readErr *storedEnvReadError
			require.ErrorAs(t, err, &readErr)
			assert.Equal(t, envReadFixedMessage("created"), err.Error())
			assertNoEnvReadDetail(t, err.Error(), "the error text")
			assert.Equal(t, 1, *calls, "no replay is sent")

			rec := httptest.NewRecorder()
			require.True(t, writeSecretResolutionError(rec, err))
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			var body ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, envReadFixedMessage("created"), body.Error.Message)
			assertNoEnvReadDetail(t, rec.Body.String(), "the API body")
		})
	}
}

// Provision and reprovision return the fixed env settings message when
// their as-needed pass cannot read the stored env vars, and send no replay;
// a start and a restart then return the fixed message without calling the
// broker.
func TestAsNeededEnvRead_ProvisionReturnsFixedMessage(t *testing.T) {
	t.Run("provision", func(t *testing.T) {
		f, calls := newAsNeededEnvReadFixture(t)
		err := f.d.DispatchAgentProvision(context.Background(), f.agent)
		require.Error(t, err)
		var readErr *storedEnvReadError
		require.ErrorAs(t, err, &readErr)
		assert.Equal(t, envReadFixedMessage("created"), err.Error())
		assert.Equal(t, 1, *calls, "no replay is sent")

		err = f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
		require.Error(t, err)
		assert.Equal(t, envReadFixedMessage("started"), err.Error())
		err = f.d.DispatchAgentRestart(context.Background(), f.agent)
		require.Error(t, err)
		assert.Equal(t, envReadFixedMessage("restarted"), err.Error())
		assert.False(t, f.client.startCalled, "no start is sent")
		assert.False(t, f.client.restartCalled, "no restart is sent")
	})
	t.Run("reprovision", func(t *testing.T) {
		f, calls := newAsNeededEnvReadFixture(t)
		err := f.d.DispatchAgentReprovision(context.Background(), f.agent)
		require.Error(t, err)
		assert.Equal(t, envReadFixedMessage("reprovisioned"), err.Error())
		assertNoEnvReadDetail(t, err.Error(), "the error text")
		assert.Equal(t, 1, *calls, "no replay is sent")
	})
}

// The hub log line names the scope and the classification only: no store
// message, stored key or value.
func TestAsNeededEnvRead_LogNamesScopeAndKindOnly(t *testing.T) {
	f, _ := newAsNeededEnvReadFixture(t)
	_, err := f.d.DispatchAgentCreateWithGather(context.Background(), f.agent)
	require.Error(t, err)
	logs := f.logs.String()
	assert.Contains(t, logs, "failing the as-needed pass")
	assert.Regexp(t, `list \w+ env vars failed \(store_error\)`, logs)
	assertNoEnvReadDetail(t, logs, "the hub log")
}
