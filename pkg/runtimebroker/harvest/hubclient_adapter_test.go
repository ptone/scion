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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/trajectory/shipper"
)

func TestHubClientAdapter_EndToEndOverHTTP(t *testing.T) {
	var (
		mu            sync.Mutex
		gotPrepare    *telemetrycontract.TrajectorySegmentPrepareRequest
		gotUploaded   []byte
		gotCommit     *telemetrycontract.TrajectorySegmentCommitRequest
		gotUsage      *telemetrycontract.UsageIngestRequest
		gotHarvestRep *telemetrycontract.TrajectoryHarvestReportRequest
	)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == telemetrycontract.TrajectorySegmentsPrepareBrokerPath("broker-1", "agent-1"):
			var req telemetrycontract.TrajectorySegmentPrepareRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			gotPrepare = &req
			_ = json.NewEncoder(w).Encode(telemetrycontract.TrajectorySegmentPrepareResponse{
				SegmentID: "seg-1",
				State:     telemetrycontract.TrajectorySegmentStateUploadRequired,
				Upload: &telemetrycontract.TrajectorySegmentUploadInstruction{
					Method: http.MethodPut,
					URL:    srv.URL + telemetrycontract.TrajectorySegmentContentBrokerPath("broker-1", "agent-1", "seg-1"),
				},
			})

		case r.Method == http.MethodPut && r.URL.Path == telemetrycontract.TrajectorySegmentContentBrokerPath("broker-1", "agent-1", "seg-1"):
			b, _ := io.ReadAll(r.Body)
			gotUploaded = b
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && r.URL.Path == telemetrycontract.TrajectorySegmentsCommitBrokerPath("broker-1", "agent-1"):
			var req telemetrycontract.TrajectorySegmentCommitRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			gotCommit = &req
			_ = json.NewEncoder(w).Encode(telemetrycontract.TrajectorySegmentCommitResponse{
				SegmentID: req.SegmentID,
				State:     telemetrycontract.TrajectorySegmentStateCommitted,
			})

		case r.Method == http.MethodPost && r.URL.Path == telemetrycontract.UsageIngestBrokerPath("broker-1", "agent-1"):
			var req telemetrycontract.UsageIngestRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			gotUsage = &req
			_ = json.NewEncoder(w).Encode(telemetrycontract.UsageIngestResponse{
				Accepted: len(req.Events),
			})

		case r.Method == http.MethodPost && r.URL.Path == telemetrycontract.TrajectoryHarvestsBrokerPath("broker-1", "agent-1"):
			var req telemetrycontract.TrajectoryHarvestReportRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			gotHarvestRep = &req
			_ = json.NewEncoder(w).Encode(telemetrycontract.TrajectoryHarvestReportResponse{
				Recorded: true,
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	hc, err := hubclient.New(srv.URL, hubclient.WithHMACAuth("broker-1", []byte("secret-key-32-bytes-long-for-test")))
	if err != nil {
		t.Fatalf("hubclient.New: %v", err)
	}
	bhc, ok := hc.(hubclient.BrokerHarvestClient)
	if !ok {
		t.Fatal("hubclient.Client does not implement BrokerHarvestClient")
	}

	homeDir := t.TempDir()
	sessDir := filepath.Join(homeDir, ".claude", "projects", "p")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sh, err := shipper.New(shipper.Config{HomeDir: homeDir, Harness: "claude"})
	if err != nil {
		t.Fatalf("shipper.New: %v", err)
	}
	if _, err := sh.Baseline(context.Background()); err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "s.jsonl"), []byte("{\"ok\":true}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeTestUsageEvent(t, homeDir, "hub-1", "ev-1", "run-1", "agent-1")

	eng := New(Config{
		Hub:   NewHubClientAdapter(bhc, "broker-1"),
		HubID: "hub-1",
	})
	rep := eng.Harvest(context.Background(), Target{
		AgentID:      "agent-1",
		Slug:         "ag-1",
		ProjectID:    "proj-1",
		RunID:        "run-1",
		Harness:      "claude",
		HomeDir:      homeDir,
		RuntimeName:  "docker",
		HomeReadable: true,
	}, TriggerPreDelete)

	if rep.Outcome != OutcomeComplete || rep.Trajectory != TrajectoryShipped || rep.Usage != UsageDrained {
		t.Fatalf("unexpected report: %+v", rep)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPrepare == nil || gotPrepare.RunID != "run-1" {
		t.Fatalf("unexpected prepare req: %+v", gotPrepare)
	}
	if len(gotUploaded) == 0 {
		t.Fatal("expected compressed bytes uploaded")
	}
	if gotCommit == nil || gotCommit.SegmentID != "seg-1" {
		t.Fatalf("unexpected commit req: %+v", gotCommit)
	}
	if gotUsage == nil || len(gotUsage.Events) != 1 {
		t.Fatalf("unexpected usage req: %+v", gotUsage)
	}
	if gotHarvestRep == nil || gotHarvestRep.Outcome != OutcomeComplete {
		t.Fatalf("unexpected harvest report: %+v", gotHarvestRep)
	}
}
