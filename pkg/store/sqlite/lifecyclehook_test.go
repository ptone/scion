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

package sqlite

import (
	"context"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleLifecycleHook(id string) *store.LifecycleHook {
	return &store.LifecycleHook{
		ID:        id,
		Name:      "register-on-running",
		ScopeType: store.LifecycleHookScopeHub,
		Selector: &store.LifecycleHookSelector{
			Template: "registry-agent",
		},
		Trigger: store.LifecycleHookTriggerRunning,
		Action: &store.LifecycleHookAction{
			Method:         "POST",
			URL:            "https://registry.example.com/agents",
			Headers:        map[string]string{"Content-Type": "application/json"},
			Body:           `{"name":"${AGENT_NAME}"}`,
			OnError:        store.LifecycleHookOnErrorRetry,
			TimeoutSeconds: 30,
		},
		ExecutionIdentity: uuid.New().String(),
		Enabled:           true,
		CreatedBy:         "admin@example.com",
	}
}

func TestSQLite_LifecycleHook_CreateGet(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	id := uuid.New().String()
	h := sampleLifecycleHook(id)
	require.NoError(t, s.CreateLifecycleHook(ctx, h))
	assert.False(t, h.Created.IsZero())
	assert.Equal(t, int64(1), h.StateVersion)

	got, err := s.GetLifecycleHook(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "register-on-running", got.Name)
	assert.Equal(t, store.LifecycleHookTriggerRunning, got.Trigger)
	assert.True(t, got.Enabled)
	require.NotNil(t, got.Selector)
	assert.Equal(t, "registry-agent", got.Selector.Template)
	require.NotNil(t, got.Action)
	assert.Equal(t, "POST", got.Action.Method)
	assert.Equal(t, 30, got.Action.TimeoutSeconds)
	assert.Equal(t, h.ExecutionIdentity, got.ExecutionIdentity)
}

func TestSQLite_LifecycleHook_CreateDuplicate(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	id := uuid.New().String()
	require.NoError(t, s.CreateLifecycleHook(ctx, sampleLifecycleHook(id)))
	err := s.CreateLifecycleHook(ctx, sampleLifecycleHook(id))
	assert.ErrorIs(t, err, store.ErrAlreadyExists)
}

func TestSQLite_LifecycleHook_GetNotFound(t *testing.T) {
	s := setupTestStore(t)
	_, err := s.GetLifecycleHook(context.Background(), uuid.New().String())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSQLite_LifecycleHook_Update(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	id := uuid.New().String()
	h := sampleLifecycleHook(id)
	require.NoError(t, s.CreateLifecycleHook(ctx, h))

	h.Name = "deregister-on-stopped"
	h.Trigger = store.LifecycleHookTriggerStopped
	h.Enabled = false
	require.NoError(t, s.UpdateLifecycleHook(ctx, h))
	assert.Equal(t, int64(2), h.StateVersion)

	got, err := s.GetLifecycleHook(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "deregister-on-stopped", got.Name)
	assert.Equal(t, store.LifecycleHookTriggerStopped, got.Trigger)
	assert.False(t, got.Enabled)
	assert.Equal(t, int64(2), got.StateVersion)
}

func TestSQLite_LifecycleHook_UpdateNotFound(t *testing.T) {
	s := setupTestStore(t)
	h := sampleLifecycleHook(uuid.New().String())
	h.StateVersion = 1
	err := s.UpdateLifecycleHook(context.Background(), h)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSQLite_LifecycleHook_UpdateVersionConflict(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	id := uuid.New().String()
	h := sampleLifecycleHook(id)
	require.NoError(t, s.CreateLifecycleHook(ctx, h))

	// Stale snapshot at version 1.
	stale, err := s.GetLifecycleHook(ctx, id)
	require.NoError(t, err)

	// First writer succeeds, version → 2.
	h.Name = "first-writer"
	require.NoError(t, s.UpdateLifecycleHook(ctx, h))
	assert.Equal(t, int64(2), h.StateVersion)

	// Stale writer at version 1 must conflict.
	stale.Name = "stale-writer"
	err = s.UpdateLifecycleHook(ctx, stale)
	assert.ErrorIs(t, err, store.ErrVersionConflict)

	got, err := s.GetLifecycleHook(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "first-writer", got.Name)
	assert.Equal(t, int64(2), got.StateVersion)
}

func TestSQLite_LifecycleHook_UpdateClearsOptionalFields(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	id := uuid.New().String()
	h := sampleLifecycleHook(id)
	h.ScopeID = "some-project"
	require.NoError(t, s.CreateLifecycleHook(ctx, h))

	h.ScopeID = ""
	h.Selector = nil
	h.Action = nil
	h.ExecutionIdentity = ""
	require.NoError(t, s.UpdateLifecycleHook(ctx, h))

	got, err := s.GetLifecycleHook(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, got.ScopeID)
	assert.Nil(t, got.Selector)
	assert.Nil(t, got.Action)
	assert.Empty(t, got.ExecutionIdentity)
}

func TestSQLite_LifecycleHook_Delete(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	id := uuid.New().String()
	require.NoError(t, s.CreateLifecycleHook(ctx, sampleLifecycleHook(id)))
	require.NoError(t, s.DeleteLifecycleHook(ctx, id))

	_, err := s.GetLifecycleHook(ctx, id)
	assert.ErrorIs(t, err, store.ErrNotFound)

	err = s.DeleteLifecycleHook(ctx, id)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSQLite_LifecycleHook_List(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	h1 := sampleLifecycleHook(uuid.New().String())
	h1.Trigger = store.LifecycleHookTriggerRunning
	h1.Enabled = true
	require.NoError(t, s.CreateLifecycleHook(ctx, h1))

	h2 := sampleLifecycleHook(uuid.New().String())
	h2.Trigger = store.LifecycleHookTriggerStopped
	h2.Enabled = false
	require.NoError(t, s.CreateLifecycleHook(ctx, h2))

	all, err := s.ListLifecycleHooks(ctx, store.LifecycleHookFilter{}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, all.TotalCount)
	assert.Len(t, all.Items, 2)

	running, err := s.ListLifecycleHooks(ctx, store.LifecycleHookFilter{
		Trigger: store.LifecycleHookTriggerRunning,
	}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, running.TotalCount)

	enabled := true
	enabledOnly, err := s.ListLifecycleHooks(ctx, store.LifecycleHookFilter{
		Enabled: &enabled,
	}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, enabledOnly.TotalCount)
}
