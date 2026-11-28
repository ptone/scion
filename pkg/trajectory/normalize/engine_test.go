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

package normalize

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/trajectory/shipper"
)

// fakeExecutor scripts normalizer invocations and records received Requests.
type fakeExecutor struct {
	mu       sync.Mutex
	requests []Request
	fn       func(ctx context.Context, req Request) (ExecResult, error)
}

func (f *fakeExecutor) Execute(ctx context.Context, req Request) (ExecResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	fn := f.fn
	f.mu.Unlock()
	return fn(ctx, req)
}

func (f *fakeExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// fakeUploader simulates Hub L1 prepare/upload/commit with idempotency,
// conflict detection, fault injection, and call ordering capture.
type fakeUploader struct {
	mu sync.Mutex

	// Committed objects keyed by identity string -> sha256 + content + commit req.
	committed   map[string]committedArtifact
	pending     map[string]pendingArtifact
	events      []string // e.g. "prepare:usage_records", "upload:usage_records", "commit:usage_records"
	parseFailed []ParseFailedReport
	nextID      int

	// Fault injection hooks (optional).
	failPrepare func(req ArtifactPrepareRequest) error
	failUpload  func(req ArtifactPrepareRequest) error
	failCommit  func(req ArtifactPrepareRequest) error
}

type pendingArtifact struct {
	req     ArtifactPrepareRequest
	idKey   string
	content []byte
}

type committedArtifact struct {
	req         ArtifactPrepareRequest
	content     []byte
	sessionMeta *telemetrycontract.SessionMeta
}

func newFakeUploader() *fakeUploader {
	return &fakeUploader{
		committed: make(map[string]committedArtifact),
		pending:   make(map[string]pendingArtifact),
	}
}

func identityKey(req ArtifactPrepareRequest) string {
	switch req.ArtifactType {
	case ArtifactTypeUsageRecords:
		return fmt.Sprintf("usage_records:%s:%s", req.ParserVersion, req.ChunkID)
	case ArtifactTypeRunInventory:
		return fmt.Sprintf("run_inventory:%s:%d", req.ParserVersion, req.InventorySeq)
	case ArtifactTypeATIF:
		return fmt.Sprintf("atif:%s:%s:%d", req.ParserVersion, req.NativeRelativePath, req.ParsedOffset)
	default:
		return req.ArtifactType
	}
}

func (u *fakeUploader) Prepare(_ context.Context, req ArtifactPrepareRequest) (ArtifactPrepareResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.events = append(u.events, "prepare:"+req.ArtifactType)
	if u.failPrepare != nil {
		if err := u.failPrepare(req); err != nil {
			return ArtifactPrepareResponse{}, err
		}
	}
	idKey := identityKey(req)
	if existing, ok := u.committed[idKey]; ok {
		if existing.req.SHA256 == req.SHA256 {
			return ArtifactPrepareResponse{
				ArtifactID: "existing-" + idKey,
				State:      ArtifactStateCommitted,
			}, nil
		}
		return ArtifactPrepareResponse{}, ErrArtifactConflict
	}
	u.nextID++
	artID := fmt.Sprintf("art-%d", u.nextID)
	u.pending[artID] = pendingArtifact{req: req, idKey: idKey}
	return ArtifactPrepareResponse{
		ArtifactID: artID,
		State:      ArtifactStateUploadRequired,
		Upload: &UploadTarget{
			Method: "PUT",
			URL:    "https://fake.upload/" + artID,
		},
	}, nil
}

func (u *fakeUploader) Upload(_ context.Context, target UploadTarget, content []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	artID := strings.TrimPrefix(target.URL, "https://fake.upload/")
	p, ok := u.pending[artID]
	if !ok {
		return fmt.Errorf("unknown upload target %s", target.URL)
	}
	u.events = append(u.events, "upload:"+p.req.ArtifactType)
	if u.failUpload != nil {
		if err := u.failUpload(p.req); err != nil {
			return err
		}
	}
	p.content = append([]byte(nil), content...)
	u.pending[artID] = p
	return nil
}

func (u *fakeUploader) Commit(_ context.Context, req ArtifactCommitRequest) (ArtifactCommitResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	p, ok := u.pending[req.ArtifactID]
	if !ok {
		return ArtifactCommitResponse{}, fmt.Errorf("unknown artifact_id %s", req.ArtifactID)
	}
	u.events = append(u.events, "commit:"+p.req.ArtifactType)
	if u.failCommit != nil {
		if err := u.failCommit(p.req); err != nil {
			return ArtifactCommitResponse{}, err
		}
	}
	u.committed[p.idKey] = committedArtifact{
		req:         p.req,
		content:     p.content,
		sessionMeta: req.SessionMeta,
	}
	delete(u.pending, req.ArtifactID)
	return ArtifactCommitResponse{
		ArtifactID: req.ArtifactID,
		State:      ArtifactStateCommitted,
	}, nil
}

func (u *fakeUploader) ReportParseFailed(_ context.Context, report ParseFailedReport) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.parseFailed = append(u.parseFailed, report)
	return nil
}

// Test helpers.
func writeNativeFile(t *testing.T, home, relPath, content string) int64 {
	t.Helper()
	abs := filepath.Join(home, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir native: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write native: %v", err)
	}
	return int64(len(content))
}

func seedShipperState(t *testing.T, home string, artifacts map[string]*shipper.ArtifactState) {
	t.Helper()
	st := &shipper.State{
		Version:   shipper.StateVersion,
		Harness:   "claude",
		Artifacts: artifacts,
	}
	if err := shipper.SaveState(home, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
}

func gunzipBytes(t *testing.T, gz []byte) []byte {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = gr.Close() }()
	out, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("gunzip read: %v", err)
	}
	return out
}

func decodeUsageChunkLines(t *testing.T, gz []byte) []telemetrycontract.UsageRecord {
	t.Helper()
	raw := gunzipBytes(t, gz)
	var out []telemetrycontract.UsageRecord
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec telemetrycontract.UsageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal usage record: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

func TestEngine_FullRound_DedupeEnrichAccumulateAndFinal(t *testing.T) {
	home := t.TempDir()
	tmpBase := t.TempDir()
	relPath := ".claude/projects/-workspace/sess-1.jsonl"
	line1 := "{\"msg\":1}\n"
	partial := "{\"msg\":2-partial"
	size1 := writeNativeFile(t, home, relPath, line1+partial)

	seedShipperState(t, home, map[string]*shipper.ArtifactState{
		relPath: {
			NativeRelativePath: relPath,
			ArtifactKind:       shipper.ArtifactKindJSONLAppend,
			Generation:         0,
			RawCommittedOffset: size1,
			ParsedOffset:       0,
		},
	})

	uploader := newFakeUploader()
	var observedRoundDir string
	roundNum := 0
	exec := &fakeExecutor{
		fn: func(_ context.Context, req Request) (ExecResult, error) {
			roundNum++
			if len(req.Artifacts) != 1 {
				return ExecResult{}, fmt.Errorf("expected 1 artifact, got %d", len(req.Artifacts))
			}
			art := req.Artifacts[0]
			observedRoundDir = filepath.Dir(art.Path)

			// Verify round copy permissions: 0500 dir, 0444 file.
			dirInfo, err := os.Stat(observedRoundDir)
			if err != nil {
				return ExecResult{}, err
			}
			if dirInfo.Mode().Perm() != 0o500 {
				return ExecResult{}, fmt.Errorf("round dir mode = %o, want 0500", dirInfo.Mode().Perm())
			}
			fileInfo, err := os.Stat(art.Path)
			if err != nil {
				return ExecResult{}, err
			}
			if fileInfo.Mode().Perm() != 0o444 {
				return ExecResult{}, fmt.Errorf("round file mode = %o, want 0444", fileInfo.Mode().Perm())
			}

			if roundNum == 1 {
				// Partial trailing line must have been excluded: end_offset == len(line1).
				if art.EndOffset != int64(len(line1)) {
					return ExecResult{}, fmt.Errorf("round 1 EndOffset = %d, want %d", art.EndOffset, len(line1))
				}
				return ExecResult{
					ExitCode:     0,
					PeakRSSBytes: 300 << 20, // Exceeds 256 MiB soft budget
					StdoutLines: [][]byte{
						// Duplicate dedupe_key (streaming chunks) + injected dimension fields + invalid line.
						[]byte(`{"type":"usage_record","session_id":"sess-1","hub_id":"evil","project_id":"evil","native_request_id_raw":"req_011CP9XYZ0123456789a","dedupe_key":"claude:m1:req_011CP9XYZ0123456789a","event_ts":"2026-11-28T12:00:00Z","model":"claude-sonnet-4-6","status":"ok","calls":1,"input":100,"output":10,"cache_read":200,"cache_write":50,"input_derivation":"reported","token_status":"reported"}`),
						[]byte(`{"type":"usage_record","session_id":"sess-1","native_request_id_raw":"req_011CP9XYZ0123456789a","dedupe_key":"claude:m1:req_011CP9XYZ0123456789a","event_ts":"2026-11-28T12:00:01Z","model":"claude-sonnet-4-6","status":"ok","calls":1,"input":100,"output":45,"cache_read":200,"cache_write":50,"input_derivation":"reported","token_status":"reported"}`),
						// Invalid line (disallowed prompt field) -> dropped and counted in session_meta.parse_error.
						[]byte(`{"type":"usage_record","session_id":"sess-1","dedupe_key":"claude:bad","event_ts":"2026-11-28T12:00:01Z","status":"ok","calls":1,"token_status":"reported","prompt":"leak"}`),
						[]byte(`{"type":"atif_step","session_id":"sess-1","step":{"step_id":1,"source":"user","message":"hello","extra":{"scion":{"scion_input_channel":"initial_task"}}}}`),
						[]byte(`{"type":"atif_step","session_id":"sess-1","step":{"step_id":2,"source":"agent","message":"world","metrics":{"prompt_tokens":350,"completion_tokens":45,"cached_tokens":200}}}`),
						[]byte(fmt.Sprintf(`{"type":"session_meta","session_id":"sess-1","native_relative_path":%q,"agent":{"name":"claude-code","version":"2.1.296","model_name":"claude-sonnet-4-6"},"is_subagent":false}`, relPath)),
						[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:%d}}`, relPath, art.EndOffset)),
						[]byte(`{"type":"state","state":{"cursor":1}}`),
					},
				}, nil
			}

			// Round 2: remaining completed line.
			return ExecResult{
				ExitCode:     0,
				PeakRSSBytes: 64 << 20,
				StdoutLines: [][]byte{
					[]byte(`{"type":"usage_record","session_id":"sess-1","native_request_id_raw":"req_011CP9XYZ9999999999b","dedupe_key":"claude:m2:req_011CP9XYZ9999999999b","event_ts":"2026-11-28T12:05:00Z","model":"claude-sonnet-4-6","status":"ok","calls":1,"input":20,"output":30,"cache_read":0,"cache_write":0,"input_derivation":"reported","token_status":"reported"}`),
					[]byte(`{"type":"atif_step","session_id":"sess-1","step":{"step_id":3,"source":"agent","message":"done","metrics":{"prompt_tokens":20,"completion_tokens":30,"cached_tokens":0}}}`),
					[]byte(fmt.Sprintf(`{"type":"session_meta","session_id":"sess-1","native_relative_path":%q,"agent":{"name":"claude-code","version":"2.1.296","model_name":"claude-sonnet-4-6"},"is_subagent":false}`, relPath)),
					[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:%d}}`, relPath, art.EndOffset)),
					[]byte(`{"type":"state","state":{"cursor":2}}`),
				},
			}, nil
		},
	}

	fixedNow := time.Date(2026, 11, 28, 12, 10, 0, 0, time.UTC)
	eng, err := New(Config{
		HomeDir:               home,
		TempBaseDir:           tmpBase,
		Harness:               "claude",
		HarnessVersion:        "2.1.296",
		ParserVersion:         "claude-1",
		TestedHarnessVersions: []string{"2.1.*"},
		RunID:                 "run-1",
		AgentID:               "agent-1",
		Executor:              exec,
		Uploader:              uploader,
		Now:                   func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Round 1 (even on session_end, partial trailing bytes remain -> final MUST be false).
	res1, err := eng.RunRound(context.Background(), RoundRequest{
		Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
		Reason: shipper.ReasonSessionEnd,
	})
	if err != nil {
		t.Fatalf("Round 1 error: %v", err)
	}
	if _, err := os.Stat(observedRoundDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("round temp dir %s was not cleaned up", observedRoundDir)
	}
	if res1.UsageRecordsEmitted != 1 || res1.UsageRecordsMerged != 1 {
		t.Fatalf("round 1 records: emitted=%d merged=%d, want 1/1", res1.UsageRecordsEmitted, res1.UsageRecordsMerged)
	}
	if res1.StrippedDimensionFields != 2 {
		t.Fatalf("StrippedDimensionFields = %d, want 2", res1.StrippedDimensionFields)
	}
	if res1.ParseErrorsDropped != 1 {
		t.Fatalf("ParseErrorsDropped = %d, want 1", res1.ParseErrorsDropped)
	}
	if res1.InventorySeq != 1 || res1.InventoryFinal {
		t.Fatalf("round 1 inventory: seq=%d final=%v, want 1/false", res1.InventorySeq, res1.InventoryFinal)
	}
	if !res1.RSSBudgetExceeded {
		t.Fatalf("expected RSSBudgetExceeded=true")
	}

	// Verify upload ordering: usage_records -> atif -> run_inventory.
	wantOrder := []string{
		"prepare:usage_records", "upload:usage_records", "commit:usage_records",
		"prepare:atif", "upload:atif", "commit:atif",
		"prepare:run_inventory", "upload:run_inventory", "commit:run_inventory",
	}
	if strings.Join(uploader.events, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("upload order = %v, want %v", uploader.events, wantOrder)
	}

	// Verify usage_records chunk content: merged max output=45, rid1 hash, context_input_total=350.
	usageArt := uploader.committed["usage_records:claude-1:"+res1.ChunkID]
	rows1 := decodeUsageChunkLines(t, usageArt.content)
	if len(rows1) != 1 {
		t.Fatalf("rows1 len = %d, want 1", len(rows1))
	}
	if *rows1[0].Output != 45 || *rows1[0].ContextInputTotal != 350 {
		t.Fatalf("merged row mismatch: output=%d context=%d", *rows1[0].Output, *rows1[0].ContextInputTotal)
	}
	wantRID := telemetrycontract.NativeRequestIDHashForHarness("claude", "req_011CP9XYZ0123456789a")
	if rows1[0].NativeRequestID != wantRID {
		t.Fatalf("NativeRequestID = %q, want %q", rows1[0].NativeRequestID, wantRID)
	}

	// Verify ATIF session_meta carried parse_error and rss_budget_exceeded.
	atifArt1 := uploader.committed[fmt.Sprintf("atif:claude-1:%s:%d", relPath, len(line1))]
	if atifArt1.sessionMeta == nil || atifArt1.sessionMeta.ParseError == nil || atifArt1.sessionMeta.ParseError.Count != 1 {
		t.Fatalf("unexpected round 1 session_meta: %+v", atifArt1.sessionMeta)
	}
	if atifArt1.sessionMeta.Stats["rss_budget_exceeded"] != true {
		t.Fatalf("expected rss_budget_exceeded in session_meta.stats: %+v", atifArt1.sessionMeta.Stats)
	}

	// Complete the second line on disk and advance RawCommittedOffset for Round 2.
	line2Full := line1 + partial + "\"}\n"
	size2 := writeNativeFile(t, home, relPath, line2Full)
	if err := shipper.WithLockedState(context.Background(), home, time.Second, func(st *shipper.State) (bool, error) {
		st.Artifacts[relPath].RawCommittedOffset = size2
		return true, nil
	}); err != nil {
		t.Fatalf("update RawCommittedOffset: %v", err)
	}

	// Round 2 on session_end: all bytes parsed -> final MUST be true, ATIF has all 3 steps, inventory has 2 chunks!
	res2, err := eng.RunRound(context.Background(), RoundRequest{
		Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
		Reason: shipper.ReasonSessionEnd,
	})
	if err != nil {
		t.Fatalf("Round 2 error: %v", err)
	}
	if res2.InventorySeq != 2 || !res2.InventoryFinal {
		t.Fatalf("round 2 inventory: seq=%d final=%v, want 2/true", res2.InventorySeq, res2.InventoryFinal)
	}

	// Verify cumulative run_inventory seq=2 has both chunks.
	invArt2 := uploader.committed["run_inventory:claude-1:2"]
	var inv2 telemetrycontract.RunInventory
	if err := json.Unmarshal(invArt2.content, &inv2); err != nil {
		t.Fatalf("unmarshal inv2: %v", err)
	}
	if inv2.InventorySeq != 2 || !inv2.Final || len(inv2.Chunks) != 2 {
		t.Fatalf("unexpected inv2: %+v", inv2)
	}
	if inv2.Chunks[0].ChunkID != res1.ChunkID || inv2.Chunks[1].ChunkID != res2.ChunkID {
		t.Fatalf("inv2 chunks not cumulative: %+v", inv2.Chunks)
	}

	// Verify rebuilt ATIF trajectory has all 3 accumulated steps and parser_version in agent.extra.scion.
	atifArt2 := uploader.committed[fmt.Sprintf("atif:claude-1:%s:%d", relPath, size2)]
	var traj2 telemetrycontract.ATIFTrajectory
	if err := json.Unmarshal(gunzipBytes(t, atifArt2.content), &traj2); err != nil {
		t.Fatalf("unmarshal traj2: %v", err)
	}
	if traj2.SchemaVersion != telemetrycontract.ATIFSchemaVersionV18 || len(traj2.Steps) != 3 {
		t.Fatalf("unexpected traj2: schema=%q steps=%d", traj2.SchemaVersion, len(traj2.Steps))
	}
	if traj2.FinalMetrics == nil || traj2.FinalMetrics.TotalPromptTokens != 370 || traj2.FinalMetrics.TotalCompletionTokens != 75 {
		t.Fatalf("unexpected traj2 final_metrics: %+v", traj2.FinalMetrics)
	}
	scionExtra, ok := traj2.Agent.Extra["scion"].(map[string]any)
	if !ok || scionExtra["parser_version"] != "claude-1" || len(scionExtra) != 1 {
		t.Fatalf("unexpected agent.extra.scion: %+v", traj2.Agent.Extra)
	}
}

func TestEngine_CrashBetweenCommitAndState_ReplayIdempotent(t *testing.T) {
	home := t.TempDir()
	relPath := ".claude/projects/-workspace/sess-replay.jsonl"
	size := writeNativeFile(t, home, relPath, "{\"line\":1}\n")
	seedShipperState(t, home, map[string]*shipper.ArtifactState{
		relPath: {
			NativeRelativePath: relPath,
			ArtifactKind:       shipper.ArtifactKindJSONLAppend,
			Generation:         0,
			RawCommittedOffset: size,
			ParsedOffset:       0,
		},
	})

	uploader := newFakeUploader()
	// Fail run_inventory commit on first attempt: usage_records and atif are ALREADY committed on Hub!
	failedOnce := false
	uploader.failCommit = func(req ArtifactPrepareRequest) error {
		if req.ArtifactType == ArtifactTypeRunInventory && !failedOnce {
			failedOnce = true
			return errors.New("simulated crash before state advance")
		}
		return nil
	}

	exec := &fakeExecutor{
		fn: func(_ context.Context, req Request) (ExecResult, error) {
			art := req.Artifacts[0]
			return ExecResult{
				ExitCode: 0,
				StdoutLines: [][]byte{
					[]byte(`{"type":"usage_record","session_id":"sess-replay","dedupe_key":"claude:m1:r1","event_ts":"2026-11-28T12:00:00Z","model":"claude-sonnet-4-6","status":"ok","calls":1,"input":10,"output":5,"token_status":"reported"}`),
					[]byte(`{"type":"atif_step","session_id":"sess-replay","step":{"step_id":1,"source":"user","message":"hi"}}`),
					[]byte(fmt.Sprintf(`{"type":"session_meta","session_id":"sess-replay","native_relative_path":%q,"agent":{"name":"claude-code"},"is_subagent":false}`, relPath)),
					[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:%d}}`, relPath, art.EndOffset)),
					[]byte(`{"type":"state","state":{"ok":true}}`),
				},
			}, nil
		},
	}

	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	eng, err := New(Config{
		HomeDir:       home,
		Harness:       "claude",
		ParserVersion: "claude-1",
		RunID:         "run-replay",
		AgentID:       "agent-1",
		Executor:      exec,
		Uploader:      uploader,
		Now:           func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Attempt 1 fails at inventory commit.
	if _, err := eng.RunRound(context.Background(), RoundRequest{
		Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
		Reason: shipper.ReasonPeriodic,
	}); err == nil {
		t.Fatal("expected attempt 1 to fail at inventory commit")
	}

	// Verify state was NOT advanced and .steps.jsonl was NOT written.
	st, _, err := shipper.LoadState(home)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.Artifacts[relPath].ParsedOffset != 0 {
		t.Fatalf("ParsedOffset advanced on failed round: %d", st.Artifacts[relPath].ParsedOffset)
	}
	if _, err := os.Stat(atifStepsPath(home, relPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ATIF steps file should not exist before commit succeeds")
	}

	uploader.events = nil

	// Attempt 2 replays the exact same range.
	res2, err := eng.RunRound(context.Background(), RoundRequest{
		Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
		Reason: shipper.ReasonPeriodic,
	})
	if err != nil {
		t.Fatalf("Attempt 2 replay failed: %v", err)
	}

	// Replay must skip uploading already-committed usage_records and atif, and only upload+commit run_inventory!
	wantReplayEvents := []string{
		"prepare:usage_records",
		"prepare:atif",
		"prepare:run_inventory", "upload:run_inventory", "commit:run_inventory",
	}
	if strings.Join(uploader.events, ",") != strings.Join(wantReplayEvents, ",") {
		t.Fatalf("replay events = %v, want %v", uploader.events, wantReplayEvents)
	}

	// Verify deterministic chunk_id and no duplicate ATIF steps.
	wantChunkID := DeterministicChunkID("run-replay", "claude-1", []ArtifactRange{
		{Key: relPath, Generation: 0, Start: 0, End: size},
	})
	if res2.ChunkID != wantChunkID {
		t.Fatalf("replay chunk_id = %q, want %q", res2.ChunkID, wantChunkID)
	}
	steps, _, err := readATIFSteps(home, relPath, DefaultMaxATIFBytes)
	if err != nil || len(steps) != 1 {
		t.Fatalf("replay ATIF steps = %d (err=%v), want 1", len(steps), err)
	}
}

func TestEngine_MetadataOnlyProducesNoATIF(t *testing.T) {
	home := t.TempDir()
	relPath := ".claude/projects/-workspace/sess-meta.jsonl"
	writeNativeFile(t, home, relPath, "{\"line\":1}\n")

	uploader := newFakeUploader()
	exec := &fakeExecutor{
		fn: func(_ context.Context, req Request) (ExecResult, error) {
			if req.Mode != telemetrycontract.TrajectoryCapturePolicyMetadataOnly {
				return ExecResult{}, fmt.Errorf("mode = %q, want metadata_only", req.Mode)
			}
			art := req.Artifacts[0]
			return ExecResult{
				ExitCode: 0,
				StdoutLines: [][]byte{
					[]byte(`{"type":"usage_record","session_id":"sess-meta","dedupe_key":"claude:m1:r1","event_ts":"2026-11-28T12:00:00Z","model":"claude-sonnet-4-6","status":"ok","calls":1,"input":5,"output":5,"token_status":"reported"}`),
					// Even if the normalizer emits atif_step, metadata_only MUST NOT upload ATIF.
					[]byte(`{"type":"atif_step","session_id":"sess-meta","step":{"step_id":1,"source":"user","message":"secret"}}`),
					[]byte(fmt.Sprintf(`{"type":"session_meta","session_id":"sess-meta","native_relative_path":%q,"agent":{"name":"claude-code"},"is_subagent":false}`, relPath)),
					[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:%d}}`, relPath, art.EndOffset)),
					[]byte(`{"type":"state","state":{}}`),
				},
			}, nil
		},
	}

	eng, err := New(Config{
		HomeDir:       home,
		Harness:       "claude",
		ParserVersion: "claude-1",
		RunID:         "run-meta",
		AgentID:       "agent-1",
		Executor:      exec,
		Uploader:      uploader,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := eng.RunRound(context.Background(), RoundRequest{
		Mode:   telemetrycontract.TrajectoryCapturePolicyMetadataOnly,
		Reason: shipper.ReasonSessionEnd,
	})
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if res.ATIFSessionsUploaded != 0 {
		t.Fatalf("ATIFSessionsUploaded = %d, want 0", res.ATIFSessionsUploaded)
	}
	for _, ev := range uploader.events {
		if strings.Contains(ev, "atif") {
			t.Fatalf("metadata_only attempted ATIF upload: %v", uploader.events)
		}
	}
	if !res.InventoryFinal {
		t.Fatalf("expected final=true on session_end when fully parsed")
	}
}

func TestEngine_PoisonBreakerAndGenerationReset(t *testing.T) {
	home := t.TempDir()
	relPath := ".claude/projects/-workspace/sess-poison.jsonl"
	size := writeNativeFile(t, home, relPath, "{\"bad\":true}\n")
	seedShipperState(t, home, map[string]*shipper.ArtifactState{
		relPath: {
			NativeRelativePath: relPath,
			ArtifactKind:       shipper.ArtifactKindJSONLAppend,
			Generation:         0,
			RawCommittedOffset: size,
			ParsedOffset:       0,
		},
	})

	uploader := newFakeUploader()
	shouldFail := true
	exec := &fakeExecutor{
		fn: func(_ context.Context, req Request) (ExecResult, error) {
			if shouldFail {
				return ExecResult{ExitCode: 2, Stderr: []byte("boom")}, nil
			}
			art := req.Artifacts[0]
			return ExecResult{
				ExitCode: 0,
				StdoutLines: [][]byte{
					[]byte(`{"type":"usage_record","session_id":"sess-poison","dedupe_key":"claude:m1:r1","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"not_reported"}`),
					[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:%d}}`, relPath, art.EndOffset)),
					[]byte(`{"type":"state","state":{}}`),
				},
			}, nil
		},
	}

	eng, err := New(Config{
		HomeDir:       home,
		Harness:       "claude",
		ParserVersion: "claude-1",
		RunID:         "run-poison",
		AgentID:       "agent-1",
		Executor:      exec,
		Uploader:      uploader,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Failures 1 and 2: not yet poisoned.
	for i := 1; i <= 2; i++ {
		res, err := eng.RunRound(context.Background(), RoundRequest{
			Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
			Reason: shipper.ReasonPeriodic,
		})
		if !errors.Is(err, ErrParseFailure) {
			t.Fatalf("attempt %d: err = %v, want ErrParseFailure", i, err)
		}
		if len(res.PoisonedArtifacts) != 0 {
			t.Fatalf("attempt %d poisoned early: %v", i, res.PoisonedArtifacts)
		}
	}

	// Failure 3: trips poison breaker and reports parse_failed to Hub.
	res3, err := eng.RunRound(context.Background(), RoundRequest{
		Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
		Reason: shipper.ReasonPeriodic,
	})
	if !errors.Is(err, ErrParseFailure) {
		t.Fatalf("attempt 3: err = %v, want ErrParseFailure", err)
	}
	if len(res3.PoisonedArtifacts) != 1 || res3.PoisonedArtifacts[0] != relPath {
		t.Fatalf("attempt 3 PoisonedArtifacts = %v, want [%s]", res3.PoisonedArtifacts, relPath)
	}
	if len(uploader.parseFailed) != 1 || uploader.parseFailed[0].SessionMeta.NormalizeStatus != telemetrycontract.NormalizeStatusParseFailed {
		t.Fatalf("expected 1 parse_failed report, got %+v", uploader.parseFailed)
	}

	// Attempt 4: artifact is skipped, Executor NOT called again.
	callsBefore := exec.callCount()
	res4, err := eng.RunRound(context.Background(), RoundRequest{
		Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
		Reason: shipper.ReasonPeriodic,
	})
	if err != nil {
		t.Fatalf("attempt 4 error: %v", err)
	}
	if !res4.Skipped || exec.callCount() != callsBefore {
		t.Fatalf("expected poisoned artifact to be skipped without invoking executor")
	}

	// Generation bump resets poison state and resumes normalization!
	shouldFail = false
	if err := shipper.WithLockedState(context.Background(), home, time.Second, func(st *shipper.State) (bool, error) {
		st.Artifacts[relPath].Generation = 1
		st.Artifacts[relPath].ParsedOffset = 0
		return true, nil
	}); err != nil {
		t.Fatalf("bump generation: %v", err)
	}

	res5, err := eng.RunRound(context.Background(), RoundRequest{
		Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
		Reason: shipper.ReasonPeriodic,
	})
	if err != nil {
		t.Fatalf("post-generation-bump round failed: %v", err)
	}
	if res5.Skipped || res5.UsageRecordsEmitted != 1 {
		t.Fatalf("expected post-generation-bump round to succeed, got %+v", res5)
	}
}

func TestEngine_VersionGateForcesUnverified(t *testing.T) {
	home := t.TempDir()
	relPath := ".claude/projects/-workspace/sess-ver.jsonl"
	size := writeNativeFile(t, home, relPath, "{\"a\":1}\n")
	seedShipperState(t, home, map[string]*shipper.ArtifactState{
		relPath: {
			NativeRelativePath: relPath,
			ArtifactKind:       shipper.ArtifactKindJSONLAppend,
			Generation:         0,
			RawCommittedOffset: size,
			ParsedOffset:       0,
		},
	})

	uploader := newFakeUploader()
	exec := &fakeExecutor{
		fn: func(_ context.Context, req Request) (ExecResult, error) {
			art := req.Artifacts[0]
			return ExecResult{
				ExitCode: 0,
				StdoutLines: [][]byte{
					[]byte(`{"type":"usage_record","session_id":"sess-ver","dedupe_key":"claude:m1:r1","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"input":10,"output":5,"token_status":"reported"}`),
					[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:%d}}`, relPath, art.EndOffset)),
					[]byte(`{"type":"state","state":{}}`),
				},
			}, nil
		},
	}

	eng, err := New(Config{
		HomeDir:               home,
		Harness:               "claude",
		HarnessVersion:        "3.0.0", // does not match 2.1.*
		ParserVersion:         "claude-1",
		TestedHarnessVersions: []string{"2.1.*"},
		RunID:                 "run-ver",
		AgentID:               "agent-1",
		Executor:              exec,
		Uploader:              uploader,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := eng.RunRound(context.Background(), RoundRequest{
		Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
		Reason: shipper.ReasonPeriodic,
	})
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	rows := decodeUsageChunkLines(t, uploader.committed["usage_records:claude-1:"+res.ChunkID].content)
	if len(rows) != 1 || rows[0].TokenStatus != telemetrycontract.TokenStatusUnverified {
		t.Fatalf("expected token_status=unverified, got %+v", rows)
	}
}

func TestEngine_FramingFailuresAndSkipModes(t *testing.T) {
	home := t.TempDir()
	relPath := ".claude/projects/-workspace/sess-frame.jsonl"
	size := writeNativeFile(t, home, relPath, "{\"a\":1}\n")
	seedShipperState(t, home, map[string]*shipper.ArtifactState{
		relPath: {
			NativeRelativePath: relPath,
			ArtifactKind:       shipper.ArtifactKindJSONLAppend,
			Generation:         0,
			RawCommittedOffset: size,
			ParsedOffset:       0,
		},
	})

	// Test skip modes: off and pre_stop.
	uploader := newFakeUploader()
	noopExec := &fakeExecutor{
		fn: func(_ context.Context, _ Request) (ExecResult, error) {
			return ExecResult{}, errors.New("should not be called")
		},
	}
	eng, err := New(Config{
		HomeDir:       home,
		Harness:       "claude",
		ParserVersion: "claude-1",
		RunID:         "run-skip",
		AgentID:       "agent-1",
		Executor:      noopExec,
		Uploader:      uploader,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if res, err := eng.RunRound(context.Background(), RoundRequest{Mode: telemetrycontract.TrajectoryCapturePolicyOff, Reason: shipper.ReasonPeriodic}); err != nil || !res.Skipped {
		t.Fatalf("off mode: res=%+v err=%v", res, err)
	}
	if res, err := eng.RunRound(context.Background(), RoundRequest{Mode: telemetrycontract.TrajectoryCapturePolicyFull, Reason: shipper.ReasonPreStop}); err != nil || !res.Skipped {
		t.Fatalf("pre_stop: res=%+v err=%v", res, err)
	}
	if noopExec.callCount() != 0 {
		t.Fatalf("executor called %d times on skipped rounds", noopExec.callCount())
	}

	// Test invalid framing cases.
	framingCases := []struct {
		name  string
		lines [][]byte
		err   error
	}{
		{
			name:  "missing state line",
			lines: [][]byte{[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:%d}}`, relPath, size))},
		},
		{
			name:  "missing parsed_offsets line",
			lines: [][]byte{[]byte(`{"type":"state","state":{}}`)},
		},
		{
			name: "state not last line",
			lines: [][]byte{
				[]byte(`{"type":"state","state":{}}`),
				[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:%d}}`, relPath, size)),
			},
		},
		{
			name: "offset not on line boundary",
			lines: [][]byte{
				[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:3}}`, relPath)),
				[]byte(`{"type":"state","state":{}}`),
			},
		},
		{
			name: "exec timeout error",
			err:  context.DeadlineExceeded,
		},
	}

	for _, tc := range framingCases {
		t.Run(tc.name, func(t *testing.T) {
			subHome := t.TempDir()
			writeNativeFile(t, subHome, relPath, "{\"a\":1}\n")
			seedShipperState(t, subHome, map[string]*shipper.ArtifactState{
				relPath: {
					NativeRelativePath: relPath,
					ArtifactKind:       shipper.ArtifactKindJSONLAppend,
					Generation:         0,
					RawCommittedOffset: size,
					ParsedOffset:       0,
				},
			})
			e, err := New(Config{
				HomeDir:       subHome,
				Harness:       "claude",
				ParserVersion: "claude-1",
				RunID:         "run-frame",
				AgentID:       "agent-1",
				Executor: &fakeExecutor{
					fn: func(_ context.Context, _ Request) (ExecResult, error) {
						return ExecResult{ExitCode: 0, StdoutLines: tc.lines}, tc.err
					},
				},
				Uploader: newFakeUploader(),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, runErr := e.RunRound(context.Background(), RoundRequest{
				Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
				Reason: shipper.ReasonPeriodic,
			})
			if !errors.Is(runErr, ErrParseFailure) {
				t.Fatalf("expected ErrParseFailure for %s, got %v", tc.name, runErr)
			}
		})
	}
}

func TestEngine_UploaderFailuresAndConflict(t *testing.T) {
	cases := []struct {
		name  string
		setup func(u *fakeUploader)
		want  error
	}{
		{
			name: "prepare conflict",
			setup: func(u *fakeUploader) {
				u.failPrepare = func(_ ArtifactPrepareRequest) error { return ErrArtifactConflict }
			},
			want: ErrArtifactConflict,
		},
		{
			name: "upload failure on usage_records",
			setup: func(u *fakeUploader) {
				u.failUpload = func(req ArtifactPrepareRequest) error {
					if req.ArtifactType == ArtifactTypeUsageRecords {
						return errors.New("upload failed")
					}
					return nil
				}
			},
		},
		{
			name: "commit failure on atif",
			setup: func(u *fakeUploader) {
				u.failCommit = func(req ArtifactPrepareRequest) error {
					if req.ArtifactType == ArtifactTypeATIF {
						return errors.New("atif commit failed")
					}
					return nil
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			relPath := ".claude/projects/-workspace/sess-fail.jsonl"
			size := writeNativeFile(t, home, relPath, "{\"a\":1}\n")
			seedShipperState(t, home, map[string]*shipper.ArtifactState{
				relPath: {
					NativeRelativePath: relPath,
					ArtifactKind:       shipper.ArtifactKindJSONLAppend,
					Generation:         0,
					RawCommittedOffset: size,
					ParsedOffset:       0,
				},
			})

			uploader := newFakeUploader()
			tc.setup(uploader)
			eng, err := New(Config{
				HomeDir:       home,
				Harness:       "claude",
				ParserVersion: "claude-1",
				RunID:         "run-fail",
				AgentID:       "agent-1",
				Executor: &fakeExecutor{
					fn: func(_ context.Context, req Request) (ExecResult, error) {
						art := req.Artifacts[0]
						return ExecResult{
							ExitCode: 0,
							StdoutLines: [][]byte{
								[]byte(`{"type":"usage_record","session_id":"sess-fail","dedupe_key":"claude:m1:r1","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"not_reported"}`),
								[]byte(`{"type":"atif_step","session_id":"sess-fail","step":{"step_id":1,"source":"user","message":"hi"}}`),
								[]byte(fmt.Sprintf(`{"type":"session_meta","session_id":"sess-fail","native_relative_path":%q,"agent":{"name":"claude-code"},"is_subagent":false}`, relPath)),
								[]byte(fmt.Sprintf(`{"type":"parsed_offsets","offsets":{%q:%d}}`, relPath, art.EndOffset)),
								[]byte(`{"type":"state","state":{}}`),
							},
						}, nil
					},
				},
				Uploader: uploader,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			_, err = eng.RunRound(context.Background(), RoundRequest{
				Mode:   telemetrycontract.TrajectoryCapturePolicyFull,
				Reason: shipper.ReasonPeriodic,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}

			st, _, err := shipper.LoadState(home)
			if err != nil {
				t.Fatalf("LoadState: %v", err)
			}
			if st.Artifacts[relPath].ParsedOffset != 0 {
				t.Fatalf("ParsedOffset must not advance on uploader error (got %d)", st.Artifacts[relPath].ParsedOffset)
			}
		})
	}
}
