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

package harvest

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/telemetry"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/trajectory/shipper"
	"github.com/GoogleCloudPlatform/scion/pkg/util/filelock"
)

type fakeHub struct {
	mu sync.Mutex

	prepareErr error
	uploadErr  error
	commitErr  error
	usageErr   error

	prepared []PrepareSegmentRequest
	uploaded map[string][]byte // segmentID -> compressed bytes
	committed map[string][]byte // segmentID -> raw bytes
	usage    []telemetrycontract.UsageEvent
	reports  []Report

	onPrepare func(agentID string, req PrepareSegmentRequest) (*telemetrycontract.TrajectorySegmentPrepareResponse, error)
}

func newFakeHub() *fakeHub {
	return &fakeHub{
		uploaded:  make(map[string][]byte),
		committed: make(map[string][]byte),
	}
}

func (f *fakeHub) PrepareTrajectorySegment(_ context.Context, agentID string, req PrepareSegmentRequest) (*telemetrycontract.TrajectorySegmentPrepareResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onPrepare != nil {
		return f.onPrepare(agentID, req)
	}
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	f.prepared = append(f.prepared, req)
	segID := fmt.Sprintf("seg-%d", len(f.prepared))
	return &telemetrycontract.TrajectorySegmentPrepareResponse{
		SegmentID: segID,
		State:     telemetrycontract.TrajectorySegmentStateUploadRequired,
		Upload: &telemetrycontract.TrajectorySegmentUploadInstruction{
			Method: http.MethodPut,
			URL:    "https://fake.local/upload/" + segID,
		},
	}, nil
}

func (f *fakeHub) UploadTrajectorySegment(_ context.Context, inst telemetrycontract.TrajectorySegmentUploadInstruction, compressed []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploadErr != nil {
		return f.uploadErr
	}
	segID := strings.TrimPrefix(inst.URL, "https://fake.local/upload/")
	f.uploaded[segID] = append([]byte(nil), compressed...)
	return nil
}

func (f *fakeHub) CommitTrajectorySegment(_ context.Context, _ string, segmentID string) (*telemetrycontract.TrajectorySegmentCommitResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commitErr != nil {
		return nil, f.commitErr
	}
	gz, ok := f.uploaded[segmentID]
	if !ok {
		return nil, &HTTPError{Operation: "commit", StatusCode: http.StatusUnprocessableEntity, Code: telemetrycontract.TrajectoryErrVerificationFailed}
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		return nil, err
	}
	f.committed[segmentID] = raw
	return &telemetrycontract.TrajectorySegmentCommitResponse{
		SegmentID: segmentID,
		State:     telemetrycontract.TrajectorySegmentStateCommitted,
	}, nil
}

func (f *fakeHub) IngestUsageEvents(_ context.Context, _ string, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.usageErr != nil {
		return nil, f.usageErr
	}
	f.usage = append(f.usage, req.Events...)
	return &telemetrycontract.UsageIngestResponse{Accepted: len(req.Events)}, nil
}

func (f *fakeHub) ReportTrajectoryHarvest(_ context.Context, _ string, report Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, report)
	return nil
}

func writeTestUsageEvent(t *testing.T, homeDir, hubID, id, runID, agentID string) {
	t.Helper()
	ob, err := telemetry.OpenAnalyticsOutbox(telemetry.HubAnalyticsOutboxDirFor(homeDir))
	if err != nil {
		t.Fatalf("OpenAnalyticsOutbox: %v", err)
	}
	if _, err := ob.PinHubID(hubID); err != nil {
		t.Fatalf("PinHubID: %v", err)
	}
	now := time.Now().UTC()
	inTok := int64(10)
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
	if _, err := ob.AppendUsage(ev); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
}

func TestEngine_Harvest_HappyPathAndReplayIdempotent(t *testing.T) {
	homeDir := t.TempDir()
	// Create a claude session file, baseline it, then append complete + incomplete tail lines.
	sessDir := filepath.Join(homeDir, ".claude", "projects", "proj-a")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sessFile := filepath.Join(sessDir, "session-1.jsonl")
	if err := os.WriteFile(sessFile, []byte("{\"pre\":\"baseline\"}\n"), 0o600); err != nil {
		t.Fatalf("write pre-baseline: %v", err)
	}
	sh, err := shipper.New(shipper.Config{HomeDir: homeDir, Harness: "claude"})
	if err != nil {
		t.Fatalf("shipper.New: %v", err)
	}
	if _, err := sh.Baseline(context.Background()); err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	// Append new lines including an incomplete tail (no trailing newline).
	appended := "{\"turn\":1}\n{\"turn\":2,\"tail\":true}"
	f, err := os.OpenFile(sessFile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(appended); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	// Also leave a crash-left usage event in staging.jsonl.
	writeTestUsageEvent(t, homeDir, "hub-1", "ev-1", "run-1", "agent-1")

	hub := newFakeHub()
	eng := New(Config{Hub: hub, HubID: "hub-1"})
	target := Target{
		AgentID:      "agent-1",
		Slug:         "ag-1",
		ProjectID:    "proj-1",
		RunID:        "run-1",
		Harness:      "claude",
		HomeDir:      homeDir,
		RuntimeName:  "docker",
		HomeReadable: true,
	}

	rep := eng.Harvest(context.Background(), target, TriggerPreDelete)
	if rep.Outcome != OutcomeComplete {
		t.Fatalf("Outcome = %q, want %q (report=%+v)", rep.Outcome, OutcomeComplete, rep)
	}
	if rep.Trajectory != TrajectoryShipped {
		t.Fatalf("Trajectory = %q, want %q", rep.Trajectory, TrajectoryShipped)
	}
	if rep.Usage != UsageDrained {
		t.Fatalf("Usage = %q, want %q", rep.Usage, UsageDrained)
	}
	if rep.SegmentsCommitted != 1 || rep.BytesCommitted != int64(len(appended)) {
		t.Fatalf("committed = (%d, %d), want (1, %d)", rep.SegmentsCommitted, rep.BytesCommitted, len(appended))
	}
	if rep.UsageEventsDrained != 1 || rep.UsageEventsPending != 0 {
		t.Fatalf("usage = (%d, %d), want (1, 0)", rep.UsageEventsDrained, rep.UsageEventsPending)
	}
	if len(hub.prepared) != 1 || !hub.prepared[0].IncompleteTail || hub.prepared[0].RunID != "run-1" {
		t.Fatalf("unexpected prepare req: %+v", hub.prepared)
	}
	if got := string(hub.committed["seg-1"]); got != appended {
		t.Fatalf("committed bytes = %q, want %q", got, appended)
	}
	if len(hub.reports) != 1 || hub.reports[0].Outcome != OutcomeComplete {
		t.Fatalf("reports = %+v, want 1 complete", hub.reports)
	}

	// Replay harvest must be nothing_pending with zero new uploads.
	rep2 := eng.Harvest(context.Background(), target, TriggerPreDelete)
	if rep2.Outcome != OutcomeNothingPending {
		t.Fatalf("replay Outcome = %q, want %q", rep2.Outcome, OutcomeNothingPending)
	}
	if rep2.Trajectory != TrajectoryNothingPending || rep2.Usage != UsageNothingPending {
		t.Fatalf("replay traj/usage = %q/%q, want nothing_pending/nothing_pending", rep2.Trajectory, rep2.Usage)
	}
	if len(hub.prepared) != 1 || len(hub.usage) != 1 {
		t.Fatalf("replay duplicated uploads: prepared=%d usage=%d", len(hub.prepared), len(hub.usage))
	}
}

func TestEngine_Harvest_NoStateNoOutboxCreatesNothing(t *testing.T) {
	homeDir := t.TempDir()
	hub := newFakeHub()
	eng := New(Config{Hub: hub, HubID: "hub-1"})

	rep := eng.Harvest(context.Background(), Target{
		AgentID:      "agent-1",
		RunID:        "run-1",
		Harness:      "claude",
		HomeDir:      homeDir,
		RuntimeName:  "docker",
		HomeReadable: true,
	}, TriggerPostExit)

	if rep.Outcome != OutcomeNothingPending || rep.Trajectory != TrajectoryNoState || rep.Usage != UsageNoOutbox {
		t.Fatalf("unexpected report: %+v", rep)
	}
	entries, err := os.ReadDir(homeDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("harvest created files in empty home: %v", entries)
	}
}

func TestEngine_Harvest_NoStateStillDrainsOutbox(t *testing.T) {
	homeDir := t.TempDir()
	writeTestUsageEvent(t, homeDir, "hub-1", "ev-1", "run-1", "agent-1")

	hub := newFakeHub()
	eng := New(Config{Hub: hub, HubID: "hub-1"})

	rep := eng.Harvest(context.Background(), Target{
		AgentID:      "agent-1",
		RunID:        "run-1",
		Harness:      "claude",
		HomeDir:      homeDir,
		RuntimeName:  "docker",
		HomeReadable: true,
	}, TriggerPostExit)

	if rep.Outcome != OutcomeComplete || rep.Trajectory != TrajectoryNoState || rep.Usage != UsageDrained {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if _, err := os.Stat(shipper.StatePath(homeDir)); !os.IsNotExist(err) {
		t.Fatalf("harvest must not create state.json when absent")
	}
}

func TestEngine_Harvest_CaptureRefusedAndRunCap(t *testing.T) {
	setupHome := func(t *testing.T) string {
		t.Helper()
		homeDir := t.TempDir()
		sessDir := filepath.Join(homeDir, ".claude", "projects", "p")
		if err := os.MkdirAll(sessDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		sh, _ := shipper.New(shipper.Config{HomeDir: homeDir, Harness: "claude"})
		if _, err := sh.Baseline(context.Background()); err != nil {
			t.Fatalf("Baseline: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sessDir, "s.jsonl"), []byte("{\"a\":1}\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		return homeDir
	}

	t.Run("capture_disabled", func(t *testing.T) {
		hub := newFakeHub()
		hub.prepareErr = &HTTPError{Operation: "prepare", StatusCode: http.StatusForbidden, Code: telemetrycontract.TrajectoryErrCaptureDisabled}
		eng := New(Config{Hub: hub, HubID: "hub-1"})
		rep := eng.Harvest(context.Background(), Target{
			AgentID: "a1", RunID: "r1", Harness: "claude", HomeDir: setupHome(t), RuntimeName: "docker", HomeReadable: true,
		}, TriggerPostExit)
		if rep.Trajectory != TrajectoryCaptureRefused || rep.Outcome != OutcomeNothingPending {
			t.Fatalf("got traj=%q outcome=%q, want capture_refused/nothing_pending", rep.Trajectory, rep.Outcome)
		}
	})

	t.Run("run_cap_exceeded", func(t *testing.T) {
		hub := newFakeHub()
		hub.prepareErr = &HTTPError{Operation: "prepare", StatusCode: http.StatusRequestEntityTooLarge, Code: telemetrycontract.TrajectoryErrRunCapExceeded}
		eng := New(Config{Hub: hub, HubID: "hub-1"})
		rep := eng.Harvest(context.Background(), Target{
			AgentID: "a1", RunID: "r1", Harness: "claude", HomeDir: setupHome(t), RuntimeName: "docker", HomeReadable: true,
		}, TriggerPostExit)
		if rep.Outcome != OutcomePartial {
			t.Fatalf("got outcome=%q, want partial (rep=%+v)", rep.Outcome, rep)
		}
	})
}

func TestEngine_Harvest_ShipperLockHeldNeverSteals(t *testing.T) {
	homeDir := t.TempDir()
	sessDir := filepath.Join(homeDir, ".claude", "projects", "p")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sh, _ := shipper.New(shipper.Config{HomeDir: homeDir, Harness: "claude"})
	if _, err := sh.Baseline(context.Background()); err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "s.jsonl"), []byte("{\"a\":1}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Hold state.lock externally.
	lk, err := filelock.Acquire(shipper.LockPath(homeDir), 0o600, time.Second)
	if err != nil {
		t.Fatalf("Acquire lock: %v", err)
	}
	defer lk.Unlock()

	hub := newFakeHub()
	eng := New(Config{Hub: hub, HubID: "hub-1", LockTimeout: 100 * time.Millisecond})
	rep := eng.Harvest(context.Background(), Target{
		AgentID: "a1", RunID: "r1", Harness: "claude", HomeDir: homeDir, RuntimeName: "docker", HomeReadable: true,
	}, TriggerPostExit)

	if rep.Trajectory != TrajectoryError || rep.ErrorCode != ErrCodeLockTimeout || rep.Outcome != OutcomeFailed {
		t.Fatalf("unexpected lock-held report: %+v", rep)
	}
	if strings.Contains(rep.Error, homeDir) {
		t.Fatalf("Report.Error leaked homeDir: %q", rep.Error)
	}
}

func TestEngine_Harvest_UnavailableReportedOncePerAgentRunTrigger(t *testing.T) {
	hub := newFakeHub()
	eng := New(Config{Hub: hub, HubID: "hub-1"})
	target := Target{
		AgentID:      "a1",
		RunID:        "r1",
		Harness:      "claude",
		RuntimeName:  "substrate",
		HomeReadable: false,
	}

	rep1 := eng.Harvest(context.Background(), target, TriggerPostExit)
	rep2 := eng.Harvest(context.Background(), target, TriggerPostExit)
	if rep1.Outcome != OutcomeUnavailable || rep2.Outcome != OutcomeUnavailable {
		t.Fatalf("expected unavailable, got %q and %q", rep1.Outcome, rep2.Outcome)
	}
	if len(hub.reports) != 1 {
		t.Fatalf("expected unavailable reported once, got %d reports", len(hub.reports))
	}
	// Different trigger reports once too.
	_ = eng.Harvest(context.Background(), target, TriggerPreDelete)
	if len(hub.reports) != 2 {
		t.Fatalf("expected 2 reports across distinct triggers, got %d", len(hub.reports))
	}
}

func TestEngine_Harvest_NonClaudeHarnessSkipped(t *testing.T) {
	homeDir := t.TempDir()
	writeTestUsageEvent(t, homeDir, "hub-1", "ev-1", "run-1", "agent-1")

	hub := newFakeHub()
	eng := New(Config{Hub: hub, HubID: "hub-1"})
	rep := eng.Harvest(context.Background(), Target{
		AgentID: "agent-1", RunID: "run-1", Harness: "gemini", HomeDir: homeDir, RuntimeName: "docker", HomeReadable: true,
	}, TriggerPreDelete)

	if rep.Trajectory != TrajectorySkipped || rep.Usage != UsageDrained || rep.Outcome != OutcomeComplete {
		t.Fatalf("unexpected report: %+v", rep)
	}
}

func TestEngine_Harvest_OutboxHubMismatch(t *testing.T) {
	homeDir := t.TempDir()
	writeTestUsageEvent(t, homeDir, "hub-old", "ev-1", "run-1", "agent-1")

	hub := newFakeHub()
	eng := New(Config{Hub: hub, HubID: "hub-new"})
	rep := eng.Harvest(context.Background(), Target{
		AgentID: "agent-1", RunID: "run-1", Harness: "claude", HomeDir: homeDir, RuntimeName: "docker", HomeReadable: true,
	}, TriggerPreDelete)

	if rep.Usage != UsageSkipped || rep.ErrorCode != ErrCodeHubMismatch {
		t.Fatalf("expected usage=skipped error_code=hub_mismatch, got %+v", rep)
	}
	if len(hub.usage) != 0 {
		t.Fatalf("expected 0 usage events drained on hub mismatch, got %d", len(hub.usage))
	}
}

func TestSanitizeReportError(t *testing.T) {
	home := "/var/lib/scion/projects/p1/agents/a1/home"
	raw := "failed to open " + home + "/.scion/state.json and /etc/secret/path: " + strings.Repeat("x", 2000)
	got := sanitizeReportError(raw, home)
	if strings.Contains(got, home) || strings.Contains(got, "/etc/secret/path") {
		t.Fatalf("absolute path leaked: %q", got)
	}
	if len(got) > maxReportErrorBytes {
		t.Fatalf("len(got) = %d, want <= %d", len(got), maxReportErrorBytes)
	}
}
