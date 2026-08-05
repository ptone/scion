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
	"sort"
)

// Event weights used to build the density function. Starts matter more than
// ends (a start is where attention goes), and a message is the single most
// attention-worthy thing that can happen, since it is the only event that
// couples two lifelines.
const (
	weightIntervalStart = 1.0
	weightIntervalEnd   = 0.4
	weightMessageEdge   = 1.5
	weightLifecycleEdge = 1.0
)

// densityEpsilon guards the division in the desired-velocity computation.
const densityEpsilon = 1e-9

// computeDensity buckets weighted events over wall time and smooths the result
// with a frame-wide kernel.
//
// The smoothing is not cosmetic: it is what gives the velocity planner
// lookahead. Density at time t reflects everything visible in a viewport
// centered on t, so the planner starts slowing down while a burst is still
// off-screen instead of discovering it on arrival.
func computeDensity(intervals []Interval, edges []Edge, durationMs float64, opts Options) Density {
	opts = normalizeOptions(opts)
	bucket := opts.DensityBucketMs

	n := 1
	if durationMs > 0 {
		n = int(math.Ceil(durationMs / bucket))
		if n < 1 {
			n = 1
		}
	}
	raw := make([]float64, n)
	add := func(t, w float64) {
		i := int(t / bucket)
		if i < 0 {
			i = 0
		}
		if i >= n {
			i = n - 1
		}
		raw[i] += w
	}

	for _, iv := range intervals {
		add(iv.StartMs, weightIntervalStart)
		// Only a measured end corresponds to an observed event; inferred and open
		// ends are bookkeeping and must not inflate the density.
		if iv.Confidence == ConfidenceMeasured {
			add(iv.EndMs, weightIntervalEnd)
		}
	}
	for _, e := range edges {
		w := weightLifecycleEdge
		if e.Kind == EdgeMessage {
			w = weightMessageEdge
		}
		add(e.SendMs, w)
	}

	samples := smooth(raw, opts.FrameMs, bucket)
	peak := 0.0
	for _, s := range samples {
		if s > peak {
			peak = s
		}
	}
	return Density{BucketMs: bucket, Samples: samples, Peak: peak}
}

// smooth applies a normalized triangular kernel of the given width. Weights are
// renormalized at the boundaries so a uniform input stays uniform.
func smooth(raw []float64, frameMs, bucketMs float64) []float64 {
	n := len(raw)
	out := make([]float64, n)
	radius := int(math.Round(frameMs / 2 / bucketMs))
	if radius < 1 || n < 2 {
		copy(out, raw)
		return out
	}
	if radius > n {
		radius = n
	}
	// Triangular weights, peak at the center, zero just past the radius.
	kernel := make([]float64, radius+1)
	for k := 0; k <= radius; k++ {
		kernel[k] = 1 - float64(k)/float64(radius+1)
	}
	for i := 0; i < n; i++ {
		var acc, wsum float64
		lo, hi := i-radius, i+radius
		if lo < 0 {
			lo = 0
		}
		if hi > n-1 {
			hi = n - 1
		}
		for j := lo; j <= hi; j++ {
			k := j - i
			if k < 0 {
				k = -k
			}
			w := kernel[k]
			acc += raw[j] * w
			wsum += w
		}
		if wsum > 0 {
			out[i] = acc / wsum
		}
	}
	return out
}

// planWarp turns a density profile into the viewer-time to wall-time mapping.
//
// The invariant is constant events per second of viewer attention. If density
// is R_wall events per wall-second and we want R events per viewer-second, the
// required velocity is R / R_wall wall-seconds per viewer-second.
//
// That desired profile is then acceleration-limited. The constraint is on how
// fast velocity changes with respect to viewer time, |dv/dtau| <= A. Since
// dtau = dt / v, that is v*|dv/dt| <= A, and substituting u = v^2/2 collapses
// it to the linear condition |du/dt| <= A - which a forward and a backward
// min-pass solve exactly. The backward pass is the important one: it is what
// guarantees the profile has already slowed to reading speed by the time a
// dense region arrives.
func planWarp(d Density, durationMs float64, opts Options) Warp {
	opts = normalizeOptions(opts)

	if durationMs <= 0 || len(d.Samples) == 0 {
		return Warp{
			Knots:       []WarpKnot{{TauMs: 0, WallMs: 0, Velocity: opts.MaxVelocity}},
			TotalTauMs:  0,
			MinVelocity: opts.MaxVelocity,
			MaxVelocity: opts.MaxVelocity,
		}
	}

	bucket := d.BucketMs
	if bucket <= 0 {
		bucket = opts.DensityBucketMs
	}
	n := len(d.Samples)

	// Desired velocity, in wall-ms per viewer-ms.
	v := make([]float64, n)
	for i, s := range d.Samples {
		ratePerWallSec := s / (bucket / 1000)
		if ratePerWallSec <= densityEpsilon {
			v[i] = opts.MaxVelocity
			continue
		}
		v[i] = clamp(opts.TargetEventsPerViewerSecond/ratePerWallSec, opts.MinVelocity, opts.MaxVelocity)
	}

	// Acceleration limiting in u = v^2/2 space.
	u := make([]float64, n)
	for i := range v {
		u[i] = v[i] * v[i] / 2
	}
	step := opts.MaxAccel * bucket
	for i := 1; i < n; i++ {
		if lim := u[i-1] + step; u[i] > lim {
			u[i] = lim
		}
	}
	for i := n - 2; i >= 0; i-- {
		if lim := u[i+1] + step; u[i] > lim {
			u[i] = lim
		}
	}
	for i := range u {
		v[i] = clamp(math.Sqrt(2*u[i]), opts.MinVelocity, opts.MaxVelocity)
	}

	// Integrate dtau = dt / v across buckets. Knot i opens the segment traversed
	// at velocity segV[i]; both coordinates are strictly increasing because every
	// dt is positive and every velocity is finite and positive.
	raw := make([]WarpKnot, 0, n+1)
	segV := make([]float64, 0, n)
	tau, wall := 0.0, 0.0
	for i := 0; i < n; i++ {
		dt := bucket
		if wall+dt > durationMs {
			dt = durationMs - wall
		}
		if dt <= 0 {
			break
		}
		raw = append(raw, WarpKnot{TauMs: tau, WallMs: wall, Velocity: v[i]})
		segV = append(segV, v[i])
		tau += dt / v[i]
		wall += dt
	}
	if len(raw) == 0 {
		return Warp{
			Knots:       []WarpKnot{{TauMs: 0, WallMs: 0, Velocity: v[0]}},
			TotalTauMs:  0,
			MinVelocity: v[0],
			MaxVelocity: v[0],
		}
	}
	raw = append(raw, WarpKnot{TauMs: tau, WallMs: wall, Velocity: segV[len(segV)-1]})

	// Merge runs of identical velocity: such a run is exactly linear, so the
	// interior knots carry no information and only bloat the digest.
	knots := make([]WarpKnot, 0, len(raw))
	knots = append(knots, raw[0])
	minV, maxV := segV[0], segV[0]
	for i := 1; i < len(raw)-1; i++ {
		minV = math.Min(minV, segV[i])
		maxV = math.Max(maxV, segV[i])
		if segV[i] == segV[i-1] {
			continue
		}
		knots = append(knots, raw[i])
	}
	knots = append(knots, raw[len(raw)-1])

	return Warp{
		Knots:       knots,
		TotalTauMs:  tau,
		MinVelocity: minV,
		MaxVelocity: maxV,
	}
}

// WallAt maps viewer time to wall time by piecewise-linear interpolation,
// clamping outside the knot range.
func (w Warp) WallAt(tauMs float64) float64 {
	if len(w.Knots) == 0 {
		return 0
	}
	if len(w.Knots) == 1 || tauMs <= w.Knots[0].TauMs {
		return w.Knots[0].WallMs
	}
	last := w.Knots[len(w.Knots)-1]
	if tauMs >= last.TauMs {
		return last.WallMs
	}
	i := w.segmentByTau(tauMs)
	a, b := w.Knots[i], w.Knots[i+1]
	span := b.TauMs - a.TauMs
	if span <= 0 {
		return a.WallMs
	}
	return a.WallMs + (tauMs-a.TauMs)*(b.WallMs-a.WallMs)/span
}

// TauAt is the exact inverse of WallAt: both interpolate linearly over the same
// knots, so a round trip returns the input up to floating-point error.
func (w Warp) TauAt(wallMs float64) float64 {
	if len(w.Knots) == 0 {
		return 0
	}
	if len(w.Knots) == 1 || wallMs <= w.Knots[0].WallMs {
		return w.Knots[0].TauMs
	}
	last := w.Knots[len(w.Knots)-1]
	if wallMs >= last.WallMs {
		return last.TauMs
	}
	i := w.segmentByWall(wallMs)
	a, b := w.Knots[i], w.Knots[i+1]
	span := b.WallMs - a.WallMs
	if span <= 0 {
		return a.TauMs
	}
	return a.TauMs + (wallMs-a.WallMs)*(b.TauMs-a.TauMs)/span
}

// VelocityAt returns dWall/dTau at a viewer time, in wall-ms per viewer-ms.
// This is the slope of the segment containing tauMs, so it is consistent with
// WallAt by construction.
func (w Warp) VelocityAt(tauMs float64) float64 {
	if len(w.Knots) == 0 {
		return 0
	}
	if len(w.Knots) == 1 {
		return w.Knots[0].Velocity
	}
	last := w.Knots[len(w.Knots)-1]
	if tauMs >= last.TauMs {
		return last.Velocity
	}
	i := w.segmentByTau(tauMs)
	a, b := w.Knots[i], w.Knots[i+1]
	span := b.TauMs - a.TauMs
	if span <= 0 {
		return a.Velocity
	}
	return (b.WallMs - a.WallMs) / span
}

// segmentByTau returns the index i such that Knots[i].TauMs <= tauMs < Knots[i+1].TauMs.
func (w Warp) segmentByTau(tauMs float64) int {
	i := sort.Search(len(w.Knots), func(k int) bool { return w.Knots[k].TauMs > tauMs }) - 1
	return clampIndex(i, len(w.Knots)-2)
}

// segmentByWall returns the index i such that Knots[i].WallMs <= wallMs < Knots[i+1].WallMs.
func (w Warp) segmentByWall(wallMs float64) int {
	i := sort.Search(len(w.Knots), func(k int) bool { return w.Knots[k].WallMs > wallMs }) - 1
	return clampIndex(i, len(w.Knots)-2)
}

func clampIndex(i, max int) int {
	if i < 0 {
		return 0
	}
	if i > max {
		return max
	}
	return i
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
