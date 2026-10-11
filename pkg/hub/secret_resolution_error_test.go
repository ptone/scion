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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// fakeSecretResolutionErr is the dispatch error buildStartEnv returns on a
// secret resolution error, wrapping a backend error that names the fake
// secret.
func fakeSecretResolutionErr(verb string) error {
	return &secretResolutionError{Verb: verb, Err: errors.New(fakeResolveBackendError + ": " + fakeFileSecret.Name)}
}

// lifecycleErrorBody returns the error code and message of a lifecycle
// response body, and the body re-encoded for detail checks.
func lifecycleErrorBody(t *testing.T, body map[string]interface{}) (code, message, raw string) {
	t.Helper()
	e, _ := body["error"].(map[string]interface{})
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	data, err := json.Marshal(body)
	require.NoError(t, err)
	return code, message, string(data)
}

func TestSecretResolutionError_FixedMessageAndUnwrap(t *testing.T) {
	backendErr := errors.New(fakeResolveBackendError)
	err := fmt.Errorf("dispatch start: %w", &secretResolutionError{Verb: "started", Err: backendErr})
	assert.ErrorIs(t, err, backendErr, "the backend error stays reachable for errors.Is")
	assert.True(t, isSecretResolutionError(err))
	assert.Equal(t, "dispatch start: agent secrets could not be resolved; the agent was not started", err.Error())
	assertNoSecretResolutionDetail(t, err.Error(), "the error text")
	assert.True(t, startDidNotHappen(err))
	assert.Equal(t, startReleased, startOutcomeOf(err))
	assert.False(t, isSecretResolutionError(errors.New("other")))
}

// A start of a stopped agent that fails on a secret resolution error is a
// 503 unavailable with the fixed message; the claim is released and the
// agent keeps its phase.
func TestSecretResolutionError_StartIs503(t *testing.T) {
	f, d, a := newClaimFixture(t)
	d.start = func(context.Context, *store.Agent) error { return fakeSecretResolutionErr("started") }

	status, body := lifecycle(t, f, a.ID, "start")
	require.Equal(t, http.StatusServiceUnavailable, status, "%v", body)
	code, message, raw := lifecycleErrorBody(t, body)
	assert.Equal(t, ErrCodeUnavailable, code)
	assert.Equal(t, "agent secrets could not be resolved; the agent was not started", message)
	assertNoSecretResolutionDetail(t, raw, "the API body")

	got := getAgent(t, f.s, a.ID)
	assert.Equal(t, "stopped", got.Phase)
	assert.Empty(t, got.StartClaimID, "the start did not happen: the claim is released")
	assertNoSecretResolutionDetail(t, got.Message, "the agent message")
}

// A provisioned agent whose start fails on a secret resolution error goes
// back to rest with the fixed message, like any start that did not happen.
func TestSecretResolutionError_ProvisionedStartReturnsToRest(t *testing.T) {
	f, d, _ := newClaimFixture(t)
	a := f.addAgent("provisioned", "created", "")
	_, err := f.s.SetRunIntent(context.Background(), a.ID, store.RunIntentStopped)
	require.NoError(t, err)
	d.start = func(context.Context, *store.Agent) error { return fakeSecretResolutionErr("started") }

	status, body := lifecycle(t, f, a.ID, "start")
	require.Equal(t, http.StatusServiceUnavailable, status, "%v", body)
	_, _, raw := lifecycleErrorBody(t, body)
	assertNoSecretResolutionDetail(t, raw, "the API body")

	got := getAgent(t, f.s, a.ID)
	assert.Equal(t, "created", got.Phase)
	assert.Equal(t, store.RunIntentStopped, got.RunIntent, "back at rest")
	assert.Equal(t, "Start failed: agent secrets could not be resolved; the agent was not started. "+provisionedRestingNote, got.Message)
	assertNoSecretResolutionDetail(t, got.Message, "the agent message")
	assert.Empty(t, got.StartClaimID)
}

// A restart whose start leg fails on a secret resolution error is a 503
// with the fixed message, after the stop leg ran; the claim is released.
func TestSecretResolutionError_RestartIs503(t *testing.T) {
	f, d, _ := newClaimFixture(t)
	a := f.addAgent("restarting", "running", "working")
	d.start = func(context.Context, *store.Agent) error { return fakeSecretResolutionErr("started") }

	status, body := lifecycle(t, f, a.ID, "restart")
	require.Equal(t, http.StatusServiceUnavailable, status, "%v", body)
	code, message, raw := lifecycleErrorBody(t, body)
	assert.Equal(t, ErrCodeUnavailable, code)
	assert.Equal(t, "agent secrets could not be resolved; the agent was not started", message)
	assertNoSecretResolutionDetail(t, raw, "the API body")
	assert.Equal(t, int32(1), d.stops.Load(), "the stop leg ran")

	got := getAgent(t, f.s, a.ID)
	assert.Empty(t, got.StartClaimID)
	assertNoSecretResolutionDetail(t, got.Message, "the agent message")
}
