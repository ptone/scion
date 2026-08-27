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
	"bytes"
	"log/slog"
	"testing"
)

func TestDirectMessageExternalRef_Deterministic(t *testing.T) {
	// Order should not matter — the ref is sorted.
	refAB := DirectMessageExternalRef("aaa", "bbb")
	refBA := DirectMessageExternalRef("bbb", "aaa")
	if refAB != refBA {
		t.Errorf("refs should be identical regardless of order: %q vs %q", refAB, refBA)
	}
	if refAB != "dm:aaa:bbb" {
		t.Errorf("unexpected ref format: %q", refAB)
	}
}

func TestDirectMessageExternalRef_EmptyID(t *testing.T) {
	ref := DirectMessageExternalRef("", "xyz")
	if ref != "dm::xyz" {
		t.Errorf("expected dm::xyz, got %q", ref)
	}
}

func TestOldRoutingFromMessage_WithThreadID(t *testing.T) {
	routing := OldRoutingFromMessage("sender1", "recip1", "dm:abc123")
	if routing != "thread:dm:abc123" {
		t.Errorf("expected thread:dm:abc123, got %q", routing)
	}
}

func TestOldRoutingFromMessage_WithoutThreadID(t *testing.T) {
	routing := OldRoutingFromMessage("sender1", "recip1", "")
	// sender-recipient with sorted IDs
	expected := "sender-recipient:recip1:sender1"
	if routing != expected {
		t.Errorf("expected %q, got %q", expected, routing)
	}

	// Order-independent
	routing2 := OldRoutingFromMessage("recip1", "sender1", "")
	if routing != routing2 {
		t.Errorf("routing should be order-independent: %q vs %q", routing, routing2)
	}
}

func TestLogDivergence_Match(t *testing.T) {
	// Reset global counter for this test.
	DivergenceMetrics = &DivergenceCounter{}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	entry := DivergenceEntry{
		MessageID:  "msg-1",
		OldRouting: "thread:dm:abc",
		NewRouting: "conv:uuid-123",
		Match:      true,
		Reason:     "both resolve to same pair",
	}
	LogDivergence(logger, entry)

	if DivergenceMetrics.Matches() != 1 {
		t.Errorf("expected 1 match, got %d", DivergenceMetrics.Matches())
	}
	if DivergenceMetrics.Mismatches() != 0 {
		t.Errorf("expected 0 mismatches, got %d", DivergenceMetrics.Mismatches())
	}
	if DivergenceMetrics.Total() != 1 {
		t.Errorf("expected 1 total, got %d", DivergenceMetrics.Total())
	}

	output := buf.String()
	if !contains(output, "match") {
		t.Errorf("expected 'match' in log output, got: %s", output)
	}
}

func TestLogDivergence_Mismatch(t *testing.T) {
	DivergenceMetrics = &DivergenceCounter{}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	entry := DivergenceEntry{
		MessageID:  "msg-2",
		OldRouting: "thread:dm:abc",
		NewRouting: "conv:uuid-456",
		Match:      false,
		Reason:     "thread_id maps to different conversation",
	}
	LogDivergence(logger, entry)

	if DivergenceMetrics.Mismatches() != 1 {
		t.Errorf("expected 1 mismatch, got %d", DivergenceMetrics.Mismatches())
	}

	output := buf.String()
	if !contains(output, "DIVERGENCE") {
		t.Errorf("expected 'DIVERGENCE' in log output, got: %s", output)
	}
}

func TestDivergenceCounter_Concurrent(t *testing.T) {
	DivergenceMetrics = &DivergenceCounter{}

	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func(match bool) {
			DivergenceMetrics.Inc(match)
			done <- struct{}{}
		}(i%2 == 0)
	}
	for i := 0; i < 100; i++ {
		<-done
	}

	if DivergenceMetrics.Total() != 100 {
		t.Errorf("expected 100 total, got %d", DivergenceMetrics.Total())
	}
	if DivergenceMetrics.Matches() != 50 {
		t.Errorf("expected 50 matches, got %d", DivergenceMetrics.Matches())
	}
	if DivergenceMetrics.Mismatches() != 50 {
		t.Errorf("expected 50 mismatches, got %d", DivergenceMetrics.Mismatches())
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && bytes.Contains([]byte(s), []byte(substr))
}

// ---------------------------------------------------------------------------
// ComputeDivergenceMatch tests
// ---------------------------------------------------------------------------

func TestComputeDivergenceMatch_EmptyConvID(t *testing.T) {
	match, reason := ComputeDivergenceMatch("sender", "recip", "", "")
	if match {
		t.Error("expected match=false when convID is empty")
	}
	if reason != "no-new-routing" {
		t.Errorf("expected reason 'no-new-routing', got %q", reason)
	}
}

func TestComputeDivergenceMatch_ThreadIDNonEmpty(t *testing.T) {
	match, reason := ComputeDivergenceMatch("sender", "recip", "thread-123", "conv-abc")
	if match {
		t.Error("expected match=false when threadID is non-empty")
	}
	if reason != "old-model-thread vs new-model-dm" {
		t.Errorf("expected reason 'old-model-thread vs new-model-dm', got %q", reason)
	}
}

func TestComputeDivergenceMatch_NormalDM(t *testing.T) {
	match, reason := ComputeDivergenceMatch("sender", "recip", "", "conv-abc")
	if !match {
		t.Error("expected match=true for normal DM (no thread, valid convID)")
	}
	if reason != "both-models-dm-agreement" {
		t.Errorf("expected reason 'both-models-dm-agreement', got %q", reason)
	}
}

func TestComputeDivergenceMatch_NoOldRouting(t *testing.T) {
	match, reason := ComputeDivergenceMatch("", "", "", "conv-abc")
	if match {
		t.Error("expected match=false when senderID and recipientID are both empty")
	}
	if reason != "unknown/no-old-routing" {
		t.Errorf("expected reason 'unknown/no-old-routing', got %q", reason)
	}
}

// ---------------------------------------------------------------------------
// NewRoutingStr tests
// ---------------------------------------------------------------------------

func TestNewRoutingStr(t *testing.T) {
	if got := NewRoutingStr(""); got != "none" {
		t.Errorf("expected 'none' for empty convID, got %q", got)
	}
	if got := NewRoutingStr("conv-abc"); got != "conv:conv-abc" {
		t.Errorf("expected 'conv:conv-abc', got %q", got)
	}
}
