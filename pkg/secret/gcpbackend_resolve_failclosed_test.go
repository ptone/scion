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

package secret

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// GCPBackend.Resolve fails closed (ptone/scion#5959): any per-secret Secret
// Manager access error fails the whole resolution with ErrResolveAccess,
// naming the secret's scope and key and never a value or backend detail.

// failAccessSMClient wraps mockSMClient and answers AccessSecretVersion with
// errFn's error for every resource name under failPrefix. It counts the
// calls it failed.
type failAccessSMClient struct {
	*mockSMClient
	failPrefix string
	errFn      func(ctx context.Context) error

	mu    sync.Mutex
	calls int
}

func (c *failAccessSMClient) AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	if c.failPrefix != "" && strings.HasPrefix(req.Name, c.failPrefix) {
		c.mu.Lock()
		c.calls++
		c.mu.Unlock()
		return nil, c.errFn(ctx)
	}
	return c.mockSMClient.AccessSecretVersion(ctx, req)
}

func (c *failAccessSMClient) failedCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

const fakeSMDetail = "FAKE-sm-detail"

// newFailClosedBackend returns a GCPBackend over a failAccessSMClient. The
// client fails nothing until failPrefix is set.
func newFailClosedBackend(t *testing.T) (*GCPBackend, *failAccessSMClient) {
	t.Helper()
	s, err := newTestStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate test store: %v", err)
	}
	fake := &failAccessSMClient{mockSMClient: newMockSMClient()}
	return NewGCPBackendWithClient(s, fake, "test-project", "test-hub-id"), fake
}

func smFullName(b *GCPBackend, key, scope, scopeID string) string {
	return fmt.Sprintf("projects/%s/secrets/%s", b.projectID, b.gcpSecretName(key, scope, scopeID))
}

func mustSet(t *testing.T, b *GCPBackend, in *SetSecretInput) {
	t.Helper()
	if in.SecretType == "" {
		in.SecretType = TypeEnvironment
	}
	if _, _, err := b.Set(context.Background(), in); err != nil {
		t.Fatalf("Set(%s) failed: %v", in.Name, err)
	}
}

// assertAccessFailure checks err is a *ResolveAccessError for scope/key with
// the given kind, that resolved is empty, and that err.Error() carries none
// of the forbidden strings (values, backend message, resource paths).
func assertAccessFailure(t *testing.T, resolved []SecretWithValue, err error, scope, key, kind string, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Resolve succeeded with %d secrets; want ErrResolveAccess", len(resolved))
	}
	if resolved != nil {
		t.Fatalf("a failed Resolve returned %d secrets", len(resolved))
	}
	if !errors.Is(err, ErrResolveAccess) {
		t.Fatalf("err = %v, want ErrResolveAccess", err)
	}
	var rae *ResolveAccessError
	if !errors.As(err, &rae) {
		t.Fatalf("err = %T, want *ResolveAccessError", err)
	}
	if rae.Key != key || rae.Scope != scope || rae.Kind != kind {
		t.Fatalf("ResolveAccessError = {Key:%q Scope:%q Kind:%q}, want {%q %q %q}", rae.Key, rae.Scope, rae.Kind, key, scope, kind)
	}
	msg := err.Error()
	if !strings.Contains(msg, scope+"/"+key) {
		t.Fatalf("Error() %q does not name %s/%s", msg, scope, key)
	}
	for _, f := range append(forbidden, fakeSMDetail, "projects/") {
		if strings.Contains(msg, f) {
			t.Fatalf("Error() %q leaks %q", msg, f)
		}
	}
}

func TestGCPBackend_Resolve_FailsClosedOnAccessError(t *testing.T) {
	for _, tc := range []struct {
		name string
		code codes.Code
		kind string
	}{
		{"permission denied", codes.PermissionDenied, FailurePermissionDenied},
		{"unavailable", codes.Unavailable, FailureUnavailable},
		{"deadline exceeded", codes.DeadlineExceeded, FailureDeadlineExceeded},
		{"failed precondition", codes.FailedPrecondition, FailureFailedPrecondition},
		{"internal", codes.Internal, FailureAccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, fake := newFailClosedBackend(t)
			mustSet(t, b, &SetSecretInput{Name: "OK_KEY", Value: "FAKE-ok-value", Scope: ScopeProject, ScopeID: "project-1"})
			mustSet(t, b, &SetSecretInput{Name: "BAD_KEY", Value: "FAKE-bad-value", Scope: ScopeUser, ScopeID: "user-1"})
			fake.failPrefix = smFullName(b, "BAD_KEY", ScopeUser, "user-1")
			fake.errFn = func(context.Context) error { return status.Error(tc.code, fakeSMDetail+" "+fake.failPrefix) }

			resolved, err := b.Resolve(context.Background(), "user-1", "project-1", "", nil)
			assertAccessFailure(t, resolved, err, ScopeUser, "BAD_KEY", tc.kind, "FAKE-ok-value", "FAKE-bad-value")
			if status.Code(err) != tc.code {
				t.Fatalf("status.Code(err) = %v, want %v (cause must be wrapped)", status.Code(err), tc.code)
			}
		})
	}
}

// A DB record whose stored gcpsm: reference points at a Secret Manager
// secret that no longer exists is a dangling reference, not an absence.
func TestGCPBackend_Resolve_DanglingStoredRefFails(t *testing.T) {
	b, fake := newFailClosedBackend(t)
	mustSet(t, b, &SetSecretInput{Name: "OK_KEY", Value: "FAKE-ok-value", Scope: ScopeProject, ScopeID: "project-1"})
	mustSet(t, b, &SetSecretInput{Name: "GONE_KEY", Value: "FAKE-gone-value", Scope: ScopeProject, ScopeID: "project-1"})
	rec, err := b.store.GetSecret(context.Background(), "GONE_KEY", ScopeProject, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rec.SecretRef, "gcpsm:") {
		t.Fatalf("Set did not store a gcpsm: ref: %q", rec.SecretRef)
	}
	fake.mockSMClient.mu.Lock()
	delete(fake.mockSMClient.versions, strings.TrimPrefix(rec.SecretRef, "gcpsm:"))
	fake.mockSMClient.mu.Unlock()

	resolved, err := b.Resolve(context.Background(), "", "project-1", "", nil)
	assertAccessFailure(t, resolved, err, ScopeProject, "GONE_KEY", FailureNotFound, "FAKE-ok-value", "FAKE-gone-value")
}

// A DB record with no stored reference and no Secret Manager copy under
// either the prefixed or the legacy name fails too.
func TestGCPBackend_Resolve_DanglingComputedNameFails(t *testing.T) {
	b, _ := newFailClosedBackend(t)
	if err := b.store.CreateSecret(context.Background(), &store.Secret{
		ID: tid("failclosed-dangling-computed"), Key: "ORPHAN_KEY",
		SecretType: store.SecretTypeEnvironment, Scope: ScopeHub, ScopeID: "test-hub-id",
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := b.Resolve(context.Background(), "", "", "", nil)
	assertAccessFailure(t, resolved, err, ScopeHub, "ORPHAN_KEY", FailureNotFound)
}

func TestGCPBackend_Resolve_ContextCanceledFails(t *testing.T) {
	b, fake := newFailClosedBackend(t)
	mustSet(t, b, &SetSecretInput{Name: "API_KEY", Value: "FAKE-api-value", Scope: ScopeProject, ScopeID: "project-1"})
	// The store honours ctx too, so cancel only once the Secret Manager read
	// starts: this is the read itself failing on the canceled context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.failPrefix = "projects/"
	fake.errFn = func(ctx context.Context) error { cancel(); return ctx.Err() }

	resolved, err := b.Resolve(ctx, "", "project-1", "", nil)
	assertAccessFailure(t, resolved, err, ScopeProject, "API_KEY", FailureCanceled, "FAKE-api-value")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
}

// Fail fast: after the first access error Resolve reads no further secret.
func TestGCPBackend_Resolve_StopsAtFirstAccessError(t *testing.T) {
	b, fake := newFailClosedBackend(t)
	for _, k := range []string{"A_KEY", "B_KEY", "C_KEY"} {
		mustSet(t, b, &SetSecretInput{Name: k, Value: "FAKE-" + k, Scope: ScopeProject, ScopeID: "project-1"})
	}
	fake.failPrefix = "projects/"
	fake.errFn = func(context.Context) error { return status.Error(codes.PermissionDenied, fakeSMDetail) }

	if _, err := b.Resolve(context.Background(), "", "project-1", "", nil); !errors.Is(err, ErrResolveAccess) {
		t.Fatalf("err = %v, want ErrResolveAccess", err)
	}
	if n := fake.failedCalls(); n != 1 {
		t.Fatalf("Resolve made %d failing reads, want 1", n)
	}
}

// Internal secrets are never read, so their Secret Manager state cannot fail
// a resolution.
func TestGCPBackend_Resolve_InternalSecretStillSkipped(t *testing.T) {
	b, fake := newFailClosedBackend(t)
	mustSet(t, b, &SetSecretInput{Name: "OK_KEY", Value: "FAKE-ok-value", Scope: ScopeHub, ScopeID: "test-hub-id"})
	if err := b.store.CreateSecret(context.Background(), &store.Secret{
		ID: tid("failclosed-internal"), Key: "SIGNING_KEY",
		SecretType: store.SecretTypeInternal, Scope: ScopeHub, ScopeID: "test-hub-id",
	}); err != nil {
		t.Fatal(err)
	}
	fake.failPrefix = smFullName(b, "SIGNING_KEY", ScopeHub, "test-hub-id")
	fake.errFn = func(context.Context) error { return status.Error(codes.PermissionDenied, fakeSMDetail) }

	resolved, err := b.Resolve(context.Background(), "", "", "", nil)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "OK_KEY" || resolved[0].Value != "FAKE-ok-value" {
		t.Fatalf("resolved = %+v, want only OK_KEY", resolved)
	}
	if n := fake.failedCalls(); n != 0 {
		t.Fatalf("internal secret was read %d times", n)
	}
}

func TestGCPBackend_Resolve_ProgenyAccessErrorFails(t *testing.T) {
	newProgeny := func(t *testing.T) (*GCPBackend, *failAccessSMClient) {
		b, fake := newFailClosedBackend(t)
		mustSet(t, b, &SetSecretInput{
			Name: "SHARED_KEY", Value: "FAKE-shared-value", Scope: ScopeUser, ScopeID: "alice-123",
			AllowProgeny: true, CreatedBy: "alice-123",
		})
		fake.failPrefix = smFullName(b, "SHARED_KEY", ScopeUser, "alice-123")
		fake.errFn = func(context.Context) error { return status.Error(codes.PermissionDenied, fakeSMDetail) }
		return b, fake
	}

	t.Run("allowed progeny secret unreadable fails", func(t *testing.T) {
		b, _ := newProgeny(t)
		resolved, err := b.Resolve(context.Background(), "", "", "", &ResolveOpts{
			AgentAncestry: []string{"alice-123", "agent-a"},
			AuthzCheck:    func(SecretMeta) bool { return true },
		})
		assertAccessFailure(t, resolved, err, ScopeUser, "SHARED_KEY", FailurePermissionDenied, "FAKE-shared-value")
	})

	t.Run("refused progeny secret is skipped unread", func(t *testing.T) {
		b, fake := newProgeny(t)
		resolved, err := b.Resolve(context.Background(), "", "", "", &ResolveOpts{
			AgentAncestry: []string{"alice-123", "agent-a"},
			AuthzCheck:    func(SecretMeta) bool { return false },
		})
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}
		if len(resolved) != 0 {
			t.Fatalf("resolved = %+v, want none", resolved)
		}
		if n := fake.failedCalls(); n != 0 {
			t.Fatalf("refused progeny secret was read %d times", n)
		}
	})
}
