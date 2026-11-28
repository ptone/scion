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

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

func setTrajectoryCaptureExperimentForTest(t *testing.T, srv *Server, enabled bool) {
	t.Helper()
	cur := srv.experimentRegistry()
	var active []experiments.Experiment
	for _, e := range cur.All() {
		if e.Name == experiments.TokenomicsTrajectoryCapture {
			e.Default = enabled
		}
		active = append(active, e)
	}
	reg, err := experiments.NewRegistry(active, cur.Retired())
	require.NoError(t, err)
	srv.setExperimentRegistry(reg)
}

func doBrokerSignedPut(t *testing.T, srv *Server, brokerID string, secret []byte, rawURL string, payload []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	target := u.RequestURI()
	req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(payload))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	signer := &apiclient.HMACAuth{BrokerID: brokerID, SecretKey: secret}
	require.NoError(t, signer.ApplyAuth(req))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func setupBrokerTrajectoryFixture(t *testing.T) (*Server, store.Store, *storage.LocalStorage, *store.Project, *store.Agent, *store.RuntimeBroker, []byte, *store.RuntimeBroker, []byte) {
	t.Helper()
	srv, s := testServer(t)
	setTrajectoryCaptureExperimentForTest(t, srv, true)

	loc, err := storage.NewLocal(storage.Config{
		Provider:  storage.ProviderLocal,
		Bucket:    "local",
		LocalPath: t.TempDir(),
	})
	require.NoError(t, err)
	srv.SetStorage(loc)

	ctx := context.Background()
	proj := &store.Project{
		ID:   api.NewUUID(),
		Name: "brk-traj-proj",
		Slug: "brk-traj-proj",
	}
	require.NoError(t, s.CreateProject(ctx, proj))

	broker1, key1 := newOnboardingSigningBroker(t, s, "traj-broker-1")
	broker2, key2 := newOnboardingSigningBroker(t, s, "traj-broker-2")

	runID := "run-" + api.NewUUID()
	agent := &store.Agent{
		ID:              api.NewUUID(),
		Slug:            "brk-traj-agent",
		Name:            "brk-traj-agent",
		ProjectID:       proj.ID,
		RuntimeBrokerID: broker1.ID,
		RunID:           runID,
		OwnerID:         DevUserID,
		CreatedBy:       DevUserID,
		AppliedConfig: &store.AgentAppliedConfig{
			Harness:       "claude",
			HarnessConfig: "claude-default",
		},
		Template: "developer",
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	_, err = s.EnsureRunAttribution(ctx, &store.RunAttribution{
		RunID:         runID,
		HubID:         srv.usageLedgerHubID(),
		ProjectID:     proj.ID,
		AgentID:       agent.ID,
		AgentSlug:     agent.Slug,
		Harness:       "claude",
		HarnessConfig: "claude-default",
		Template:      "developer",
	})
	require.NoError(t, err)

	return srv, s, loc, proj, agent, broker1, key1, broker2, key2
}

func TestBrokerTrajectorySegments_HappyPathAndAuthz(t *testing.T) {
	ctx := context.Background()
	srv, s, loc, proj, agent, broker1, key1, broker2, key2 := setupBrokerTrajectoryFixture(t)

	raw := []byte("{\"type\":\"turn\",\"seq\":1}\n")
	comp, rawSHA, compSHA := gzipAndHash(t, raw)

	prepReq := telemetrycontract.TrajectorySegmentPrepareRequest{
		RunID:              agent.RunID,
		Harness:            "claude",
		NativeRelativePath: ".claude/projects/-workspace/sess-brk.jsonl",
		ArtifactKind:       telemetrycontract.TrajectoryArtifactKindJSONLAppend,
		Generation:         0,
		ByteStart:          0,
		ByteEnd:            int64(len(raw)),
		RawSHA256:          rawSHA,
		CompressedSize:     int64(len(comp)),
		CompressedSHA256:   compSHA,
		ContentEncoding:    telemetrycontract.TrajectoryContentEncodingGzip,
		IncompleteTail:     true,
		SessionID:          "sess-brk",
	}

	// 1. Happy path end-to-end on local storage: prepare -> proxied PUT -> commit.
	prepRec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker1.ID, agent.ID), prepReq)
	require.Equal(t, http.StatusOK, prepRec.Code, prepRec.Body.String())

	var prepResp telemetrycontract.TrajectorySegmentPrepareResponse
	require.NoError(t, json.Unmarshal(prepRec.Body.Bytes(), &prepResp))
	assert.Equal(t, telemetrycontract.TrajectorySegmentStateUploadRequired, prepResp.State)
	require.NotNil(t, prepResp.Upload)
	assert.Contains(t, prepResp.Upload.URL, telemetrycontract.TrajectorySegmentContentBrokerPath(broker1.ID, agent.ID, prepResp.SegmentID))

	putRec := doBrokerSignedPut(t, srv, broker1.ID, key1, prepResp.Upload.URL, comp, prepResp.Upload.Headers)
	require.Equal(t, http.StatusOK, putRec.Code, putRec.Body.String())

	commitRec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsCommitBrokerPath(broker1.ID, agent.ID),
		telemetrycontract.TrajectorySegmentCommitRequest{SegmentID: prepResp.SegmentID})
	require.Equal(t, http.StatusOK, commitRec.Code, commitRec.Body.String())

	var commitResp telemetrycontract.TrajectorySegmentCommitResponse
	require.NoError(t, json.Unmarshal(commitRec.Body.Bytes(), &commitResp))
	assert.Equal(t, telemetrycontract.TrajectorySegmentStateCommitted, commitResp.State)
	assert.Equal(t, prepResp.SegmentID, commitResp.SegmentID)

	// Verify DB row and manifest both record upload_source = broker_harvest.
	segRow, err := s.GetTrajectorySegment(ctx, prepResp.SegmentID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryUploadSourceBrokerHarvest, segRow.UploadSource)

	rc, _, err := loc.Download(ctx, commitResp.ManifestPath)
	require.NoError(t, err)
	manifestRaw, err := io.ReadAll(rc)
	_ = rc.Close()
	require.NoError(t, err)

	var manifest telemetrycontract.TrajectorySegmentManifest
	require.NoError(t, json.Unmarshal(manifestRaw, &manifest))
	assert.Equal(t, telemetrycontract.TrajectoryManifestSchemaVersion, manifest.SchemaVersion)
	assert.Equal(t, telemetrycontract.TrajectoryUploadSourceBrokerHarvest, manifest.UploadSource)

	// 2. Wrong broker calling broker1's path -> 403.
	wrongPathRec := doBrokerSignedRequest(t, srv, broker2.ID, key2, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker1.ID, agent.ID), prepReq)
	assert.Equal(t, http.StatusForbidden, wrongPathRec.Code)

	// 3. Non-owning broker calling its own path for broker1's agent -> 403.
	nonOwnerRec := doBrokerSignedRequest(t, srv, broker2.ID, key2, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker2.ID, agent.ID), prepReq)
	assert.Equal(t, http.StatusForbidden, nonOwnerRec.Code)

	// 4. Missing run_id -> 400 invalid_segment with reason=run_mismatch.
	missingRunReq := prepReq
	missingRunReq.RunID = ""
	missingRunReq.Generation = 1
	missingRunRec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker1.ID, agent.ID), missingRunReq)
	require.Equal(t, http.StatusBadRequest, missingRunRec.Code, missingRunRec.Body.String())
	assert.Equal(t, telemetrycontract.TrajectoryErrInvalidSegment, decodeTrajectoryErrorCode(t, missingRunRec))
	assert.Contains(t, missingRunRec.Body.String(), "run_mismatch")

	// 5. Unknown / mismatched run_id -> 400 invalid_segment with reason=run_mismatch.
	otherAgent := &store.Agent{
		ID:              api.NewUUID(),
		Slug:            "other-agent",
		Name:            "other-agent",
		ProjectID:       proj.ID,
		RuntimeBrokerID: broker1.ID,
		RunID:           "run-other-agent",
		OwnerID:         DevUserID,
		CreatedBy:       DevUserID,
	}
	require.NoError(t, s.CreateAgent(ctx, otherAgent))
	_, err = s.EnsureRunAttribution(ctx, &store.RunAttribution{
		RunID:     "run-other-agent",
		HubID:     srv.usageLedgerHubID(),
		ProjectID: proj.ID,
		AgentID:   otherAgent.ID,
		AgentSlug: otherAgent.Slug,
		Harness:   "claude",
	})
	require.NoError(t, err)

	mismatchRunReq := prepReq
	mismatchRunReq.RunID = "run-other-agent"
	mismatchRunReq.Generation = 2
	mismatchRunRec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker1.ID, agent.ID), mismatchRunReq)
	require.Equal(t, http.StatusBadRequest, mismatchRunRec.Code, mismatchRunRec.Body.String())
	assert.Equal(t, telemetrycontract.TrajectoryErrInvalidSegment, decodeTrajectoryErrorCode(t, mismatchRunRec))
	assert.Contains(t, mismatchRunRec.Body.String(), "run_mismatch")

	// 6. Mid-delete (deleting & finalizing) and soft-deleted agents are accepted.
	for i, mutate := range []func(*store.Agent){
		func(a *store.Agent) { a.DeletionState = store.DeletionStateDeleting },
		func(a *store.Agent) { a.DeletionState = store.DeletionStateFinalizing },
		func(a *store.Agent) {
			a.DeletionState = ""
			a.DeletedAt = time.Now().UTC().Truncate(time.Microsecond)
		},
	} {
		ag, err := s.GetAgent(ctx, agent.ID)
		require.NoError(t, err)
		mutate(ag)
		require.NoError(t, s.UpdateAgent(ctx, ag))

		req := prepReq
		req.Generation = 10 + i
		rec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
			telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker1.ID, agent.ID), req)
		require.Equal(t, http.StatusOK, rec.Code, "state case %d: %s", i, rec.Body.String())
	}

	// 7. Policy off -> 403 capture_disabled; metadata_only -> 403 capture_metadata_only.
	proj.Annotations = map[string]string{projectSettingTrajectoryCapture: telemetrycontract.TrajectoryCapturePolicyOff}
	require.NoError(t, s.UpdateProject(ctx, proj))
	offRec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker1.ID, agent.ID), prepReq)
	require.Equal(t, http.StatusForbidden, offRec.Code, offRec.Body.String())
	assert.Equal(t, telemetrycontract.TrajectoryErrCaptureDisabled, decodeTrajectoryErrorCode(t, offRec))

	proj.Annotations = map[string]string{projectSettingTrajectoryCapture: telemetrycontract.TrajectoryCapturePolicyMetadataOnly}
	require.NoError(t, s.UpdateProject(ctx, proj))
	metaRec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker1.ID, agent.ID), prepReq)
	require.Equal(t, http.StatusForbidden, metaRec.Code, metaRec.Body.String())
	assert.Equal(t, telemetrycontract.TrajectoryErrCaptureMetadataOnly, decodeTrajectoryErrorCode(t, metaRec))

	// 8. Experiment off -> 404.
	setTrajectoryCaptureExperimentForTest(t, srv, false)
	expOffRec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker1.ID, agent.ID), prepReq)
	assert.Equal(t, http.StatusNotFound, expOffRec.Code)
}

func TestBrokerTrajectorySegments_ObjectConflictVsMissing(t *testing.T) {
	ctx := context.Background()
	srv, _, loc, _, agent, broker1, key1, _, _ := setupBrokerTrajectoryFixture(t)

	raw := []byte("{\"seq\":42}\n")
	comp, rawSHA, compSHA := gzipAndHash(t, raw)

	prepReq := telemetrycontract.TrajectorySegmentPrepareRequest{
		RunID:              agent.RunID,
		Harness:            "claude",
		NativeRelativePath: ".claude/projects/-workspace/sess-conflict.jsonl",
		ArtifactKind:       telemetrycontract.TrajectoryArtifactKindJSONLAppend,
		Generation:         0,
		ByteStart:          0,
		ByteEnd:            int64(len(raw)),
		RawSHA256:          rawSHA,
		CompressedSize:     int64(len(comp)),
		CompressedSHA256:   compSHA,
		ContentEncoding:    telemetrycontract.TrajectoryContentEncodingGzip,
	}

	prepRec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsPrepareBrokerPath(broker1.ID, agent.ID), prepReq)
	require.Equal(t, http.StatusOK, prepRec.Code)
	var prepResp telemetrycontract.TrajectorySegmentPrepareResponse
	require.NoError(t, json.Unmarshal(prepRec.Body.Bytes(), &prepResp))

	// Missing object -> 422 segment_verification_failed (retryable).
	commitMissing := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsCommitBrokerPath(broker1.ID, agent.ID),
		telemetrycontract.TrajectorySegmentCommitRequest{SegmentID: prepResp.SegmentID})
	require.Equal(t, http.StatusUnprocessableEntity, commitMissing.Code, commitMissing.Body.String())
	assert.Equal(t, telemetrycontract.TrajectoryErrVerificationFailed, decodeTrajectoryErrorCode(t, commitMissing))

	// Existing object at immutable path with wrong size -> 409 segment_object_conflict (terminal).
	_, err := loc.Upload(ctx, prepResp.ObjectPath, bytes.NewReader([]byte("short")), storage.UploadOptions{})
	require.NoError(t, err)
	commitWrongSize := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectorySegmentsCommitBrokerPath(broker1.ID, agent.ID),
		telemetrycontract.TrajectorySegmentCommitRequest{SegmentID: prepResp.SegmentID})
	require.Equal(t, http.StatusConflict, commitWrongSize.Code, commitWrongSize.Body.String())
	assert.Equal(t, telemetrycontract.TrajectoryErrObjectConflict, decodeTrajectoryErrorCode(t, commitWrongSize))
}

func TestBrokerTrajectoryHarvests_ReportReplayAndValidation(t *testing.T) {
	ctx := context.Background()
	srv, s, _, _, agent, broker1, key1, broker2, key2 := setupBrokerTrajectoryFixture(t)

	oldest := "2026-11-28T19:50:00Z"
	req := telemetrycontract.TrajectoryHarvestReportRequest{
		HarvestID:          "harv-uuid-1",
		RunID:              agent.RunID,
		Trigger:            telemetrycontract.TrajectoryHarvestTriggerPreDelete,
		Outcome:            telemetrycontract.TrajectoryHarvestOutcomeComplete,
		Trajectory:         telemetrycontract.TrajectoryHarvestTrajectoryShipped,
		Usage:              telemetrycontract.TrajectoryHarvestUsageDrained,
		SegmentsCommitted:  3,
		BytesCommitted:     4096,
		PendingBytes:       0,
		OldestPendingAt:    &oldest,
		UsageEventsDrained: 12,
		UsageEventsPending: 0,
		StartedAt:          "2026-11-28T20:00:00Z",
		FinishedAt:         "2026-11-28T20:00:02Z",
	}

	// Happy path.
	rec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectoryHarvestsBrokerPath(broker1.ID, agent.ID), req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp telemetrycontract.TrajectoryHarvestReportResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Recorded)

	latest, err := s.LatestTrajectoryHarvest(ctx, agent.ID, telemetrycontract.TrajectoryHarvestTriggerPreDelete)
	require.NoError(t, err)
	assert.Equal(t, "harv-uuid-1", latest.HarvestID)
	assert.Equal(t, 3, latest.SegmentsCommitted)

	// Idempotent replay -> 200 {"recorded": true}, original row intact.
	replayReq := req
	replayReq.Outcome = telemetrycontract.TrajectoryHarvestOutcomeFailed
	replayRec := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectoryHarvestsBrokerPath(broker1.ID, agent.ID), replayReq)
	require.Equal(t, http.StatusOK, replayRec.Code, replayRec.Body.String())
	latestAfter, err := s.LatestTrajectoryHarvest(ctx, agent.ID, telemetrycontract.TrajectoryHarvestTriggerPreDelete)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryHarvestOutcomeComplete, latestAfter.Outcome)

	// Bad enums and over-length error -> 400.
	badCases := []struct {
		name   string
		mutate func(*telemetrycontract.TrajectoryHarvestReportRequest)
	}{
		{"bad_trigger", func(r *telemetrycontract.TrajectoryHarvestReportRequest) { r.Trigger = "force_delete" }},
		{"bad_outcome", func(r *telemetrycontract.TrajectoryHarvestReportRequest) { r.Outcome = "forced" }},
		{"bad_trajectory", func(r *telemetrycontract.TrajectoryHarvestReportRequest) { r.Trajectory = "bogus" }},
		{"bad_usage", func(r *telemetrycontract.TrajectoryHarvestReportRequest) { r.Usage = "bogus" }},
		{"error_too_long", func(r *telemetrycontract.TrajectoryHarvestReportRequest) { r.Error = strings.Repeat("a", 1025) }},
		{"missing_harvest_id", func(r *telemetrycontract.TrajectoryHarvestReportRequest) { r.HarvestID = "" }},
		{"bad_started_at", func(r *telemetrycontract.TrajectoryHarvestReportRequest) { r.StartedAt = "not-a-time" }},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			bad := req
			bad.HarvestID = "harv-" + tc.name
			tc.mutate(&bad)
			r := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
				telemetrycontract.TrajectoryHarvestsBrokerPath(broker1.ID, agent.ID), bad)
			assert.Equal(t, http.StatusBadRequest, r.Code, r.Body.String())
		})
	}

	// Non-owning broker -> 403.
	nonOwner := doBrokerSignedRequest(t, srv, broker2.ID, key2, "", http.MethodPost,
		telemetrycontract.TrajectoryHarvestsBrokerPath(broker2.ID, agent.ID), req)
	assert.Equal(t, http.StatusForbidden, nonOwner.Code)

	// Experiment off -> 404.
	setTrajectoryCaptureExperimentForTest(t, srv, false)
	expOff := doBrokerSignedRequest(t, srv, broker1.ID, key1, "", http.MethodPost,
		telemetrycontract.TrajectoryHarvestsBrokerPath(broker1.ID, agent.ID), req)
	assert.Equal(t, http.StatusNotFound, expOff.Code)
}
