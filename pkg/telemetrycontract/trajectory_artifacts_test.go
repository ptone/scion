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

package telemetrycontract

import (
	"testing"
	"time"
)

func TestTrajectoryArtifactPaths(t *testing.T) {
	if got, want := TrajectoryArtifactsPrepareAgentPath("agent-1"), "/api/v1/agents/agent-1/trajectory/artifacts:prepare"; got != want {
		t.Fatalf("prepare path = %q, want %q", got, want)
	}
	if got, want := TrajectoryArtifactsCommitAgentPath("agent-1"), "/api/v1/agents/agent-1/trajectory/artifacts:commit"; got != want {
		t.Fatalf("commit path = %q, want %q", got, want)
	}
	if got, want := TrajectoryArtifactContentAgentPath("agent-1", "art-1"), "/api/v1/agents/agent-1/trajectory/artifacts/art-1/content"; got != want {
		t.Fatalf("content path = %q, want %q", got, want)
	}
}

func TestTrajectoryArtifactObjectPaths(t *testing.T) {
	dt := time.Date(2026, 11, 28, 15, 4, 5, 0, time.UTC)

	if got, want := TrajectoryArtifactPendingObjectPath("hub-1", "art-123"), "hub-1/trajectories/v1/l1-pending/art-123"; got != want {
		t.Fatalf("pending path = %q, want %q", got, want)
	}

	if got, want := TrajectoryUsageRecordsObjectPath("hub-1", dt, "proj-1", "agent-1", "run-1", "claude-1", 3, "chunk-abc"),
		"hub-1/analytics/v1/usage_records/dt=2026-11-28/project=proj-1/agent-1__run-1__pclaude-1__s3__chunk-abc.jsonl.gz"; got != want {
		t.Fatalf("usage_records path = %q, want %q", got, want)
	}

	if got, want := TrajectoryRunInventoryObjectPath("hub-1", dt, "proj-1", "agent-1", "run-1", "claude-1", 3),
		"hub-1/analytics/v1/run_inventories/dt=2026-11-28/project=proj-1/agent-1__run-1__pclaude-1__s3.inventory.json"; got != want {
		t.Fatalf("run_inventory path = %q, want %q", got, want)
	}

	sk := TrajectorySessionKey(".claude/projects/-workspace/sess-1.jsonl")
	if len(sk) != 16 {
		t.Fatalf("session key length = %d, want 16", len(sk))
	}
	if got, want := TrajectoryATIFObjectPath("hub-1", "proj-1", "agent-1", "run-1", sk, "claude-1", 4096, TrajectoryFileFormatJSON, TrajectoryContentEncodingGzip),
		"hub-1/trajectories/v1/atif/project=proj-1/agent=agent-1/run=run-1/"+sk+".pclaude-1.s4096.atif.json.gz"; got != want {
		t.Fatalf("atif json.gz path = %q, want %q", got, want)
	}
	if got, want := TrajectoryATIFObjectPath("hub-1", "proj-1", "agent-1", "run-1", sk, "claude-2", 8192, TrajectoryFileFormatJSONL, TrajectoryContentEncodingGzip),
		"hub-1/trajectories/v1/atif/project=proj-1/agent=agent-1/run=run-1/"+sk+".pclaude-2.s8192.atif.jsonl.gz"; got != want {
		t.Fatalf("atif jsonl.gz path = %q, want %q", got, want)
	}
	if got, want := TrajectoryATIFObjectPath("hub-1", "proj-1", "agent-1", "run-1", sk, "claude-1", 100, TrajectoryFileFormatJSON, TrajectoryContentEncodingIdentity),
		"hub-1/trajectories/v1/atif/project=proj-1/agent=agent-1/run=run-1/"+sk+".pclaude-1.s100.atif.json"; got != want {
		t.Fatalf("atif json path = %q, want %q", got, want)
	}
}

func TestParseAndCompareTrajectoryParserVersion(t *testing.T) {
	valid := []struct {
		in     string
		prefix string
		num    int64
	}{
		{"claude-1", "claude", 1},
		{"claude-10", "claude", 10},
		{"gemini-cli-2", "gemini-cli", 2},
		{"a-0", "a", 0},
	}
	for _, tc := range valid {
		p, n, ok := ParseTrajectoryParserVersion(tc.in)
		if !ok || p != tc.prefix || n != tc.num {
			t.Fatalf("ParseTrajectoryParserVersion(%q) = (%q, %d, %v), want (%q, %d, true)", tc.in, p, n, ok, tc.prefix, tc.num)
		}
		if !IsValidTrajectoryParserVersion(tc.in) {
			t.Fatalf("IsValidTrajectoryParserVersion(%q) = false, want true", tc.in)
		}
	}

	invalid := []string{"", "claude", "claude-", "-1", "Claude-1", "claude_1-2", "claude-1a", "claude-"}
	for _, in := range invalid {
		if IsValidTrajectoryParserVersion(in) {
			t.Fatalf("IsValidTrajectoryParserVersion(%q) = true, want false", in)
		}
	}

	if cmp := CompareTrajectoryParserVersion("claude-2", "claude-10"); cmp >= 0 {
		t.Fatalf("CompareTrajectoryParserVersion(claude-2, claude-10) = %d, want < 0", cmp)
	}
	if cmp := CompareTrajectoryParserVersion("claude-10", "claude-2"); cmp <= 0 {
		t.Fatalf("CompareTrajectoryParserVersion(claude-10, claude-2) = %d, want > 0", cmp)
	}
	if cmp := CompareTrajectoryParserVersion("claude-1", "claude-1"); cmp != 0 {
		t.Fatalf("CompareTrajectoryParserVersion(claude-1, claude-1) = %d, want 0", cmp)
	}
}
