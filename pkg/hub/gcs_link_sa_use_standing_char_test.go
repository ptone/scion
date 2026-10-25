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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// TestGCSLink_SAUse_SenderStandingChar_Base characterizes the proxy before
// it checks the sending agent's standing: a held sender, and a sender whose
// root user is inactive, are not in good standing (agentStanding refuses
// them, as the GCP token handlers would), yet the gs:// fetch is admitted
// and a token is minted. Same scenarios as TestGCSLink_SAUse_SenderStanding.
func TestGCSLink_SAUse_SenderStandingChar_Base(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(t *testing.T, f *gcsFixture, sc gcsUseScenario)
	}{
		{"held agent", func(t *testing.T, f *gcsFixture, sc gcsUseScenario) {
			holdAgentFor(t, f.store, sc.agent.ID, sc.agent.ProjectID, gcsStandingRootID(sc.agent.Slug))
		}},
		{"inactive root", func(t *testing.T, f *gcsFixture, sc gcsUseScenario) {
			ctx := context.Background()
			u, err := f.store.GetUser(ctx, gcsStandingRootID(sc.agent.Slug))
			require.NoError(t, err)
			u.Status = store.UserStatusSuspended
			require.NoError(t, f.store.UpdateUser(ctx, u))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGCSFixture(t)
			sc := newGCSUseScenario(t, f, "use-standing")
			require.NoError(t, f.srv.agentStanding(context.Background(), sc.agent.ID), "precondition: the sender is in good standing")
			tc.fault(t, f, sc)
			require.Error(t, f.srv.agentStanding(context.Background(), sc.agent.ID), "precondition: the sender is not in good standing")

			requireGCSFetchAllowed(t, f, sc.fetch(t, f), sc.sa)
		})
	}
}
