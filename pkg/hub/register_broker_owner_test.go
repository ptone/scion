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
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression for the register-by-name broker MINT branch of handleProjectRegister
// (#591, #221). Before this fix the new-broker literal set no CreatedBy, so every
// broker minted through /projects/register was ownerless — and an ownerless broker
// makes each owner-equality guard (rotate-secret, capabilities, dispatch) read as
// unowned. The fix records the caller as the broker owner, which is only meaningful
// for a user caller: the owner guards compare CreatedBy against a user id. A
// non-user principal (agent or broker token, for which GetUserIdentityFromContext
// returns nil) is therefore REFUSED on this branch rather than minting an ownerless
// broker or recording a non-user id as an owner.
//
// These arms drive the real /projects/register route (both callers register a
// brand-new project of their own, so the register provider gate does not fire and
// what is measured is the mint branch itself).

// TestRegisterBrokerOwner_NonUserRefused pins the Option-A refusal: an agent caller
// reaching the new-broker mint branch is denied and no broker row is created.
//
// RED-on-revert: dropping the nil-user refusal (reverting to the ownerless mint)
// makes this arm return 200 and persist the broker, reddening both the 403 and the
// not-created assertions below.
func TestRegisterBrokerOwner_NonUserRefused(t *testing.T) {
	f := rbmSetup(t)
	ctx := context.Background()

	newProject := tid("rbo-agent-project")
	newBrokerID := tid("rbo-agent-broker")

	rec := f.asAgent(t, http.MethodPost, rbmRegisterPath, RegisterProjectRequest{
		ID: newProject, Name: "RBO Agent Project",
		GitRemote: "https://example.invalid/" + newProject + ".git",
		Broker:    rbmHostileBroker(newBrokerID, "rbo-agent-broker-name"),
		Path:      f.workspace,
	})

	require.Equal(t, http.StatusForbidden, rec.Code,
		"a non-user caller minting a broker via register must be refused; body=%s",
		rec.Body.String())

	_, err := f.store.GetRuntimeBroker(ctx, newBrokerID)
	require.Error(t, err,
		"the refused register still minted a broker (ownerless), so the nil-user "+
			"branch is not refusing")
}

// TestRegisterBrokerOwner_UserMintSetsCreatedBy is the positive: a user caller
// still mints successfully AND the minted broker records the caller as its owner.
// Rule 2a — the fix must not over-deny the legitimate user path.
func TestRegisterBrokerOwner_UserMintSetsCreatedBy(t *testing.T) {
	f := rbmSetup(t)
	ctx := context.Background()

	newProject := tid("rbo-user-project")
	newBrokerID := tid("rbo-user-broker")

	rec := f.asUser(t, http.MethodPost, rbmRegisterPath, RegisterProjectRequest{
		ID: newProject, Name: "RBO User Project",
		GitRemote: "https://example.invalid/" + newProject + ".git",
		Broker:    rbmHostileBroker(newBrokerID, "rbo-user-broker-name"),
		Path:      f.workspace,
	})
	require.Equal(t, http.StatusOK, rec.Code,
		"the user mint path must still succeed; body=%s", rec.Body.String())

	var resp RegisterProjectResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Broker)
	require.Equal(t, newBrokerID, resp.Broker.ID)

	// Store-verify ownership was recorded on the minted broker.
	got, err := f.store.GetRuntimeBroker(ctx, newBrokerID)
	require.NoError(t, err, "the user mint path no longer creates the broker")
	require.Equal(t, f.intruder.ID, got.CreatedBy,
		"the minted broker did not record the registering user as its owner")
}
