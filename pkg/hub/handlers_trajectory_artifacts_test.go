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
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

func setupTrajectoryArtifactFixture(t *testing.T) (*Server, store.Store, *storage.LocalStorage, *store.Project, *store.Agent, string) {
	t.Helper()
	srv, s := testServer(t)
	enableTrajectoryCaptureExperiment(t, srv)

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
		Name: "traj-l1-proj",
		Slug: "traj-l1-proj",
	}
	require.NoError(t, s.CreateProject(ctx, proj))

	billingUser := "billing-user-42"
	hcDigest := "sha256:hcdigest123"
	tplDigest := "sha256:tpldigest456"
	runID := "run-" + api.NewUUID()
	agent := &store.Agent{
		ID:        api.NewUUID(),
		Slug:      "traj-l1-agent",
		Name:      "traj-l1-agent",
		ProjectID: proj.ID,
		RunID:     runID,
		OwnerID:   DevUserID,
		CreatedBy: DevUserID,
		Ancestry:  []string{DevUserID, "parent-agent-99"},
		AppliedConfig: &store.AgentAppliedConfig{
			Harness:       "claude",
			HarnessConfig: "claude-default",
		},
		Template: "developer",
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	_, err = s.EnsureRunAttribution(ctx, &store.RunAttribution{
		RunID:               runID,
		LaunchID:            "launch-abc",
		HubID:               srv.usageLedgerHubID(),
		ProjectID:           proj.ID,
		AgentID:             agent.ID,
		AgentSlug:           agent.Slug,
		BillingUserID:       &billingUser,
		Harness:             "claude",
		HarnessConfig:       "claude-default",
		HarnessConfigDigest: &hcDigest,
		Template:            "developer",
		TemplateDigest:      &tplDigest,
		LabelsAtLaunch: map[string]string{
			"env":  "staging",
			"team": "tokenomics",
		},
	})
	require.NoError(t, err)

	tok, cred, err := srv.agentTokenService.SignAgentToken(AgentTokenGrant{
		AgentID:   agent.ID,
		ProjectID: proj.ID,
		Scopes:    []AgentTokenScope{ScopeAgentStatusUpdate},
	}, runID)
	require.NoError(t, err)
	require.NoError(t, s.CreateAgentCredential(ctx, cred))

	return srv, s, loc, proj, agent, tok
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func gunzipBytes(t *testing.T, gzData []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(gzData))
	require.NoError(t, err)
	defer func() { _ = r.Close() }()
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	return out
}

func readStorageBytes(t *testing.T, stor storage.Storage, path string) []byte {
	t.Helper()
	rc, _, err := stor.Download(context.Background(), path)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return b
}

func TestTrajectoryArtifacts_ExperimentOffAndAuth(t *testing.T) {
	srv, s, _, proj, agent, tok := setupTrajectoryArtifactFixture(t)
	ctx := context.Background()

	// Wrong agent -> 403 on prepare, content, commit.
	otherAgent := &store.Agent{
		ID:        api.NewUUID(),
		Slug:      "other-agent",
		Name:      "other-agent",
		ProjectID: proj.ID,
		RunID:     "run-" + api.NewUUID(),
		OwnerID:   DevUserID,
		CreatedBy: DevUserID,
	}
	require.NoError(t, s.CreateAgent(ctx, otherAgent))
	otherTok, otherCred, err := srv.agentTokenService.SignAgentToken(AgentTokenGrant{
		AgentID:   otherAgent.ID,
		ProjectID: proj.ID,
		Scopes:    []AgentTokenScope{ScopeAgentStatusUpdate},
	}, otherAgent.RunID)
	require.NoError(t, err)
	require.NoError(t, s.CreateAgentCredential(ctx, otherCred))

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID)},
		{http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID)},
		{http.MethodPut, telemetrycontract.TrajectoryArtifactContentAgentPath(agent.ID, "art-1")},
	} {
		rec := doTrajectoryAgentJSON(t, srv, tc.method, tc.path, otherTok, map[string]any{})
		assert.Equal(t, http.StatusForbidden, rec.Code, "wrong agent %s %s", tc.method, tc.path)
	}

	// Experiment off -> 404 on prepare, content, commit.
	srvOff, _ := testServer(t)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID)},
		{http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID)},
		{http.MethodPut, telemetrycontract.TrajectoryArtifactContentAgentPath(agent.ID, "art-1")},
	} {
		rec := doTrajectoryAgentJSON(t, srvOff, tc.method, tc.path, tok, map[string]any{})
		assert.Equal(t, http.StatusNotFound, rec.Code, "experiment off %s %s", tc.method, tc.path)
	}
}

func TestTrajectoryArtifacts_PolicyMatrix(t *testing.T) {
	srv, _, _, _, agent, tok := setupTrajectoryArtifactFixture(t)

	usageGZ, _, usageSHA := gzipAndHash(t, []byte(`{"dedupe_key":"claude:r1","event_ts":"2026-11-28T20:00:00Z","status":"ok","calls":1,"input":10,"output":5}`+"\n"))
	invJSON := []byte(fmt.Sprintf(`{"schema_version":1,"run_id":%q,"parser_version":"claude-1","inventory_seq":1,"chunks":[],"final":true}`, agent.RunID))
	atifGZ, _, atifSHA := gzipAndHash(t, []byte(`{"schema_version":"ATIF-v1.8","session_id":"s1","agent":{"name":"claude"},"steps":[]}`))

	usageReq := telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeUsageRecords,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		ChunkID:         "chunk-pol-1",
		InventorySeq:    1,
		RowCount:        1,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingGzip,
		Size:            int64(len(usageGZ)),
		SHA256:          usageSHA,
	}
	invReq := telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeRunInventory,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		InventorySeq:    1,
		Final:           true,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingIdentity,
		Size:            int64(len(invJSON)),
		SHA256:          sha256Hex(invJSON),
	}
	atifReq := telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:       telemetrycontract.TrajectoryArtifactTypeATIF,
		ParserVersion:      "claude-1",
		Harness:            "claude",
		SessionID:          "s1",
		NativeRelativePath: ".claude/projects/-workspace/s1.jsonl",
		ParsedOffset:       100,
		ATIFSchemaVersion:  telemetrycontract.TrajectoryATIFSchemaVersionV18,
		FileFormat:         telemetrycontract.TrajectoryFileFormatJSON,
		ContentEncoding:    telemetrycontract.TrajectoryContentEncodingGzip,
		Size:               int64(len(atifGZ)),
		SHA256:             atifSHA,
	}

	// Policy = off: refuses all three + parse_failed with 403 capture_disabled.
	srv.trajectoryCapturePolicyOverride = func(_ context.Context, _ *store.Project) string {
		return telemetrycontract.TrajectoryCapturePolicyOff
	}
	for name, req := range map[string]any{"usage_records": usageReq, "run_inventory": invReq, "atif": atifReq} {
		rec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, req)
		assert.Equal(t, http.StatusForbidden, rec.Code, "off/%s", name)
		assert.Equal(t, telemetrycontract.TrajectoryErrCaptureDisabled, decodeTrajectoryErrorCode(t, rec), "off/%s", name)
	}
	pfRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactCommitRequest{
		SessionMeta: &telemetrycontract.TrajectorySessionMetaInput{
			Harness:            "claude",
			ParserVersion:      "claude-1",
			NativeRelativePath: ".claude/projects/-workspace/s1.jsonl",
			ParsedOffset:       0,
			NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusParseFailed,
			ParseErrorCount:    3,
			FirstError:         "boom",
		},
	})
	assert.Equal(t, http.StatusForbidden, pfRec.Code)
	assert.Equal(t, telemetrycontract.TrajectoryErrCaptureDisabled, decodeTrajectoryErrorCode(t, pfRec))

	// Policy = metadata_only: accepts usage_records + run_inventory, refuses atif with 403 capture_metadata_only.
	srv.trajectoryCapturePolicyOverride = func(_ context.Context, _ *store.Project) string {
		return telemetrycontract.TrajectoryCapturePolicyMetadataOnly
	}
	rec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, usageReq)
	assert.Equal(t, http.StatusOK, rec.Code, "metadata_only/usage_records: %s", rec.Body.String())

	rec = doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, invReq)
	assert.Equal(t, http.StatusOK, rec.Code, "metadata_only/run_inventory: %s", rec.Body.String())

	rec = doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, atifReq)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, telemetrycontract.TrajectoryErrCaptureMetadataOnly, decodeTrajectoryErrorCode(t, rec))

	// Policy = full: accepts atif as well.
	srv.trajectoryCapturePolicyOverride = func(_ context.Context, _ *store.Project) string {
		return telemetrycontract.TrajectoryCapturePolicyFull
	}
	rec = doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, atifReq)
	assert.Equal(t, http.StatusOK, rec.Code, "full/atif: %s", rec.Body.String())
}

func TestTrajectoryArtifacts_UsageRecordsStampingInventoryAndReplay(t *testing.T) {
	srv, s, loc, proj, agent, tok := setupTrajectoryArtifactFixture(t)
	ctx := context.Background()

	// Client attempts to spoof dimension fields; Hub must overwrite all of them from run_attribution.
	rawLines := strings.Join([]string{
		`{"dedupe_key":"claude:req_1","event_ts":"2026-11-28T20:01:00Z","status":"ok","calls":1,"input":100,"output":25,"cache_read":10,"hub_id":"spoof-hub","project_id":"spoof-proj","billing_user_id":"spoof-user","agent_id":"spoof-agent","run_id":"spoof-run","labels":[{"key":"spoof","value":"yes"}],"harness_config":"spoof-hc","template":"spoof-tpl","release_seq":999,"chunk_id":"spoof-chunk","row_ordinal":42}`,
		`{"dedupe_key":"claude:req_2","event_ts":"2026-11-28T20:02:00Z","status":"error","calls":1,"input":null,"output":0}`,
	}, "\n") + "\n"
	comp, _, compSHA := gzipAndHash(t, []byte(rawLines))

	// 1. Prepare usage_records chunk.
	prepRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeUsageRecords,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		ChunkID:         "chunk-001",
		InventorySeq:    2,
		RowCount:        2,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingGzip,
		Size:            int64(len(comp)),
		SHA256:          compSHA,
	})
	require.Equal(t, http.StatusOK, prepRec.Code, prepRec.Body.String())
	var prepResp telemetrycontract.TrajectoryArtifactPrepareResponse
	require.NoError(t, json.Unmarshal(prepRec.Body.Bytes(), &prepResp))
	assert.Equal(t, telemetrycontract.TrajectoryArtifactStateUploadRequired, prepResp.State)
	require.NotNil(t, prepResp.Upload)
	assert.Contains(t, prepResp.ObjectPath, "/analytics/v1/usage_records/")

	// 2. Upload pending bytes via Hub-proxied HMAC PUT; verify pending path is NOT under analytics/.
	putRec := doTrajectoryAgentPut(t, srv, prepResp.Upload.URL, tok, comp, prepResp.Upload.Headers)
	require.Equal(t, http.StatusOK, putRec.Code, putRec.Body.String())

	artRow, err := s.GetTrajectoryArtifact(ctx, prepResp.ArtifactID)
	require.NoError(t, err)
	assert.Contains(t, artRow.PendingObjectPath, "/trajectories/v1/l1-pending/")
	assert.NotContains(t, artRow.PendingObjectPath, "/analytics/")
	pendingExists, err := loc.Exists(ctx, artRow.PendingObjectPath)
	require.NoError(t, err)
	assert.True(t, pendingExists)

	// Final object must NOT exist before commit.
	finalExists, err := loc.Exists(ctx, prepResp.ObjectPath)
	require.NoError(t, err)
	assert.False(t, finalExists)

	// 3. Commit usage_records chunk.
	commitRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactCommitRequest{
		ArtifactID: prepResp.ArtifactID,
	})
	require.Equal(t, http.StatusOK, commitRec.Code, commitRec.Body.String())
	var commitResp telemetrycontract.TrajectoryArtifactCommitResponse
	require.NoError(t, json.Unmarshal(commitRec.Body.Bytes(), &commitResp))
	assert.Equal(t, telemetrycontract.TrajectoryArtifactStateCommitted, commitResp.State)
	assert.NotEmpty(t, commitResp.StampedSHA256)
	assert.NotEqual(t, compSHA, commitResp.StampedSHA256)

	// Pending object deleted after commit; final stamped object exists.
	pendingExists, err = loc.Exists(ctx, artRow.PendingObjectPath)
	require.NoError(t, err)
	assert.False(t, pendingExists)

	stampedGZ := readStorageBytes(t, loc, prepResp.ObjectPath)
	assert.Equal(t, commitResp.StampedSHA256, sha256Hex(stampedGZ))
	stampedPlain := gunzipBytes(t, stampedGZ)

	// Verify stamped rows carry run_attribution dimensions and zero spoofed values.
	sc := bufio.NewScanner(bytes.NewReader(stampedPlain))
	var rows []map[string]any
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		rows = append(rows, m)
	}
	require.Len(t, rows, 2)
	for i, row := range rows {
		assert.Equal(t, srv.usageLedgerHubID(), row["hub_id"])
		assert.Equal(t, srv.usageLedgerHubID(), row["origin_hub_id"])
		assert.Equal(t, proj.ID, row["project_id"])
		assert.Equal(t, "billing-user-42", row["billing_user_id"])
		assert.Equal(t, agent.ID, row["agent_id"])
		assert.Equal(t, agent.Slug, row["agent_slug"])
		assert.Equal(t, "parent-agent-99", row["parent_agent_id"])
		assert.Equal(t, agent.RunID, row["run_id"])
		assert.Equal(t, "launch-abc", row["launch_id"])
		assert.Equal(t, "claude-default", row["harness_config"])
		assert.Equal(t, "sha256:hcdigest123", row["harness_config_digest"])
		assert.Equal(t, "developer", row["template"])
		assert.Equal(t, "sha256:tpldigest456", row["template_digest"])
		assert.Equal(t, float64(2), row["release_seq"])
		assert.Equal(t, "chunk-001", row["chunk_id"])
		assert.Equal(t, float64(i), row["row_ordinal"])
		labels, ok := row["labels"].([]any)
		require.True(t, ok)
		require.Len(t, labels, 2)
	}

	// 4. Idempotent prepare replay with same sha256 -> 200 committed; different sha256 -> 409 artifact_conflict.
	replayRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeUsageRecords,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		ChunkID:         "chunk-001",
		InventorySeq:    2,
		RowCount:        2,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingGzip,
		Size:            int64(len(comp)),
		SHA256:          compSHA,
	})
	require.Equal(t, http.StatusOK, replayRec.Code)
	var replayResp telemetrycontract.TrajectoryArtifactPrepareResponse
	require.NoError(t, json.Unmarshal(replayRec.Body.Bytes(), &replayResp))
	assert.Equal(t, telemetrycontract.TrajectoryArtifactStateCommitted, replayResp.State)

	conflictRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeUsageRecords,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		ChunkID:         "chunk-001",
		InventorySeq:    2,
		RowCount:        2,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingGzip,
		Size:            int64(len(comp)),
		SHA256:          strings.Repeat("f", 64),
	})
	assert.Equal(t, http.StatusConflict, conflictRec.Code)
	assert.Equal(t, telemetrycontract.TrajectoryErrArtifactConflict, decodeTrajectoryErrorCode(t, conflictRec))

	// 5. Incomplete run_inventory (wrong expected_row_count or missing chunk) -> 422 inventory_incomplete.
	badInv := telemetrycontract.TrajectoryRunInventory{
		SchemaVersion: 1,
		RunID:         agent.RunID,
		ParserVersion: "claude-1",
		InventorySeq:  1,
		Chunks: []telemetrycontract.TrajectoryRunInventoryChunk{
			{ChunkID: "chunk-001", ExpectedRowCount: 99, SHA256: compSHA},
		},
		Final: false,
	}
	badInvBytes, err := json.Marshal(badInv)
	require.NoError(t, err)
	badPrep := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeRunInventory,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		InventorySeq:    1,
		Final:           false,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingIdentity,
		Size:            int64(len(badInvBytes)),
		SHA256:          sha256Hex(badInvBytes),
	})
	require.Equal(t, http.StatusOK, badPrep.Code)
	var badPrepResp telemetrycontract.TrajectoryArtifactPrepareResponse
	require.NoError(t, json.Unmarshal(badPrep.Body.Bytes(), &badPrepResp))
	require.Equal(t, http.StatusOK, doTrajectoryAgentPut(t, srv, badPrepResp.Upload.URL, tok, badInvBytes, badPrepResp.Upload.Headers).Code)
	badCommit := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactCommitRequest{
		ArtifactID: badPrepResp.ArtifactID,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, badCommit.Code)
	assert.Equal(t, telemetrycontract.TrajectoryErrInventoryIncomplete, decodeTrajectoryErrorCode(t, badCommit))

	// 6. Complete run_inventory -> 200, rewrites chunk sha256 to stamped_sha256.
	goodInv := telemetrycontract.TrajectoryRunInventory{
		SchemaVersion: 1,
		RunID:         agent.RunID,
		ParserVersion: "claude-1",
		InventorySeq:  2,
		Chunks: []telemetrycontract.TrajectoryRunInventoryChunk{
			{ChunkID: "chunk-001", ExpectedRowCount: 2, SHA256: compSHA},
		},
		Final: true,
	}
	goodInvBytes, err := json.Marshal(goodInv)
	require.NoError(t, err)
	invPrep := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeRunInventory,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		InventorySeq:    2,
		Final:           true,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingIdentity,
		Size:            int64(len(goodInvBytes)),
		SHA256:          sha256Hex(goodInvBytes),
	})
	require.Equal(t, http.StatusOK, invPrep.Code)
	var invPrepResp telemetrycontract.TrajectoryArtifactPrepareResponse
	require.NoError(t, json.Unmarshal(invPrep.Body.Bytes(), &invPrepResp))
	require.Equal(t, http.StatusOK, doTrajectoryAgentPut(t, srv, invPrepResp.Upload.URL, tok, goodInvBytes, invPrepResp.Upload.Headers).Code)
	invCommit := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactCommitRequest{
		ArtifactID: invPrepResp.ArtifactID,
	})
	require.Equal(t, http.StatusOK, invCommit.Code, invCommit.Body.String())

	storedInvBytes := readStorageBytes(t, loc, invPrepResp.ObjectPath)
	var storedInv telemetrycontract.TrajectoryRunInventory
	require.NoError(t, json.Unmarshal(storedInvBytes, &storedInv))
	require.Len(t, storedInv.Chunks, 1)
	assert.Equal(t, commitResp.StampedSHA256, storedInv.Chunks[0].SHA256)

	// 7. Zero-row run_inventory is valid and commits cleanly.
	zeroInv := telemetrycontract.TrajectoryRunInventory{
		SchemaVersion: 1,
		RunID:         agent.RunID,
		ParserVersion: "claude-2",
		InventorySeq:  1,
		Chunks:        []telemetrycontract.TrajectoryRunInventoryChunk{},
		Final:         true,
	}
	zeroInvBytes, err := json.Marshal(zeroInv)
	require.NoError(t, err)
	zPrep := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeRunInventory,
		ParserVersion:   "claude-2",
		Harness:         "claude",
		InventorySeq:    1,
		Final:           true,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingIdentity,
		Size:            int64(len(zeroInvBytes)),
		SHA256:          sha256Hex(zeroInvBytes),
	})
	require.Equal(t, http.StatusOK, zPrep.Code)
	var zPrepResp telemetrycontract.TrajectoryArtifactPrepareResponse
	require.NoError(t, json.Unmarshal(zPrep.Body.Bytes(), &zPrepResp))
	require.Equal(t, http.StatusOK, doTrajectoryAgentPut(t, srv, zPrepResp.Upload.URL, tok, zeroInvBytes, zPrepResp.Upload.Headers).Code)
	zCommit := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactCommitRequest{
		ArtifactID: zPrepResp.ArtifactID,
	})
	require.Equal(t, http.StatusOK, zCommit.Code, zCommit.Body.String())
}

func TestTrajectoryArtifacts_ATIFStampingSessionPrecedenceAndL0NormalizeStatus(t *testing.T) {
	srv, s, loc, proj, agent, tok := setupTrajectoryArtifactFixture(t)
	ctx := context.Background()

	relPath := ".claude/projects/-workspace/sess-atif.jsonl"

	// Create two committed L0 segments: [0, 100) and [100, 300).
	commitL0 := func(start, end int64) string {
		raw := bytes.Repeat([]byte("x"), int(end-start))
		comp, rawSHA, compSHA := gzipAndHash(t, raw)
		prepRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectorySegmentsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectorySegmentPrepareRequest{
			Harness:            "claude",
			NativeRelativePath: relPath,
			ArtifactKind:       telemetrycontract.TrajectoryArtifactKindJSONLAppend,
			Generation:         0,
			ByteStart:          start,
			ByteEnd:            end,
			RawSHA256:          rawSHA,
			CompressedSize:     int64(len(comp)),
			CompressedSHA256:   compSHA,
			ContentEncoding:    telemetrycontract.TrajectoryContentEncodingGzip,
		})
		require.Equal(t, http.StatusOK, prepRec.Code, prepRec.Body.String())
		var pr telemetrycontract.TrajectorySegmentPrepareResponse
		require.NoError(t, json.Unmarshal(prepRec.Body.Bytes(), &pr))
		require.Equal(t, http.StatusOK, doTrajectoryAgentPut(t, srv, pr.Upload.URL, tok, comp, pr.Upload.Headers).Code)
		cm := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectorySegmentsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectorySegmentCommitRequest{
			SegmentID: pr.SegmentID,
		})
		require.Equal(t, http.StatusOK, cm.Code, cm.Body.String())
		return pr.SegmentID
	}
	seg1ID := commitL0(0, 100)
	seg2ID := commitL0(100, 300)

	commitATIF := func(parserVersion string, offset int64, stepsCount int) telemetrycontract.TrajectoryArtifactCommitResponse {
		steps := make([]map[string]any, stepsCount)
		for i := range steps {
			steps[i] = map[string]any{"step_id": i + 1, "source": "user", "message": "hi"}
		}
		doc := map[string]any{
			"schema_version": telemetrycontract.TrajectoryATIFSchemaVersionV18,
			"session_id":     "sess-atif",
			"agent": map[string]any{
				"name": "claude",
				"extra": map[string]any{
					"scion": map[string]any{
						"hub_id":         "spoof-hub",
						"project_id":     "spoof-proj",
						"agent_id":       "spoof-agent",
						"run_id":         "spoof-run",
						"parser_version": "spoof-99",
					},
				},
			},
			"steps": steps,
		}
		raw, err := json.Marshal(doc)
		require.NoError(t, err)
		comp, _, compSHA := gzipAndHash(t, raw)

		prepRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
			ArtifactType:       telemetrycontract.TrajectoryArtifactTypeATIF,
			ParserVersion:      parserVersion,
			Harness:            "claude",
			SessionID:          "sess-atif",
			NativeRelativePath: relPath,
			ParsedOffset:       offset,
			ATIFSchemaVersion:  telemetrycontract.TrajectoryATIFSchemaVersionV18,
			FileFormat:         telemetrycontract.TrajectoryFileFormatJSON,
			ContentEncoding:    telemetrycontract.TrajectoryContentEncodingGzip,
			Size:               int64(len(comp)),
			SHA256:             compSHA,
		})
		require.Equal(t, http.StatusOK, prepRec.Code, prepRec.Body.String())
		var pr telemetrycontract.TrajectoryArtifactPrepareResponse
		require.NoError(t, json.Unmarshal(prepRec.Body.Bytes(), &pr))
		require.Equal(t, http.StatusOK, doTrajectoryAgentPut(t, srv, pr.Upload.URL, tok, comp, pr.Upload.Headers).Code)

		cmRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactCommitRequest{
			ArtifactID: pr.ArtifactID,
			SessionMeta: &telemetrycontract.TrajectorySessionMetaInput{
				StepCount: stepsCount,
			},
		})
		require.Equal(t, http.StatusOK, cmRec.Code, cmRec.Body.String())
		var cmResp telemetrycontract.TrajectoryArtifactCommitResponse
		require.NoError(t, json.Unmarshal(cmRec.Body.Bytes(), &cmResp))
		return cmResp
	}

	// Commit ATIF at (claude-1, 100): stamps agent.extra.scion, marks seg1 normalized, leaves seg2 pending.
	cm1 := commitATIF("claude-1", 100, 2)
	stampedATIF := gunzipBytes(t, readStorageBytes(t, loc, cm1.ObjectPath))
	var atifDoc map[string]any
	require.NoError(t, json.Unmarshal(stampedATIF, &atifDoc))
	scionMeta := atifDoc["agent"].(map[string]any)["extra"].(map[string]any)["scion"].(map[string]any)
	assert.Equal(t, srv.usageLedgerHubID(), scionMeta["hub_id"])
	assert.Equal(t, proj.ID, scionMeta["project_id"])
	assert.Equal(t, agent.ID, scionMeta["agent_id"])
	assert.Equal(t, agent.RunID, scionMeta["run_id"])
	assert.Equal(t, "claude-1", scionMeta["parser_version"])
	assert.Equal(t, "billing-user-42", scionMeta["billing_user_id"])

	seg1, err := s.GetTrajectorySegment(ctx, seg1ID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryNormalizeStatusNormalized, seg1.NormalizeStatus)
	seg2, err := s.GetTrajectorySegment(ctx, seg2ID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryNormalizeStatusPending, seg2.NormalizeStatus)

	// Older offset (claude-1, 50) must NOT overwrite trajectory_sessions.
	_ = commitATIF("claude-1", 50, 1)
	sess, err := s.GetTrajectorySessionByPath(ctx, agent.RunID, relPath)
	require.NoError(t, err)
	assert.Equal(t, "claude-1", sess.ParserVersion)
	assert.Equal(t, int64(100), sess.ParsedOffset)
	assert.Equal(t, cm1.ArtifactID, sess.ATIFArtifactID)

	// Newer parser_version (claude-2, 80) MUST overwrite trajectory_sessions.
	cm2 := commitATIF("claude-2", 80, 3)
	sess, err = s.GetTrajectorySessionByPath(ctx, agent.RunID, relPath)
	require.NoError(t, err)
	assert.Equal(t, "claude-2", sess.ParserVersion)
	assert.Equal(t, int64(80), sess.ParsedOffset)
	assert.Equal(t, cm2.ArtifactID, sess.ATIFArtifactID)
	assert.Equal(t, 3, sess.StepCount)

	// Standalone parse_failed reporting marks remaining pending L0 segment (seg2) parse_failed without clobbering seg1.
	pfRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactCommitRequest{
		SessionMeta: &telemetrycontract.TrajectorySessionMetaInput{
			Harness:            "claude",
			ParserVersion:      "claude-2",
			NativeRelativePath: relPath,
			ParsedOffset:       100,
			NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusParseFailed,
			ParseErrorCount:    3,
			FirstError:         "poisoned line",
		},
	})
	require.Equal(t, http.StatusOK, pfRec.Code, pfRec.Body.String())

	sess, err = s.GetTrajectorySessionByPath(ctx, agent.RunID, relPath)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectorySessionNormalizeStatusParseFailed, sess.NormalizeStatus)
	assert.Equal(t, 3, sess.ParseErrorCount)
	assert.Equal(t, "poisoned line", sess.FirstError)

	seg1, err = s.GetTrajectorySegment(ctx, seg1ID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryNormalizeStatusNormalized, seg1.NormalizeStatus)
	seg2, err = s.GetTrajectorySegment(ctx, seg2ID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryNormalizeStatusParseFailed, seg2.NormalizeStatus)

	// Concurrent ATIF commits on a new path converge on the highest (parser_version, parsed_offset).
	concPath := ".claude/projects/-workspace/sess-conc-http.jsonl"
	var wg sync.WaitGroup
	for i := 1; i <= 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			doc := map[string]any{
				"schema_version": telemetrycontract.TrajectoryATIFSchemaVersionV18,
				"session_id":     "sess-conc-http",
				"agent":          map[string]any{"name": "claude"},
				"steps":          []any{},
			}
			raw, _ := json.Marshal(doc)
			comp, _, compSHA := gzipAndHash(t, raw)
			prRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
				ArtifactType:       telemetrycontract.TrajectoryArtifactTypeATIF,
				ParserVersion:      fmt.Sprintf("claude-%d", (n%2)+1),
				Harness:            "claude",
				SessionID:          "sess-conc-http",
				NativeRelativePath: concPath,
				ParsedOffset:       int64(n * 100),
				ATIFSchemaVersion:  telemetrycontract.TrajectoryATIFSchemaVersionV18,
				FileFormat:         telemetrycontract.TrajectoryFileFormatJSON,
				ContentEncoding:    telemetrycontract.TrajectoryContentEncodingGzip,
				Size:               int64(len(comp)),
				SHA256:             compSHA,
			})
			if prRec.Code != http.StatusOK {
				return
			}
			var pr telemetrycontract.TrajectoryArtifactPrepareResponse
			if err := json.Unmarshal(prRec.Body.Bytes(), &pr); err != nil {
				return
			}
			_ = doTrajectoryAgentPut(t, srv, pr.Upload.URL, tok, comp, pr.Upload.Headers)
			_ = doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactCommitRequest{
				ArtifactID: pr.ArtifactID,
			})
		}(i)
	}
	wg.Wait()
	concSess, err := s.GetTrajectorySessionByPath(ctx, agent.RunID, concPath)
	require.NoError(t, err)
	// Highest parser_version is claude-2 (n in {1, 3, 5}), highest offset is n=5 -> 500.
	assert.Equal(t, "claude-2", concSess.ParserVersion)
	assert.Equal(t, int64(500), concSess.ParsedOffset)
}

func TestTrajectoryArtifacts_CapsAndValidation(t *testing.T) {
	srv, _, _, _, agent, tok := setupTrajectoryArtifactFixture(t)

	// Per-object cap: usage_records > 16 MiB -> 413 artifact_too_large.
	rec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeUsageRecords,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		ChunkID:         "chunk-big",
		InventorySeq:    1,
		RowCount:        1,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingGzip,
		Size:            telemetrycontract.DefaultTrajectoryArtifactMaxBytes + 1,
		SHA256:          strings.Repeat("a", 64),
	})
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, telemetrycontract.TrajectoryErrArtifactTooLarge, decodeTrajectoryErrorCode(t, rec))

	// Per-object cap: ATIF > 64 MiB -> 413 artifact_too_large.
	rec = doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:       telemetrycontract.TrajectoryArtifactTypeATIF,
		ParserVersion:      "claude-1",
		Harness:            "claude",
		SessionID:          "s-big",
		NativeRelativePath: ".claude/projects/-workspace/s-big.jsonl",
		ParsedOffset:       10,
		ATIFSchemaVersion:  telemetrycontract.TrajectoryATIFSchemaVersionV18,
		FileFormat:         telemetrycontract.TrajectoryFileFormatJSON,
		ContentEncoding:    telemetrycontract.TrajectoryContentEncodingGzip,
		Size:               telemetrycontract.DefaultTrajectoryArtifactATIFMaxBytes + 1,
		SHA256:             strings.Repeat("b", 64),
	})
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, telemetrycontract.TrajectoryErrArtifactTooLarge, decodeTrajectoryErrorCode(t, rec))

	// Shared per-run cap (L0 + L1): lower server cap to 500 bytes and verify L1 trips run_cap_exceeded.
	srv.trajectoryMaxRunRawBytes = 500
	rawSeg := bytes.Repeat([]byte("a"), 300)
	compSeg, rawSHA, compSHA := gzipAndHash(t, rawSeg)
	segRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectorySegmentsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectorySegmentPrepareRequest{
		Harness:            "claude",
		NativeRelativePath: ".claude/projects/-workspace/cap.jsonl",
		ArtifactKind:       telemetrycontract.TrajectoryArtifactKindJSONLAppend,
		Generation:         0,
		ByteStart:          0,
		ByteEnd:            300,
		RawSHA256:          rawSHA,
		CompressedSize:     int64(len(compSeg)),
		CompressedSHA256:   compSHA,
		ContentEncoding:    telemetrycontract.TrajectoryContentEncodingGzip,
	})
	require.Equal(t, http.StatusOK, segRec.Code, segRec.Body.String())

	// L1 artifact of 250 bytes pushes L0 (300) + L1 (250) = 550 > 500 -> 413 run_cap_exceeded.
	rec = doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeUsageRecords,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		ChunkID:         "chunk-cap",
		InventorySeq:    1,
		RowCount:        1,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingGzip,
		Size:            250,
		SHA256:          strings.Repeat("c", 64),
	})
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, telemetrycontract.TrajectoryErrRunCapExceeded, decodeTrajectoryErrorCode(t, rec))

	// Forbidden payload field ("prompt") in usage_records fails commit with 422 artifact_verification_failed.
	srv.trajectoryMaxRunRawBytes = 0
	badLines := `{"dedupe_key":"claude:r1","event_ts":"2026-11-28T20:00:00Z","status":"ok","calls":1,"prompt":"secret"}` + "\n"
	badGZ, _, badSHA := gzipAndHash(t, []byte(badLines))
	pRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsPrepareAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactPrepareRequest{
		ArtifactType:    telemetrycontract.TrajectoryArtifactTypeUsageRecords,
		ParserVersion:   "claude-1",
		Harness:         "claude",
		ChunkID:         "chunk-forbidden",
		InventorySeq:    1,
		RowCount:        1,
		ContentEncoding: telemetrycontract.TrajectoryContentEncodingGzip,
		Size:            int64(len(badGZ)),
		SHA256:          badSHA,
	})
	require.Equal(t, http.StatusOK, pRec.Code)
	var pResp telemetrycontract.TrajectoryArtifactPrepareResponse
	require.NoError(t, json.Unmarshal(pRec.Body.Bytes(), &pResp))
	require.Equal(t, http.StatusOK, doTrajectoryAgentPut(t, srv, pResp.Upload.URL, tok, badGZ, pResp.Upload.Headers).Code)
	cRec := doTrajectoryAgentJSON(t, srv, http.MethodPost, telemetrycontract.TrajectoryArtifactsCommitAgentPath(agent.ID), tok, telemetrycontract.TrajectoryArtifactCommitRequest{
		ArtifactID: pResp.ArtifactID,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, cRec.Code)
	assert.Equal(t, telemetrycontract.TrajectoryErrArtifactVerificationFailed, decodeTrajectoryErrorCode(t, cRec))
}
