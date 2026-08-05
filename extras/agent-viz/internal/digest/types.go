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

// Package digest builds a precomputed "run digest" from Google Cloud Logging
// exports of Scion agent activity.
//
// The digest is the sole input to the sequence visualizer frontend. Everything
// the renderer draws comes from here, which keeps the renderer honest (it never
// infers anything) and unit-testable (it is a pure function of the digest).
//
// # Time model
//
// All times in the digest are float64 milliseconds relative to the start of the
// run (RunStartMs == 0). Absolute wall-clock is recoverable by adding
// Digest.StartedAt. Two distinct time domains matter:
//
//   - wall time (t): real elapsed time within the run. All geometry is in this
//     domain, so an interval's rendered length is always its true duration.
//   - viewer time (tau): time as experienced by someone watching playback. The
//     Warp maps monotonically between the two.
//
// The elasticity of the visualization lives entirely in that mapping, never in
// the geometry. Idle stretches are traversed quickly (high velocity) rather than
// being drawn shorter, so a bar's length never lies about duration.
package digest

// SchemaVersion is incremented on breaking changes to the wire format. The
// frontend refuses to render a digest whose version it does not recognize.
const SchemaVersion = 1

// Confidence describes how much the duration of an interval can be trusted.
//
// This distinction exists because Scion's hook-per-process telemetry mode
// frequently emits an end event with no matching start event, which would
// otherwise silently degrade to a zero-duration span. Rather than fabricate a
// plausible duration, the digest labels what it actually knows and the renderer
// draws each case differently.
type Confidence string

const (
	// ConfidenceMeasured means both a start and a matching end event were
	// observed. The duration is real.
	ConfidenceMeasured Confidence = "measured"

	// ConfidenceInferred means only one endpoint was observed and the other was
	// derived from the next/previous event on the same lifeline. The duration is
	// an upper bound, not a measurement.
	ConfidenceInferred Confidence = "inferred"

	// ConfidenceOpen means no end was observed and none could be inferred; the
	// interval runs to the death of its lifeline or the end of the run. Rendered
	// with an open (unterminated) edge.
	ConfidenceOpen Confidence = "open"
)

// IntervalKind identifies the nesting layer an interval belongs to. Lower
// Depth values enclose higher ones on the same lifeline, which is what produces
// the flame-graph-within-a-lifeline effect.
type IntervalKind string

const (
	KindLifecycle IntervalKind = "lifecycle" // depth 0: pre_start .. pre_stop
	KindSession   IntervalKind = "session"   // depth 1: agent.session.start .. end
	KindTurn      IntervalKind = "turn"      // depth 2: agent.turn.start .. end
	KindTool      IntervalKind = "tool"      // depth 3: agent.tool.call .. result
)

// EdgeKind classifies a message edge between two lifelines.
type EdgeKind string

const (
	EdgeMessage EdgeKind = "message" // agent-to-agent or user-to-agent message
	EdgeSpawn   EdgeKind = "spawn"   // parent created child
	EdgeDestroy EdgeKind = "destroy" // requester destroyed target
)

// Digest is the complete precomputed model of one run.
type Digest struct {
	Version int `json:"version"`

	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`

	// StartedAt and EndedAt are RFC3339Nano absolute bounds of the run. All
	// millisecond offsets elsewhere are relative to StartedAt.
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt"`

	// DurationMs is the total wall-clock span of the run.
	DurationMs float64 `json:"durationMs"`

	Lifelines []Lifeline `json:"lifelines"`
	Intervals []Interval `json:"intervals"`
	Edges     []Edge     `json:"edges"`

	// Density is a uniformly-sampled measure of how much is happening, used to
	// drive the default velocity profile.
	Density Density `json:"density"`

	// Warp maps viewer time to wall time. See the Warp type.
	Warp Warp `json:"warp"`

	// Stats carries counts useful for diagnostics and for honest reporting in
	// the UI about how much of the data was inferred rather than measured.
	Stats Stats `json:"stats"`
}

// Lifeline is one persistent actor column. Unlike a trace viewer, where an
// actor appearing N times produces N unrelated bars, a lifeline exists once for
// the whole run and every interval and edge references it by ID.
type Lifeline struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Harness string `json:"harness,omitempty"`
	Color   string `json:"color"`

	// ParentID is the lifeline that spawned this one, empty for roots. Derived
	// from the log parser's requestedBy attribution.
	ParentID string `json:"parentId,omitempty"`

	// Ancestry is the ordered chain of ancestor lifeline IDs, [root, .., parent].
	Ancestry []string `json:"ancestry"`

	// Depth is len(Ancestry): 0 for roots.
	Depth int `json:"depth"`

	// Order is the position of this lifeline in a depth-first traversal of the
	// ancestry forest. Rendering in this order keeps parents adjacent to their
	// children, which keeps message edges short.
	Order int `json:"order"`

	// Slot is a recycled column index: two lifelines whose lifetimes do not
	// overlap may share a slot. Computed by greedy interval-graph coloring so a
	// run with 100 agents but only 8-12 concurrent needs only ~12 columns.
	Slot int `json:"slot"`

	// BirthMs and DeathMs bound the lifeline's existence. DeathMs equals the run
	// duration when the agent never terminated within the captured window.
	BirthMs float64 `json:"birthMs"`
	DeathMs float64 `json:"deathMs"`

	// Died reports whether an actual termination was observed, as opposed to the
	// lifeline simply running to the end of the log.
	Died bool `json:"died"`

	// LogID is the Cloud Logging insertId of the event that created this
	// lifeline, enabling a deep link back to the source record.
	LogID string `json:"logId,omitempty"`
}

// Interval is a span of activity on one lifeline. Because the vertical axis is
// metric, an interval is simultaneously the sequence-diagram "activation box"
// and the trace "span" - which is the central idea of this visualization.
type Interval struct {
	ID         string       `json:"id"`
	LifelineID string       `json:"lifelineId"`
	Kind       IntervalKind `json:"kind"`

	// Depth is the nesting level within the lifeline; children are drawn inset
	// from their parent, producing a per-actor flame graph.
	Depth int `json:"depth"`

	StartMs float64 `json:"startMs"`
	EndMs   float64 `json:"endMs"`

	Label      string     `json:"label,omitempty"`
	Confidence Confidence `json:"confidence"`

	// Error marks intervals whose source event reported a failure.
	Error bool `json:"error,omitempty"`

	// LogID is the Cloud Logging insertId of the originating (start) record.
	LogID string `json:"logId,omitempty"`
}

// DurationMs returns the interval's wall-clock length.
func (i Interval) DurationMs() float64 { return i.EndMs - i.StartMs }

// Edge is a message between two lifelines.
//
// On a metric time axis a message is not horizontal: it leaves the sender at
// SendMs and arrives at RecvMs, so the drawn line is sloped and the slope is
// the delivery latency. Steep edges reveal queueing or a busy recipient without
// the viewer reading a single number.
type Edge struct {
	ID   string   `json:"id"`
	Kind EdgeKind `json:"kind"`

	FromID string `json:"fromId"`
	ToID   string `json:"toId"`

	SendMs float64 `json:"sendMs"`
	RecvMs float64 `json:"recvMs"`

	// RecvConfidence describes where RecvMs came from.
	//
	// The Cloud Logging message stream records a single timestamp per message,
	// so a true receive time is not available from this source. When possible we
	// infer arrival as the recipient's next observed activity after SendMs and
	// mark it inferred - which is arguably the more useful quantity anyway,
	// since it shows when the recipient actually acted. When no such activity
	// exists, RecvMs == SendMs and this is ConfidenceOpen, drawn horizontal.
	//
	// A future digest sourced from the messages table (which carries both
	// created and dispatched_at) can populate this as ConfidenceMeasured.
	RecvConfidence Confidence `json:"recvConfidence"`

	MsgType   string `json:"msgType,omitempty"`
	Label     string `json:"label,omitempty"`
	Broadcast bool   `json:"broadcast,omitempty"`

	LogID string `json:"logId,omitempty"`
}

// Density is a uniform sampling of activity intensity across the run. It is the
// default driver of the velocity profile: the express lane accelerates where
// this is low and decelerates where it is high.
type Density struct {
	// BucketMs is the width of each sample in wall-clock milliseconds.
	BucketMs float64 `json:"bucketMs"`

	// Samples[i] is the weighted event count in wall time
	// [i*BucketMs, (i+1)*BucketMs), smoothed by a frame-sized kernel so that
	// deceleration begins before a burst enters the viewport rather than after.
	Samples []float64 `json:"samples"`

	// Peak is max(Samples), provided so the frontend can normalize without a
	// full pass.
	Peak float64 `json:"peak"`
}

// Warp is the precomputed monotonic mapping between viewer time and wall time.
//
// It is stored as a piecewise-linear set of knots. Because both coordinates are
// strictly increasing, the mapping inverts cheaply, which is what lets the
// scrubber, the minimap, the timestamp readout and shareable deep links all
// stay consistent with one another: they are different projections of one
// function.
//
// Determinism matters here. The profile is computed once, offline, so the same
// run always plays back identically and a link to a moment always lands on that
// moment.
type Warp struct {
	// Knots are ordered by both TauMs and WallMs (both strictly increasing).
	Knots []WarpKnot `json:"knots"`

	// TotalTauMs is the total viewer-time duration of playback at 1x. This is
	// what the transport control reports as the length of the run, and it is
	// shorter than DurationMs whenever idle stretches were compressed.
	TotalTauMs float64 `json:"totalTauMs"`

	// MinVelocity and MaxVelocity bound the profile, in wall-ms per viewer-ms.
	// A velocity of 1 is real time; 60 means one minute of run per second of
	// watching.
	MinVelocity float64 `json:"minVelocity"`
	MaxVelocity float64 `json:"maxVelocity"`
}

// WarpKnot is a single sample of the viewer-time to wall-time mapping.
type WarpKnot struct {
	// TauMs is viewer time (the clock that advances steadily while watching).
	TauMs float64 `json:"tauMs"`

	// WallMs is the corresponding wall time within the run.
	WallMs float64 `json:"wallMs"`

	// Velocity is dWall/dTau at this knot, in wall-ms per viewer-ms. The
	// frontend uses it directly to drive the visual treatment of the staging
	// zone: as velocity rises, discrete geometry cross-fades into motion
	// streaks, signalling "this is not meant to be read right now".
	Velocity float64 `json:"velocity"`
}

// Stats summarizes what the digest is made of, including how much of it is
// inferred rather than measured.
type Stats struct {
	LifelineCount int `json:"lifelineCount"`
	IntervalCount int `json:"intervalCount"`
	EdgeCount     int `json:"edgeCount"`

	// MaxConcurrent is the largest number of simultaneously alive lifelines,
	// which equals the number of column slots required.
	MaxConcurrent int `json:"maxConcurrent"`

	MeasuredIntervals int `json:"measuredIntervals"`
	InferredIntervals int `json:"inferredIntervals"`
	OpenIntervals     int `json:"openIntervals"`

	// InferredEdges counts edges whose arrival time was inferred from recipient
	// activity rather than measured.
	InferredEdges int `json:"inferredEdges"`

	// MeasuredEdges counts edges whose arrival time came from the log itself --
	// a dispatch row paired with the recipient's acknowledgement. Only these
	// arrows have an honest slope; the rest are a plausible guess.
	MeasuredEdges int `json:"measuredEdges"`

	// CompressionRatio is DurationMs / Warp.TotalTauMs: how many times faster
	// than real time the run plays at 1x under the density-driven profile.
	CompressionRatio float64 `json:"compressionRatio"`
}

// Options controls digest construction.
type Options struct {
	// FrameMs is the wall-clock duration visible in the viewport at velocity 1.
	// It sets the smoothing kernel width for density, so that deceleration is
	// planned a full frame ahead of a burst.
	FrameMs float64

	// TargetEventsPerViewerSecond is the invariant the velocity profile aims to
	// hold: rather than constant time-per-second, constant events-per-second of
	// viewer attention.
	TargetEventsPerViewerSecond float64

	// MinVelocity and MaxVelocity clamp the profile (wall-ms per viewer-ms).
	MinVelocity float64
	MaxVelocity float64

	// MaxAccel limits how sharply velocity may change, expressed as the maximum
	// rate of change of v^2/2 with respect to wall time. This is what forces the
	// express lane to ease out of a gap and be at reading speed by the time a
	// burst reaches the viewport, instead of blowing through it.
	MaxAccel float64

	// DensityBucketMs is the sampling resolution of the density function.
	DensityBucketMs float64

	// InferRecvWindowMs caps how far ahead to look for recipient activity when
	// inferring a message arrival time. Beyond this the edge is left horizontal.
	InferRecvWindowMs float64

	// PairDeliveryWindowMs caps how far after a dispatch we will accept a
	// delivery acknowledgement as belonging to the same message. Pairing is
	// keyed on endpoints plus content, which is unique enough in practice, but
	// a repeated message ("ping") could otherwise be matched across a huge gap
	// and invent an absurd latency. Beyond this window the dispatch falls back
	// to an inferred arrival.
	PairDeliveryWindowMs float64
}

// DefaultOptions returns tuned defaults suitable for typical Scion runs.
func DefaultOptions() Options {
	return Options{
		FrameMs:                     60_000,
		TargetEventsPerViewerSecond: 6,
		MinVelocity:                 1,
		MaxVelocity:                 120,
		MaxAccel:                    0.02,
		DensityBucketMs:             1_000,
		InferRecvWindowMs:           120_000,
		PairDeliveryWindowMs:        300_000,
	}
}
