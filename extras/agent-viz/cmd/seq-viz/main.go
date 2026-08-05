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

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/digest"
	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/seqserver"
)

func main() {
	logFile := flag.String("log-file", "", "Path to GCP log JSON export; if empty, a synthetic run is generated")
	digestFile := flag.String("digest-file", "", "Path to a precomputed digest JSON (skips log parsing and synthesis)")
	dumpDigest := flag.String("dump-digest", "", "Write the digest to this path and exit without serving")
	dumpLog := flag.String("dump-log", "", "Write the synthetic GCP log export to this path and exit without serving")
	fsLog := flag.String("fs-log", "", "Path to fs-watcher NDJSON log")
	maxDepth := flag.Int("max-depth", 3, "Maximum directory depth for file graph nodes (0 = unlimited)")
	synthAgents := flag.Int("synth-agents", 60, "Number of agents in the synthetic run")
	synthDuration := flag.Duration("synth-duration", 45*time.Minute, "Wall-clock length of the synthetic run")
	synthSeed := flag.Int64("synth-seed", 1, "Seed for the synthetic run (same seed, same run)")
	synthConcurrency := flag.Int("synth-concurrency", 0,
		"Agents alive at once in the synthetic run (0 = default 8-12); raise it to pressure the column axis")
	port := flag.Int("port", 8090, "Port to serve on")
	devMode := flag.Bool("dev", false, "Serve web assets from disk (development mode)")

	opts := digest.DefaultOptions()
	frame := flag.Duration("frame", time.Duration(opts.FrameMs)*time.Millisecond,
		"Wall-clock duration visible in the viewport at velocity 1")
	targetRate := flag.Float64("target-rate", opts.TargetEventsPerViewerSecond,
		"Target events per second of viewer attention")
	maxVelocity := flag.Float64("max-velocity", opts.MaxVelocity,
		"Maximum playback velocity, in wall-ms per viewer-ms")
	maxAccel := flag.Float64("max-accel", opts.MaxAccel,
		"Maximum rate of change of v^2/2 with respect to wall time")
	maxBody := flag.Int("max-body", opts.MaxBodyLen,
		"Characters of message text to carry in the digest (-1 to omit bodies entirely)")
	flag.Parse()

	opts.FrameMs = float64(frame.Milliseconds())
	opts.TargetEventsPerViewerSecond = *targetRate
	opts.MaxVelocity = *maxVelocity
	opts.MaxAccel = *maxAccel
	opts.MaxBodyLen = *maxBody

	// --dump-log writes the raw synthetic export. Kept separate from the digest
	// path so demo data can exercise the real log parser rather than bypassing
	// it, which is the only way the parse and attribution code gets covered.
	if *dumpLog != "" {
		entries := digest.SynthesizeLogWith(
			*synthSeed, *synthAgents, float64(synthDuration.Milliseconds()), *synthConcurrency)
		if err := writeJSON(*dumpLog, entries); err != nil {
			log.Fatalf("Error writing log: %v", err)
		}
		log.Printf("Wrote %d log entries to %s", len(entries), *dumpLog)
		return
	}

	var (
		d   *digest.Digest
		err error
	)
	switch {
	case *digestFile != "":
		log.Printf("Loading precomputed digest from %s", *digestFile)
		d, err = loadDigest(*digestFile)
	case *logFile != "":
		log.Printf("Building digest from %s", *logFile)
		d, err = digest.BuildFromFile(*logFile, *fsLog, *maxDepth, opts)
	default:
		log.Printf("No --log-file given; synthesizing a %s run with up to %d agents (seed %d)",
			*synthDuration, *synthAgents, *synthSeed)
		d, err = digest.BuildSyntheticDigestWith(
			*synthSeed, *synthAgents, float64(synthDuration.Milliseconds()), *synthConcurrency, opts)
	}
	if err != nil {
		log.Fatalf("Error building digest: %v", err)
	}

	summarize(d)

	if *dumpDigest != "" {
		if err := writeJSON(*dumpDigest, d); err != nil {
			log.Fatalf("Error writing digest: %v", err)
		}
		log.Printf("Wrote digest to %s", *dumpDigest)
		return
	}

	srv := seqserver.New(d)
	if err := srv.Start(*port, *devMode); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// loadDigest reads a digest written by --dump-digest.
func loadDigest(path string) (*digest.Digest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d digest.Digest
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	// A digest written by an older build may not match what the frontend of
	// this build expects. Refusing loudly beats rendering a subtly wrong view.
	if d.Version != digest.SchemaVersion {
		return nil, fmt.Errorf(
			"%s has schema version %d, but this build expects %d; regenerate it with demo/regenerate.sh",
			path, d.Version, digest.SchemaVersion)
	}
	return &d, nil
}

// writeJSON writes v as indented JSON, creating parent directories as needed.
// Indented because these files are committed as demo data and an unreadable
// diff on a 400KB blob helps nobody.
func writeJSON(path string, v any) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func summarize(d *digest.Digest) {
	s := d.Stats
	log.Printf("Run %s .. %s (%.1f min of wall time)",
		d.StartedAt, d.EndedAt, d.DurationMs/60_000)
	log.Printf("  lifelines: %d (max %d concurrent, so %d columns)",
		s.LifelineCount, s.MaxConcurrent, s.MaxConcurrent)
	log.Printf("  intervals: %d (%d measured / %d inferred / %d open)",
		s.IntervalCount, s.MeasuredIntervals, s.InferredIntervals, s.OpenIntervals)
	log.Printf("  edges:     %d (%d measured arrival / %d inferred / %d open)",
		s.EdgeCount, s.MeasuredEdges, s.InferredEdges,
		s.EdgeCount-s.MeasuredEdges-s.InferredEdges)
	log.Printf("  playback:  %.1f min of viewing at 1x (%.1fx compression, velocity %.1f..%.1f, %d warp knots)",
		d.Warp.TotalTauMs/60_000, s.CompressionRatio,
		d.Warp.MinVelocity, d.Warp.MaxVelocity, len(d.Warp.Knots))
}
