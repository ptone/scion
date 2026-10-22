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
	"net/http"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/authctx"
	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
)

// Session generation on conduit paths. A user's session generation is
// raised by revoking all of the user's sessions. A conduit session or
// stream authenticated with a hub user token keeps the generation its
// request was authenticated with, and every later check of that session or
// stream compares it with the user row's current generation:
//
//   - relay-held sessions: the admission token's sg claim
//     (conduitAdmissionClaims.SessionGen), compared by relayUserIdentity
//     and, for their streams, by the renewal re-check;
//   - in-process sessions and streams: the identity the request
//     authenticated (AuthenticatedUser.SessionGeneration), compared by
//     conduitUserStatus.
//
// Any inequality denies: the row ahead of the captured value is a
// revocation, and a captured value ahead of the row means the row was
// rolled back or restored. The user row is read directly from the store
// (there is no read replica or user-row cache on this path); revisit the
// rule before adding one. Other credentials (user access tokens, the
// development user, trusted-proxy users) are not affected.
//
// The two paths treat an absent value oppositely, on purpose. Admission
// claims carry the auth type, so a relay-held claim set can tell "a hub
// user token without sg" (a token from before sg existed: it reads 0, the
// legacy rule) from "not a hub user token". An in-process identity carries
// no auth type, so there presence is the only signal: a captured
// generation means a hub user token authenticated it, and conduitUserStatus
// compares only when one is present. That skip is sound only because every
// in-process entry point refuses a hub user token request whose generation
// is absent (conduitCaptureSessionGeneration), so a hub-user-token identity
// without one cannot exist. Never soften that refusal to a default
// (for example 0): the skip would then let every such stream through.

// conduitReasonSessionRevoked is the deny reason of a conduit session or
// stream whose user's sessions were revoked after it was authenticated.
const conduitReasonSessionRevoked = "session revoked"

// requestAuthType is the credential kind the auth middleware recorded for
// ctx's request ("" when none).
func requestAuthType(ctx context.Context) string {
	t, _ := ctx.Value(logging.AuthTypeKey{}).(string)
	return t
}

// conduitCaptureSessionGeneration returns identity carrying the session
// generation the request in ctx was authenticated with, when the request
// was authenticated with a hub user token. Other credentials return
// identity unchanged. ok is false for a hub user token whose generation is
// absent from ctx or whose identity is not an AuthenticatedUser: the caller
// refuses the request (it never proceeds without the generation).
//
// The refusal is load-bearing: it is what makes conduitUserStatus's skip of
// an identity without a generation sound (see the comment at the top of
// this file). Do not replace it with a default value.
func conduitCaptureSessionGeneration(ctx context.Context, identity Identity) (Identity, bool) {
	if requestAuthType(ctx) != AuthTypeJWT {
		return identity, true
	}
	gen, ok := authctx.SessionGeneration(ctx)
	if !ok {
		return nil, false
	}
	u, ok := identity.(*AuthenticatedUser)
	if !ok || u == nil {
		return nil, false
	}
	return u.WithCapturedSessionGeneration(gen), true
}

// refuseConduitUncapturedSessionGeneration writes the refusal of a request
// conduitCaptureSessionGeneration could not capture a generation for.
func refuseConduitUncapturedSessionGeneration(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "invalid access token", nil)
}

// conduitCapturedSessionGeneration returns the session generation captured
// for identity (an AuthenticatedUser, or a scoped identity over one), and
// whether one was captured.
//
// Capture never produces a scoped identity, and user access token sessions
// never carry a generation. The scoped case exists because the renewal
// re-check rebuilds the user and, for a grant with a scope claim, wraps it
// (conduitScopedIdentity) before the captured value is stamped on it; this
// keeps the stamp from being dropped if a hub-user-token grant ever carries
// a scope.
func conduitCapturedSessionGeneration(identity Identity) (int64, bool) {
	switch id := identity.(type) {
	case *AuthenticatedUser:
		if id == nil {
			return 0, false
		}
		return id.CapturedSessionGeneration()
	case *ScopedUserIdentity:
		if id == nil {
			return 0, false
		}
		return conduitCapturedSessionGeneration(id.UserIdentity)
	default:
		return 0, false
	}
}

// conduitIdentityWithSessionGeneration returns identity carrying gen when
// it is an AuthenticatedUser or a scoped identity over one (see
// conduitCapturedSessionGeneration for why the scoped case exists). Any
// other identity (the development user) is returned unchanged.
func conduitIdentityWithSessionGeneration(identity Identity, gen int64) Identity {
	switch id := identity.(type) {
	case *AuthenticatedUser:
		if id != nil {
			return id.WithCapturedSessionGeneration(gen)
		}
	case *ScopedUserIdentity:
		if id != nil {
			if inner, ok := conduitIdentityWithSessionGeneration(id.UserIdentity, gen).(UserIdentity); ok {
				c := *id
				c.UserIdentity = inner
				return &c
			}
		}
	}
	return identity
}

// conduitSessionGenerationCurrent reports whether a captured session
// generation is the user row's current one. Any inequality is not current.
func conduitSessionGenerationCurrent(row, captured int64) bool {
	return row == captured
}
