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

package hub

import (
	"context"
	"log/slog"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExplicitDispatchImage(t *testing.T) {
	tests := []struct {
		name string
		ac   *store.AgentAppliedConfig
		want string
	}{
		{name: "nil applied config", ac: nil, want: ""},
		{
			name: "template-derived image only is not explicit",
			ac: &store.AgentAppliedConfig{
				Image:        "template-image:v1",
				InlineConfig: &api.ScionConfig{},
				CreateInputs: &store.AgentCreateInputs{InlineConfig: &api.ScionConfig{}},
			},
			want: "",
		},
		{
			name: "broker-echoed image is not explicit",
			ac: &store.AgentAppliedConfig{
				Image:        "resolved-by-broker:v2",
				CreateInputs: &store.AgentCreateInputs{},
			},
			want: "",
		},
		{
			name: "request image recorded in CreateInputs is explicit",
			ac: &store.AgentAppliedConfig{
				Image:        "user-image:v3",
				InlineConfig: &api.ScionConfig{Image: "user-image:v3"},
				CreateInputs: &store.AgentCreateInputs{InlineConfig: &api.ScionConfig{Image: "user-image:v3"}},
			},
			want: "user-image:v3",
		},
		{
			name: "CreateInputs wins over a live InlineConfig echo",
			ac: &store.AgentAppliedConfig{
				InlineConfig: &api.ScionConfig{Image: "echoed-derived:v1"},
				CreateInputs: &store.AgentCreateInputs{InlineConfig: &api.ScionConfig{}},
			},
			want: "",
		},
		{
			name: "legacy agent without CreateInputs falls back to InlineConfig",
			ac: &store.AgentAppliedConfig{
				Image:        "legacy-user:v1",
				InlineConfig: &api.ScionConfig{Image: "legacy-user:v1"},
			},
			want: "legacy-user:v1",
		},
		{
			name: "legacy agent with only a derived image sends none",
			ac:   &store.AgentAppliedConfig{Image: "legacy-template:v1"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, explicitDispatchImage(tt.ac))
		})
	}
}

// TestDispatch_TemplateImageNotSentAsTopTier pins ptone/scion#1799 end to
// end on the hub: resolveDerivedConfig still records a template's image on
// AppliedConfig.Image (for display), but the create dispatch must not carry
// it as Config.Image, which the broker maps to its top tier (opts.Image).
// The broker resolves the template image from the template itself at the
// template tier, where an explicit profile harness override can beat it.
func TestDispatch_TemplateImageNotSentAsTopTier(t *testing.T) {
	srv, s := testServer(t)
	ctx := t.Context()

	project := &store.Project{
		ID: tid("img-project"), Name: "Img Project", Slug: "img-project",
		OwnerID: "dev@localhost", GitRemote: "https://example.com/img/repo.git",
	}
	require.NoError(t, s.CreateProject(ctx, project))
	template := &store.Template{
		ID: tid("img-template"), Name: "Img Template", Slug: "img-template",
		Harness: "claude", Scope: store.TemplateScopeGlobal, Status: store.TemplateStatusActive,
		ContentHash: "img-tmpl-hash",
		Config:      &store.TemplateConfig{Image: "template-image:v1"},
	}
	require.NoError(t, s.CreateTemplate(ctx, template))

	newAgent := func(id string, reqCfg *api.ScionConfig) *store.Agent {
		ac := &store.AgentAppliedConfig{Workspace: "/tmp/img-ws", CreatorName: "dev@localhost", AgentRole: "baseline"}
		if reqCfg != nil {
			ac.Image = reqCfg.Image
			ac.InlineConfig = reqCfg
		}
		ac.CreateInputs = &store.AgentCreateInputs{InlineConfig: deepCopyScionConfig(reqCfg)}
		return &store.Agent{ID: tid(id), Name: id, Slug: id, ProjectID: project.ID, OwnerID: "dev@localhost", AppliedConfig: ac}
	}

	client := &mockRuntimeBrokerClient{}
	d := NewHTTPAgentDispatcherWithClient(s, client, false, slog.Default())

	// Template image only.
	derived := newAgent("img-derived", nil)
	srv.populateAgentConfig(ctx, derived, project, template)
	require.Equal(t, "template-image:v1", derived.AppliedConfig.Image,
		"AppliedConfig.Image still records the template image for display")
	req, err := d.buildCreateRequest(ctx, derived, "test")
	require.NoError(t, err)
	require.NotNil(t, req.Config)
	assert.Empty(t, req.Config.Image, "a template-derived image must not be dispatched as the top-tier image")
	assert.Equal(t, template.ID, req.Config.TemplateID, "the template itself still reaches the broker")

	// User-specified image beats the template and is sent as the top tier.
	explicit := newAgent("img-explicit", &api.ScionConfig{Image: "user-image:v2"})
	srv.populateAgentConfig(ctx, explicit, project, template)
	require.Equal(t, "user-image:v2", explicit.AppliedConfig.Image)
	req, err = d.buildCreateRequest(ctx, explicit, "test")
	require.NoError(t, err)
	assert.Equal(t, "user-image:v2", req.Config.Image, "a user-specified image stays the top tier")
}

// TestDispatch_StartAndRestartCarryOnlyExplicitImage: start and restart send
// the user's explicit image (registry-rewritten) so it ranks as the top tier
// exactly as on create, and never the broker-echoed AppliedConfig.Image —
// which is what used to freeze a resolved image across restarts.
func TestDispatch_StartAndRestartCarryOnlyExplicitImage(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit image is sent, registry-rewritten", func(t *testing.T) {
		d, client, ag := autoExposeDispatchFixture(t)
		d.SetImageRegistry("us-docker.pkg.dev/p/scion")
		ag.AppliedConfig = &store.AgentAppliedConfig{
			Image:        "us-docker.pkg.dev/p/scion/user-image:v2",
			InlineConfig: &api.ScionConfig{Image: "user-image:v2"},
			CreateInputs: &store.AgentCreateInputs{InlineConfig: &api.ScionConfig{Image: "user-image:v2"}},
		}
		require.NoError(t, d.DispatchAgentStart(ctx, ag, "", false))
		assert.Equal(t, "us-docker.pkg.dev/p/scion/user-image:v2", client.lastStartExtras.Image)
		require.NoError(t, d.DispatchAgentRestart(ctx, ag))
		assert.Equal(t, "us-docker.pkg.dev/p/scion/user-image:v2", client.lastRestartExtras.Image)
	})

	t.Run("broker-echoed image is not sent", func(t *testing.T) {
		d, client, ag := autoExposeDispatchFixture(t)
		ag.AppliedConfig = &store.AgentAppliedConfig{CreateInputs: &store.AgentCreateInputs{}}
		applyBrokerAgentConfig(ag, &RemoteAgentInfo{Image: "template-image:v1"})
		require.Equal(t, "template-image:v1", ag.AppliedConfig.Image, "the echo is still recorded for display")

		require.NoError(t, d.DispatchAgentStart(ctx, ag, "", false))
		assert.Empty(t, client.lastStartExtras.Image)
		require.NoError(t, d.DispatchAgentRestart(ctx, ag))
		assert.Empty(t, client.lastRestartExtras.Image)
		req, err := d.buildCreateRequest(ctx, ag, "test")
		require.NoError(t, err)
		assert.Empty(t, req.Config.Image, "a re-dispatch (finalize-env / reprovision) must not freeze the echo either")
	})
}

func TestApplyStartExtras_Image(t *testing.T) {
	payload := map[string]interface{}{}
	applyStartExtras(payload, StartExtras{})
	_, ok := payload["image"]
	assert.False(t, ok, "no image key when the user chose none")

	applyStartExtras(payload, StartExtras{Image: "user-image:v2"})
	assert.Equal(t, "user-image:v2", payload["image"])
}
