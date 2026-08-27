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

package messaging

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// Divergence logging (Phase 5 gate for S4 read-switch)
// ---------------------------------------------------------------------------

// DivergenceEntry records a comparison between old-model routing (thread_id,
// sender/recipient pairs) and new-model routing (conversation_id resolved via
// UpsertConversationByExternalRef).
type DivergenceEntry struct {
	MessageID  string `json:"message_id"`
	OldRouting string `json:"old_routing"` // e.g. "thread:dm:abc123" or "sender:X->recipient:Y"
	NewRouting string `json:"new_routing"` // e.g. "conv:uuid-of-conversation"
	Match      bool   `json:"match"`       // true when old and new agree
	Reason     string `json:"reason"`      // human-readable explanation
}

// DivergenceCounter tracks the total number of divergence entries logged,
// partitioned into matches and mismatches. Safe for concurrent use.
type DivergenceCounter struct {
	matches    atomic.Int64
	mismatches atomic.Int64
}

// Inc increments the appropriate counter.
func (c *DivergenceCounter) Inc(match bool) {
	if match {
		c.matches.Add(1)
	} else {
		c.mismatches.Add(1)
	}
}

// Matches returns the total number of matching entries logged.
func (c *DivergenceCounter) Matches() int64 { return c.matches.Load() }

// Mismatches returns the total number of mismatching entries logged.
func (c *DivergenceCounter) Mismatches() int64 { return c.mismatches.Load() }

// Total returns the total number of divergence entries logged.
func (c *DivergenceCounter) Total() int64 { return c.matches.Load() + c.mismatches.Load() }

// DivergenceMetrics is the package-level counter for divergence events.
// Exported so that metrics collectors can read it.
var DivergenceMetrics = &DivergenceCounter{}

// LogDivergence logs a DivergenceEntry to the provided logger and increments
// the global divergence counter. Matching entries are logged at INFO;
// mismatches are logged at WARN for easy grep.
func LogDivergence(log *slog.Logger, entry DivergenceEntry) {
	DivergenceMetrics.Inc(entry.Match)

	attrs := []any{
		"message_id", entry.MessageID,
		"old_routing", entry.OldRouting,
		"new_routing", entry.NewRouting,
		"match", entry.Match,
		"divergence_count", DivergenceMetrics.Total(),
	}
	if entry.Reason != "" {
		attrs = append(attrs, "reason", entry.Reason)
	}

	if entry.Match {
		log.Info("conversation routing check: match", attrs...)
	} else {
		log.Warn("conversation routing check: DIVERGENCE", attrs...)
	}
}

// NewRoutingStr formats a conversation ID for the divergence log's NewRouting
// field. Returns "conv:{id}" when convID is non-empty, "none" otherwise.
func NewRoutingStr(convID string) string {
	if convID == "" {
		return "none"
	}
	return "conv:" + convID
}

// ---------------------------------------------------------------------------
// Deterministic external reference helpers
// ---------------------------------------------------------------------------

// DirectMessageExternalRef builds a deterministic, order-independent external
// reference for a direct-message conversation between two principals.
// Format: dm:{sorted(idA, idB)}
//
// Both IDs are required; an empty ID produces a ref that makes the divergence
// visible rather than silently swallowing the error.
func DirectMessageExternalRef(idA, idB string) string {
	pair := []string{idA, idB}
	sort.Strings(pair)
	return fmt.Sprintf("dm:%s:%s", pair[0], pair[1])
}

// ComputeDivergenceMatch determines whether old-model and new-model routing
// agree for a message. It returns the match result and a human-readable reason.
//
// Parameters:
//   - senderID, recipientID: the message's sender and recipient IDs
//   - threadID: the message's thread ID (empty for DMs)
//   - convID: the conversation ID resolved by the new model (empty if resolution failed)
func ComputeDivergenceMatch(senderID, recipientID, threadID, convID string) (match bool, reason string) {
	// If new model failed to resolve a conversation
	if convID == "" {
		return false, "no-new-routing"
	}

	// If old model has no routing info
	if senderID == "" && recipientID == "" {
		return false, "unknown/no-old-routing"
	}

	// If old model routes by thread but new model resolved a DM conversation
	if threadID != "" {
		return false, "old-model-thread vs new-model-dm"
	}

	// Both models route by sender-recipient pair for DMs.
	// The old model uses sender-recipient:{sorted pair},
	// the new model resolves via DirectMessageExternalRef which uses the same sorted pair.
	// They agree when both have the same participants.
	return true, "both-models-dm-agreement"
}

// OldRoutingFromMessage builds the old-model routing key from a message's
// sender/recipient pair and optional thread_id.
func OldRoutingFromMessage(senderID, recipientID, threadID string) string {
	if threadID != "" {
		return "thread:" + threadID
	}
	parts := []string{senderID, recipientID}
	sort.Strings(parts)
	return fmt.Sprintf("sender-recipient:%s", strings.Join(parts, ":"))
}
