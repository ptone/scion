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

package runtimebroker

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	scionrt "github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker/harvest"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/trajectory/shipper"
)

type seamFakeHub struct {
	mu      sync.Mutex
	reports []harvest.Report
	segs    int
}

func (f *seamFakeHub) PrepareTrajectorySegment(_ context.Context, _ string, _ harvest.PrepareSegmentRequest) (*telemetrycontract.TrajectorySegmentPrepareResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.segs++
	return &telemetrycontract.TrajectorySegmentPrepareResponse{
		SegmentID: "seg-1",
		State:     telemetrycontract.TrajectorySegmentStateCommitted,
	}, nil
}

func (f *seamFakeHub) UploadTrajectorySegment(_ context.Context, _ telemetrycontract.TrajectorySegmentUploadInstruction, _ []byte) error {
	return nil
}

func (f *seamFakeHub) CommitTrajectorySegment(_ context.Context, _, segID string) (*telemetrycontract.TrajectorySegmentCommitResponse, error) {
	return &telemetrycontract.TrajectorySegmentCommitResponse{
		SegmentID: segID,
		State:     telemetrycontract.TrajectorySegmentStateCommitted,
	}, nil
}

func (f *seamFakeHub) IngestUsageEvents(_ context.Context, _ string, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
	return &telemetrycontract.UsageIngestResponse{Accepted: len(req.Events)}, nil
}

func (f *seamFakeHub) ReportTrajectoryHarvest(_ context.Context, _ string, report harvest.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, report)
	return nil
}

func (f *seamFakeHub) reportCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reports)
}

func (f *seamFakeHub) lastReport() harvest.Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reports[len(f.reports)-1]
}

func TestExitHarvestTracker_TransitionsAndDebounce(t *testing.T) {
	var mu sync.Mutex
	var scheduled []api.AgentInfo
	tracker := newExitHarvestTracker(func(ag api.AgentInfo) {
		mu.Lock()
		scheduled = append(scheduled, ag)
		mu.Unlock()
	})

	// 1. Agent already exited on first sight -> must NOT trigger.
	tracker.observe([]api.AgentInfo{
		{Name: "ag-1", ProjectID: "p1", RunID: "r0", ContainerStatus: "exited (0)", Labels: map[string]string{api.LabelAgentID: "aid-1"}},
	})
	if len(scheduled) != 0 {
		t.Fatalf("expected 0 scheduled for already-exited agent, got %d", len(scheduled))
	}

	// 2. Agent running -> then OOM killed (exited 137) -> triggers once.
	tracker.observe([]api.AgentInfo{
		{Name: "ag-1", ProjectID: "p1", RunID: "r1", ContainerStatus: "Up 5 seconds", Labels: map[string]string{api.LabelAgentID: "aid-1"}},
	})
	tracker.observe([]api.AgentInfo{
		{Name: "ag-1", ProjectID: "p1", RunID: "r1", ContainerStatus: "Exited (137) 1 second ago", Labels: map[string]string{api.LabelAgentID: "aid-1"}},
	})
	if len(scheduled) != 1 || scheduled[0].RunID != "r1" {
		t.Fatalf("expected 1 scheduled for r1, got %+v", scheduled)
	}

	// 3. Repeated exited heartbeat for same (agent, run) -> debounced.
	tracker.observe([]api.AgentInfo{
		{Name: "ag-1", ProjectID: "p1", RunID: "r1", ContainerStatus: "Exited (137) 30 seconds ago", Labels: map[string]string{api.LabelAgentID: "aid-1"}},
	})
	if len(scheduled) != 1 {
		t.Fatalf("expected debounce to keep count at 1, got %d", len(scheduled))
	}

	// 4. New run starts and stops normally -> triggers for r2.
	tracker.observe([]api.AgentInfo{
		{Name: "ag-1", ProjectID: "p1", RunID: "r2", ContainerStatus: "running", Labels: map[string]string{api.LabelAgentID: "aid-1"}},
	})
	tracker.observe([]api.AgentInfo{
		{Name: "ag-1", ProjectID: "p1", RunID: "r2", ContainerStatus: "stopped", Labels: map[string]string{api.LabelAgentID: "aid-1"}},
	})
	if len(scheduled) != 2 || scheduled[1].RunID != "r2" {
		t.Fatalf("expected 2 scheduled (r1, r2), got %+v", scheduled)
	}
}

func TestPreDeleteAndPreResetHarvestSeams(t *testing.T) {
	// Set up a project dir with an agent home containing baselined shipper state
	// and unshipped claude JSONL bytes.
	projDir := filepath.Join(t.TempDir(), ".scion")
	agentDir := filepath.Join(projDir, "agents", "ag-1")
	homeDir := filepath.Join(agentDir, "home")
	sessDir := filepath.Join(homeDir, ".claude", "projects", "p")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "scion-agent.json"), []byte(`{"harness":"claude"}`), 0o600); err != nil {
		t.Fatalf("write scion-agent.json: %v", err)
	}
	sh, err := shipper.New(shipper.Config{HomeDir: homeDir, Harness: "claude"})
	if err != nil {
		t.Fatalf("shipper.New: %v", err)
	}
	if _, err := sh.Baseline(context.Background()); err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "s.jsonl"), []byte("{\"msg\":\"hello\"}\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	fakeHub := &seamFakeHub{}
	rt := scionrt.NewDockerRuntime()
	mgr := agent.NewManager(rt)
	srv := New(ServerConfig{BrokerID: "b1"}, mgr, rt, config.RuntimeBuildRecord{})
	srv.SetHarvestEngine(harvest.New(harvest.Config{Hub: fakeHub, HubID: "hub-1"}))

	// Test pre_delete seam returns Report (P6.2 hook) and ships pending segment.
	dt := &deleteTarget{
		mgr:         mgr,
		name:        "ag-1",
		runID:       "run-1",
		agentID:     "aid-1",
		projectPath: projDir,
		projectID:   "proj-1",
	}
	rep := srv.runPreDeleteHarvest(context.Background(), dt, "aid-1", "proj-1", "run-1", projDir)
	if rep == nil {
		t.Fatal("expected non-nil pre_delete Report")
	}
	if rep.Trigger != string(harvest.TriggerPreDelete) || rep.Outcome != harvest.OutcomeComplete || rep.SegmentsCommitted != 1 {
		t.Fatalf("unexpected pre_delete report: %+v", *rep)
	}

	// Test pre_reset seam after appending another line.
	f, err := os.OpenFile(filepath.Join(sessDir, "s.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteString("{\"msg\":\"before-reset\"}\n")
	_ = f.Close()

	resetRep := srv.runPreResetHarvest(context.Background(), mgr, "docker", CreateAgentRequest{
		ID:        "aid-1",
		Slug:      "ag-1",
		ProjectID: "proj-1",
	}, api.StartOptions{
		Name:        "ag-1",
		RunID:       "run-1",
		ProjectPath: projDir,
	})
	if resetRep == nil {
		t.Fatal("expected non-nil pre_reset Report")
	}
	if resetRep.Trigger != string(harvest.TriggerPreReset) || resetRep.Outcome != harvest.OutcomeComplete || resetRep.SegmentsCommitted != 1 {
		t.Fatalf("unexpected pre_reset report: %+v", *resetRep)
	}
}

func TestPostExitHarvest_UnavailableReportedOnceForNonReadableRuntime(t *testing.T) {
	fakeHub := &seamFakeHub{}
	rt := &scionrt.SubstrateRuntime{}
	mgr := agent.NewManager(rt)
	srv := New(ServerConfig{BrokerID: "b1"}, mgr, rt, config.RuntimeBuildRecord{})
	srv.SetHarvestEngine(harvest.New(harvest.Config{Hub: fakeHub, HubID: "hub-1"}))

	aid := api.NewUUID()
	srv.observeHeartbeatAgentsForHarvest([]api.AgentInfo{
		{Name: "ag-sub", ProjectID: "p1", RunID: "r1", Runtime: "substrate", ContainerStatus: "running", Labels: map[string]string{api.LabelAgentID: aid}},
	})
	srv.observeHeartbeatAgentsForHarvest([]api.AgentInfo{
		{Name: "ag-sub", ProjectID: "p1", RunID: "r1", Runtime: "substrate", ContainerStatus: "exited (0)", Labels: map[string]string{api.LabelAgentID: aid}},
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fakeHub.reportCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if fakeHub.reportCount() != 1 {
		t.Fatalf("expected 1 unavailable report, got %d", fakeHub.reportCount())
	}
	rep := fakeHub.lastReport()
	if rep.Outcome != harvest.OutcomeUnavailable || rep.Trigger != string(harvest.TriggerPostExit) {
		t.Fatalf("unexpected report: %+v", rep)
	}
}
