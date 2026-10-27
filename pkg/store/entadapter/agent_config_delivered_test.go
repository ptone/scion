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

// SetAgentConfigDelivered sets only the configDelivered key of the applied
// config (ptone/scion#6096): every other key, every other column and
// state_version are left alone, it is idempotent, and a write that finds the
// applied config changed since its read is retried, a bounded number of
// times.
func TestAgentStore_SetAgentConfigDelivered(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)
	const target = "kubernetes|context=c|namespace=n"
	create := func(t *testing.T, name string) *store.Agent {
		t.Helper()
		a := makeAgent(projectID, name)
		a.AppliedConfig = &store.AgentAppliedConfig{Image: "example/image:1", RuntimeTarget: target, Task: "fake task"}
		require.NoError(t, s.CreateAgent(ctx, a))
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		return got
	}
	t.Cleanup(func() { setConfigDeliveredHook = nil })

	t.Run("sets only the marker", func(t *testing.T) {
		a := create(t, "delivered-only")
		written, err := s.SetAgentConfigDelivered(ctx, a.ID)
		require.NoError(t, err)
		assert.True(t, written)
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.True(t, got.AppliedConfig.ConfigDelivered)
		assert.Equal(t, target, got.AppliedConfig.RuntimeTarget, "other keys are kept")
		assert.Equal(t, "example/image:1", got.AppliedConfig.Image)
		assert.Equal(t, "fake task", got.AppliedConfig.Task)
		assert.Equal(t, a.StateVersion, got.StateVersion, "state_version is not bumped")

		again, err := s.SetAgentConfigDelivered(ctx, a.ID)
		require.NoError(t, err)
		assert.False(t, again, "an already-set marker writes nothing")
	})
	t.Run("a missing agent writes nothing", func(t *testing.T) {
		written, err := s.SetAgentConfigDelivered(ctx, uuid.NewString())
		require.NoError(t, err)
		assert.False(t, written)
	})
	t.Run("a config changed after the read is retried", func(t *testing.T) {
		a := create(t, "delivered-retry")
		calls := 0
		setConfigDeliveredHook = func(ctx context.Context, tx *ent.Tx, id uuid.UUID) {
			calls++
			if calls == 1 {
				// A change after this attempt's read: its write misses.
				require.NoError(t, tx.Agent.UpdateOneID(id).SetAppliedConfig(`{"image":"example/image:2"}`).Exec(ctx))
			}
		}
		defer func() { setConfigDeliveredHook = nil }()
		written, err := s.SetAgentConfigDelivered(ctx, a.ID)
		require.NoError(t, err)
		assert.True(t, written)
		assert.Equal(t, 2, calls, "the miss is retried once")
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.True(t, got.AppliedConfig.ConfigDelivered)
		assert.Equal(t, target, got.AppliedConfig.RuntimeTarget, "the retry re-read the stored config")
	})
	t.Run("retries are bounded", func(t *testing.T) {
		a := create(t, "delivered-bounded")
		calls := 0
		setConfigDeliveredHook = func(ctx context.Context, tx *ent.Tx, id uuid.UUID) {
			calls++
			require.NoError(t, tx.Agent.UpdateOneID(id).SetAppliedConfig(`{"image":"example/image:3"}`).Exec(ctx))
		}
		defer func() { setConfigDeliveredHook = nil }()
		written, err := s.SetAgentConfigDelivered(ctx, a.ID)
		require.Error(t, err)
		assert.False(t, written)
		assert.Contains(t, err.Error(), "changed concurrently")
		assert.Equal(t, configDeliveredAttempts, calls)
	})
}
