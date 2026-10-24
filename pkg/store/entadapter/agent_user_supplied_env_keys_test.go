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

package entadapter

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// AddAgentUserSuppliedEnvKeys adds names to the userSuppliedEnvKeys key of
// the applied config, names only (ptone/scion#6101): the stored list becomes
// the sorted union, every other key, every other column and state_version
// are left alone, the same names write nothing, and a write that finds the
// applied config changed since its read is retried with the union
// recomputed, so concurrent additions keep each other's names.
func TestAgentStore_AddAgentUserSuppliedEnvKeys(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)
	create := func(t *testing.T, name string) *store.Agent {
		t.Helper()
		a := makeAgent(projectID, name)
		a.AppliedConfig = &store.AgentAppliedConfig{Image: "example/image:1", Task: "fake task", ConfigDelivered: true}
		require.NoError(t, s.CreateAgent(ctx, a))
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		return got
	}
	t.Cleanup(func() { addUserSuppliedEnvKeysHook = nil })

	t.Run("adds only names, as a sorted union", func(t *testing.T) {
		a := create(t, "names-union")
		names, _, err := s.AddAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_KEY_B"})
		require.NoError(t, err)
		assert.Equal(t, []string{"FAKE_KEY_B"}, names)
		names, _, err = s.AddAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_KEY_A"})
		require.NoError(t, err)
		assert.Equal(t, []string{"FAKE_KEY_A", "FAKE_KEY_B"}, names)
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{"FAKE_KEY_A", "FAKE_KEY_B"}, got.AppliedConfig.UserSuppliedEnvKeys)
		assert.True(t, got.AppliedConfig.ConfigDelivered, "other keys are kept")
		assert.Equal(t, "example/image:1", got.AppliedConfig.Image)
		assert.Equal(t, "fake task", got.AppliedConfig.Task)
		assert.Equal(t, a.StateVersion, got.StateVersion, "state_version is not bumped")
	})
	t.Run("the same or no names write nothing", func(t *testing.T) {
		a := create(t, "names-same")
		_, _, err := s.AddAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_KEY"})
		require.NoError(t, err)
		calls := 0
		addUserSuppliedEnvKeysHook = func(context.Context, *ent.Tx, uuid.UUID) { calls++ }
		defer func() { addUserSuppliedEnvKeysHook = nil }()
		names, _, err := s.AddAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_KEY"})
		require.NoError(t, err)
		assert.Equal(t, []string{"FAKE_KEY"}, names)
		names, _, err = s.AddAgentUserSuppliedEnvKeys(ctx, a.ID, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"FAKE_KEY"}, names, "nothing to add keeps the stored names")
		assert.Zero(t, calls, "no write attempted")
	})
	t.Run("a missing agent writes nothing", func(t *testing.T) {
		names, _, err := s.AddAgentUserSuppliedEnvKeys(ctx, uuid.NewString(), []string{"FAKE_KEY"})
		require.NoError(t, err)
		assert.Nil(t, names)
	})
	t.Run("a config changed after the read is retried, the union recomputed", func(t *testing.T) {
		a := create(t, "names-retry")
		_, _, err := s.AddAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_EARLIER_KEY"})
		require.NoError(t, err)
		calls := 0
		addUserSuppliedEnvKeysHook = func(ctx context.Context, tx *ent.Tx, id uuid.UUID) {
			calls++
			if calls == 1 {
				// A change after this attempt's read: its write misses.
				require.NoError(t, tx.Agent.UpdateOneID(id).SetAppliedConfig(`{"image":"example/image:2"}`).Exec(ctx))
			}
		}
		defer func() { addUserSuppliedEnvKeysHook = nil }()
		names, _, err := s.AddAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_MY_KEY"})
		require.NoError(t, err)
		assert.Equal(t, 2, calls, "the miss is retried once")
		assert.Equal(t, []string{"FAKE_EARLIER_KEY", "FAKE_MY_KEY"}, names, "the retry re-read the stored names")
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{"FAKE_EARLIER_KEY", "FAKE_MY_KEY"}, got.AppliedConfig.UserSuppliedEnvKeys)
		assert.Equal(t, "example/image:1", got.AppliedConfig.Image)
	})
}

// The whole-row writers keep every stored user-supplied key name
// (ptone/scion#6101): a stale UpdateAgent (each of its paths) and a stale
// UpdateAgentConfig, whose copy was read before AddAgentUserSuppliedEnvKeys
// added names, store the union of the stored and their own names. A name
// added between a writer's read and its write is kept by the retry. Only
// that one field is merged: configDelivered is written as given, so a
// provision still clears it.
func TestAgentStore_WholeRowWritesKeepUserSuppliedEnvKeys(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)
	t.Cleanup(func() { keepUserSuppliedKeysHook = nil })
	// staleCopy creates an agent whose copy carries FAKE_OWN_KEY, then adds
	// FAKE_ADDED_KEY field-level after that copy was read.
	staleCopy := func(t *testing.T, name string) *store.Agent {
		t.Helper()
		a := makeAgent(projectID, name)
		a.Phase = "created" // eligible for a launch
		a.AppliedConfig = &store.AgentAppliedConfig{Image: "example/image:1", UserSuppliedEnvKeys: []string{"FAKE_OWN_KEY"}}
		require.NoError(t, s.CreateAgent(ctx, a))
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		_, _, err = s.AddAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_ADDED_KEY"})
		require.NoError(t, err)
		return got
	}
	stored := func(t *testing.T, id string) *store.AgentAppliedConfig {
		t.Helper()
		got, err := s.GetAgent(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, got.AppliedConfig)
		return got.AppliedConfig
	}
	want := []string{"FAKE_ADDED_KEY", "FAKE_OWN_KEY"}

	t.Run("a stale UpdateAgent keeps the names", func(t *testing.T) {
		a := staleCopy(t, "keep-update")
		a.Message = "fake message"
		require.NoError(t, s.UpdateAgent(ctx, a))
		cfg := stored(t, a.ID)
		assert.Equal(t, want, cfg.UserSuppliedEnvKeys)
		assert.Equal(t, "example/image:1", cfg.Image)
		assert.Equal(t, []string{"FAKE_OWN_KEY"}, a.AppliedConfig.UserSuppliedEnvKeys, "the caller's copy is not changed")
	})
	t.Run("a stale running UpdateAgent keeps the names", func(t *testing.T) {
		a := staleCopy(t, "keep-running")
		a.Phase = "running"
		require.NoError(t, s.UpdateAgent(ctx, a))
		assert.Equal(t, want, stored(t, a.ID).UserSuppliedEnvKeys)
	})
	t.Run("a stale running UpdateAgent with an active launch keeps the names", func(t *testing.T) {
		a := staleCopy(t, "keep-running-launch")
		_, err := s.BeginLaunch(ctx, a.ID, store.LaunchKindCreate, 5*time.Minute)
		require.NoError(t, err)
		fresh, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		a.StateVersion = fresh.StateVersion
		a.Phase = "running"
		require.NoError(t, s.UpdateAgent(ctx, a))
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, want, got.AppliedConfig.UserSuppliedEnvKeys)
		assert.Equal(t, store.LaunchStateEnded, got.LaunchState, "the transactional path ran")
	})
	t.Run("a stale UpdateAgentConfig keeps the names", func(t *testing.T) {
		a := staleCopy(t, "keep-config")
		_, err := s.UpdateAgentConfig(ctx, a.ID, a.StateVersion, &store.AgentAppliedConfig{Image: "example/image:2"}, nil)
		require.NoError(t, err)
		cfg := stored(t, a.ID)
		assert.Equal(t, want, cfg.UserSuppliedEnvKeys)
		assert.Equal(t, "example/image:2", cfg.Image)
	})
	t.Run("a nil applied config keeps the names", func(t *testing.T) {
		a := staleCopy(t, "keep-nil")
		a.AppliedConfig = nil
		require.NoError(t, s.UpdateAgent(ctx, a))
		assert.Equal(t, []string{"FAKE_ADDED_KEY", "FAKE_OWN_KEY"}, stored(t, a.ID).UserSuppliedEnvKeys)
	})
	for _, w := range []struct {
		name  string
		write func(a *store.Agent) error
	}{
		{"UpdateAgent", func(a *store.Agent) error { return s.UpdateAgent(ctx, a) }},
		{"UpdateAgentConfig", func(a *store.Agent) error {
			_, err := s.UpdateAgentConfig(ctx, a.ID, a.StateVersion, a.AppliedConfig, nil)
			return err
		}},
	} {
		t.Run("a name added after the read is kept by the retry: "+w.name, func(t *testing.T) {
			a := staleCopy(t, "keep-race-"+strings.ToLower(w.name))
			calls := 0
			keepUserSuppliedKeysHook = func(ctx context.Context, client *ent.Client, id uuid.UUID) {
				calls++
				if calls == 1 {
					// Inside the attempt's transaction, as a concurrent
					// field-level writer that committed after the read.
					require.NoError(t, client.Agent.UpdateOneID(id).
						SetAppliedConfig(`{"image":"example/image:1","userSuppliedEnvKeys":["FAKE_ADDED_KEY","FAKE_LATE_KEY","FAKE_OWN_KEY"]}`).
						Exec(ctx))
				}
			}
			defer func() { keepUserSuppliedKeysHook = nil }()
			require.NoError(t, w.write(a))
			assert.Equal(t, 2, calls, "the write was retried")
			assert.Equal(t, []string{"FAKE_ADDED_KEY", "FAKE_LATE_KEY", "FAKE_OWN_KEY"}, stored(t, a.ID).UserSuppliedEnvKeys)
		})
	}
	t.Run("an applied config that keeps changing is a version conflict", func(t *testing.T) {
		a := staleCopy(t, "keep-churn")
		calls := 0
		keepUserSuppliedKeysHook = func(ctx context.Context, client *ent.Client, id uuid.UUID) {
			calls++
			require.NoError(t, client.Agent.UpdateOneID(id).
				SetAppliedConfig(fmt.Sprintf(`{"image":"example/image:%d","userSuppliedEnvKeys":["FAKE_ADDED_KEY"]}`, calls)).
				Exec(ctx))
		}
		defer func() { keepUserSuppliedKeysHook = nil }()
		assert.ErrorIs(t, s.UpdateAgent(ctx, a), store.ErrVersionConflict)
		assert.Equal(t, configDeliveredAttempts, calls, "bounded retries")
	})
	t.Run("configDelivered is written as given", func(t *testing.T) {
		a := staleCopy(t, "keep-delivered")
		written, err := s.SetAgentConfigDelivered(ctx, a.ID)
		require.NoError(t, err)
		require.True(t, written)
		a.AppliedConfig.ConfigDelivered = false // a provision clears it
		require.NoError(t, s.UpdateAgent(ctx, a))
		cfg := stored(t, a.ID)
		assert.False(t, cfg.ConfigDelivered, "a provision still clears configDelivered")
		assert.Equal(t, want, cfg.UserSuppliedEnvKeys)

		_, err = s.SetAgentConfigDelivered(ctx, a.ID)
		require.NoError(t, err)
		fresh, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		_, err = s.UpdateAgentConfig(ctx, a.ID, fresh.StateVersion, &store.AgentAppliedConfig{Image: "example/image:1"}, nil)
		require.NoError(t, err)
		assert.False(t, stored(t, a.ID).ConfigDelivered)
	})
	t.Run("a stale version still conflicts", func(t *testing.T) {
		a := staleCopy(t, "keep-conflict")
		b := *a
		require.NoError(t, s.UpdateAgent(ctx, &b))
		assert.ErrorIs(t, s.UpdateAgent(ctx, a), store.ErrVersionConflict)
		_, err := s.UpdateAgentConfig(ctx, a.ID, a.StateVersion, a.AppliedConfig, nil)
		assert.ErrorIs(t, err, store.ErrVersionConflict)
	})
}
