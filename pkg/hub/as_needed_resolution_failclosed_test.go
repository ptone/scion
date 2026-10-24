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
	return secret.NewGCPBackendWithClient(s, deniedSMClient{}, "fake-sm-project", "test-hub-failclosed")
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
		{"provision", func(ctx context.Context, f *runDispatchFixture) error {
			return f.d.DispatchAgentProvision(ctx, f.agent)
		}},
	}
	causes := []struct {
		name string
		err  error
	}{
		{"access", &secret.ResolveAccessError{Key: failClosedKey, Scope: "project", Kind: secret.FailurePermissionDenied,
			Err: status.Error(codes.PermissionDenied, failClosedSMError+" "+failClosedSMPath)}},
		{"decrypt", &secret.ResolveDecryptError{Failures: []secret.ResolveFailure{{Key: failClosedKey, Scope: "project", Kind: secret.FailureDecrypt}}}},
		{"store", errFakeListSecrets},
	}
	for _, dc := range dispatches {
		for _, cc := range causes {
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
	for _, tc := range runDispatches[:5] {
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
