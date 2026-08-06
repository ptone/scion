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

// Tests for the two remaining ways a rebuilt digest can move things that the
// reader is looking at: the column an agent sits in, and where t=0 is.
//
// Both are pure functions of the whole log, which is exactly right for an
// export that will never change again, and exactly wrong for a session being
// read as it happens.

package digest

import (
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/logparser"
)

// lived returns the pre_start/pre_stop pair for an agent with a measured death.
func lived(id string, birthMs, deathMs float64) []logparser.GCPLogEntry {
	return []logparser.GCPLogEntry{
		agentEntry(id, birthMs, msgPreStart, nil),
		agentEntry(id, deathMs, msgPreStop, nil),
	}
}

func slotOf(t *testing.T, d *Digest, id string) int {
	t.Helper()
	return lifelineOf(t, d, id).Slot
}

// assertNoSharedColumn is the invariant that column recycling exists to respect
// and that pinning must not be allowed to break.
func assertNoSharedColumn(t *testing.T, d *Digest) {
	t.Helper()
	for i, a := range d.Lifelines {
		for _, b := range d.Lifelines[i+1:] {
			if a.Slot != b.Slot {
				continue
			}
			if a.BirthMs < b.DeathMs && b.BirthMs < a.DeathMs {
				t.Errorf("lifelines %s [%.0f,%.0f) and %s [%.0f,%.0f) share column %d",
					a.ID, a.BirthMs, a.DeathMs, b.ID, b.BirthMs, b.DeathMs, a.Slot)
			}
		}
	}
}

func TestColumnsRecycleWhenColoredFromScratch(t *testing.T) {
	// The behavior pinning has to preserve when there is nothing to pin: two
	// agents whose lives do not overlap share one column.
	parsed := manifest(agent("a", "alpha"), agent("c", "charlie"))
	entries := append(lived("a", 1_000, 2_000), lived("c", 3_000, 4_000)...)
	d := mustBuild(t, entries, parsed, DefaultOptions())

	if got := slotOf(t, d, "a"); got != 0 {
		t.Errorf("alpha: column %d, want 0", got)
	}
	if got := slotOf(t, d, "c"); got != 0 {
		t.Errorf("charlie: column %d, want 0 (recycled)", got)
	}
	if d.Stats.MaxConcurrent != 1 {
		t.Errorf("MaxConcurrent %d, want 1", d.Stats.MaxConcurrent)
	}
}

func TestPinnedColumnsSurviveAnAgentDiscoveredEarlier(t *testing.T) {
	parsed := manifest(agent("a", "alpha"), agent("b", "bravo"), agent("c", "charlie"))
	base := append(lived("a", 1_000, 2_000), lived("c", 3_000, 4_000)...)
	first := mustBuild(t, base, parsed, DefaultOptions())

	// Bravo turns out to have been alive across both of them all along. Colored
	// from scratch it claims column 0 and displaces the pair.
	withB := append(lived("b", 500, 3_500), base...)
	loose := mustBuild(t, withB, parsed, DefaultOptions())
	if slotOf(t, loose, "a") == slotOf(t, first, "a") &&
		slotOf(t, loose, "c") == slotOf(t, first, "c") {
		t.Fatal("nothing moved without pinning, so this test cannot detect a reseat")
	}

	pinned := DefaultOptions()
	pinned.PinnedSlots = SlotsOf(first)
	after := mustBuild(t, withB, parsed, pinned)
	for _, id := range []string{"a", "c"} {
		if got, want := slotOf(t, after, id), slotOf(t, first, id); got != want {
			t.Errorf("%s was reseated from column %d to %d", id, want, got)
		}
	}
	if slotOf(t, after, "b") == slotOf(t, after, "a") {
		t.Errorf("bravo took the column it overlaps")
	}
	assertNoSharedColumn(t, after)
}

func TestPinnedColumnsSurviveAnAgentsDeathArriving(t *testing.T) {
	// While an agent is still alive its death is the end of the run, so nothing
	// can share its column. The moment a pre_stop arrives that stops being true
	// and a later agent would collapse into the freed column -- a jump caused by
	// news about someone else entirely.
	parsed := manifest(agent("a", "alpha"), agent("c", "charlie"))
	open := append([]logparser.GCPLogEntry{agentEntry("a", 1_000, msgPreStart, nil)},
		lived("c", 3_000, 4_000)...)
	first := mustBuild(t, open, parsed, DefaultOptions())
	if slotOf(t, first, "c") != 1 {
		t.Fatalf("charlie: column %d, want 1 while alpha is open", slotOf(t, first, "c"))
	}

	closed := append([]logparser.GCPLogEntry{agentEntry("a", 2_000, msgPreStop, nil)}, open...)
	if got := slotOf(t, mustBuild(t, closed, parsed, DefaultOptions()), "c"); got != 0 {
		t.Fatalf("charlie: column %d, want the unpinned collapse to 0", got)
	}

	pinned := DefaultOptions()
	pinned.PinnedSlots = SlotsOf(first)
	after := mustBuild(t, closed, parsed, pinned)
	if got := slotOf(t, after, "c"); got != 1 {
		t.Errorf("charlie moved to column %d when alpha's death arrived", got)
	}
	if got := after.Stats.MaxConcurrent; got != 2 {
		t.Errorf("MaxConcurrent %d, want 2 columns including the one held open", got)
	}
}

func TestUnhonorableColumnPinIsDropped(t *testing.T) {
	// Two agents pinned to one column, alive at the same time. Nothing should
	// produce this, but drawing two lifelines down one column would be a far
	// worse failure than moving one of them.
	parsed := manifest(agent("a", "alpha"), agent("b", "bravo"))
	entries := append(lived("a", 1_000, 4_000), lived("b", 2_000, 5_000)...)
	opts := DefaultOptions()
	opts.PinnedSlots = map[string]int{"a": 0, "b": 0, "ghost": 9}
	d := mustBuild(t, entries, parsed, opts)

	if slotOf(t, d, "a") != 0 {
		t.Errorf("alpha lost its pin to the later claim")
	}
	assertNoSharedColumn(t, d)
	// A pin for a lifeline that no longer exists must not reserve a column.
	if d.Stats.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent %d, want 2", d.Stats.MaxConcurrent)
	}
}

func TestOriginDefaultsToTheFirstEntry(t *testing.T) {
	entries, parsed := twoAgentRun()
	d := mustBuild(t, entries, parsed, DefaultOptions())
	if got, want := d.StartedAt, at(1_000); got != want {
		t.Errorf("StartedAt %q, want %q", got, want)
	}
	if got := d.DurationMs; got != 3_000 {
		t.Errorf("DurationMs %.0f, want 3000", got)
	}
}

func TestPinnedOriginHoldsOffsetsStill(t *testing.T) {
	base, parsed := twoAgentRun()
	origin := testBase.Add(-60 * time.Second)
	opts := DefaultOptions()
	opts.Origin = origin

	first := mustBuild(t, base, parsed, opts)
	if got, want := first.StartedAt, origin.Format(time.RFC3339Nano); got != want {
		t.Errorf("StartedAt %q, want the pinned origin %q", got, want)
	}
	if got := first.DurationMs; got != 64_000 {
		t.Errorf("DurationMs %.0f, want 64000 measured from the pinned origin", got)
	}

	// An agent that was busy before the first entry we had. Unpinned, this moves
	// t=0 back by 900ms and every offset in the run with it.
	earlier := append([]logparser.GCPLogEntry{
		agentEntry("b", 100, msgSessStart, nil),
		agentEntry("b", 900, msgTurnEnd, nil),
	}, base...)
	if got := mustBuild(t, earlier, parsed, DefaultOptions()).StartedAt; got == at(1_000) {
		t.Fatal("the unpinned origin did not move, so this test proves nothing")
	}

	// Compare alpha's intervals only: bravo's own bars legitimately move, since
	// the new entries are the first evidence of when bravo was alive.
	second := mustBuild(t, earlier, parsed, opts)
	byID := map[string]float64{}
	for _, iv := range first.Intervals {
		if iv.LifelineID == "a" {
			byID[iv.ID] = iv.StartMs
		}
	}
	shared := 0
	for _, iv := range second.Intervals {
		want, ok := byID[iv.ID]
		if !ok {
			continue
		}
		shared++
		if iv.StartMs != want {
			t.Errorf("interval %s slid from %.0f to %.0f", iv.ID, want, iv.StartMs)
		}
	}
	if shared < 4 {
		t.Fatalf("the two builds shared only %d intervals", shared)
	}
}

func TestEntriesBeforeAPinnedOriginClampToZero(t *testing.T) {
	// Dropping them would lose the session start a later interval depends on, so
	// they are kept at the origin rather than given a negative offset.
	parsed := manifest(agent("a", "alpha"))
	entries := []logparser.GCPLogEntry{
		agentEntry("a", 0, msgSessStart, nil),
		agentEntry("a", 1_000, msgTurnStart, nil),
		agentEntry("a", 2_000, msgTurnEnd, nil),
	}
	opts := DefaultOptions()
	opts.Origin = testBase.Add(500 * time.Millisecond)
	d := mustBuild(t, entries, parsed, opts)

	if d.DurationMs != 1_500 {
		t.Errorf("DurationMs %.0f, want 1500", d.DurationMs)
	}
	sess := intervalsOf(d, "a", KindSession)
	if len(sess) != 1 {
		t.Fatalf("expected the pre-origin session to survive, got %d", len(sess))
	}
	if sess[0].StartMs != 0 {
		t.Errorf("session starts at %.0f, want 0", sess[0].StartMs)
	}
	for _, iv := range d.Intervals {
		if iv.StartMs < 0 || iv.EndMs < 0 {
			t.Errorf("interval %s has a negative offset [%.0f,%.0f]", iv.ID, iv.StartMs, iv.EndMs)
		}
	}
}
