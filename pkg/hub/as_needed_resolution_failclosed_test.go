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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	smpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Secret resolution fails closed on every dispatch path (ptone/scion#5959):
// a GCP Secret Manager access error, a decrypt error or a store error in the
// as-needed pass fails the dispatch with the fixed *secretResolutionError
// message instead of answering the broker's needs as if the secrets were
// missing, and a start whose secrets cannot be read is not sent.

const (
	failClosedKey     = "FAKE_FAILCLOSED_KEY"
	failClosedValue   = "FAKE-failclosed-value"
	failClosedSMPath  = "projects/fake-sm-project/secrets/scion-fake-FAKE_FAILCLOSED_KEY"
	failClosedSMError = "FAKE-sm-permission-detail"
)

// deniedSMClient answers every AccessSecretVersion with PermissionDenied
// whose message carries the resource path and a fake backend detail.
type deniedSMClient struct{ failingSMClient }

func (deniedSMClient) AccessSecretVersion(_ context.Context, req *smpb.AccessSecretVersionRequest) (*smpb.AccessSecretVersionResponse, error) {
	return nil, status.Errorf(codes.PermissionDenied, "%s: %s", failClosedSMError, req.Name)
}

// newDeniedGCPBackend returns a real GCPBackend over deniedSMClient, with
// one as-needed environment secret recorded in s for project-1 whose stored
// reference points at failClosedSMPath.
func newDeniedGCPBackend(t *testing.T, s store.Store, projectID string) *secret.GCPBackend {
	t.Helper()
	require.NoError(t, s.CreateSecret(context.Background(), &store.Secret{
		ID: tid("failclosed-gcp-" + projectID), Key: failClosedKey, Target: failClosedKey,
		SecretType: store.SecretTypeEnvironment, InjectionMode: store.InjectionModeAsNeeded,
		Scope: store.ScopeProject, ScopeID: projectID, SecretRef: "gcpsm:" + failClosedSMPath,
	}))
	return secret.NewGCPBackendWithClient(s, &deniedSMClient{}, "fake-sm-project", "test-hub-failclosed")
}

// assertNoFailClosedDetail checks that text carries no secret key, value,
// Secret Manager path or backend message.
func assertNoFailClosedDetail(t *testing.T, text, what string) {
	t.Helper()
	for _, f := range []string{failClosedKey, failClosedValue, failClosedSMPath, failClosedSMError, "projects/", fakeResolveBackendError} {
		assert.NotContains(t, text, f, "%s must not contain %q", what, f)
	}
}

// assertFixedCreate503 writes err as the handlers do and checks the 503
// body carries only the fixed message.
func assertFixedCreate503(t *testing.T, err error) {
	t.Helper()
	rec := httptest.NewRecorder()
	require.True(t, writeSecretResolutionError(rec, err), "err must be a *secretResolutionError: %v", err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, ErrCodeUnavailable, body.Error.Code)
	assert.Equal(t, fixedCreateSecretMessage, body.Error.Message)
	assertNoFailClosedDetail(t, rec.Body.String(), "the API body")
}

// failListSecretsStore is a store whose ListSecrets fails.
type failListSecretsStore struct{ store.Store }

var errFakeListSecrets = errors.New(fakeResolveBackendError + ": list secrets")

func (failListSecretsStore) ListSecrets(context.Context, store.SecretFilter) ([]store.Secret, error) {
	return nil, errFakeListSecrets
}

func TestResolveAsNeededForKeys_FailsClosed(t *testing.T) {
	agent := &store.Agent{ID: "agent-failclosed", Name: "failclosed", OwnerID: "user-1",
		ProjectID: "project-1", AppliedConfig: &store.AgentAppliedConfig{}}

	cases := []struct {
		name    string
		backend func(t *testing.T, s store.Store) secret.SecretBackend
		is      error
	}{
		{"gcp access denied", func(t *testing.T, s store.Store) secret.SecretBackend {
			return newDeniedGCPBackend(t, s, "project-1")
		}, secret.ErrResolveAccess},
		{"undecryptable stored value", func(t *testing.T, s store.Store) secret.SecretBackend {
			foreign, err := secret.NewLegacyValueCipher("FAKE-foreign-secret").Encrypt(failClosedValue)
			require.NoError(t, err)
			require.NoError(t, s.CreateSecret(context.Background(), &store.Secret{ID: tid("failclosed-undecryptable"),
				Key: failClosedKey, Target: failClosedKey, EncryptedValue: foreign,
				SecretType: store.SecretTypeEnvironment, InjectionMode: store.InjectionModeAsNeeded,
				Scope: store.ScopeProject, ScopeID: "project-1"}))
			return secret.NewLocalBackend(s, "test-hub-failclosed", "FAKE-hub-secret")
		}, secret.ErrResolveDecrypt},
		{"store ListSecrets error", func(t *testing.T, s store.Store) secret.SecretBackend {
			return secret.NewLocalBackend(failListSecretsStore{s}, "test-hub-failclosed", "FAKE-hub-secret")
		}, errFakeListSecrets},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := createTestStore(t)
			d := NewHTTPAgentDispatcherWithClient(s, &mockRuntimeBrokerClient{}, false, slog.Default())
			d.SetHubID("test-hub-failclosed")
			d.SetSecretBackend(tc.backend(t, s))

			got, err := d.resolveAsNeededForKeys(context.Background(), agent, []string{failClosedKey}, nil)
			require.Error(t, err)
			assert.Nil(t, got, "a failed pass returns no values")
			assert.ErrorIs(t, err, tc.is, "the typed cause stays reachable")
			var resErr *secretResolutionError
			require.ErrorAs(t, err, &resErr)
			assert.Equal(t, "created", resErr.Verb)
			assert.Equal(t, fixedCreateSecretMessage, err.Error())
			assertNoFailClosedDetail(t, err.Error(), "the error text")
			assertFixedCreate503(t, err)
		})
	}
}

// nthResolveFailingBackend resolves no secrets on its first Resolve call
// and fails every later one with err, so a dispatch's first pass succeeds
// and only its as-needed pass meets the error.
type nthResolveFailingBackend struct {
	mockSecretBackend
	err   error
	mu    sync.Mutex
	calls int
}

func (b *nthResolveFailingBackend) Resolve(context.Context, string, string, string, *secret.ResolveOpts) ([]secret.SecretWithValue, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.calls == 1 {
		return nil, nil
	}
	return nil, b.err
}

// asNeededCauses are the resolution errors the as-needed pass meets: a
// backend access error, an undecryptable value and a store error.
var asNeededCauses = []struct {
	name string
	err  error
}{
	{"access", &secret.ResolveAccessError{Key: failClosedKey, Scope: "project", Kind: secret.FailurePermissionDenied,
		Err: status.Error(codes.PermissionDenied, failClosedSMError+" "+failClosedSMPath)}},
	{"decrypt", &secret.ResolveDecryptError{Failures: []secret.ResolveFailure{{Key: failClosedKey, Scope: "project", Kind: secret.FailureDecrypt}}}},
	{"store", errFakeListSecrets},
}

// A gather dispatch whose as-needed pass fails does not replay the create:
// the broker is called once (the needs report) and never sent a request
// built from a partial secret set.
func TestGatherDispatch_AsNeededResolutionErrorFailsClosed(t *testing.T) {
	dispatches := []struct {
		name     string
		dispatch func(ctx context.Context, f *runDispatchFixture) error
	}{
		{"create with env gather", func(ctx context.Context, f *runDispatchFixture) error {
			_, err := f.d.DispatchAgentCreateWithGather(ctx, f.agent)
			return err
		}},
		{"finalize env", func(ctx context.Context, f *runDispatchFixture) error {
			_, err := f.d.DispatchFinalizeEnv(ctx, f.agent, nil)
			return err
		}},
	}
	for _, dc := range dispatches {
		for _, cc := range asNeededCauses {
			t.Run(dc.name+"/"+cc.name, func(t *testing.T) {
				f := newRunDispatchFixture(t)
				f.d.SetSecretBackend(&nthResolveFailingBackend{err: cc.err})
				brokerCalls := 0
				f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
					brokerCalls++
					return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{failClosedKey}}, nil
				}

				err := dc.dispatch(context.Background(), f)
				require.Error(t, err)
				assert.ErrorIs(t, err, cc.err)
				var resErr *secretResolutionError
				require.ErrorAs(t, err, &resErr)
				assertNoFailClosedDetail(t, err.Error(), "the error text")
				assertFixedCreate503(t, err)
				assert.Equal(t, 1, brokerCalls, "no replay is sent after the as-needed pass fails")
			})
		}
	}
}

// A start, restart or create whose GCP-backed secrets cannot be read fails
// with the fixed message wrapping ErrResolveAccess and sends nothing to the
// broker: the agent never starts with a partial secret set.
func TestDispatch_GCPAccessErrorFailsClosed(t *testing.T) {
	for _, tc := range runDispatches {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunDispatchFixture(t)
			f.d.SetSecretBackend(newDeniedGCPBackend(t, f.store, f.agent.ProjectID))

			err := tc.dispatch(context.Background(), f)
			require.Error(t, err)
			assert.ErrorIs(t, err, secret.ErrResolveAccess)
			var resErr *secretResolutionError
			require.ErrorAs(t, err, &resErr)
			assertNoFailClosedDetail(t, err.Error(), "the error text")
			assert.True(t, isConfirmedStartNotActedOnError(err))
			_, _, called := tc.sent(f)
			assert.False(t, called, "nothing is sent to the broker")
		})
	}
}

// gatherNeedsClient makes f's broker report failClosedKey as needed on a
// create-with-gather call whose request does not carry it, and accept the
// call (a provisioned agent) when it does. It returns the call counter.
func gatherNeedsClient(f *runDispatchFixture) *int {
	calls := 0
	f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
		calls++
		if req.ResolvedEnv[failClosedKey] != "" {
			return &RemoteAgentResponse{Agent: &RemoteAgentInfo{ID: req.ID, Slug: req.Slug, Name: req.Name},
				Created: true, Reprovisioned: req.Reprovision}, nil, nil
		}
		return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{failClosedKey}}, nil
	}
	return &calls
}

// assertStartsFailClosed checks that a start and a restart of f's agent
// fail on cause with the fixed message and send nothing to the broker.
func assertStartsFailClosed(t *testing.T, f *runDispatchFixture, cause error) {
	t.Helper()
	for _, tc := range runDispatches {
		if tc.name != "start" && tc.name != "restart" {
			continue
		}
		err := tc.dispatch(context.Background(), f)
		require.Error(t, err, tc.name)
		assert.ErrorIs(t, err, cause, tc.name)
		var resErr *secretResolutionError
		require.ErrorAs(t, err, &resErr, tc.name)
		assertNoFailClosedDetail(t, err.Error(), tc.name+" error text")
		_, _, called := tc.sent(f)
		assert.False(t, called, "%s sends nothing to the broker", tc.name)
	}
}

// switchableBackend resolves one as-needed environment secret for
// failClosedKey, or fails every Resolve with err while failing is set.
type switchableBackend struct {
	mockSecretBackend
	err     error
	failing bool
}

func (b *switchableBackend) Resolve(context.Context, string, string, string, *secret.ResolveOpts) ([]secret.SecretWithValue, error) {
	if b.failing {
		return nil, b.err
	}
	return []secret.SecretWithValue{{SecretMeta: secret.SecretMeta{Name: failClosedKey, Target: failClosedKey,
		SecretType: store.SecretTypeEnvironment, InjectionMode: store.InjectionModeAsNeeded, Scope: secret.ScopeProject},
		Value: failClosedValue}}, nil
}

// A start does not resolve as-needed keys, so provision and reprovision
// return the fixed message when their as-needed pass meets a resolution
// error, and send no replay: no provisioned config is left without the
// values the broker asked for. Start and restart still return the fixed
// message while the error persists.
func TestProvisionGather_AsNeededResolutionErrorFails(t *testing.T) {
	for _, cc := range asNeededCauses {
		t.Run("provision/"+cc.name, func(t *testing.T) {
			f := newRunDispatchFixture(t)
			f.d.SetSecretBackend(&nthResolveFailingBackend{err: cc.err})
			calls := gatherNeedsClient(f)

			err := f.d.DispatchAgentProvision(context.Background(), f.agent)
			require.Error(t, err)
			assert.ErrorIs(t, err, cc.err)
			assert.Equal(t, fixedCreateSecretMessage, err.Error())
			assertFixedCreate503(t, err)
			assert.Equal(t, 1, *calls, "no replay is sent")
			assertStartsFailClosed(t, f, cc.err)
		})
		t.Run("reprovision/"+cc.name, func(t *testing.T) {
			f := newRunDispatchFixture(t)
			f.d.SetSecretBackend(&nthResolveFailingBackend{err: cc.err})
			calls := gatherNeedsClient(f)

			err := f.d.DispatchAgentReprovision(context.Background(), f.agent)
			require.Error(t, err)
			assert.ErrorIs(t, err, cc.err)
			var resErr *secretResolutionError
			require.ErrorAs(t, err, &resErr)
			assert.Equal(t, "agent secrets could not be resolved; the agent was not reprovisioned", err.Error())
			assertNoFailClosedDetail(t, err.Error(), "the error text")
			assert.Equal(t, 1, *calls, "no replay is sent")
		})
	}

	// The same GCP access error end to end.
	t.Run("provision, real GCP backend", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.d.SetSecretBackend(newDeniedGCPBackend(t, f.store, f.agent.ProjectID))
		calls := gatherNeedsClient(f)

		err := f.d.DispatchAgentProvision(context.Background(), f.agent)
		require.Error(t, err)
		assert.ErrorIs(t, err, secret.ErrResolveAccess)
		assert.Equal(t, 1, *calls)
		assertStartsFailClosed(t, f, secret.ErrResolveAccess)
	})
}

// A failed provision is retried once the backend is healthy again: the
// retry's replay carries the as-needed value, so the provisioned config
// has it.
func TestProvisionGather_HealedBackendCarriesAsNeededValues(t *testing.T) {
	for _, reprovision := range []bool{false, true} {
		t.Run(fmt.Sprintf("reprovision=%v", reprovision), func(t *testing.T) {
			f := newRunDispatchFixture(t)
			b := &switchableBackend{err: asNeededCauses[0].err, failing: true}
			f.d.SetSecretBackend(b)
			calls := gatherNeedsClient(f)
			dispatch := func() error {
				if reprovision {
					return f.d.DispatchAgentReprovision(context.Background(), f.agent)
				}
				return f.d.DispatchAgentProvision(context.Background(), f.agent)
			}

			require.Error(t, dispatch(), "the first attempt meets the error")
			assert.Equal(t, 1, *calls)

			b.failing = false
			require.NoError(t, dispatch(), "the retry succeeds")
			assert.Equal(t, 3, *calls, "the retry sends the needs report and one replay")
			require.NotNil(t, f.client.lastCreateReq)
			assert.Equal(t, failClosedValue, f.client.lastCreateReq.ResolvedEnv[failClosedKey], "the replay carries the as-needed value")
		})
	}
}

// A reincarnation whose reprovision leg returns the as-needed resolution
// error fails before any start: the previous config is re-rendered and
// restored on the row, and the recorded error and agent message carry the
// fixed message only.
func TestReincarnateAgent_AsNeededResolutionErrorRestoresPrevious(t *testing.T) {
	disp := newReincarnateTestDispatcher()
	disp.reprovisionErr = &secretResolutionError{Verb: "reprovisioned", Err: asNeededCauses[0].err}
	srv, s, project, broker := setupReincarnateTestServer(t, disp)
	agent := newReincarnateTestAgent(t, s, project, broker, nil)
	self := agentIdentityFor(agent.ID, project.ID)

	rec := httptest.NewRecorder()
	srv.handleReincarnateAgent(rec, reincarnateRequest(t, agent.ID, self, ReincarnateAgentRequest{Handoff: "h"}), agent.ID)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	r := waitForReincarnationSettled(t, s, agent.ID)
	require.Equal(t, store.AgentReincarnationStateFailed, r.State)
	assert.Contains(t, r.Error, "reprovision failed: agent secrets could not be resolved; the agent was not reprovisioned")
	assertNoFailClosedDetail(t, r.Error, "the reincarnation error")

	calls, cfgs := disp.reprovisionSnapshot()
	require.Equal(t, 2, calls, "one reprovision, one re-render of the previous config")
	require.NotNil(t, r.PreviousAppliedConfig)
	assert.Equal(t, *reincarnationRecoveryConfig(r.PreviousAppliedConfig, r.ToGeneration, r.Handoff, store.AgentReincarnationStateProvisioning), cfgs[1])
	assert.Zero(t, disp.startCalls, "no start runs after the failed reprovision")

	final, err := s.GetAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, final.Generation)
	assert.Equal(t, "old-image:v1", final.AppliedConfig.Image, "the previous config is restored on the row")
	assertNoFailClosedDetail(t, final.Message, "the agent message")
}

// A provision-only create whose provision returns a secret resolution error
// answers the fixed 503, removes the broker files and the agent row like a
// failed skill resolution, and logs at WARN; no row is left to start.
func TestCreateAgent_ProvisionOnlySecretResolutionFailure(t *testing.T) {
	disp := &skillFailDispatcher{provisionErr: fakeSecretResolutionErr("created")}
	srv, s, project := setupCreateAgentServer(t, disp)
	logs := &levelCapturingHandler{}
	srv.agentLifecycleLog = slog.New(logs)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:          "secret-fail-agent",
		ProjectID:     project.ID,
		Task:          "some task",
		ProvisionOnly: true,
	})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, ErrCodeUnavailable, body.Error.Code)
	assert.Equal(t, fixedCreateSecretMessage, body.Error.Message)
	assertNoSecretResolutionDetail(t, rec.Body.String(), "the API body")

	require.NotNil(t, disp.provisionedAgent, "provision must have been dispatched")
	agentID := disp.provisionedAgent.ID
	_, err := s.GetAgent(context.Background(), agentID)
	assert.ErrorIs(t, err, store.ErrNotFound, "the failed agent record is deleted")
	assert.True(t, disp.deleteCalled, "the broker-side files are cleaned up")

	found := false
	logs.mu.Lock()
	for _, r := range logs.records {
		if r.Level == slog.LevelWarn && r.Message == "Provision-only create failed: agent secrets could not be resolved" {
			found = true
		}
	}
	logs.mu.Unlock()
	assert.True(t, found, "the failure is logged at WARN")

	// A start after the failed create finds no agent to start.
	rec = doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agentID+"/start", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
