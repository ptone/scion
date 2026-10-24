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

// A forced move whose provision on the target returns a secret resolution
// error (after the needs report, so marked sent) rolls back to the source
// like any target provision failure, with the fixed message recorded and
// no backend detail; no agent is left on the target to start.
func TestReincarnateForceMove_SecretResolutionErrorRollsBack(t *testing.T) {
	f := setupForceMoveFixture(t, true, nil)
	prepareForceMoveWorker(t, f)
	f.disp.mu.Lock()
	f.disp.moveProvisionErr = provisionSent(&secretResolutionError{Verb: "created", Err: asNeededCauses[0].err})
	f.disp.mu.Unlock()

	before := seedGenerationOneTask(t, f)

	r, a := startForceMove(t, f)
	assertForceRolledBack(t, f, r, a, "", store.AgentReincarnationStateProvisioning, before.AppliedConfig.Task)
	assert.Contains(t, r.Error, "provision on the target broker failed: agent secrets could not be resolved")
	assertNoFailClosedDetail(t, r.Error, "the reincarnation error")
}

// A reincarnation whose start leg fails after a successful reprovision, and
// whose best-effort re-render of the previous config then returns a secret
// resolution error, keeps the original error and the gen N+1 config the
// disk holds (the re-render's needs report wrote nothing).
func TestReincarnateAgent_RerenderSecretResolutionErrorKeepsOriginalError(t *testing.T) {
	disp := newReincarnateTestDispatcher()
	disp.startErr = definitiveStartErr()
	disp.rerenderErr = &secretResolutionError{Verb: "reprovisioned", Err: asNeededCauses[0].err}
	srv, s, project, broker := setupReincarnateTestServer(t, disp)
	agent := newReincarnateTestAgent(t, s, project, broker, nil)
	self := agentIdentityFor(agent.ID, project.ID)

	rec := httptest.NewRecorder()
	srv.handleReincarnateAgent(rec, reincarnateRequest(t, agent.ID, self, ReincarnateAgentRequest{Handoff: "h"}), agent.ID)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	r := waitForReincarnationSettled(t, s, agent.ID)
	require.Equal(t, store.AgentReincarnationStateFailed, r.State)
	assert.Contains(t, r.Error, "start failed: runtime broker returned error 409")
	assert.NotContains(t, r.Error, "agent secrets could not be resolved", "the re-render failure must not mask the original error")

	calls, _ := disp.reprovisionSnapshot()
	assert.Equal(t, 2, calls, "one reprovision, one re-render attempt")
	final, err := s.GetAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	assert.Contains(t, final.AppliedConfig.Task, "[SCION REINCARNATION]", "the row keeps the gen N+1 config the disk holds")
}

// A provision-only create whose provision fails after it reached the broker
// (an error marked errProvisionSent: the replay with the as-needed values
// failing in transport, or a broker error status) fails the create: no 201,
// the broker files and the agent row are removed, and a start of the agent
// afterwards answers 404 (ptone/scion#6096). An error from before any
// request left the hub still keeps the row with a warning
// (TestCreateAgent_ProvisionOnlyPreSendFailureStaysWarning).
func TestCreateAgent_ProvisionOnlyFailureAfterSendFailsCreate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"replay transport failure", provisionSent(errors.New("dial tcp 10.0.0.9:9800: connection refused")), http.StatusBadGateway},
		// 0: any error status the dispatch error maps to (never a 201).
		{"broker 500 after the needs report", provisionSent(&brokerStatusError{StatusCode: http.StatusInternalServerError, Body: "boom"}), 0},
		{"broker 404 after the needs report", provisionSent(&brokerStatusError{StatusCode: http.StatusNotFound,
			Body: `{"error":{"code":"not_found","message":"Failed to provision agent: template not found"}}`}), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disp := &skillFailDispatcher{provisionErr: tc.err}
			srv, s, project := setupCreateAgentServer(t, disp)
			logs := &levelCapturingHandler{}
			srv.agentLifecycleLog = slog.New(logs)

			rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
				Name:          "sent-fail-agent",
				ProjectID:     project.ID,
				Task:          "some task",
				ProvisionOnly: true,
			})
			if tc.wantStatus != 0 {
				require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			} else {
				require.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
			}

			require.NotNil(t, disp.provisionedAgent, "provision must have been dispatched")
			agentID := disp.provisionedAgent.ID
			_, err := s.GetAgent(context.Background(), agentID)
			assert.ErrorIs(t, err, store.ErrNotFound, "the failed agent record is deleted")
			assert.True(t, disp.deleteCalled, "the broker-side files are cleaned up")

			found := false
			logs.mu.Lock()
			for _, r := range logs.records {
				if r.Level == slog.LevelWarn && r.Message == "Provision-only create failed after the provision reached the broker" {
					found = true
				}
			}
			logs.mu.Unlock()
			assert.True(t, found, "the failure is logged at WARN")

			rec = doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agentID+"/start", nil)
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		})
	}
}

// With the real dispatcher, a replay that fails in transport after the
// broker's needs report returns an error marked errProvisionSent, which the
// provision-only create handler turns into a failed create.
func TestProvisionGather_ReplayTransportFailureIsMarkedSent(t *testing.T) {
	f := newRunDispatchFixture(t)
	f.d.SetSecretBackend(&switchableBackend{})
	calls := 0
	f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
		calls++
		if calls == 1 {
			return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{failClosedKey}}, nil
		}
		return nil, nil, errors.New("dial tcp 10.0.0.9:9800: connection refused")
	}

	err := f.d.DispatchAgentProvision(context.Background(), f.agent)
	require.Error(t, err)
	assert.ErrorIs(t, err, errProvisionSent)
	assert.Equal(t, 2, calls, "the needs report and the failed replay")
}

// failedProvisionOnlyCreate runs a provision-only create whose provision
// returns provisionErr and checks the outcome every post-send failure
// shares: the wanted status and message, the agent row and broker files
// removed, and a start afterwards answering 404 (ptone/scion#6096).
func failedProvisionOnlyCreate(t *testing.T, provisionErr error, wantStatus int, wantCode, wantMessage string) {
	t.Helper()
	disp := &skillFailDispatcher{provisionErr: provisionErr}
	srv, s, project := setupCreateAgentServer(t, disp)
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:          "post-send-fail-agent",
		ProjectID:     project.ID,
		Task:          "some task",
		ProvisionOnly: true,
	})
	require.Equal(t, wantStatus, rec.Code, rec.Body.String())
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, wantCode, body.Error.Code)
	assert.Equal(t, wantMessage, body.Error.Message)
	assert.Empty(t, body.Error.Details, "no key names or other details")
	assert.NotContains(t, rec.Body.String(), failClosedKey)

	require.NotNil(t, disp.provisionedAgent)
	agentID := disp.provisionedAgent.ID
	_, err := s.GetAgent(context.Background(), agentID)
	assert.ErrorIs(t, err, store.ErrNotFound, "the failed agent record is deleted")
	assert.True(t, disp.deleteCalled, "the broker-side files are cleaned up")
	rec = doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agentID+"/start", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// Unmet needs at the end of the provision fail the create with a fixed
// message that names no key, and a cross-node provision (deferred, so no
// as-needed pass ran on any node) fails it with a fixed message too.
func TestCreateAgent_ProvisionOnlyUnmetNeedsAndCrossNodeFailCreate(t *testing.T) {
	t.Run("unmet needs", func(t *testing.T) {
		failedProvisionOnlyCreate(t, provisionSent(fmt.Errorf("DispatchAgentProvision: %w", errProvisionEnvUnmet)),
			http.StatusUnprocessableEntity, ErrCodeMissingEnvVars,
			"Cannot create agent: required environment values are still unavailable after the provision")
	})
	t.Run("cross-node", func(t *testing.T) {
		failedProvisionOnlyCreate(t, fmt.Errorf("DispatchAgentProvision not supported for cross-node broker: %w", ErrLifecycleDeferred),
			http.StatusBadGateway, ErrCodeRuntimeError,
			"Failed to dispatch to runtime broker: a provision-only create is not supported for a broker served by another hub node")
	})
}

// With the real dispatcher, a provision, move provision or reprovision whose
// broker still reports needed keys at the end returns errProvisionEnvUnmet,
// marked errProvisionSent, whether no replay was possible (nothing resolved)
// or the replay was answered with needs again; and a cross-node provision
// returns the deferral unmarked.
func TestProvisionGather_UnmetNeedsAndCrossNode(t *testing.T) {
	for _, reprovision := range []bool{false, true} {
		dispatch := func(f *runDispatchFixture) error {
			if reprovision {
				return f.d.DispatchAgentReprovision(context.Background(), f.agent)
			}
			return f.d.DispatchAgentProvision(context.Background(), f.agent)
		}
		t.Run(fmt.Sprintf("reprovision=%v/nothing resolved, no replay", reprovision), func(t *testing.T) {
			f := newRunDispatchFixture(t)
			f.d.SetSecretBackend(&switchableBackend{})
			calls := 0
			f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
				calls++
				return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{"FAKE_UNRESOLVABLE_KEY"}}, nil
			}
			err := dispatch(f)
			require.Error(t, err)
			assert.ErrorIs(t, err, errProvisionEnvUnmet)
			assert.ErrorIs(t, err, errProvisionSent)
			assert.NotContains(t, err.Error(), "FAKE_UNRESOLVABLE_KEY", "the error names no key")
			assert.Equal(t, 1, calls)
		})
		t.Run(fmt.Sprintf("reprovision=%v/replay answered with needs again", reprovision), func(t *testing.T) {
			f := newRunDispatchFixture(t)
			f.d.SetSecretBackend(&switchableBackend{})
			calls := 0
			f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
				calls++
				return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{failClosedKey}}, nil
			}
			err := dispatch(f)
			require.Error(t, err)
			assert.ErrorIs(t, err, errProvisionEnvUnmet)
			assert.ErrorIs(t, err, errProvisionSent)
			assert.Equal(t, 2, calls, "the needs report and one replay")
		})
	}
	t.Run("cross-node provision", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.d.SetSecretBackend(&switchableBackend{})
		f.client.createWithGatherFunc = func(context.Context, string, string, *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
			return nil, nil, ErrLifecycleDeferred
		}
		err := f.d.DispatchAgentProvision(context.Background(), f.agent)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrLifecycleDeferred)
		assert.False(t, errors.Is(err, errProvisionSent), "a deferral sends nothing")
	})
}

// A forced move whose target provision ends with unmet needs rolls back to
// the source instead of starting on the target (ptone/scion#6096).
func TestReincarnateForceMove_UnmetNeedsRollsBack(t *testing.T) {
	f := setupForceMoveFixture(t, true, nil)
	prepareForceMoveWorker(t, f)
	f.disp.mu.Lock()
	f.disp.moveProvisionErr = provisionSent(fmt.Errorf("DispatchAgentProvisionForMove: %w", errProvisionEnvUnmet))
	f.disp.mu.Unlock()

	before := seedGenerationOneTask(t, f)

	r, a := startForceMove(t, f)
	assertForceRolledBack(t, f, r, a, "", store.AgentReincarnationStateProvisioning, before.AppliedConfig.Task)
	assert.Contains(t, r.Error, "provision on the target broker failed: DispatchAgentProvisionForMove: required environment values are still unavailable after the provision")
	assert.Zero(t, f.disp.startCalls, "no start runs on the target")
}
