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

// Tests for the stability of interval and edge identity.
//
// Identity used to be positional, which is correct exactly once: for a run that
// has already finished. A digest that is rebuilt as more log arrives has to
// hand the same event the same id every time, or the reader's selection, the
// open message reader and any deep link quietly move to a neighbouring event.

package digest

import (
	"os"
	"testing"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/logparser"
)

// originKey identifies an event by what it is, independently of its id, so a
// test can ask "did this same thing keep its name".
type originKey struct {
	lifeline string
	kind     string
	logID    string
}

func intervalOrigins(d *Digest) map[originKey]string {
	out := make(map[originKey]string, len(d.Intervals))
	for _, iv := range d.Intervals {
		out[originKey{iv.LifelineID, string(iv.Kind), iv.LogID}] = iv.ID
	}
	return out
}

func intervalPositions(d *Digest) map[originKey]int {
	out := make(map[originKey]int, len(d.Intervals))
	for i, iv := range d.Intervals {
		out[originKey{iv.LifelineID, string(iv.Kind), iv.LogID}] = i
	}
	return out
}

// twoAgentRun is the baseline: one agent working from 1s to 4s.
func twoAgentRun() ([]logparser.GCPLogEntry, *logparser.ParseResult) {
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 1_000, msgSessStart, nil),
		agentEntry("a", 1_500, msgTurnStart, nil),
		agentEntry("a", 2_000, msgToolCall, map[string]any{"tool_name": "read"}),
		agentEntry("a", 2_500, msgToolRes, map[string]any{"tool_name": "read"}),
		agentEntry("a", 3_000, msgTurnEnd, nil),
		agentEntry("a", 4_000, msgSessEnd, nil),
	}
	return entries, manifest(agent("a", "alpha"), agent("b", "bravo"))
}

func TestIdentitySurvivesAnEarlierInsertion(t *testing.T) {
	base, parsed := twoAgentRun()
	before := mustBuild(t, base, parsed, DefaultOptions())

	// A second agent turns out to have been busy before the first one started.
	// Under positional ids this pushes every interval in the run down a slot.
	earlier := append([]logparser.GCPLogEntry{
		agentEntry("b", 100, msgSessStart, nil),
		agentEntry("b", 200, msgTurnStart, nil),
		agentEntry("b", 900, msgTurnEnd, nil),
	}, base...)
	after := mustBuild(t, earlier, parsed, DefaultOptions())

	beforeIDs, afterIDs := intervalOrigins(before), intervalOrigins(after)
	shared := 0
	for k, id := range beforeIDs {
		got, ok := afterIDs[k]
		if !ok {
			t.Errorf("interval %+v vanished from the rebuilt digest", k)
			continue
		}
		shared++
		if got != id {
			t.Errorf("interval %+v was renamed %q -> %q", k, id, got)
		}
	}
	if shared < 4 {
		t.Fatalf("expected the two builds to share intervals, shared %d", shared)
	}

	// Guard against the test passing for the wrong reason: if nothing actually
	// moved, positional ids would have survived too and this proves nothing.
	beforePos, afterPos := intervalPositions(before), intervalPositions(after)
	moved := 0
	for k, i := range beforePos {
		if j, ok := afterPos[k]; ok && j != i {
			moved++
		}
	}
	if moved == 0 {
		t.Fatal("no interval changed position, so this test cannot detect renumbering")
	}
}

func TestEdgeIdentitySurvivesAnEarlierInsertion(t *testing.T) {
	parsed := manifest(agent("a", "alpha"), agent("b", "bravo"))
	base := []logparser.GCPLogEntry{
		agentEntry("a", 1_000, msgSessStart, nil),
		agentEntry("b", 1_100, msgSessStart, nil),
		msgEntry("a", "alpha", "b", "bravo", 2_000, "second"),
		agentEntry("b", 2_100, msgTurnStart, nil),
	}
	before := mustBuild(t, base, parsed, DefaultOptions())

	earlier := append([]logparser.GCPLogEntry{
		msgEntry("b", "bravo", "a", "alpha", 1_200, "first"),
	}, base...)
	after := mustBuild(t, earlier, parsed, DefaultOptions())

	byLog := func(d *Digest) map[string]string {
		out := map[string]string{}
		for _, e := range d.Edges {
			out[string(e.Kind)+"/"+e.LogID+"/"+e.FromID+"/"+e.ToID] = e.ID
		}
		return out
	}
	b, a := byLog(before), byLog(after)
	if len(b) == 0 {
		t.Fatal("baseline produced no edges")
	}
	for k, id := range b {
		if got, ok := a[k]; !ok {
			t.Errorf("edge %s vanished", k)
		} else if got != id {
			t.Errorf("edge %s was renamed %q -> %q", k, id, got)
		}
	}
	if len(a) <= len(b) {
		t.Fatalf("expected the rebuild to add an edge, had %d then %d", len(b), len(a))
	}
}

func TestIdentityIsStableWhileAnIntervalIsStillOpen(t *testing.T) {
	// The live case proper: a turn that has started but not finished, rebuilt
	// after its end arrives. The bar's confidence and length both change; its
	// identity must not.
	parsed := manifest(agent("a", "alpha"))
	open := []logparser.GCPLogEntry{
		agentEntry("a", 1_000, msgSessStart, nil),
		agentEntry("a", 1_500, msgTurnStart, nil),
	}
	closed := append(append([]logparser.GCPLogEntry{}, open...),
		agentEntry("a", 3_000, msgTurnEnd, nil))

	turnOf := func(d *Digest) Interval {
		t.Helper()
		ivs := intervalsOf(d, "a", KindTurn)
		if len(ivs) != 1 {
			t.Fatalf("expected exactly one turn, got %d", len(ivs))
		}
		return ivs[0]
	}
	first, second := turnOf(mustBuild(t, open, parsed, DefaultOptions())),
		turnOf(mustBuild(t, closed, parsed, DefaultOptions()))

	if first.ID != second.ID {
		t.Errorf("turn was renamed as it closed: %q -> %q", first.ID, second.ID)
	}
	if first.Confidence == second.Confidence {
		t.Errorf("expected the turn's confidence to resolve, both were %q", first.Confidence)
	}
}

func TestIdentitiesAreUnique(t *testing.T) {
	check := func(t *testing.T, d *Digest) {
		t.Helper()
		seen := make(map[string]bool, len(d.Intervals)+len(d.Edges))
		for _, iv := range d.Intervals {
			if iv.ID == "" {
				t.Fatalf("interval on %s has no id", iv.LifelineID)
			}
			if seen[iv.ID] {
				t.Errorf("duplicate interval id %q", iv.ID)
			}
			seen[iv.ID] = true
		}
		seen = make(map[string]bool, len(d.Edges))
		for _, e := range d.Edges {
			if e.ID == "" {
				t.Fatalf("edge %s->%s has no id", e.FromID, e.ToID)
			}
			if seen[e.ID] {
				t.Errorf("duplicate edge id %q", e.ID)
			}
			seen[e.ID] = true
		}
	}

	t.Run("synthetic", func(t *testing.T) {
		d, err := BuildSyntheticDigest(7, 24, 20*60*1000, DefaultOptions())
		if err != nil {
			t.Fatalf("BuildSyntheticDigest: %v", err)
		}
		check(t, d)
	})

	// The synthetic generator is partly circular, so where a real export is
	// available it is the one that counts.
	t.Run("real export", func(t *testing.T) {
		const path = "/tmp/relic-game.json"
		if _, err := os.Stat(path); err != nil {
			t.Skip("no real export available")
		}
		d, err := BuildFromFile(path, "", 3, DefaultOptions())
		if err != nil {
			t.Fatalf("BuildFromFile: %v", err)
		}
		check(t, d)
		t.Logf("checked %d intervals and %d edges", len(d.Intervals), len(d.Edges))
	})
}

func TestUniquifyDisambiguatesCollisions(t *testing.T) {
	keys := []string{"a", "b", "a", "a"}
	got := make([]string, len(keys))
	uniquify(len(keys), func(i int) string { return keys[i] }, func(i int, id string) { got[i] = id })
	want := []string{"a", "b", "a~2", "a~3"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}
