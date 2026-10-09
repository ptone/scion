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

package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/brokerhost"
	"github.com/GoogleCloudPlatform/scion/pkg/brokeridentity"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker"
)

func preflightCandidate(list func(context.Context, map[string]string) ([]api.AgentInfo, error)) brokerhost.Candidate {
	return brokerhost.Candidate{
		Instance: config.V1RuntimeBrokerInstanceConfig{Key: "docker-a", Name: "a", RuntimeTarget: &config.V1RuntimeTargetConfig{Type: "docker"}},
		Identity: &brokeridentity.Identity{InstanceKey: "docker-a", RuntimeBrokerID: "rb-a"},
		Runtime:  &runtime.MockRuntime{ListFunc: list},
	}
}

// TestFlatOwnershipPreflight: unlabeled agent objects on the scope, or a
// scope that cannot be read, refuse activation; objects labelled for this or
// another instance do not.
func TestFlatOwnershipPreflight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	labelled := func(context.Context, map[string]string) ([]api.AgentInfo, error) {
		return []api.AgentInfo{
			{Name: "mine", ContainerID: "cid-m", Labels: map[string]string{api.LabelRuntimeBrokerID: "rb-a",
				"scion.project_id": "proj-1", "agent_id": "agent-m", "scion.name": "mine", api.LabelRunID: "run-m"}},
			{Name: "theirs", Labels: map[string]string{api.LabelRuntimeBrokerID: "rb-b"}},
		}, nil
	}
	require.NoError(t, flatOwnershipPreflight(ctx, preflightCandidate(labelled)))

	unlabeled := func(context.Context, map[string]string) ([]api.AgentInfo, error) {
		return []api.AgentInfo{
			{Name: "old-agent", Labels: map[string]string{"scion.project_id": "proj-1"}},
		}, nil
	}
	err := flatOwnershipPreflight(ctx, preflightCandidate(unlabeled))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proj-1/old-agent")

	failing := func(context.Context, map[string]string) ([]api.AgentInfo, error) {
		return nil, errors.New("permission denied")
	}
	err = flatOwnershipPreflight(ctx, preflightCandidate(failing))
	require.Error(t, err, "a scope that cannot be read refuses (no inference of absence)")
	assert.Contains(t, err.Error(), "cannot read the execution scope")
}

// TestFlatOwnershipPreflight_RefusesBeforeActivation: through the host, an
// unlabeled object refuses the instance in pass 1, with the reason code, and
// the Activator is never called.
func TestFlatOwnershipPreflight_RefusesBeforeActivation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	act := &recordingFlatActivator{}
	h, err := brokerhost.New(brokerhost.Config{
		GlobalDir: t.TempDir(),
		Instances: []config.V1RuntimeBrokerInstanceConfig{{Key: "docker-a", Name: "a", RuntimeTarget: &config.V1RuntimeTargetConfig{Type: "docker"}}},
		Mode:      brokerhost.ModeRemote,
		NewRuntime: func(context.Context, config.V1RuntimeBrokerInstanceConfig) (runtime.Runtime, error) {
			return &runtime.MockRuntime{ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) {
				return []api.AgentInfo{{Name: "legacy-agent"}}, nil
			}}, nil
		},
		ProbeScope: fakeScopeProber("daemon-1"),
		Activator:  act,
		BuildServer: func(brokerhost.InstanceContext) (*runtimebroker.Server, error) {
			t.Fatal("a refused instance is never built")
			return nil, nil
		},
		OwnershipPreflight: flatOwnershipPreflight,
	})
	require.NoError(t, err)
	require.NoError(t, h.Prepare(context.Background()))
	st := h.Status()[0]
	assert.Equal(t, brokerhost.StateRefused, st.State)
	assert.Equal(t, "ownership_unresolved", st.Reason)
	assert.Contains(t, st.Error, "legacy-agent")
	assert.Empty(t, act.activated, "no Hub activation for an instance with unresolved ownership")
}

// TestFlatOwnershipPreflight_ReconstructsOwnObjects: this instance's
// labelled objects with complete metadata re-create their records; an own
// object with incomplete labels is unresolved.
func TestFlatOwnershipPreflight_ReconstructsOwnObjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	complete := api.AgentInfo{Name: "worker", ContainerID: "cid-1", Labels: map[string]string{
		api.LabelRuntimeBrokerID: "rb-a", "scion.project_id": "proj-1", "agent_id": "agent-1", "scion.name": "worker", api.LabelRunID: "run-1"}}
	require.NoError(t, flatOwnershipPreflight(ctx, preflightCandidate(func(context.Context, map[string]string) ([]api.AgentInfo, error) {
		return []api.AgentInfo{complete}, nil
	})))
	dir, err := runtimebroker.DefaultStateDir("rb-a")
	require.NoError(t, err)
	rec, ok, err := runtimebroker.NewOwnershipStore(dir, "rb-a").Get("proj-1", "agent-1")
	require.NoError(t, err)
	require.True(t, ok, "record reconstructed from complete labels")
	assert.True(t, rec.OwnsUID("cid-1"))

	incomplete := api.AgentInfo{Name: "half", ContainerID: "cid-2", Labels: map[string]string{api.LabelRuntimeBrokerID: "rb-a"}}
	err = flatOwnershipPreflight(ctx, preflightCandidate(func(context.Context, map[string]string) ([]api.AgentInfo, error) {
		return []api.AgentInfo{incomplete}, nil
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "half")
}
