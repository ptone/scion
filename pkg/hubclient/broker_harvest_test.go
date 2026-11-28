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

package hubclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

func newTestBrokerHarvestClient(t *testing.T, baseURL string) BrokerHarvestClient {
	t.Helper()
	c, err := New(baseURL, WithHMACAuth("broker-1", []byte("test-secret-key-32-bytes-minimum!")))
	require.NoError(t, err)
	bh, ok := c.(BrokerHarvestClient)
	require.True(t, ok, "hubclient.Client must implement BrokerHarvestClient")
	return bh
}

func TestBrokerHarvestClient_UsagePrepareCommitReport(t *testing.T) {
	var gotPaths []string
	var gotBrokerHeader []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.Method+" "+r.URL.Path)
		gotBrokerHeader = append(gotBrokerHeader, r.Header.Get(apiclient.HeaderBrokerID))
		assert.NotEmpty(t, r.Header.Get(apiclient.HeaderSignature), "expected HMAC signature on %s", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case telemetrycontract.UsageIngestBrokerPath("broker-1", "agent-1"):
			var req telemetrycontract.UsageIngestRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "run-1", req.RunID)
			_ = json.NewEncoder(w).Encode(telemetrycontract.UsageIngestResponse{Accepted: 2, Duplicates: 1})

		case telemetrycontract.TrajectorySegmentsPrepareBrokerPath("broker-1", "agent-1"):
			var req telemetrycontract.TrajectorySegmentPrepareRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "run-1", req.RunID)
			assert.Equal(t, "claude", req.Harness)
			_ = json.NewEncoder(w).Encode(telemetrycontract.TrajectorySegmentPrepareResponse{
				SegmentID:  "seg-1",
				State:      telemetrycontract.TrajectorySegmentStateUploadRequired,
				ObjectPath: "hub/obj.seg.gz",
				Upload: &telemetrycontract.TrajectorySegmentUploadInstruction{
					Method:  http.MethodPut,
					URL:     "http://example.invalid/upload",
					Headers: map[string]string{"Content-Type": "application/gzip"},
				},
			})

		case telemetrycontract.TrajectorySegmentsCommitBrokerPath("broker-1", "agent-1"):
			var req telemetrycontract.TrajectorySegmentCommitRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "seg-1", req.SegmentID)
			_ = json.NewEncoder(w).Encode(telemetrycontract.TrajectorySegmentCommitResponse{
				State:        telemetrycontract.TrajectorySegmentStateCommitted,
				SegmentID:    "seg-1",
				ManifestPath: "hub/obj.seg.gz.manifest.json",
			})

		case telemetrycontract.TrajectoryHarvestsBrokerPath("broker-1", "agent-1"):
			var req telemetrycontract.TrajectoryHarvestReportRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "harv-1", req.HarvestID)
			assert.Equal(t, telemetrycontract.TrajectoryHarvestTriggerPostExit, req.Trigger)
			_ = json.NewEncoder(w).Encode(telemetrycontract.TrajectoryHarvestReportResponse{Recorded: true})

		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	bh := newTestBrokerHarvestClient(t, srv.URL)

	usageResp, err := bh.PostBrokerAgentUsageEvents(ctx, "broker-1", "agent-1", &telemetrycontract.UsageIngestRequest{
		RunID:  "run-1",
		Events: []telemetrycontract.UsageEvent{},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, usageResp.Accepted)
	assert.Equal(t, 1, usageResp.Duplicates)

	prepResp, err := bh.PrepareBrokerTrajectorySegment(ctx, "broker-1", "agent-1", &telemetrycontract.TrajectorySegmentPrepareRequest{
		RunID:              "run-1",
		Harness:            "claude",
		NativeRelativePath: ".claude/projects/p/s.jsonl",
		ArtifactKind:       telemetrycontract.TrajectoryArtifactKindJSONLAppend,
	})
	require.NoError(t, err)
	assert.Equal(t, "seg-1", prepResp.SegmentID)
	assert.Equal(t, telemetrycontract.TrajectorySegmentStateUploadRequired, prepResp.State)

	commitResp, err := bh.CommitBrokerTrajectorySegment(ctx, "broker-1", "agent-1", &telemetrycontract.TrajectorySegmentCommitRequest{
		SegmentID: "seg-1",
	})
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectorySegmentStateCommitted, commitResp.State)
	assert.Equal(t, "hub/obj.seg.gz.manifest.json", commitResp.ManifestPath)

	harvResp, err := bh.ReportTrajectoryHarvest(ctx, "broker-1", "agent-1", &telemetrycontract.TrajectoryHarvestReportRequest{
		HarvestID:  "harv-1",
		RunID:      "run-1",
		Trigger:    telemetrycontract.TrajectoryHarvestTriggerPostExit,
		Outcome:    telemetrycontract.TrajectoryHarvestOutcomeComplete,
		Trajectory: telemetrycontract.TrajectoryHarvestTrajectoryShipped,
		Usage:      telemetrycontract.TrajectoryHarvestUsageDrained,
		StartedAt:  "2026-11-28T20:00:00Z",
		FinishedAt: "2026-11-28T20:00:05Z",
	})
	require.NoError(t, err)
	assert.True(t, harvResp.Recorded)

	assert.Equal(t, []string{
		"POST /api/v1/brokers/broker-1/agents/agent-1/usage/events",
		"POST /api/v1/brokers/broker-1/agents/agent-1/trajectory/segments:prepare",
		"POST /api/v1/brokers/broker-1/agents/agent-1/trajectory/segments:commit",
		"POST /api/v1/brokers/broker-1/agents/agent-1/trajectory/harvests",
	}, gotPaths)
	assert.Equal(t, []string{"broker-1", "broker-1", "broker-1", "broker-1"}, gotBrokerHeader)
}

func TestBrokerHarvestClient_UploadHubProxiedAndExternalSigned(t *testing.T) {
	payload := []byte("compressed-segment-bytes")

	t.Run("hub_proxied_attaches_hmac_and_handles_200_and_412", func(t *testing.T) {
		for _, status := range []int{http.StatusOK, http.StatusPreconditionFailed} {
			var gotBrokerID, gotSig, gotCustom string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBrokerID = r.Header.Get(apiclient.HeaderBrokerID)
				gotSig = r.Header.Get(apiclient.HeaderSignature)
				gotCustom = r.Header.Get("X-Test-Header")
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(status)
			}))
			defer srv.Close()

			bh := newTestBrokerHarvestClient(t, srv.URL)
			uploadURL := srv.URL + telemetrycontract.TrajectorySegmentContentBrokerPath("broker-1", "agent-1", "seg-1") + "?exp=9999999999&sig=abc"
			gotStatus, err := bh.UploadBrokerTrajectorySegment(context.Background(), telemetrycontract.TrajectorySegmentUploadInstruction{
				Method: http.MethodPut,
				URL:    uploadURL,
				Headers: map[string]string{
					"Content-Type":  "application/gzip",
					"X-Test-Header": "verbatim",
				},
			}, payload)
			require.NoError(t, err)
			assert.Equal(t, status, gotStatus)
			assert.Equal(t, "broker-1", gotBrokerID)
			assert.NotEmpty(t, gotSig)
			assert.Equal(t, "verbatim", gotCustom)
			assert.Equal(t, payload, gotBody)
		}
	})

	t.Run("external_signed_url_never_leaks_broker_hmac", func(t *testing.T) {
		var extBrokerID, extSig, extGenMatch string
		extSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			extBrokerID = r.Header.Get(apiclient.HeaderBrokerID)
			extSig = r.Header.Get(apiclient.HeaderSignature)
			extGenMatch = r.Header.Get("x-goog-if-generation-match")
			w.WriteHeader(http.StatusOK)
		}))
		defer extSrv.Close()

		bh := newTestBrokerHarvestClient(t, "https://hub.example.com")
		status, err := bh.UploadBrokerTrajectorySegment(context.Background(), telemetrycontract.TrajectorySegmentUploadInstruction{
			Method: http.MethodPut,
			URL:    extSrv.URL + "/bucket/obj.seg.gz?X-Goog-Signature=secret",
			Headers: map[string]string{
				"Content-Type":               "application/gzip",
				"x-goog-if-generation-match": "0",
			},
		}, payload)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
		assert.Empty(t, extBrokerID)
		assert.Empty(t, extSig)
		assert.Equal(t, "0", extGenMatch)
	})

	t.Run("structured_api_error_preserved_and_signed_query_redacted", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{
					"code":    telemetrycontract.TrajectoryErrObjectConflict,
					"message": "existing object mismatch",
				},
			})
		}))
		defer srv.Close()

		bh := newTestBrokerHarvestClient(t, srv.URL)
		_, err := bh.CommitBrokerTrajectorySegment(context.Background(), "broker-1", "agent-1", &telemetrycontract.TrajectorySegmentCommitRequest{
			SegmentID: "seg-1",
		})
		require.Error(t, err)
		var apiErr *apiclient.APIError
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, http.StatusConflict, apiErr.StatusCode)
		assert.Equal(t, telemetrycontract.TrajectoryErrObjectConflict, apiErr.Code)

		// Closed server transport error must redact signed URL query params.
		closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		closedURL := closed.URL
		closed.Close()
		_, err = bh.UploadBrokerTrajectorySegment(context.Background(), telemetrycontract.TrajectorySegmentUploadInstruction{
			URL: closedURL + "/bucket/obj.seg.gz?X-Goog-Signature=SECRET_SIG",
		}, payload)
		require.Error(t, err)
		assert.False(t, strings.Contains(err.Error(), "SECRET_SIG"), "signed URL query leaked: %s", err.Error())
	})
}
