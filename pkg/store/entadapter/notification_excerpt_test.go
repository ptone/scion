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
