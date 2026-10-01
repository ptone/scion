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

// This file covers ptone/scion#1956: the Hub must revoke the agent
// credential it minted for a create or launch dispatch that then fails,
// mirroring the delete and suspend paths (handlers_agents_core.go,
// handlers_agent_lifecycle.go). See agent_credential_revoke.go for the
// shared helper and the DispatchAgentStart LaunchError guard it documents.
package hub

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestBrokerForRevoke(t *testing.T, s store.Store, idName string) *store.RuntimeBroker {
	t.Helper()
	broker := &store.RuntimeBroker{
		ID:       tid(idName),
		Name:     idName,
		Slug:     idName,
		Endpoint: "http://localhost:9800",
		Status:   store.BrokerStatusOnline,
	}
	require.NoError(t, s.CreateRuntimeBroker(context.Background(), broker))
	return broker
}

func TestHTTPAgentDispatcher_DispatchAgentCreate_RevokesCredentialOnFailure(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-create-fail-host")

	mockClient := &mockRuntimeBrokerClient{returnErr: errors.New("broker boom")}
	dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	agent := &store.Agent{
		ID:              tid("revoke-create-fail-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-create-fail-project"),
		RuntimeBrokerID: broker.ID,
	}

	err := dispatcher.DispatchAgentCreate(ctx, agent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broker boom", "the original dispatch error must not be masked by the revoke")

	require.Len(t, gen.jtis, 1, "expected exactly one credential to have been minted")
	cred := getTestAgentCredential(t, memStore, gen.lastJTI())
	require.NotNil(t, cred.RevokedAt, "credential must be revoked after a create failure")
	require.NotNil(t, cred.RevokeReason)
	assert.Equal(t, agentCredentialRevokeReasonCreateFailed, *cred.RevokeReason)
}

func TestHTTPAgentDispatcher_DispatchAgentCreate_SuccessDoesNotRevokeCredential(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-create-ok-host")

	mockClient := &mockRuntimeBrokerClient{}
	dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	agent := &store.Agent{
		ID:              tid("revoke-create-ok-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-create-ok-project"),
		RuntimeBrokerID: broker.ID,
	}

	require.NoError(t, dispatcher.DispatchAgentCreate(ctx, agent))

	require.Len(t, gen.jtis, 1)
	cred := getTestAgentCredential(t, memStore, gen.lastJTI())
	assert.Nil(t, cred.RevokedAt, "a successful create must not revoke the credential it minted")
}

func TestHTTPAgentDispatcher_DispatchAgentCreate_RevokeStoreErrorDoesNotMaskDispatchError(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-create-storeerr-host")
	wrapped := &revokeFailingCredentialStore{Store: memStore, failWith: errors.New("revoke store down")}

	mockClient := &mockRuntimeBrokerClient{returnErr: errors.New("broker boom")}
	dispatcher := NewHTTPAgentDispatcherWithClient(wrapped, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	agent := &store.Agent{
		ID:              tid("revoke-create-storeerr-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-create-storeerr-project"),
		RuntimeBrokerID: broker.ID,
	}

	err := dispatcher.DispatchAgentCreate(ctx, agent)
	require.Error(t, err, "a revoke-store failure must not turn a real dispatch failure into success")
	assert.Contains(t, err.Error(), "broker boom", "the original dispatch error must survive a revoke-store error untouched")
}

func TestHTTPAgentDispatcher_DispatchAgentCreateWithGather_RevokesCredentialOnError(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-gather-fail-host")

	mockClient := &mockRuntimeBrokerClient{returnErr: errors.New("broker unavailable")}
	dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	agent := &store.Agent{
		ID:              tid("revoke-gather-fail-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-gather-fail-project"),
		RuntimeBrokerID: broker.ID,
	}

	_, err := dispatcher.DispatchAgentCreateWithGather(ctx, agent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broker unavailable")

	require.Len(t, gen.jtis, 1)
	cred := getTestAgentCredential(t, memStore, gen.lastJTI())
	require.NotNil(t, cred.RevokedAt)
	require.NotNil(t, cred.RevokeReason)
	assert.Equal(t, agentCredentialRevokeReasonCreateFailed, *cred.RevokeReason)
}

// TestHTTPAgentDispatcher_DispatchAgentCreateWithGather_NeedsMissingDoesNotRevokeAtDispatcherLevel
// pins the split documented in DispatchAgentCreateWithGather's defer: a 202
// "needs more env" response is returned as a value, not an error, so the
// dispatcher itself must not revoke — the caller (handlers_agents_core.go)
// decides whether that is a failure and revokes explicitly when it is.
func TestHTTPAgentDispatcher_DispatchAgentCreateWithGather_NeedsMissingDoesNotRevokeAtDispatcherLevel(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-gather-needs-host")

	mockClient := &mockRuntimeBrokerClient{
		createWithGatherFunc: func(ctx context.Context, brokerID, brokerEndpoint string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
			return nil, &RemoteEnvRequirementsResponse{Needs: []string{"SOME_VAR"}}, nil
		},
	}
	dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	agent := &store.Agent{
		ID:              tid("revoke-gather-needs-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-gather-needs-project"),
		RuntimeBrokerID: broker.ID,
	}

	envReqs, err := dispatcher.DispatchAgentCreateWithGather(ctx, agent)
	require.NoError(t, err)
	require.NotNil(t, envReqs)
	require.Len(t, envReqs.Needs, 1)

	require.Len(t, gen.jtis, 1)
	cred := getTestAgentCredential(t, memStore, gen.lastJTI())
	assert.Nil(t, cred.RevokedAt, "the dispatcher must not revoke on a non-error needs-still-missing response")
}

func TestHTTPAgentDispatcher_DispatchAgentProvision_RevokesCredentialOnFailure(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-provision-fail-host")

	mockClient := &mockRuntimeBrokerClient{returnErr: errors.New("provision rejected")}
	dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	agent := &store.Agent{
		ID:              tid("revoke-provision-fail-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-provision-fail-project"),
		RuntimeBrokerID: broker.ID,
	}

	err := dispatcher.DispatchAgentProvision(ctx, agent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision rejected")

	require.Len(t, gen.jtis, 1)
	cred := getTestAgentCredential(t, memStore, gen.lastJTI())
	require.NotNil(t, cred.RevokedAt)
	require.NotNil(t, cred.RevokeReason)
	assert.Equal(t, agentCredentialRevokeReasonCreateFailed, *cred.RevokeReason)
}

func TestHTTPAgentDispatcher_DispatchFinalizeEnv_RevokesCredentialOnStillMissing(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-finalize-fail-host")

	mockClient := &mockRuntimeBrokerClient{
		createWithGatherFunc: func(ctx context.Context, brokerID, brokerEndpoint string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
			return nil, &RemoteEnvRequirementsResponse{Needs: []string{"STILL_MISSING"}}, nil
		},
	}
	dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	agent := &store.Agent{
		ID:              tid("revoke-finalize-fail-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-finalize-fail-project"),
		RuntimeBrokerID: broker.ID,
	}

	err := dispatcher.DispatchFinalizeEnv(ctx, agent, nil)
	require.Error(t, err)
	var stillMissing *ErrEnvStillMissing
	require.ErrorAs(t, err, &stillMissing, "the revoke must not change the error type callers switch on")
	assert.Equal(t, []string{"STILL_MISSING"}, stillMissing.Requirements.Needs)

	require.Len(t, gen.jtis, 1)
	cred := getTestAgentCredential(t, memStore, gen.lastJTI())
	require.NotNil(t, cred.RevokedAt)
	require.NotNil(t, cred.RevokeReason)
	assert.Equal(t, agentCredentialRevokeReasonCreateFailed, *cred.RevokeReason)
}

func TestHTTPAgentDispatcher_DispatchAgentStart_RevokesCredentialOnFailure(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-start-fail-host")

	mockClient := &mockRuntimeBrokerClient{returnErr: errors.New("start rejected")}
	dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	// A stopped agent being resumed: LaunchError is empty (a clean stop, or
	// never set), so the start_failed revoke must fire.
	agent := &store.Agent{
		ID:              tid("revoke-start-fail-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-start-fail-project"),
		RuntimeBrokerID: broker.ID,
	}

	err := dispatcher.DispatchAgentStart(ctx, agent, "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start rejected")

	require.Len(t, gen.jtis, 1)
	cred := getTestAgentCredential(t, memStore, gen.lastJTI())
	require.NotNil(t, cred.RevokedAt)
	require.NotNil(t, cred.RevokeReason)
	assert.Equal(t, agentCredentialRevokeReasonStartFailed, *cred.RevokeReason)
}

func TestHTTPAgentDispatcher_DispatchAgentStart_SuccessDoesNotRevokeCredential(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-start-ok-host")

	mockClient := &mockRuntimeBrokerClient{}
	dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	agent := &store.Agent{
		ID:              tid("revoke-start-ok-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-start-ok-project"),
		RuntimeBrokerID: broker.ID,
	}

	require.NoError(t, dispatcher.DispatchAgentStart(ctx, agent, "", false))

	require.Len(t, gen.jtis, 1)
	cred := getTestAgentCredential(t, memStore, gen.lastJTI())
	assert.Nil(t, cred.RevokedAt)
}

// TestHTTPAgentDispatcher_DispatchAgentStart_UnconfirmedLaunchErrorSkipsRevoke
// covers the unconfirmed-launch case: a resume dispatched at an agent
// whose last launch ended on the Hub's own silence timeout (LaunchErrorLaunchTimeout
// / LaunchErrorBrokerLost) must not revoke-by-agent on a subsequent start
// failure, because the broker never confirmed that agent's prior container —
// and its still-valid credential — is actually gone.
func TestHTTPAgentDispatcher_DispatchAgentStart_UnconfirmedLaunchErrorSkipsRevoke(t *testing.T) {
	for _, launchError := range []string{store.LaunchErrorLaunchTimeout, store.LaunchErrorBrokerLost} {
		t.Run(launchError, func(t *testing.T) {
			ctx := context.Background()
			memStore := createTestStore(t)
			broker := newTestBrokerForRevoke(t, memStore, "revoke-start-unconfirmed-host-"+launchError)

			mockClient := &mockRuntimeBrokerClient{returnErr: errors.New("start rejected")}
			dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
			gen := &fakeMintingTokenGenerator{store: memStore}
			dispatcher.SetTokenGenerator(gen)

			agent := &store.Agent{
				ID:              tid("revoke-start-unconfirmed-agent-" + launchError),
				Name:            "test-agent",
				Slug:            "test-agent",
				ProjectID:       tid("revoke-start-unconfirmed-project-" + launchError),
				RuntimeBrokerID: broker.ID,
				LaunchError:     launchError,
			}

			err := dispatcher.DispatchAgentStart(ctx, agent, "", true)
			require.Error(t, err)

			require.Len(t, gen.jtis, 1)
			cred := getTestAgentCredential(t, memStore, gen.lastJTI())
			assert.Nil(t, cred.RevokedAt, "must not revoke when the prior launch end was Hub-declared, not broker-confirmed")
		})
	}
}

// TestHTTPAgentDispatcher_DispatchAgentStart_ConfirmedLaunchErrorStillRevokes
// is the complement: a broker-confirmed prior failure (e.g. the agent
// process itself crashed) must still revoke on a subsequent start failure.
func TestHTTPAgentDispatcher_DispatchAgentStart_ConfirmedLaunchErrorStillRevokes(t *testing.T) {
	ctx := context.Background()
	memStore := createTestStore(t)
	broker := newTestBrokerForRevoke(t, memStore, "revoke-start-confirmed-host")

	mockClient := &mockRuntimeBrokerClient{returnErr: errors.New("start rejected")}
	dispatcher := NewHTTPAgentDispatcherWithClient(memStore, mockClient, false, slog.Default())
	gen := &fakeMintingTokenGenerator{store: memStore}
	dispatcher.SetTokenGenerator(gen)

	agent := &store.Agent{
		ID:              tid("revoke-start-confirmed-agent"),
		Name:            "test-agent",
		Slug:            "test-agent",
		ProjectID:       tid("revoke-start-confirmed-project"),
		RuntimeBrokerID: broker.ID,
		LaunchError:     store.LaunchErrorAgentError,
	}

	err := dispatcher.DispatchAgentStart(ctx, agent, "", true)
	require.Error(t, err)

	require.Len(t, gen.jtis, 1)
	cred := getTestAgentCredential(t, memStore, gen.lastJTI())
	require.NotNil(t, cred.RevokedAt, "a broker-confirmed prior failure must still revoke on a start failure")
	assert.Equal(t, agentCredentialRevokeReasonStartFailed, *cred.RevokeReason)
}
