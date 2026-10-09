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

package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// Instance ownership on the manager (ptone/scion#3274).

func ownedEntries() []api.AgentInfo {
	return []api.AgentInfo{
		{ID: "c-a", ContainerID: "c-a", Name: "worker", Labels: map[string]string{"scion.name": "worker", api.LabelRuntimeBrokerID: "broker-a"}},
		{ID: "c-b", ContainerID: "c-b", Name: "worker", Labels: map[string]string{"scion.name": "worker", api.LabelRuntimeBrokerID: "broker-b"}},
		{ID: "c-legacy", ContainerID: "c-legacy", Name: "worker", Labels: map[string]string{"scion.name": "worker"}},
	}
}

func TestOwner_ListSeesOnlyOwnedRuntimeObjects(t *testing.T) {
	rt := &runtime.MockRuntime{ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) { return ownedEntries(), nil }}
	m := NewManager(rt).(*AgentManager)

	legacy, err := m.listRuntime(context.Background(), map[string]string{"scion.name": "worker"})
	require.NoError(t, err)
	assert.Len(t, legacy, 3, "a legacy manager is unchanged")

	m.SetOwner(OwnerScope{RuntimeBrokerID: "broker-a"})
	owned, err := m.listRuntime(context.Background(), map[string]string{"scion.name": "worker"})
	require.NoError(t, err)
	require.Len(t, owned, 1)
	assert.Equal(t, "c-a", owned[0].ContainerID, "another instance's and unlabeled objects are not owned")
	assert.Equal(t, "broker-a", m.OwnerRuntimeBrokerID())
}

func TestOwner_ListErrorIsNotRetriedUnfiltered(t *testing.T) {
	calls := 0
	rt := &runtime.MockRuntime{ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) {
		calls++
		return nil, errors.New("daemon unavailable")
	}}
	m := NewManager(rt).(*AgentManager)
	m.SetOwner(OwnerScope{RuntimeBrokerID: "broker-a"})
	_, err := m.listRuntime(context.Background(), map[string]string{"scion.name": "worker"})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
}

func TestOwner_StopNeverPassesAnUnresolvedName(t *testing.T) {
	var stopped []string
	rt := &runtime.MockRuntime{
		ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) { return ownedEntries()[1:], nil },
		StopFunc: func(_ context.Context, ref runtime.RunRef) error { stopped = append(stopped, ref.ID); return nil },
	}
	m := NewManager(rt).(*AgentManager)
	m.SetOwner(OwnerScope{RuntimeBrokerID: "broker-a"})

	err := m.Stop(context.Background(), "worker", "", "")
	assert.ErrorIs(t, err, ErrNotOwned)
	assert.Empty(t, stopped, "neither another instance's object nor the bare name is stopped")

	rt.ListFunc = func(context.Context, map[string]string) ([]api.AgentInfo, error) { return nil, errors.New("down") }
	assert.Error(t, m.Stop(context.Background(), "worker", "", ""))
	assert.Empty(t, stopped, "a list failure never falls back to the bare name")
}

func TestOwner_LabelsCarryTheReservedOwner(t *testing.T) {
	m := NewManager(&runtime.MockRuntime{}).(*AgentManager)
	assert.Nil(t, m.ownerLabels())
	m.SetOwner(OwnerScope{RuntimeBrokerID: "broker-a"})
	assert.Equal(t, map[string]string{api.LabelRuntimeBrokerID: "broker-a"}, m.ownerLabels())

	assert.False(t, m.ownsFileAgent("p", "worker"), "no record callback: no file-only agent is owned")
	m.SetOwner(OwnerScope{RuntimeBrokerID: "broker-a", FileAgentOwned: func(p, s string) bool { return p == "p" && s == "worker" }})
	assert.True(t, m.ownsFileAgent("p", "worker"))
	assert.False(t, m.ownsFileAgent("p", "other"))
}
