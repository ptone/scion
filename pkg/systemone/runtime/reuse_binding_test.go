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

package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/spec"
)

type reuseTestAdapter struct {
	name string
}

func (a *reuseTestAdapter) Name() string    { return a.name }
func (a *reuseTestAdapter) Version() string { return "1.0.0-test" }
func (a *reuseTestAdapter) Capabilities(provider.InstanceView) (provider.Capabilities, error) {
	return provider.Capabilities{
		Primitives: map[string]provider.PrimitiveLimits{
			spec.PrimitiveBinary: {Supported: true, DistributionCompleteness: spec.CompletenessComplete},
		},
		MaxQuestionsPerRequest: 4,
		MaxInputTokens:         4096,
		Truncation:             provider.TruncationNever,
		SideEffectFree:         true,
		AuthScheme:             provider.AuthScheme{Kind: provider.AuthKindBearer},
	}, nil
}
func (a *reuseTestAdapter) Validate(provider.InstanceView, provider.RuleSpec) error { return nil }
func (a *reuseTestAdapter) Invoke(context.Context, provider.Transport, provider.InstanceView, provider.ModelRequest) (provider.ModelResult, error) {
	panic("ResolveBindingForReuse must never invoke provider")
}

type countingCreds struct {
	calls atomic.Int32
}

func (c *countingCreds) Resolve(context.Context, runtime.Instance) (string, error) {
	c.calls.Add(1)
	return "sk-must-not-be-read", nil
}

type staticAllowlist map[string][]string

func (a staticAllowlist) AllowedInstances(_ context.Context, projectID string) ([]string, error) {
	return a[projectID], nil
}

func TestResolveBindingForReuse_ParityAndNoSecrets(t *testing.T) {
	reg := provider.NewRegistry()
	reg.MustRegister(&reuseTestAdapter{name: "test-adapter"})

	creds := &countingCreds{}
	instA := runtime.Instance{
		ID:       "id-a",
		Name:     "inst-a",
		Adapter:  "test-adapter",
		Model:    "model-v1",
		Endpoint: "https://example.test",
		Auth:     runtime.InstanceAuth{Kind: "api_key", SecretName: "sec-a"},
		Enabled:  true,
		Revision: 5,
	}
	instDisabled := runtime.Instance{
		ID:       "id-dis",
		Name:     "inst-dis",
		Adapter:  "test-adapter",
		Model:    "model-v1",
		Endpoint: "https://example.test",
		Auth:     runtime.InstanceAuth{Kind: "api_key", SecretName: "sec-dis"},
		Enabled:  false,
		Revision: 1,
	}

	rt := runtime.New(runtime.Config{
		Registry:         reg,
		Instances:        runtime.StaticInstances(instA, instDisabled),
		ProjectAllowlist: staticAllowlist{"proj-1": {"inst-a", "inst-dis"}},
		Credentials:      creds.Resolve,
	})
	t.Cleanup(rt.CloseIdleConnections)

	env := spec.ProbeEnvironment()
	compiledExplicit, err := spec.Compile(
		[]byte(`{"dialect":"system_one","inputs":["probe.text"],"spec":{"version":1,"binding":{"instance":"inst-a"},"questions":[{"id":"q","type":"binary","instructions":"ok?"}]}}`),
		spec.CompileOptions{Kind: spec.KindJudgments, Environment: &env},
	)
	require.NoError(t, err)

	got, ok := rt.ResolveBindingForReuse(context.Background(), compiledExplicit, "proj-1")
	require.True(t, ok)
	assert.Equal(t, runtime.ResolvedBinding{
		InstanceName:     "inst-a",
		InstanceRevision: 5,
		Adapter:          "test-adapter",
		ConfiguredModel:  "model-v1",
		Enabled:          true,
	}, got)

	// Unlisted project fails closed.
	_, ok = rt.ResolveBindingForReuse(context.Background(), compiledExplicit, "proj-other")
	assert.False(t, ok)

	// Empty project fails closed (RequireProject: true).
	_, ok = rt.ResolveBindingForReuse(context.Background(), compiledExplicit, "")
	assert.False(t, ok)

	// Disabled instance fails closed.
	compiledDisabled, err := spec.Compile(
		[]byte(`{"dialect":"system_one","inputs":["probe.text"],"spec":{"version":1,"binding":{"instance":"inst-dis"},"questions":[{"id":"q","type":"binary","instructions":"ok?"}]}}`),
		spec.CompileOptions{Kind: spec.KindJudgments, Environment: &env},
	)
	require.NoError(t, err)
	_, ok = rt.ResolveBindingForReuse(context.Background(), compiledDisabled, "proj-1")
	assert.False(t, ok)

	// Implicit selection picks single enabled allowlisted instance.
	compiledImplicit, err := spec.Compile(
		[]byte(`{"dialect":"system_one","inputs":["probe.text"],"spec":{"version":1,"questions":[{"id":"q","type":"binary","instructions":"ok?"}]}}`),
		spec.CompileOptions{Kind: spec.KindJudgments, Environment: &env},
	)
	require.NoError(t, err)
	gotImp, ok := rt.ResolveBindingForReuse(context.Background(), compiledImplicit, "proj-1")
	require.True(t, ok)
	assert.Equal(t, "inst-a", gotImp.InstanceName)

	// Must never read secrets / mint credentials.
	assert.Zero(t, creds.calls.Load(), "ResolveBindingForReuse must never touch credentials")
}
