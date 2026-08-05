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

package digest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/logparser"
)

var testBase = time.Date(2026, 3, 22, 16, 0, 0, 0, time.UTC)

func at(ms float64) string {
	return testBase.Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339Nano)
}

// agentEntry builds a scion-agents log entry for one agent.
func agentEntry(id string, ms float64, message string, extra map[string]any) logparser.GCPLogEntry {
	jp := map[string]any{"message": message}
	for k, v := range extra {
		jp[k] = v
	}
	return logparser.GCPLogEntry{
		InsertID:    id + "@" + at(ms),
		Timestamp:   at(ms),
		Severity:    "INFO",
		LogName:     "projects/test/logs/scion-agents",
		Labels:      map[string]string{"agent_id": id},
		JSONPayload: jp,
	}
}

// msgEntry builds a scion-messages log entry.
func msgEntry(fromID, fromName, toID, toName string, ms float64, content string) logparser.GCPLogEntry {
	return logparser.GCPLogEntry{
		InsertID:  "msg@" + at(ms),
		Timestamp: at(ms),
		Severity:  "INFO",
		LogName:   "projects/test/logs/scion-messages",
		Labels:    map[string]string{},
		JSONPayload: map[string]any{
			"message":         "message dispatched",
			"sender":          "agent:" + fromName,
			"sender_id":       fromID,
			"recipient":       "agent:" + toName,
			"recipient_id":    toID,
			"msg_type":        "instruction",
			"message_content": content,
		},
	}
}

// manifest builds a ParseResult carrying just agent identities.
func manifest(agents ...logparser.AgentInfo) *logparser.ParseResult {
	return &logparser.ParseResult{
		Manifest: logparser.PlaybackManifest{Agents: agents},
	}
}

func agent(id, name string) logparser.AgentInfo {
	return logparser.AgentInfo{ID: id, Name: name, Harness: "test", Color: "#123456"}
}

func createdBy(child, parentName string, ms float64) logparser.PlaybackEvent {
	return logparser.PlaybackEvent{
		Type:      "agent_create",
		Timestamp: at(ms),
		Data: logparser.AgentLifecycleEvent{
			AgentID:     child,
			Action:      "create",
			RequestedBy: parentName,
		},
	}
}

func mustBuild(t *testing.T, entries []logparser.GCPLogEntry, parsed *logparser.ParseResult, opts Options) *Digest {
	t.Helper()
	d, err := Build(entries, parsed, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return d
}

func intervalsOf(d *Digest, lifelineID string, kind IntervalKind) []Interval {
	var out []Interval
	for _, iv := range d.Intervals {
		if iv.LifelineID == lifelineID && iv.Kind == kind {
			out = append(out, iv)
		}
	}
	return out
}

func lifelineOf(t *testing.T, d *Digest, id string) Lifeline {
	t.Helper()
	for _, l := range d.Lifelines {
		if l.ID == id {
			return l
		}
	}
	t.Fatalf("lifeline %q not found", id)
	return Lifeline{}
}

func TestIntervalPairingConfidence(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("a", 1000, msgSessStart, nil),
		agentEntry("a", 2000, msgTurnStart, nil),
		agentEntry("a", 3000, msgToolCall, map[string]any{"tool_name": "Read"}),
		agentEntry("a", 4000, msgToolRes, map[string]any{"tool_name": "Read"}),
		// A tool call with no result, but bounded by the turn end that follows.
		agentEntry("a", 5000, msgToolCall, map[string]any{"tool_name": "Write"}),
		agentEntry("a", 6000, msgTurnEnd, nil),
		agentEntry("a", 7000, msgSessEnd, nil),
		// A tool call with nothing after it at all on this lifeline.
		agentEntry("a", 8000, msgToolCall, map[string]any{"tool_name": "Grep"}),
		// An unrelated agent keeps the run going to 12s.
		agentEntry("b", 12000, msgPreStart, nil),
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha"), agent("b", "beta")), DefaultOptions())

	tools := intervalsOf(d, "a", KindTool)
	if len(tools) != 3 {
		t.Fatalf("want 3 tool intervals, got %d: %+v", len(tools), tools)
	}
	want := []struct {
		label string
		start float64
		end   float64
		conf  Confidence
	}{
		{"Read", 3000, 4000, ConfidenceMeasured},
		{"Write", 5000, 6000, ConfidenceInferred},
		{"Grep", 8000, 12000, ConfidenceOpen},
	}
	for i, w := range want {
		got := tools[i]
		if got.Label != w.label || got.StartMs != w.start || got.EndMs != w.end || got.Confidence != w.conf {
			t.Errorf("tool[%d] = %s [%v,%v] %s, want %s [%v,%v] %s",
				i, got.Label, got.StartMs, got.EndMs, got.Confidence, w.label, w.start, w.end, w.conf)
		}
	}

	turns := intervalsOf(d, "a", KindTurn)
	if len(turns) != 1 || turns[0].Confidence != ConfidenceMeasured || turns[0].EndMs != 6000 {
		t.Errorf("turn = %+v, want one measured [2000,6000]", turns)
	}
	sessions := intervalsOf(d, "a", KindSession)
	if len(sessions) != 1 || sessions[0].Confidence != ConfidenceMeasured {
		t.Errorf("session = %+v, want one measured", sessions)
	}
	// No pre_stop was ever seen, so the lifecycle is genuinely open-ended.
	life := intervalsOf(d, "a", KindLifecycle)
	if len(life) != 1 || life[0].Confidence != ConfidenceOpen || life[0].EndMs != 12000 {
		t.Errorf("lifecycle = %+v, want one open ending at 12000", life)
	}
	if lifelineOf(t, d, "a").Died {
		t.Error("lifeline a should not be marked as died")
	}

	if d.Stats.MeasuredIntervals == 0 || d.Stats.InferredIntervals == 0 || d.Stats.OpenIntervals == 0 {
		t.Errorf("stats should cover all three confidences: %+v", d.Stats)
	}
}

func TestIntervalLogIDPreserved(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("a", 1000, msgToolCall, map[string]any{"tool_name": "Read"}),
		agentEntry("a", 2000, msgToolRes, map[string]any{"tool_name": "Read"}),
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha")), DefaultOptions())
	tool := intervalsOf(d, "a", KindTool)[0]
	if tool.LogID != "a@"+at(1000) {
		t.Errorf("LogID = %q, want the insertId of the start entry", tool.LogID)
	}
}

func TestOrphanEndIsInferredFromPreviousEvent(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("a", 1000, msgTurnStart, nil),
		// A result with no matching call: the start must be inferred, not zeroed.
		agentEntry("a", 3000, msgToolRes, map[string]any{"tool_name": "Bash"}),
		agentEntry("a", 4000, msgTurnEnd, nil),
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha")), DefaultOptions())
	tools := intervalsOf(d, "a", KindTool)
	if len(tools) != 1 {
		t.Fatalf("want 1 tool interval, got %d", len(tools))
	}
	if tools[0].Confidence != ConfidenceInferred {
		t.Errorf("confidence = %s, want inferred", tools[0].Confidence)
	}
	if tools[0].StartMs != 1000 || tools[0].EndMs != 3000 {
		t.Errorf("interval = [%v,%v], want [1000,3000]", tools[0].StartMs, tools[0].EndMs)
	}
}

func TestErrorSeverityMarksInterval(t *testing.T) {
	errEntry := agentEntry("a", 2000, msgToolRes, map[string]any{"tool_name": "Read"})
	errEntry.Severity = "ERROR"
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("a", 1000, msgToolCall, map[string]any{"tool_name": "Read"}),
		errEntry,
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha")), DefaultOptions())
	if tool := intervalsOf(d, "a", KindTool)[0]; !tool.Error {
		t.Error("interval should be marked as an error")
	}
}

func TestNestingDepthsAndContainment(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("a", 500, msgSessStart, nil),
		agentEntry("a", 1000, msgTurnStart, nil),
		agentEntry("a", 1500, msgToolCall, map[string]any{"tool_name": "Read"}),
		agentEntry("a", 2000, msgToolRes, map[string]any{"tool_name": "Read"}),
		agentEntry("a", 2500, msgToolCall, map[string]any{"tool_name": "Edit"}),
		agentEntry("a", 3000, msgToolRes, map[string]any{"tool_name": "Edit"}),
		agentEntry("a", 3500, msgTurnEnd, nil),
		agentEntry("a", 4000, msgSessEnd, nil),
		agentEntry("a", 4500, msgPreStop, nil),
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha")), DefaultOptions())

	wantDepth := map[IntervalKind]int{KindLifecycle: 0, KindSession: 1, KindTurn: 2, KindTool: 3}
	for _, iv := range d.Intervals {
		if iv.Depth != wantDepth[iv.Kind] {
			t.Errorf("%s depth = %d, want %d", iv.Kind, iv.Depth, wantDepth[iv.Kind])
		}
		if iv.Confidence != ConfidenceMeasured {
			t.Errorf("%s should be measured, got %s", iv.Kind, iv.Confidence)
		}
	}

	// Every interval below depth 0 must sit inside one at the depth above it.
	for _, iv := range d.Intervals {
		if iv.Depth == 0 {
			continue
		}
		contained := false
		for _, parent := range d.Intervals {
			if parent.LifelineID != iv.LifelineID || parent.Depth != iv.Depth-1 {
				continue
			}
			if parent.StartMs <= iv.StartMs && parent.EndMs >= iv.EndMs {
				contained = true
				break
			}
		}
		if !contained {
			t.Errorf("%s [%v,%v] is not contained by any depth-%d interval",
				iv.Kind, iv.StartMs, iv.EndMs, iv.Depth-1)
		}
	}

	if l := lifelineOf(t, d, "a"); !l.Died || l.DeathMs != 4500 {
		t.Errorf("lifeline = %+v, want died at 4500", l)
	}
}

func TestSlotRecycling(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("a", 10000, msgPreStop, nil),
		agentEntry("c", 5000, msgPreStart, nil),
		agentEntry("c", 25000, msgPreStop, nil),
		agentEntry("b", 20000, msgPreStart, nil),
		agentEntry("b", 30000, msgPreStop, nil),
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha"), agent("b", "beta"), agent("c", "gamma")), DefaultOptions())

	a, b, c := lifelineOf(t, d, "a"), lifelineOf(t, d, "b"), lifelineOf(t, d, "c")
	if a.Slot != b.Slot {
		t.Errorf("non-overlapping lifelines a(slot %d) and b(slot %d) should share a slot", a.Slot, b.Slot)
	}
	if a.Slot == c.Slot {
		t.Errorf("overlapping lifelines a and c must not share slot %d", a.Slot)
	}
	if d.Stats.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent = %d, want 2", d.Stats.MaxConcurrent)
	}
}

func TestAncestryAndDepthFirstOrder(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("r", 0, msgPreStart, nil),
		agentEntry("c1", 1000, msgPreStart, nil),
		agentEntry("g1", 1500, msgPreStart, nil),
		agentEntry("c2", 2000, msgPreStart, nil),
		agentEntry("r", 9000, msgTurnStart, nil),
	}
	parsed := manifest(agent("r", "root"), agent("c1", "child1"), agent("c2", "child2"), agent("g1", "grand1"))
	parsed.Events = []logparser.PlaybackEvent{
		createdBy("c1", "root", 1000),
		createdBy("g1", "child1", 1500),
		createdBy("c2", "root", 2000),
	}
	d := mustBuild(t, entries, parsed, DefaultOptions())

	g1 := lifelineOf(t, d, "g1")
	if g1.ParentID != "c1" {
		t.Errorf("g1.ParentID = %q, want c1", g1.ParentID)
	}
	if len(g1.Ancestry) != 2 || g1.Ancestry[0] != "r" || g1.Ancestry[1] != "c1" {
		t.Errorf("g1.Ancestry = %v, want [r c1]", g1.Ancestry)
	}
	if g1.Depth != 2 {
		t.Errorf("g1.Depth = %d, want 2", g1.Depth)
	}

	// Depth-first: root, child1, grand1, child2 (children ordered by birth).
	wantOrder := []string{"r", "c1", "g1", "c2"}
	for i, id := range wantOrder {
		if got := lifelineOf(t, d, id).Order; got != i {
			t.Errorf("%s.Order = %d, want %d", id, got, i)
		}
	}
	// Digest lifelines are emitted in Order, so a child follows its parent.
	for i, l := range d.Lifelines {
		if l.ID != wantOrder[i] {
			t.Errorf("Lifelines[%d] = %s, want %s", i, l.ID, wantOrder[i])
		}
	}
}

func TestAncestryCycleDoesNotHang(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("b", 1000, msgPreStart, nil),
		agentEntry("a", 5000, msgTurnStart, nil),
	}
	parsed := manifest(agent("a", "alpha"), agent("b", "beta"))
	parsed.Events = []logparser.PlaybackEvent{
		createdBy("a", "beta", 0),
		createdBy("b", "alpha", 1000),
	}

	done := make(chan *Digest, 1)
	go func() {
		d, err := Build(entries, parsed, DefaultOptions())
		if err != nil {
			t.Errorf("Build: %v", err)
		}
		done <- d
	}()
	select {
	case d := <-done:
		if len(d.Lifelines) != 2 {
			t.Fatalf("want 2 lifelines, got %d", len(d.Lifelines))
		}
		seen := map[int]bool{}
		for _, l := range d.Lifelines {
			if seen[l.Order] {
				t.Errorf("duplicate Order %d", l.Order)
			}
			seen[l.Order] = true
			if l.Depth != len(l.Ancestry) {
				t.Errorf("%s: Depth %d != len(Ancestry) %d", l.ID, l.Depth, len(l.Ancestry))
			}
			for _, anc := range l.Ancestry {
				if anc == l.ID {
					t.Errorf("%s appears in its own ancestry %v", l.ID, l.Ancestry)
				}
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Build hung on a parent cycle")
	}
}

func TestEdgeReceiveInference(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("b", 0, msgPreStart, nil),
		msgEntry("a", "alpha", "b", "beta", 5000, "please look at this"),
		agentEntry("b", 6000, msgTurnStart, nil),
		agentEntry("b", 7000, msgTurnEnd, nil),
		// Nothing happens on b after this one.
		msgEntry("a", "alpha", "b", "beta", 9000, "and this"),
		agentEntry("a", 10000, msgTurnStart, nil),
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha"), agent("b", "beta")), DefaultOptions())

	var msgs []Edge
	for _, e := range d.Edges {
		if e.Kind == EdgeMessage {
			msgs = append(msgs, e)
		}
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 message edges, got %d", len(msgs))
	}
	if msgs[0].SendMs != 5000 || msgs[0].RecvMs != 6000 || msgs[0].RecvConfidence != ConfidenceInferred {
		t.Errorf("edge0 = send %v recv %v %s, want 5000 -> 6000 inferred",
			msgs[0].SendMs, msgs[0].RecvMs, msgs[0].RecvConfidence)
	}
	if msgs[1].SendMs != 9000 || msgs[1].RecvMs != 9000 || msgs[1].RecvConfidence != ConfidenceOpen {
		t.Errorf("edge1 = send %v recv %v %s, want 9000 -> 9000 open",
			msgs[1].SendMs, msgs[1].RecvMs, msgs[1].RecvConfidence)
	}
	if msgs[0].Label != "please look at this" || msgs[0].MsgType != "instruction" {
		t.Errorf("edge0 label/type = %q/%q", msgs[0].Label, msgs[0].MsgType)
	}
	if d.Stats.InferredEdges != 1 {
		t.Errorf("InferredEdges = %d, want 1", d.Stats.InferredEdges)
	}
}

func TestEdgeReceiveWindowFallsBackToOpen(t *testing.T) {
	opts := DefaultOptions()
	opts.InferRecvWindowMs = 1000
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("b", 0, msgPreStart, nil),
		msgEntry("a", "alpha", "b", "beta", 3000, "hello"),
		// b only wakes up well outside the inference window.
		agentEntry("b", 9000, msgTurnStart, nil),
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha"), agent("b", "beta")), opts)
	for _, e := range d.Edges {
		if e.Kind != EdgeMessage {
			continue
		}
		if e.RecvConfidence != ConfidenceOpen || e.RecvMs != e.SendMs {
			t.Errorf("edge = %+v, want horizontal and open", e)
		}
	}
}

func TestUnknownEndpointsAreDropped(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		msgEntry("a", "alpha", "nobody", "nobody", 1000, "into the void"),
		agentEntry("a", 2000, msgTurnStart, nil),
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha")), DefaultOptions())
	for _, e := range d.Edges {
		if e.Kind == EdgeMessage {
			t.Errorf("edge with unknown recipient should have been dropped: %+v", e)
		}
	}
}

func TestRejectedMessagesIgnored(t *testing.T) {
	rejected := msgEntry("a", "alpha", "b", "beta", 1000, "nope")
	rejected.JSONPayload["message"] = "message rejected: recipient full"
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgPreStart, nil),
		agentEntry("b", 0, msgPreStart, nil),
		rejected,
		agentEntry("a", 2000, msgTurnStart, nil),
	}
	d := mustBuild(t, entries, manifest(agent("a", "alpha"), agent("b", "beta")), DefaultOptions())
	for _, e := range d.Edges {
		if e.Kind == EdgeMessage {
			t.Errorf("rejected message should not become an edge: %+v", e)
		}
	}
}

func TestSpawnAndDestroyEdges(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("r", 0, msgPreStart, nil),
		agentEntry("c", 1000, msgPreStart, nil),
		agentEntry("c", 5000, msgPreStop, nil),
	}
	parsed := manifest(agent("r", "root"), agent("c", "child"))
	parsed.Events = []logparser.PlaybackEvent{
		createdBy("c", "root", 1000),
		{
			Type:      "agent_destroy",
			Timestamp: at(5000),
			Data: logparser.AgentLifecycleEvent{
				AgentID: "c", Action: "destroy", RequestedBy: "root",
			},
		},
	}
	d := mustBuild(t, entries, parsed, DefaultOptions())

	kinds := map[EdgeKind]Edge{}
	for _, e := range d.Edges {
		kinds[e.Kind] = e
	}
	spawn, ok := kinds[EdgeSpawn]
	if !ok {
		t.Fatal("no spawn edge")
	}
	if spawn.FromID != "r" || spawn.ToID != "c" || spawn.SendMs != 1000 {
		t.Errorf("spawn edge = %+v, want r->c at 1000", spawn)
	}
	destroy, ok := kinds[EdgeDestroy]
	if !ok {
		t.Fatal("no destroy edge")
	}
	if destroy.FromID != "r" || destroy.ToID != "c" || destroy.SendMs != 5000 {
		t.Errorf("destroy edge = %+v, want r->c at 5000", destroy)
	}
}

func TestBuildRejectsEmptyInput(t *testing.T) {
	if _, err := Build(nil, nil, DefaultOptions()); err == nil {
		t.Fatal("want an error for an empty log")
	}
}

func TestBuildWithoutParseResult(t *testing.T) {
	entries := []logparser.GCPLogEntry{
		agentEntry("abcdef0123", 0, msgPreStart, nil),
		agentEntry("abcdef0123", 1000, msgTurnStart, nil),
	}
	d := mustBuild(t, entries, nil, DefaultOptions())
	if len(d.Lifelines) != 1 {
		t.Fatalf("want 1 lifeline, got %d", len(d.Lifelines))
	}
	if d.Lifelines[0].Name != "abcdef01" || d.Lifelines[0].Color == "" {
		t.Errorf("lifeline = %+v, want a short-id name and a color", d.Lifelines[0])
	}
}

func TestSyntheticRunEndToEnd(t *testing.T) {
	const durationMs = 45 * 60 * 1000
	d, err := BuildSyntheticDigest(1, 60, durationMs, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildSyntheticDigest: %v", err)
	}

	if d.Version != SchemaVersion {
		t.Errorf("Version = %d, want %d", d.Version, SchemaVersion)
	}
	if d.DurationMs <= 0 || d.DurationMs > durationMs+1 {
		t.Errorf("DurationMs = %v, want (0, %d]", d.DurationMs, durationMs)
	}
	s := d.Stats
	if s.LifelineCount < 40 {
		t.Errorf("LifelineCount = %d, want a realistically large run", s.LifelineCount)
	}
	if s.MaxConcurrent < 4 || s.MaxConcurrent > s.LifelineCount/2 {
		t.Errorf("MaxConcurrent = %d with %d lifelines: slot recycling is not being exercised",
			s.MaxConcurrent, s.LifelineCount)
	}
	if s.IntervalCount == 0 || s.EdgeCount == 0 {
		t.Fatalf("stats = %+v, want intervals and edges", s)
	}
	if s.MeasuredIntervals == 0 || s.InferredIntervals == 0 || s.OpenIntervals == 0 {
		t.Errorf("stats = %+v, want all three confidence classes populated", s)
	}
	if s.InferredEdges == 0 {
		t.Error("want at least one edge with an inferred arrival")
	}
	if s.CompressionRatio < 1.5 || s.CompressionRatio > 120 {
		t.Errorf("CompressionRatio = %v, want a sane speed-up", s.CompressionRatio)
	}
	if d.Density.Peak <= 0 || len(d.Density.Samples) == 0 {
		t.Errorf("density = %+v, want samples", d.Density)
	}
	if d.Warp.TotalTauMs <= 0 || len(d.Warp.Knots) < 2 {
		t.Errorf("warp = %d knots, tau %v", len(d.Warp.Knots), d.Warp.TotalTauMs)
	}

	// Ancestry should be a real forest, not a flat list.
	maxDepth, roots := 0, 0
	for _, l := range d.Lifelines {
		if l.Depth > maxDepth {
			maxDepth = l.Depth
		}
		if l.ParentID == "" {
			roots++
		}
		if l.DeathMs < l.BirthMs {
			t.Errorf("%s dies before it is born: %+v", l.Name, l)
		}
		if l.Slot < 0 || l.Slot >= s.MaxConcurrent {
			t.Errorf("%s has out-of-range slot %d", l.Name, l.Slot)
		}
	}
	if maxDepth < 2 {
		t.Errorf("maxDepth = %d, want a multi-layer ancestry", maxDepth)
	}
	if roots != 1 {
		t.Errorf("roots = %d, want exactly one root agent", roots)
	}

	// Every interval must reference a real lifeline and lie inside the run.
	known := map[string]bool{}
	for _, l := range d.Lifelines {
		known[l.ID] = true
	}
	for _, iv := range d.Intervals {
		if !known[iv.LifelineID] {
			t.Fatalf("interval %s references unknown lifeline %s", iv.ID, iv.LifelineID)
		}
		if iv.StartMs < 0 || iv.EndMs > d.DurationMs+1 || iv.EndMs < iv.StartMs {
			t.Fatalf("interval %s out of bounds: [%v,%v]", iv.ID, iv.StartMs, iv.EndMs)
		}
	}
	for _, e := range d.Edges {
		if !known[e.FromID] || !known[e.ToID] {
			t.Fatalf("edge %s references unknown lifelines %s->%s", e.ID, e.FromID, e.ToID)
		}
		if e.RecvMs < e.SendMs {
			t.Fatalf("edge %s arrives before it is sent", e.ID)
		}
	}
}

func TestBuildFromFile(t *testing.T) {
	entries := SynthesizeLog(2, 20, 10*60*1000)
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "log.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	d, err := BuildFromFile(path, "", 3, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildFromFile: %v", err)
	}
	if d.Stats.LifelineCount == 0 || d.Stats.IntervalCount == 0 {
		t.Fatalf("stats = %+v, want a populated digest", d.Stats)
	}
	if d.ProjectID == "" {
		t.Error("ProjectID should come through from the parser's manifest")
	}

	// The same input through Build directly must agree.
	direct, err := BuildSyntheticDigest(2, 20, 10*60*1000, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildSyntheticDigest: %v", err)
	}
	if direct.Stats != d.Stats {
		t.Errorf("stats differ between file and in-memory paths:\n %+v\n %+v", d.Stats, direct.Stats)
	}

	if _, err := BuildFromFile(filepath.Join(t.TempDir(), "missing.json"), "", 3, DefaultOptions()); err == nil {
		t.Error("want an error for a missing file")
	}
}

func TestSynthesizeLogIsDeterministic(t *testing.T) {
	a := SynthesizeLog(7, 30, 10*60*1000)
	b := SynthesizeLog(7, 30, 10*60*1000)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].InsertID != b[i].InsertID || a[i].Timestamp != b[i].Timestamp {
			t.Fatalf("entry %d differs between runs with the same seed", i)
		}
	}
	if c := SynthesizeLog(8, 30, 10*60*1000); len(c) == len(a) && c[len(c)/2].Timestamp == a[len(a)/2].Timestamp {
		t.Error("different seeds produced a suspiciously identical run")
	}
}
