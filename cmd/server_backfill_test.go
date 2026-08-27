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

package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedBackfillProject creates a project and returns its ID.
func seedBackfillProject(t *testing.T, ctx context.Context, s store.Store) string {
	t.Helper()
	projectID := uuid.NewString()
	err := s.CreateProject(ctx, &store.Project{
		ID:         projectID,
		Name:       "backfill-test",
		Slug:       "backfill-test-" + projectID[:8],
		Visibility: "private",
	})
	require.NoError(t, err)
	return projectID
}

// seedDMMessage creates a message with a DM-style ThreadID between two UUIDs.
// The message is created without a ConversationID, simulating pre-conversation data.
func seedDMMessage(t *testing.T, ctx context.Context, s store.Store, projectID string, senderID, recipientID string, createdAt time.Time) string {
	t.Helper()
	msgID := uuid.NewString()
	err := s.CreateMessage(ctx, &store.Message{
		ID:          msgID,
		ProjectID:   projectID,
		Sender:      "user:" + senderID,
		SenderID:    senderID,
		Recipient:   "agent:" + recipientID,
		RecipientID: recipientID,
		Msg:         "test message",
		Type:        "instruction",
		CreatedAt:   createdAt,
	})
	require.NoError(t, err)
	return msgID
}

// TestBackfillDryRunMutatesNothing verifies AC-12-2: dry-run mode reports
// what would change without modifying any database rows.
func TestBackfillDryRunMutatesNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	projectID := seedBackfillProject(t, ctx, s)
	senderID := uuid.NewString()
	recipientID := uuid.NewString()

	// Seed messages without conversation_id.
	now := time.Now()
	msgIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		msgIDs[i] = seedDMMessage(t, ctx, s, projectID, senderID, recipientID, now.Add(time.Duration(i)*time.Second))
	}

	// Run backfill in dry-run mode.
	result, err := runBackfillWithStore(ctx, s, messaging.BackfillConfig{
		DryRun:    true,
		ProjectID: projectID,
	})
	require.NoError(t, err)

	// Report should show what would change.
	assert.Equal(t, 3, result.TotalProcessed, "should process all messages")
	assert.True(t, result.Attributed+result.Inferred > 0, "should report attributions")
	assert.Equal(t, 0, result.Skipped, "no messages should be skipped (none have conversation_id)")

	// Verify messages still have NO conversation_id.
	for _, msgID := range msgIDs {
		msg, err := s.GetMessage(ctx, msgID)
		require.NoError(t, err)
		assert.Empty(t, msg.ConversationID, "dry-run should not set conversation_id on message %s", msgID)
	}
}

// TestBackfillExecuteAndIdempotent verifies AC-12-3: a real run attributes
// messages to conversations, and a second run is idempotent (all skipped).
func TestBackfillExecuteAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	projectID := seedBackfillProject(t, ctx, s)
	senderID := uuid.NewString()
	recipientID := uuid.NewString()

	// Seed messages without conversation_id.
	now := time.Now()
	msgIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		msgIDs[i] = seedDMMessage(t, ctx, s, projectID, senderID, recipientID, now.Add(time.Duration(i)*time.Second))
	}

	// First run: execute mode.
	result1, err := runBackfillWithStore(ctx, s, messaging.BackfillConfig{
		DryRun:    false,
		ProjectID: projectID,
	})
	require.NoError(t, err)

	assert.Equal(t, 3, result1.TotalProcessed)
	assert.True(t, result1.Attributed+result1.Inferred > 0, "should attribute messages")
	assert.Equal(t, 1, result1.ConversationsCreated, "should create one conversation for the DM pair")

	// Verify messages now have a conversation_id.
	for _, msgID := range msgIDs {
		msg, err := s.GetMessage(ctx, msgID)
		require.NoError(t, err)
		assert.NotEmpty(t, msg.ConversationID, "execute should set conversation_id on message %s", msgID)
	}

	// Second run: should be idempotent (all skipped).
	result2, err := runBackfillWithStore(ctx, s, messaging.BackfillConfig{
		DryRun:    false,
		ProjectID: projectID,
	})
	require.NoError(t, err)

	assert.Equal(t, 3, result2.TotalProcessed)
	assert.Equal(t, 3, result2.Skipped, "idempotent re-run should skip all messages")
	assert.Equal(t, 0, result2.Attributed, "idempotent re-run should attribute nothing new")
	assert.Equal(t, 0, result2.ConversationsCreated, "idempotent re-run should create no conversations")
}

// TestBackfillResumeViaCheckpoint verifies AC-12-4: the checkpoint mechanism
// allows resuming a backfill from where it left off.
func TestBackfillResumeViaCheckpoint(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	projectID := seedBackfillProject(t, ctx, s)
	senderID := uuid.NewString()
	recipientID := uuid.NewString()

	// Seed an initial batch of messages (the "old" messages).
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		seedDMMessage(t, ctx, s, projectID, senderID, recipientID, baseTime.Add(time.Duration(i)*time.Minute))
	}

	// First run: process the initial batch.
	result1, err := runBackfillWithStore(ctx, s, messaging.BackfillConfig{
		DryRun:    false,
		ProjectID: projectID,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result1.LastCheckpoint, "first run should record a checkpoint")
	assert.Equal(t, 3, result1.TotalProcessed, "first run should process all 3 messages")
	assert.True(t, result1.Attributed+result1.Inferred > 0, "first run should attribute messages")

	// Seed new messages well AFTER the first batch's timestamps.
	for i := 0; i < 2; i++ {
		seedDMMessage(t, ctx, s, projectID, senderID, recipientID, baseTime.Add(time.Duration(10+i)*time.Minute))
	}

	// Resume from the checkpoint. The checkpoint's CreatedAt filters out
	// messages at or before that timestamp. Messages after the checkpoint
	// that already have conversation_id are counted as "processed" but
	// immediately skipped by the idempotency guard.
	result2, err := runBackfillWithStore(ctx, s, messaging.BackfillConfig{
		DryRun:     false,
		Checkpoint: result1.LastCheckpoint,
		ProjectID:  projectID,
	})
	require.NoError(t, err)

	// The resume should attribute the 2 new messages. Some old messages
	// after the checkpoint timestamp may also be seen but skipped.
	assert.True(t, result2.Attributed+result2.Inferred > 0,
		"resume should attribute new messages")
	assert.True(t, result2.TotalProcessed >= 2,
		"resume should process at least the 2 new messages")

	// Verify the new messages now have conversation_id.
	msgs, err := s.ListMessages(ctx, store.MessageFilter{ProjectID: projectID}, store.ListOptions{Limit: 100})
	require.NoError(t, err)
	for _, msg := range msgs.Items {
		assert.NotEmpty(t, msg.ConversationID,
			"all messages should have conversation_id after backfill+resume")
	}
}

// TestBackfillMalformedThreadID verifies AC-12-5: messages with malformed
// ThreadIDs produce errors in the result without crashing.
func TestBackfillMalformedThreadID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	projectID := seedBackfillProject(t, ctx, s)

	// Seed a message with a malformed DM ThreadID.
	msgID := uuid.NewString()
	err := s.CreateMessage(ctx, &store.Message{
		ID:          msgID,
		ProjectID:   projectID,
		Sender:      "user:" + uuid.NewString(),
		SenderID:    uuid.NewString(),
		Recipient:   "agent:" + uuid.NewString(),
		RecipientID: uuid.NewString(),
		Msg:         "test message with bad thread",
		Type:        "instruction",
		ThreadID:    "dm:invalid:bad:format",
		CreatedAt:   time.Now(),
	})
	require.NoError(t, err)

	// Run backfill.
	result, err := runBackfillWithStore(ctx, s, messaging.BackfillConfig{
		DryRun:    true,
		ProjectID: projectID,
	})
	require.NoError(t, err)

	// The malformed ThreadID should produce an error entry.
	assert.NotEmpty(t, result.Errors, "malformed ThreadID should produce errors")
	assert.Equal(t, 1, result.TotalProcessed, "should still process the message")
}

// TestBackfillMergeResult verifies the result aggregation helper.
func TestBackfillMergeResult(t *testing.T) {
	dst := &messaging.BackfillResult{
		TotalProcessed:       10,
		Attributed:           5,
		Inferred:             2,
		Skipped:              3,
		ConversationsCreated: 1,
		HazardAEmailCount:    1,
		HazardBSlugCount:     0,
		LastCheckpoint:       "old-cp",
		Errors:               []string{"err1"},
	}
	src := &messaging.BackfillResult{
		TotalProcessed:       20,
		Attributed:           15,
		Inferred:             3,
		Skipped:              2,
		ConversationsCreated: 4,
		HazardAEmailCount:    2,
		HazardBSlugCount:     1,
		LastCheckpoint:       "new-cp",
		Errors:               []string{"err2", "err3"},
	}

	mergeBackfillResult(dst, src)

	assert.Equal(t, 30, dst.TotalProcessed)
	assert.Equal(t, 20, dst.Attributed)
	assert.Equal(t, 5, dst.Inferred)
	assert.Equal(t, 5, dst.Skipped)
	assert.Equal(t, 5, dst.ConversationsCreated)
	assert.Equal(t, 3, dst.HazardAEmailCount)
	assert.Equal(t, 1, dst.HazardBSlugCount)
	assert.Equal(t, "new-cp", dst.LastCheckpoint)
	assert.Equal(t, []string{"err1", "err2", "err3"}, dst.Errors)
}
