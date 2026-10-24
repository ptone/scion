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
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/secret/secretenvelope"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDecryptFailureKind(t *testing.T) {
	for want, err := range map[string]error{
		FailureUnknownVersion:       secretenvelope.ErrUnknownVersion,
		FailureMalformed:            fmt.Errorf("wrapped: %w", secretenvelope.ErrMalformed),
		FailureMissingKeyID:         secretenvelope.ErrMissingKeyID,
		FailureUnknownKeyID:         ErrUnknownKeyID,
		FailureDecrypt:              fmt.Errorf("decrypting secret value: %w", ErrDecrypt),
		FailurePlaintextNotAdmitted: ErrPlaintextNotAdmitted,
		FailureNoDecryptionKey:      ErrNoDecryptionKey,
		FailureUnreadable:           errors.New("something else"),
	} {
		if got := DecryptFailureKind(err); got != want {
			t.Errorf("DecryptFailureKind(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestResolveDecryptError_SortedAndTyped(t *testing.T) {
	err := newResolveDecryptError([]ResolveFailure{
		{Key: "B", Scope: "user", Kind: FailureDecrypt},
		{Key: "A", Scope: "user", Kind: FailureUnknownKeyID},
		{Key: "Z", Scope: "hub", Kind: FailureMalformed},
	})
	want := ErrResolveDecrypt.Error() + ": hub/Z (malformed), user/A (unknown_key_id), user/B (decrypt_failed)"
	if err.Error() != want {
		t.Fatalf("Error() = %q\nwant %q", err.Error(), want)
	}
	if !errors.Is(err, ErrResolveDecrypt) || errors.Is(err, ErrDecrypt) {
		t.Fatal("ResolveDecryptError must match ErrResolveDecrypt only")
	}
}

func TestAccessFailureKind(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{status.Error(codes.NotFound, "x"), FailureNotFound},
		{status.Error(codes.PermissionDenied, "x"), FailurePermissionDenied},
		{status.Error(codes.Unavailable, "x"), FailureUnavailable},
		{status.Error(codes.FailedPrecondition, "x"), FailureFailedPrecondition},
		{status.Error(codes.DeadlineExceeded, "x"), FailureDeadlineExceeded},
		{status.Error(codes.Canceled, "x"), FailureCanceled},
		{context.DeadlineExceeded, FailureDeadlineExceeded},
		{fmt.Errorf("wrapped: %w", context.Canceled), FailureCanceled},
		{status.Error(codes.Internal, "x"), FailureAccess},
		{errors.New("something else"), FailureAccess},
	} {
		if got := AccessFailureKind(tc.err); got != tc.want {
			t.Errorf("AccessFailureKind(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestResolveAccessError_TypedAndRedacted(t *testing.T) {
	cause := status.Error(codes.PermissionDenied, "FAKE-sm-detail projects/p/secrets/scion-x-API_KEY")
	err := newResolveAccessError("API_KEY", "project", cause)
	want := ErrResolveAccess.Error() + ": project/API_KEY (permission_denied)"
	if err.Error() != want {
		t.Fatalf("Error() = %q\nwant %q", err.Error(), want)
	}
	if strings.Contains(err.Error(), "FAKE-sm-detail") || strings.Contains(err.Error(), "projects/") {
		t.Fatalf("Error() leaks backend detail: %q", err.Error())
	}
	if !errors.Is(err, ErrResolveAccess) || errors.Is(err, ErrResolveDecrypt) {
		t.Fatal("ResolveAccessError must match ErrResolveAccess only")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status.Code through Unwrap = %v, want PermissionDenied", status.Code(err))
	}
	var rae *ResolveAccessError
	if !errors.As(fmt.Errorf("outer: %w", err), &rae) || rae.Key != "API_KEY" || rae.Scope != "project" || rae.Kind != FailurePermissionDenied {
		t.Fatalf("errors.As = %+v", rae)
	}
	if !errors.Is(newResolveAccessError("K", "hub", context.Canceled), context.Canceled) {
		t.Fatal("Unwrap must reach a context cause")
	}
}
