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
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// SetAgentUserSuppliedEnvKeys sets only the userSuppliedEnvKeys key of the
// applied config, names only (ptone/scion#6101): every other key, every
// other column and state_version are left alone, an empty list removes the
// key, an identical list writes nothing, and a write that finds the applied
// config changed since its read is retried.
func TestAgentStore_SetAgentUserSuppliedEnvKeys(t *testing.T) {
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
	t.Cleanup(func() { setUserSuppliedEnvKeysHook = nil })

	t.Run("sets only the names", func(t *testing.T) {
		a := create(t, "names-only")
		written, err := s.SetAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_KEY_A", "FAKE_KEY_B"})
		require.NoError(t, err)
		assert.True(t, written)
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{"FAKE_KEY_A", "FAKE_KEY_B"}, got.AppliedConfig.UserSuppliedEnvKeys)
		assert.True(t, got.AppliedConfig.ConfigDelivered, "other keys are kept")
		assert.Equal(t, "example/image:1", got.AppliedConfig.Image)
		assert.Equal(t, "fake task", got.AppliedConfig.Task)
		assert.Equal(t, a.StateVersion, got.StateVersion, "state_version is not bumped")

		again, err := s.SetAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_KEY_A", "FAKE_KEY_B"})
		require.NoError(t, err)
		assert.False(t, again, "the same names write nothing")
	})
	t.Run("an empty list removes the names", func(t *testing.T) {
		a := create(t, "names-clear")
		_, err := s.SetAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_OLD_KEY"})
		require.NoError(t, err)
		written, err := s.SetAgentUserSuppliedEnvKeys(ctx, a.ID, []string{})
		require.NoError(t, err)
		assert.True(t, written)
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Empty(t, got.AppliedConfig.UserSuppliedEnvKeys)
		assert.True(t, got.AppliedConfig.ConfigDelivered)
		again, err := s.SetAgentUserSuppliedEnvKeys(ctx, a.ID, nil)
		require.NoError(t, err)
		assert.False(t, again, "nothing to remove writes nothing")
	})
	t.Run("a missing agent writes nothing", func(t *testing.T) {
		written, err := s.SetAgentUserSuppliedEnvKeys(ctx, uuid.NewString(), []string{"FAKE_KEY"})
		require.NoError(t, err)
		assert.False(t, written)
	})
	t.Run("a config changed after the read is retried", func(t *testing.T) {
		a := create(t, "names-retry")
		calls := 0
		setUserSuppliedEnvKeysHook = func(ctx context.Context, tx *ent.Tx, id uuid.UUID) {
			calls++
			if calls == 1 {
				require.NoError(t, tx.Agent.UpdateOneID(id).SetAppliedConfig(`{"image":"example/image:2","configDelivered":true}`).Exec(ctx))
			}
		}
		defer func() { setUserSuppliedEnvKeysHook = nil }()
		written, err := s.SetAgentUserSuppliedEnvKeys(ctx, a.ID, []string{"FAKE_KEY"})
		require.NoError(t, err)
		assert.True(t, written)
		assert.Equal(t, 2, calls, "the miss is retried once")
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{"FAKE_KEY"}, got.AppliedConfig.UserSuppliedEnvKeys)
		assert.Equal(t, "example/image:1", got.AppliedConfig.Image, "the retry re-read the stored config")
	})
}
