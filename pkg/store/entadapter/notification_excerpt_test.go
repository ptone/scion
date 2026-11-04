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

package entadapter

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotificationStore_ExcerptRoundTrip pins that the excerpt of a review
// summary notification (ptone/scion#6853) is stored and read back as it
// was written, in its own field, by both the single read and the list, and
// that a row which sets none reads empty.
func TestNotificationStore_ExcerptRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestNotificationStore(t)

	summary := newResourceNotification("user-1")
	summary.Kind = store.NotificationKindArtifactReviewSummary
	summary.Status = store.NotificationStatusArtifactReviewSubmitted
	summary.Message = "Review summary from Alice (v2 of your artifact):"
	summary.Excerpt = "Ship it. One question on the chart."
	require.NoError(t, s.CreateNotification(ctx, summary))
	plain := newResourceNotification("user-1")
	plain.ID = uuid.NewString()
	require.NoError(t, s.CreateNotification(ctx, plain))

	got, err := s.GetNotification(ctx, summary.ID)
	require.NoError(t, err)
	assert.Equal(t, summary.Excerpt, got.Excerpt)
	assert.Equal(t, summary.Message, got.Message, "the excerpt is not folded into the message")
	assert.Equal(t, store.NotificationKindArtifactReviewSummary, got.Kind)

	list, err := s.GetNotifications(ctx, store.SubscriberTypeUser, "user-1", false)
	require.NoError(t, err)
	byID := map[string]store.Notification{}
	for _, n := range list {
		byID[n.ID] = n
	}
	require.Len(t, byID, 2)
	assert.Equal(t, summary.Excerpt, byID[summary.ID].Excerpt)
	assert.Empty(t, byID[plain.ID].Excerpt, "a writer that sets no excerpt stores an empty one")
}

// TestNotificationStore_ReviewSummaryNotSwept pins that a review summary
// row is never handed to the dispatch sweep (the path that retries
// delivery to agents and plugins) and never expired by it: the sweep reads
// agent-subscriber rows only, and this kind is always a user row.
func TestNotificationStore_ReviewSummaryNotSwept(t *testing.T) {
	ctx := context.Background()
	s := newTestNotificationStore(t)

	n := newResourceNotification("user-1")
	n.Kind = store.NotificationKindArtifactReviewSummary
	n.Status = store.NotificationStatusArtifactReviewSubmitted
	n.Excerpt = "Looks good."
	n.CreatedAt = time.Now().Add(-48 * time.Hour)
	require.NoError(t, s.CreateNotification(ctx, n))

	for _, broker := range []string{"", "broker-1"} {
		undispatched, err := s.GetUndispatchedAgentNotifications(ctx, broker)
		require.NoError(t, err)
		for _, u := range undispatched {
			assert.NotEqual(t, n.ID, u.ID, "the sweep must not see a review summary row")
		}
	}
	expired, err := s.ExpireStaleAgentNotifications(ctx, time.Now())
	require.NoError(t, err)
	assert.Zero(t, expired)
	got, err := s.GetNotification(ctx, n.ID)
	require.NoError(t, err)
	assert.False(t, got.Dispatched)
	assert.Empty(t, got.DispatchOutcome)
	assert.Equal(t, "Looks good.", got.Excerpt)
}
