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

//go:build unix

package telemetry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

func makeHarvestTestUsageEvent(id, runID, agentID string) *telemetrycontract.UsageEvent {
	now := time.Now().UTC()
	inTok := int64(25)
	ev := &telemetrycontract.UsageEvent{
		Envelope:            telemetrycontract.NewEnvelope(telemetrycontract.EventTypeUsage, id, "sciontool/agent/"+agentID, now, now),
		UsageSource:         telemetrycontract.UsageSourceNative,
		SourceKind:          "claude.api_request",
		SourceSchemaVersion: "1",
		SourceEventID:       "rid-" + id,
		SourceIdentity:      telemetrycontract.SourceIdentityRequestID,
		RunID:               runID,
		Harness:             "claude",
		Model:               "claude-sonnet-4-6",
		ModelSource:         telemetrycontract.ModelSourceEvent,
		Status:              telemetrycontract.StatusSuccess,
		Calls:               1,
		Input:               &inTok,
		InputDerivation:     telemetrycontract.InputDerivationReported,
		TokenStatus:         telemetrycontract.TokenStatusReported,
		MalformedFields:     []string{},
	}
	ev.AgentID = agentID
	return ev
}

func TestDrainOutboxOnce_AbsentCreatesNothing(t *testing.T) {
	homeDir := t.TempDir()
	called := false
	post := func(ctx context.Context, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
		called = true
		return &telemetrycontract.UsageIngestResponse{Accepted: len(req.Events)}, nil
	}

	stats, err := DrainOutboxOnce(context.Background(), homeDir, nil, post, "hub-1")
	if err != nil {
		t.Fatalf("DrainOutboxOnce error: %v", err)
	}
	if !stats.NoOutbox {
		t.Fatalf("expected NoOutbox=true, got %+v", stats)
	}
	if called {
		t.Fatal("post should not be called when outbox is absent")
	}
	// Verify nothing was created under homeDir (not even .scion).
	entries, err := os.ReadDir(homeDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty homeDir, found %v", entries)
	}
}

func TestDrainOutboxOnce_CrashLeftStagingAndDrain(t *testing.T) {
	homeDir := t.TempDir()
	outboxDir := HubAnalyticsOutboxDirFor(homeDir)
	ob, err := OpenAnalyticsOutbox(outboxDir)
	if err != nil {
		t.Fatalf("OpenAnalyticsOutbox: %v", err)
	}
	if _, err := ob.PinHubID("hub-1"); err != nil {
		t.Fatalf("PinHubID: %v", err)
	}
	// Append two events that remain in staging.jsonl (simulating a crash before finalize).
	ev1 := makeHarvestTestUsageEvent("ev-1", "run-1", "agent-1")
	ev2 := makeHarvestTestUsageEvent("ev-2", "run-1", "agent-1")
	if _, err := ob.AppendUsage(ev1); err != nil {
		t.Fatalf("AppendUsage 1: %v", err)
	}
	if _, err := ob.AppendUsage(ev2); err != nil {
		t.Fatalf("AppendUsage 2: %v", err)
	}

	var received []telemetrycontract.UsageEvent
	post := func(ctx context.Context, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
		received = append(received, req.Events...)
		return &telemetrycontract.UsageIngestResponse{Accepted: len(req.Events)}, nil
	}

	stats, err := DrainOutboxOnce(context.Background(), homeDir, nil, post, "hub-1")
	if err != nil {
		t.Fatalf("DrainOutboxOnce error: %v", err)
	}
	if stats.NoOutbox || stats.HubMismatch {
		t.Fatalf("unexpected flags: %+v", stats)
	}
	if stats.EventsDrained != 2 {
		t.Fatalf("EventsDrained = %d, want 2", stats.EventsDrained)
	}
	if stats.EventsPending != 0 || stats.PendingBytes != 0 {
		t.Fatalf("expected 0 pending after full drain, got %+v", stats)
	}
	if len(received) != 2 {
		t.Fatalf("received %d events, want 2", len(received))
	}
	for _, ev := range received {
		if ev.HubID != "hub-1" {
			t.Errorf("event %s HubID = %q, want hub-1", ev.EventID, ev.HubID)
		}
		if ev.RunID != "run-1" {
			t.Errorf("event %s RunID = %q, want run-1", ev.EventID, ev.RunID)
		}
	}

	// Second call is a clean no-op.
	stats2, err := DrainOutboxOnce(context.Background(), homeDir, nil, post, "hub-1")
	if err != nil {
		t.Fatalf("second DrainOutboxOnce error: %v", err)
	}
	if stats2.EventsDrained != 0 || stats2.EventsPending != 0 {
		t.Fatalf("expected 0 drained / 0 pending on replay, got %+v", stats2)
	}
}

func TestDrainOutboxOnce_PartialDrain(t *testing.T) {
	homeDir := t.TempDir()
	outboxDir := HubAnalyticsOutboxDirFor(homeDir)
	ob, err := OpenAnalyticsOutbox(outboxDir)
	if err != nil {
		t.Fatalf("OpenAnalyticsOutbox: %v", err)
	}
	if _, err := ob.PinHubID("hub-1"); err != nil {
		t.Fatalf("PinHubID: %v", err)
	}
	// Two events with different run_ids force HubUsageSink to send two separate
	// batches so we can succeed on the first and fail transiently on the second.
	if _, err := ob.AppendUsage(makeHarvestTestUsageEvent("ev-a", "run-1", "agent-1")); err != nil {
		t.Fatalf("AppendUsage a: %v", err)
	}
	if _, err := ob.AppendUsage(makeHarvestTestUsageEvent("ev-b", "run-2", "agent-1")); err != nil {
		t.Fatalf("AppendUsage b: %v", err)
	}

	transientErr := errors.New("hub 503 temporary")
	calls := 0
	post := func(ctx context.Context, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
		calls++
		if calls == 1 {
			return &telemetrycontract.UsageIngestResponse{Accepted: len(req.Events)}, nil
		}
		return nil, transientErr
	}

	stats, err := DrainOutboxOnce(context.Background(), homeDir, nil, post, "hub-1")
	if !errors.Is(err, transientErr) {
		t.Fatalf("expected transientErr, got %v", err)
	}
	// Both lines were in one DrainBatch call; when sendGroup fails on group 2,
	// DrainBatch does not advance cursor so EventsPending remains > 0.
	if stats.EventsPending == 0 || stats.PendingBytes == 0 {
		t.Fatalf("expected pending events after partial/failed drain, got %+v", stats)
	}

	// Retry succeeds and drains both events cleanly.
	var delivered []string
	postOK := func(ctx context.Context, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
		for _, ev := range req.Events {
			delivered = append(delivered, ev.EventID)
		}
		return &telemetrycontract.UsageIngestResponse{Accepted: len(req.Events)}, nil
	}
	stats2, err := DrainOutboxOnce(context.Background(), homeDir, nil, postOK, "hub-1")
	if err != nil {
		t.Fatalf("retry DrainOutboxOnce error: %v", err)
	}
	if stats2.EventsDrained != 2 || stats2.EventsPending != 0 {
		t.Fatalf("expected 2 drained / 0 pending on retry, got %+v", stats2)
	}
	if len(delivered) != 2 {
		t.Fatalf("delivered = %v, want 2 events", delivered)
	}
}

func TestDrainOutboxOnce_PinnedHubMismatchRefuses(t *testing.T) {
	homeDir := t.TempDir()
	outboxDir := HubAnalyticsOutboxDirFor(homeDir)
	ob, err := OpenAnalyticsOutbox(outboxDir)
	if err != nil {
		t.Fatalf("OpenAnalyticsOutbox: %v", err)
	}
	if _, err := ob.PinHubID("hub-old"); err != nil {
		t.Fatalf("PinHubID: %v", err)
	}
	if _, err := ob.AppendUsage(makeHarvestTestUsageEvent("ev-1", "run-1", "agent-1")); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}

	called := false
	post := func(ctx context.Context, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
		called = true
		return &telemetrycontract.UsageIngestResponse{Accepted: len(req.Events)}, nil
	}

	stats, err := DrainOutboxOnce(context.Background(), homeDir, nil, post, "hub-new")
	if err != nil {
		t.Fatalf("DrainOutboxOnce error: %v", err)
	}
	if !stats.HubMismatch {
		t.Fatalf("expected HubMismatch=true, got %+v", stats)
	}
	if stats.EventsDrained != 0 {
		t.Fatalf("expected 0 EventsDrained on hub mismatch, got %d", stats.EventsDrained)
	}
	if called {
		t.Fatal("post should not be called on hub mismatch")
	}
	// Verify hub_id file is unchanged.
	pinned, err := os.ReadFile(filepath.Join(outboxDir, "hub_id"))
	if err != nil {
		t.Fatalf("read hub_id: %v", err)
	}
	if string(pinned) != "hub-old" {
		t.Fatalf("hub_id mutated to %q, want hub-old", string(pinned))
	}
}

func TestDrainOutboxOnce_BoundedLockTimeout(t *testing.T) {
	homeDir := t.TempDir()
	outboxDir := HubAnalyticsOutboxDirFor(homeDir)
	ob, err := OpenAnalyticsOutbox(outboxDir)
	if err != nil {
		t.Fatalf("OpenAnalyticsOutbox: %v", err)
	}
	if _, err := ob.PinHubID("hub-1"); err != nil {
		t.Fatalf("PinHubID: %v", err)
	}
	// Hold the outbox lock in this test.
	unlock, err := ob.lock()
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	post := func(ctx context.Context, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
		return &telemetrycontract.UsageIngestResponse{}, nil
	}
	_, err = DrainOutboxOnce(ctx, homeDir, nil, post, "hub-1")
	if !errors.Is(err, ErrAnalyticsOutboxLocked) {
		t.Fatalf("expected ErrAnalyticsOutboxLocked, got %v", err)
	}
}

func TestDrainOutboxOnce_PreservesOwnerAsRoot(t *testing.T) {
	homeDir := t.TempDir()
	outboxDir := HubAnalyticsOutboxDirFor(homeDir)
	ob, err := OpenAnalyticsOutbox(outboxDir)
	if err != nil {
		t.Fatalf("OpenAnalyticsOutbox: %v", err)
	}
	if _, err := ob.PinHubID("hub-1"); err != nil {
		t.Fatalf("PinHubID: %v", err)
	}
	if _, err := ob.AppendUsage(makeHarvestTestUsageEvent("ev-1", "run-1", "agent-1")); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}

	origGeteuid := outboxGeteuid
	origChown := outboxChown
	t.Cleanup(func() {
		outboxGeteuid = origGeteuid
		outboxChown = origChown
	})
	outboxGeteuid = func() int { return 0 }
	var chowned []OutboxOwner
	outboxChown = func(_ string, uid, gid int) error {
		chowned = append(chowned, OutboxOwner{UID: uid, GID: gid})
		return nil
	}

	wantOwner := &OutboxOwner{UID: 1000, GID: 1000}
	post := func(ctx context.Context, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
		return &telemetrycontract.UsageIngestResponse{Accepted: len(req.Events)}, nil
	}
	stats, err := DrainOutboxOnce(context.Background(), homeDir, wantOwner, post, "hub-1")
	if err != nil {
		t.Fatalf("DrainOutboxOnce error: %v", err)
	}
	if stats.EventsDrained != 1 {
		t.Fatalf("EventsDrained = %d, want 1", stats.EventsDrained)
	}
	if len(chowned) == 0 {
		t.Fatal("expected root chown calls for created temp/ready/cursor files")
	}
	for _, got := range chowned {
		if got != *wantOwner {
			t.Errorf("chowned to %+v, want %+v", got, *wantOwner)
		}
	}
}
