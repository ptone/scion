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
	"math"
	"testing"
)

// velocityAtWall reports the playback velocity at a wall-clock moment, which is
// how the "are we already slow when the burst arrives?" question is phrased.
func velocityAtWall(w Warp, wallMs float64) float64 {
	return w.VelocityAt(w.TauAt(wallMs))
}

// spikeIntervals returns count zero-length intervals spread over [from, to).
func spikeIntervals(from, to float64, count int) []Interval {
	out := make([]Interval, 0, count)
	step := (to - from) / float64(count)
	for i := 0; i < count; i++ {
		t := from + float64(i)*step
		out = append(out, Interval{
			LifelineID: "a",
			Kind:       KindTool,
			Depth:      3,
			StartMs:    t,
			EndMs:      t,
			Confidence: ConfidenceOpen, // only the start counts as an observed event
		})
	}
	return out
}

func testOptions() Options {
	o := DefaultOptions()
	o.DensityBucketMs = 1000
	o.FrameMs = 60_000
	o.MinVelocity = 1
	o.MaxVelocity = 120
	o.TargetEventsPerViewerSecond = 6
	// A brisker acceleration limit than the default keeps the fixtures short
	// enough to reason about while still exercising the same code.
	o.MaxAccel = 0.05
	return o
}

func checkStrictlyIncreasing(t *testing.T, w Warp) {
	t.Helper()
	if len(w.Knots) < 2 {
		t.Fatalf("want at least 2 knots, got %d", len(w.Knots))
	}
	for i := 1; i < len(w.Knots); i++ {
		if w.Knots[i].TauMs <= w.Knots[i-1].TauMs {
			t.Fatalf("tau not strictly increasing at knot %d: %v <= %v",
				i, w.Knots[i].TauMs, w.Knots[i-1].TauMs)
		}
		if w.Knots[i].WallMs <= w.Knots[i-1].WallMs {
			t.Fatalf("wall not strictly increasing at knot %d: %v <= %v",
				i, w.Knots[i].WallMs, w.Knots[i-1].WallMs)
		}
	}
	if last := w.Knots[len(w.Knots)-1]; math.Abs(last.TauMs-w.TotalTauMs) > 1e-6 {
		t.Errorf("TotalTauMs = %v but last knot is at %v", w.TotalTauMs, last.TauMs)
	}
}

func TestWarpIsStrictlyMonotonicAndInvertible(t *testing.T) {
	opts := testOptions()
	const durationMs = 900_000

	// A run with a quiet start, a heavy middle and a moderate tail: enough
	// variation that the mapping is genuinely non-linear.
	var intervals []Interval
	intervals = append(intervals, spikeIntervals(0, 200_000, 20)...)
	intervals = append(intervals, spikeIntervals(300_000, 400_000, 900)...)
	intervals = append(intervals, spikeIntervals(600_000, 900_000, 300)...)
	edges := []Edge{{Kind: EdgeMessage, SendMs: 350_000}, {Kind: EdgeMessage, SendMs: 700_000}}

	d := computeDensity(intervals, edges, durationMs, opts)
	w := planWarp(d, durationMs, opts)
	checkStrictlyIncreasing(t, w)

	if w.MinVelocity < opts.MinVelocity-1e-9 || w.MaxVelocity > opts.MaxVelocity+1e-9 {
		t.Errorf("profile [%v,%v] escapes the clamp [%v,%v]",
			w.MinVelocity, w.MaxVelocity, opts.MinVelocity, opts.MaxVelocity)
	}

	// tau -> wall -> tau must round-trip, and so must wall -> tau -> wall.
	const steps = 401
	for i := 0; i <= steps; i++ {
		tau := w.TotalTauMs * float64(i) / steps
		wall := w.WallAt(tau)
		back := w.TauAt(wall)
		if math.Abs(back-tau) > 1e-6 {
			t.Fatalf("tau round-trip failed: %v -> %v -> %v", tau, wall, back)
		}
		if wall < -1e-9 || wall > durationMs+1e-9 {
			t.Fatalf("WallAt(%v) = %v, outside the run", tau, wall)
		}
	}
	for i := 0; i <= steps; i++ {
		wall := durationMs * float64(i) / steps
		tau := w.TauAt(wall)
		back := w.WallAt(tau)
		if math.Abs(back-wall) > 1e-6 {
			t.Fatalf("wall round-trip failed: %v -> %v -> %v", wall, tau, back)
		}
	}

	// Both directions must also be monotonic when sampled, not just at knots.
	prevWall := math.Inf(-1)
	for i := 0; i <= steps; i++ {
		wall := w.WallAt(w.TotalTauMs * float64(i) / steps)
		if wall < prevWall {
			t.Fatalf("WallAt is not monotonic at sample %d", i)
		}
		prevWall = wall
	}
}

func TestVelocityAtMatchesSegmentSlope(t *testing.T) {
	opts := testOptions()
	const durationMs = 300_000
	intervals := append(spikeIntervals(0, 100_000, 10), spikeIntervals(150_000, 200_000, 400)...)
	w := planWarp(computeDensity(intervals, nil, durationMs, opts), durationMs, opts)

	for i := 0; i < len(w.Knots)-1; i++ {
		a, b := w.Knots[i], w.Knots[i+1]
		mid := (a.TauMs + b.TauMs) / 2
		want := (b.WallMs - a.WallMs) / (b.TauMs - a.TauMs)
		if got := w.VelocityAt(mid); math.Abs(got-want) > 1e-9 {
			t.Fatalf("VelocityAt(%v) = %v, want the segment slope %v", mid, got, want)
		}
		// A finite difference of WallAt must agree with the reported velocity.
		h := (b.TauMs - a.TauMs) / 100
		fd := (w.WallAt(mid+h) - w.WallAt(mid-h)) / (2 * h)
		if math.Abs(fd-want) > 1e-6*math.Max(1, want) {
			t.Fatalf("finite difference %v disagrees with velocity %v", fd, want)
		}
	}
}

func TestUniformDensityGivesLinearWarp(t *testing.T) {
	opts := testOptions()
	const durationMs = 600_000

	// Exactly one event per bucket for the whole run.
	var intervals []Interval
	for t := 0.0; t < durationMs; t += opts.DensityBucketMs {
		intervals = append(intervals, Interval{
			LifelineID: "a", Kind: KindTurn, Depth: 2,
			StartMs: t, EndMs: t, Confidence: ConfidenceOpen,
		})
	}
	d := computeDensity(intervals, nil, durationMs, opts)
	for i, s := range d.Samples {
		if math.Abs(s-d.Samples[0]) > 1e-9 {
			t.Fatalf("smoothed density is not uniform: sample %d = %v, sample 0 = %v", i, s, d.Samples[0])
		}
	}

	w := planWarp(d, durationMs, opts)
	checkStrictlyIncreasing(t, w)
	if len(w.Knots) != 2 {
		t.Errorf("a constant-velocity profile should collapse to 2 knots, got %d", len(w.Knots))
	}

	// Linearity: wall time must advance in exact proportion to viewer time.
	slope := durationMs / w.TotalTauMs
	for i := 0; i <= 50; i++ {
		tau := w.TotalTauMs * float64(i) / 50
		want := tau * slope
		if got := w.WallAt(tau); math.Abs(got-want) > 1e-6*durationMs {
			t.Fatalf("WallAt(%v) = %v, want %v (linear)", tau, got, want)
		}
	}

	// Density is 1 event per second, so holding 6 events per viewer-second needs
	// 6 wall-seconds per viewer-second.
	if math.Abs(slope-6) > 0.05 {
		t.Errorf("velocity = %v, want ~6 (target %v events/s over density 1/s)",
			slope, opts.TargetEventsPerViewerSecond)
	}
}

func TestProfileDeceleratesBeforeTheBurst(t *testing.T) {
	opts := testOptions()
	const (
		durationMs = 900_000
		burstStart = 600_000
		burstEnd   = 660_000
	)
	// Ten minutes of near-silence, then a dense minute of work.
	intervals := append(spikeIntervals(0, burstStart, 6), spikeIntervals(burstStart, burstEnd, 900)...)
	d := computeDensity(intervals, nil, durationMs, opts)
	w := planWarp(d, durationMs, opts)
	checkStrictlyIncreasing(t, w)

	vIdle := velocityAtWall(w, burstStart/2)
	vFrameBefore := velocityAtWall(w, burstStart-opts.FrameMs)
	vAtBurst := velocityAtWall(w, burstStart)
	vInBurst := velocityAtWall(w, (burstStart+burstEnd)/2)
	t.Logf("v(idle)=%.2f v(-frame)=%.2f v(burst start)=%.2f v(in burst)=%.2f",
		vIdle, vFrameBefore, vAtBurst, vInBurst)

	if vIdle < 0.9*opts.MaxVelocity {
		t.Errorf("velocity during the idle stretch = %v, want close to max %v", vIdle, opts.MaxVelocity)
	}
	// The whole point of the backward pass: by the time the burst reaches the
	// viewport we are already at reading speed, not still doing 120x.
	if vAtBurst > 1.5*opts.MinVelocity {
		t.Errorf("velocity at the burst = %v, want at or near min %v", vAtBurst, opts.MinVelocity)
	}
	if vInBurst > 1.5*opts.MinVelocity {
		t.Errorf("velocity inside the burst = %v, want at or near min %v", vInBurst, opts.MinVelocity)
	}
	// And deceleration has to have started well before that, not at the edge.
	if vFrameBefore > 0.7*opts.MaxVelocity {
		t.Errorf("velocity one frame before the burst = %v, want already decelerating below %v",
			vFrameBefore, 0.7*opts.MaxVelocity)
	}
	if vFrameBefore <= vAtBurst {
		t.Errorf("velocity should still be falling into the burst: %v -> %v", vFrameBefore, vAtBurst)
	}

	// The idle stretch must cost far less viewer time than the burst does.
	tauIdle := w.TauAt(burstStart) - w.TauAt(0)
	tauBurst := w.TauAt(burstEnd) - w.TauAt(burstStart)
	if tauIdle >= tauBurst {
		t.Errorf("10 idle minutes took %vms of viewer time but the 1 minute burst took %vms",
			tauIdle, tauBurst)
	}
}

func TestAccelerationLimitIsRespected(t *testing.T) {
	opts := testOptions()
	const durationMs = 600_000
	intervals := append(spikeIntervals(0, 300_000, 3), spikeIntervals(300_000, 330_000, 600)...)
	w := planWarp(computeDensity(intervals, nil, durationMs, opts), durationMs, opts)

	// |d(v^2/2)/dt| <= MaxAccel, checked between consecutive knots. A small
	// tolerance covers the clamp at the velocity bounds.
	for i := 1; i < len(w.Knots); i++ {
		vPrev := w.Knots[i-1].Velocity
		vCur := w.Knots[i].Velocity
		dt := w.Knots[i].WallMs - w.Knots[i-1].WallMs
		if dt <= 0 {
			t.Fatalf("non-positive wall step at knot %d", i)
		}
		du := math.Abs(vCur*vCur/2 - vPrev*vPrev/2)
		if du > opts.MaxAccel*dt*1.0001+1e-9 {
			t.Fatalf("knot %d: |du| = %v exceeds MaxAccel*dt = %v", i, du, opts.MaxAccel*dt)
		}
	}
}

func TestWarpDegenerateRuns(t *testing.T) {
	opts := testOptions()

	// A zero-length run must still produce a usable, non-panicking warp.
	w := planWarp(Density{BucketMs: opts.DensityBucketMs}, 0, opts)
	if len(w.Knots) == 0 {
		t.Fatal("want at least one knot")
	}
	if got := w.WallAt(1234); got != 0 {
		t.Errorf("WallAt on an empty warp = %v, want 0", got)
	}
	if got := w.TauAt(1234); got != 0 {
		t.Errorf("TauAt on an empty warp = %v, want 0", got)
	}
	if w.VelocityAt(0) <= 0 {
		t.Error("velocity should stay positive even on an empty warp")
	}

	// Out-of-range queries clamp rather than extrapolate.
	d := computeDensity(spikeIntervals(0, 60_000, 60), nil, 60_000, opts)
	w = planWarp(d, 60_000, opts)
	if got := w.WallAt(-10); got != w.Knots[0].WallMs {
		t.Errorf("WallAt(-10) = %v, want the first knot", got)
	}
	if got := w.WallAt(w.TotalTauMs * 10); got != w.Knots[len(w.Knots)-1].WallMs {
		t.Errorf("WallAt past the end = %v, want the last knot", got)
	}
	if got := w.TauAt(-10); got != w.Knots[0].TauMs {
		t.Errorf("TauAt(-10) = %v, want the first knot", got)
	}
	if got := w.TauAt(1e12); got != w.TotalTauMs {
		t.Errorf("TauAt past the end = %v, want TotalTauMs %v", got, w.TotalTauMs)
	}
}

func TestDensityWeightsAndSmoothing(t *testing.T) {
	opts := testOptions()
	// A sub-bucket frame means no smoothing, so the raw weights are observable.
	opts.FrameMs = 1

	intervals := []Interval{
		{StartMs: 0, EndMs: 2000, Confidence: ConfidenceMeasured},
		// An inferred end is bookkeeping, not an observed event.
		{StartMs: 0, EndMs: 2000, Confidence: ConfidenceInferred},
	}
	edges := []Edge{{Kind: EdgeMessage, SendMs: 0}}
	d := computeDensity(intervals, edges, 5000, opts)

	want := 2*weightIntervalStart + weightMessageEdge
	if math.Abs(d.Samples[0]-want) > 1e-9 {
		t.Errorf("bucket 0 = %v, want %v", d.Samples[0], want)
	}
	if math.Abs(d.Samples[2]-weightIntervalEnd) > 1e-9 {
		t.Errorf("bucket 2 = %v, want only the measured end weight %v", d.Samples[2], weightIntervalEnd)
	}
	if d.Peak != d.Samples[0] {
		t.Errorf("Peak = %v, want %v", d.Peak, d.Samples[0])
	}
}

func TestCompressionRatioMatchesWarp(t *testing.T) {
	d, err := BuildSyntheticDigest(3, 40, 20*60*1000, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildSyntheticDigest: %v", err)
	}
	want := d.DurationMs / d.Warp.TotalTauMs
	if math.Abs(d.Stats.CompressionRatio-want) > 1e-9 {
		t.Errorf("CompressionRatio = %v, want %v", d.Stats.CompressionRatio, want)
	}
	if d.Stats.CompressionRatio <= 1 {
		t.Errorf("CompressionRatio = %v, want a real speed-up on a run with idle gaps",
			d.Stats.CompressionRatio)
	}
}
