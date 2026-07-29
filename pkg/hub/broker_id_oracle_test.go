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

package hub

import (
	"context"
	"net/http"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// TestBrokerID_RejectionIsIndistinguishable pins the oracle-close invariant for
// #591: the broker-registration endpoint must refuse a malformed broker id and a
// well-formed id that already denotes another principal with the SAME generic
// response, so a caller cannot use the endpoint to enumerate which identifiers
// belong to existing principals.
//
// It drives the real registration path via the real Handler() (production
// shapes, not hand-built fixtures). This is a WRITE path, so it asserts store
// non-persistence: neither refusal may mint a broker row, and the colliding
// principal must be left intact.
//
// RED before the uniform-error fix: the two refusals carried distinguishable
// message text ("must be a UUID" vs "identifier is not available"). GREEN after:
// both collapse to the single generic ErrBrokerIDRejected body.
func TestBrokerID_RejectionIsIndistinguishable(t *testing.T) {
	srv, s, alice, bob, _, _, _ := setupBrokerAuthzTest(t)
	ctx := context.Background()

	const malformed = "not-a-valid-uuid"
	// bob is an existing user; his id is a well-formed, canonical UUID already
	// taken in a principal namespace.
	taken := bob.ID

	malformedRec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/brokers",
		CreateBrokerRegistrationRequest{Name: "oracle-malformed", BrokerID: malformed})
	takenRec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/brokers",
		CreateBrokerRegistrationRequest{Name: "oracle-taken", BrokerID: taken})

	// Both must be a clean client-error refusal.
	require.GreaterOrEqual(t, malformedRec.Code, 400, "malformed id must be refused; body=%s", malformedRec.Body.String())
	require.Less(t, malformedRec.Code, 500, "malformed id must be a clean 4xx, not a 5xx; body=%s", malformedRec.Body.String())
	require.GreaterOrEqual(t, takenRec.Code, 400, "taken id must be refused; body=%s", takenRec.Body.String())
	require.Less(t, takenRec.Code, 500, "taken id must be a clean 4xx, not a 5xx; body=%s", takenRec.Body.String())

	// The oracle-close invariant: identical status AND identical body, so the two
	// reasons are indistinguishable to the caller.
	require.Equal(t, malformedRec.Code, takenRec.Code,
		"status must not distinguish a malformed id from one already taken")
	require.Equal(t, malformedRec.Body.String(), takenRec.Body.String(),
		"response body must not distinguish a malformed id from one already taken")

	// WRITE path: neither refusal may have persisted a broker row.
	_, err := s.GetRuntimeBroker(ctx, malformed)
	require.ErrorIs(t, err, store.ErrNotFound, "no broker row may be minted at a malformed id")
	_, err = s.GetRuntimeBroker(ctx, taken)
	require.ErrorIs(t, err, store.ErrNotFound, "no broker row may be minted at a taken id")

	// The colliding principal must be untouched.
	gotBob, err := s.GetUser(ctx, bob.ID)
	require.NoError(t, err, "the colliding user record must still exist")
	require.Equal(t, bob.Email, gotBob.Email)
}
