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
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// markAgentSoftDeletedAt marks agentID as soft-deleted at the given timestamp
// through UpdateAgentDeletion (the store's soft-delete writer).
func markAgentSoftDeletedAt(t *testing.T, ctx context.Context, s store.AgentStore, agentID string, at time.Time) {
	t.Helper()
	n, err := s.UpdateAgentDeletion(ctx, agentID, store.DeletionPredicate{}, store.DeletionFields{DeletedAt: &at})
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
