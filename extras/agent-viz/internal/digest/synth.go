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
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/logparser"
)

// synthBase is the fixed wall-clock origin of synthetic runs, so that two runs
// with the same seed are byte-identical.
var synthBase = time.Date(2026, 3, 22, 16, 0, 0, 0, time.UTC)

// synthTimeFormat is RFC3339 with a fixed-width fractional part. The fixed
// width matters: the log parser sorts entries by their timestamp *string*, and
// RFC3339Nano's trailing-zero trimming would break that ordering.
const synthTimeFormat = "2006-01-02T15:04:05.000000Z07:00"

const (
	synthProject     = "seq-demo"
	synthLogAgents   = "projects/seq-demo/logs/scion-agents"
	synthLogMessages = "projects/seq-demo/logs/scion-messages"
	synthLogServer   = "projects/seq-demo/logs/scion-server"
	synthLogRequests = "projects/seq-demo/logs/scion_request_log"
)

// synthRoles names agents by the layer they sit in, so the generated ancestry
// is readable in the UI.
var synthRoles = [][]string{
	{"orchestrator"},
	{"planner", "architect", "integrator", "reviewer"},
	{"impl", "test", "docs", "bench", "triage"},
	{"probe", "fixer", "scout", "linter"},
}

var synthTools = []string{"Read", "Edit", "Write", "Grep", "Glob", "WebFetch", "Task"}

var synthMsgTypes = []string{"instruction", "status", "question", "answer", "review", "handoff"}

var synthContent = []string{
	"picking up the parser refactor now",
	"blocked on the schema change, need a decision",
	"tests are green on my branch",
	"handing the integration work back to you",
	"please review the velocity planner maths",
	"found a race in the playback engine",
	"spawning two helpers for the sweep",
	"done - shutting down",
}

// SynthesizeLog produces a deterministic, realistic-looking Cloud Logging
// export for a multi-agent run.
//
// The shape is chosen to exercise every branch of the digest builder: a deep
// ancestry, far more agents over the run than are ever concurrent (so slot
// recycling matters), bursts of activity separated by multi-minute idle gaps
// (so the velocity planner has something to compress), cross-subtree messages,
// and a scattering of unpaired tool events so the inferred and open confidence
// paths are populated.
func SynthesizeLog(seed int64, agentCount int, durationMs float64) []logparser.GCPLogEntry {
	return SynthesizeLogWith(seed, agentCount, durationMs, 0)
}

// SynthesizeLogWith is SynthesizeLog with control over how many agents are
// alive simultaneously.
//
// Concurrency is what the column axis has to absorb, and it is independent of
// agentCount: a run can churn through 200 agents while never exceeding 10 at
// once. Passing 0 keeps the default 8-12, which is the realistic case; raising
// it is how the demo exercises collapse, fold and solo, which only earn their
// keep once there are more columns than fit on screen.
func SynthesizeLogWith(seed int64, agentCount int, durationMs float64, concurrency int) []logparser.GCPLogEntry {
	if agentCount < 1 {
		agentCount = 1
	}
	if durationMs <= 0 {
		durationMs = 45 * 60 * 1000
	}
	s := &synth{
		rng:         rand.New(rand.NewSource(seed)),
		duration:    durationMs,
		byName:      map[string]bool{},
		concurrency: concurrency,
	}
	s.run(agentCount)
	sort.SliceStable(s.entries, func(i, j int) bool {
		return s.entries[i].Timestamp < s.entries[j].Timestamp
	})
	return s.entries
}

// BuildSyntheticDigest generates a synthetic run and builds a digest from it.
//
// The entries are round-tripped through a temporary file because the parser's
// agent naming and requestedBy attribution are only reachable via
// logparser.ParseLogFile.
func BuildSyntheticDigest(seed int64, agentCount int, durationMs float64, opts Options) (*Digest, error) {
	return BuildSyntheticDigestWith(seed, agentCount, durationMs, 0, opts)
}

// BuildSyntheticDigestWith is BuildSyntheticDigest with a concurrency cap.
// See SynthesizeLogWith.
func BuildSyntheticDigestWith(
	seed int64, agentCount int, durationMs float64, concurrency int, opts Options,
) (*Digest, error) {
	entries := SynthesizeLogWith(seed, agentCount, durationMs, concurrency)
	data, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("encoding synthetic log: %w", err)
	}
	dir, err := os.MkdirTemp("", "seq-viz-synth")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "synth.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("writing synthetic log: %w", err)
	}
	parsed, err := logparser.ParseLogFile(path, "", 3)
	if err != nil {
		return nil, err
	}
	return Build(entries, parsed, opts)
}

type synthAgent struct {
	id     string
	name   string
	depth  int
	parent *synthAgent
	birth  float64
	alive  bool
	life   int // remaining busy windows before retirement
}

type synthWindow struct{ start, end float64 }

func (w synthWindow) length() float64 { return w.end - w.start }

type synth struct {
	rng      *rand.Rand
	duration float64
	entries  []logparser.GCPLogEntry
	agents   []*synthAgent
	byName   map[string]bool
	seq      int
	pressure bool
	// concurrency caps how many agents are alive at once; 0 means the default
	// 8-12 band. See SynthesizeLogWith.
	concurrency int
}

// concurrencyTarget picks how many agents should be alive in a window. The
// default band is 8-12; an explicit cap gets the same +/-20% jitter so the
// column count still moves and fold/recycle are actually exercised.
func (s *synth) concurrencyTarget() int {
	if s.concurrency <= 0 {
		return 8 + s.rng.Intn(5)
	}
	if s.concurrency < 3 {
		return s.concurrency
	}
	spread := s.concurrency / 5
	if spread < 1 {
		spread = 1
	}
	return s.concurrency - spread + s.rng.Intn(2*spread+1)
}

func (s *synth) run(agentCount int) {
	windows := s.windows()
	s.emitServer(0, "Project created", map[string]any{"slug": synthProject})

	root := s.spawn(nil, windows[0].start)
	root.life = 1 << 30

	for wi, w := range windows {
		// Retire before hiring, and keep an eye on whether the requested agent
		// count is reachable at the current churn rate.
		s.reap(w)
		remaining := agentCount - len(s.agents)
		s.pressure = remaining > 5*(len(windows)-wi)

		target := s.concurrencyTarget()
		hires := target - s.aliveCount()
		if max := agentCount - len(s.agents); hires > max {
			hires = max
		}
		for k := 0; k < hires; k++ {
			// Births land after every death in this window (so concurrency really
			// is bounded by target) and are spread out (so the parser's
			// nearest-shell-call attribution picks the right parent).
			birth := w.start + w.length()*(0.4+0.55*(float64(k)+0.5)/float64(hires))
			s.spawn(s.pickParent(), birth)
		}
		s.activity(w)
		s.messages(w)
	}
}

// windows lays out alternating busy and idle stretches. The idle stretches are
// deliberately minutes long: they are what the express lane exists for.
func (s *synth) windows() []synthWindow {
	var ws []synthWindow
	t := 0.0
	for t < s.duration {
		busy := 60_000 + s.rng.Float64()*90_000
		if t+busy > s.duration {
			busy = s.duration - t
		}
		if busy <= 0 {
			break
		}
		ws = append(ws, synthWindow{start: t, end: t + busy})
		t += busy + 90_000 + s.rng.Float64()*210_000
	}
	if len(ws) == 0 {
		ws = append(ws, synthWindow{start: 0, end: s.duration})
	}
	// Always finish with a short burst at the very end of the run so the digest
	// spans the requested duration.
	if last := ws[len(ws)-1]; last.end < s.duration-1_000 {
		start := s.duration - 30_000
		if start < last.end {
			start = last.end
		}
		ws = append(ws, synthWindow{start: start, end: s.duration})
	}
	return ws
}

func (s *synth) aliveCount() int {
	n := 0
	for _, a := range s.agents {
		if a.alive {
			n++
		}
	}
	return n
}

func (s *synth) aliveAgents() []*synthAgent {
	var out []*synthAgent
	for _, a := range s.agents {
		if a.alive {
			out = append(out, a)
		}
	}
	return out
}

// pickParent prefers shallow, long-lived agents, which produces a plausible
// 3-4 layer ancestry rather than a single long chain.
func (s *synth) pickParent() *synthAgent {
	var candidates []*synthAgent
	for _, a := range s.agents {
		if a.alive && a.depth < len(synthRoles)-1 {
			// Weight by inverse depth.
			for i := 0; i <= len(synthRoles)-1-a.depth; i++ {
				candidates = append(candidates, a)
			}
		}
	}
	if len(candidates) == 0 {
		return s.agents[0]
	}
	return candidates[s.rng.Intn(len(candidates))]
}

func (s *synth) uniqueName(depth int) string {
	roles := synthRoles[depth%len(synthRoles)]
	for i := 0; ; i++ {
		role := roles[s.rng.Intn(len(roles))]
		name := role
		if depth > 0 || i > 0 {
			name = fmt.Sprintf("%s-%d", role, len(s.agents)+i)
		}
		if !s.byName[name] {
			s.byName[name] = true
			return name
		}
	}
}

func (s *synth) spawn(parent *synthAgent, at float64) *synthAgent {
	depth := 0
	if parent != nil {
		depth = parent.depth + 1
	}
	a := &synthAgent{
		id:     s.uuid(),
		name:   s.uniqueName(depth),
		depth:  depth,
		parent: parent,
		birth:  at,
		alive:  true,
		life:   s.lifeWindows(),
	}
	s.agents = append(s.agents, a)

	// The parser attributes parenthood by finding a shell tool call from another
	// agent shortly before the child's pre_start, so the spawn has to look like
	// one agent shelling out to the CLI.
	if parent != nil {
		s.emitTool(parent, at-2_500, "Bash", false)
		s.emitAgent(parent, at-1_200, msgToolRes, nil, false)
	}
	s.emitServer(at-200, "Agent created", map[string]any{
		"agent_id": a.id,
		"slug":     a.name,
		"name":     a.name,
	})
	s.emitAgent(a, at, msgPreStart, nil, false)
	s.emitAgent(a, at+180, msgPostStart, nil, false)
	s.emitAgent(a, at+420, msgSessStart, nil, false)
	return a
}

// lifeWindows decides how many busy windows an agent survives. Short lives
// churn the population, which is what makes slot recycling worth doing.
func (s *synth) lifeWindows() int {
	if s.pressure {
		return s.rng.Intn(2)
	}
	return 1 + s.rng.Intn(3)
}

func (s *synth) reap(w synthWindow) {
	for _, a := range s.agents {
		if !a.alive || a.parent == nil {
			continue
		}
		if a.life > 0 {
			a.life--
			continue
		}
		at := w.start + s.rng.Float64()*w.length()*0.35
		if at <= a.birth+2_000 {
			at = a.birth + 2_000
		}
		a.alive = false
		s.emitAgent(a, at, msgSessEnd, nil, false)
		s.emitAgent(a, at+150, msgPreStop, nil, false)

		// Half the terminations are explicit API deletes, which is what lets the
		// parser attribute a requester and the digest draw a destroy edge.
		if a.parent.alive && s.rng.Float64() < 0.5 {
			s.emitTool(a.parent, at-4_000, "Bash", false)
			s.emitAgent(a.parent, at-3_000, msgToolRes, nil, false)
			s.emitRequest(at-2_000, a.name)
		}
	}
}

// activity emits bursty turn/tool structure for every live agent inside a busy
// window.
func (s *synth) activity(w synthWindow) {
	for _, a := range s.aliveAgents() {
		lo := w.start
		if a.birth+1_000 > lo {
			lo = a.birth + 1_000
		}
		if lo >= w.end {
			continue
		}
		t := lo + s.rng.Float64()*(w.end-lo)*0.25
		turns := 1 + s.rng.Intn(4)
		for i := 0; i < turns && t < w.end; i++ {
			t = s.emitTurn(a, t, w.end)
			t += 2_000 + s.rng.Float64()*18_000
		}
	}
}

// emitTurn writes one turn and its tool calls, returning the time it ended.
func (s *synth) emitTurn(a *synthAgent, t, limit float64) float64 {
	s.emitAgent(a, t, msgTurnStart, nil, false)
	t += 400 + s.rng.Float64()*1_600

	tools := 1 + s.rng.Intn(4)
	for i := 0; i < tools && t < limit; i++ {
		tool := synthTools[s.rng.Intn(len(synthTools))]
		s.emitTool(a, t, tool, false)
		t += 300 + s.rng.Float64()*4_000
		switch {
		case s.rng.Float64() < 0.06:
			// Tool call with no result: the digest must infer or open this span.
		case s.rng.Float64() < 0.05:
			s.emitAgent(a, t, msgToolRes, map[string]any{"tool_name": tool, "success": false}, true)
		default:
			s.emitAgent(a, t, msgToolRes, map[string]any{"tool_name": tool}, false)
		}
		t += 200 + s.rng.Float64()*1_500
	}

	// A few turns never report an end, exercising the inference path.
	if s.rng.Float64() >= 0.08 {
		s.emitAgent(a, t, msgTurnEnd, nil, false)
	}
	// An occasional orphan result with no preceding call.
	if s.rng.Float64() < 0.04 {
		t += 500
		s.emitAgent(a, t, msgToolRes, map[string]any{"tool_name": "Bash"}, false)
	}
	return t
}

func (s *synth) messages(w synthWindow) {
	alive := s.aliveAgents()
	if len(alive) < 2 {
		return
	}
	count := 4 + s.rng.Intn(8)
	for i := 0; i < count; i++ {
		from := alive[s.rng.Intn(len(alive))]
		to := alive[s.rng.Intn(len(alive))]
		if from == to {
			continue
		}
		at := w.start + s.rng.Float64()*w.length()
		s.emitMessage(from, to, at, false)
	}
	// One rejected delivery per few windows; the digest must ignore these.
	if s.rng.Float64() < 0.3 {
		from := alive[s.rng.Intn(len(alive))]
		to := alive[s.rng.Intn(len(alive))]
		if from != to {
			s.emitMessage(from, to, w.start+s.rng.Float64()*w.length(), true)
		}
	}
}

func (s *synth) emitAgent(a *synthAgent, at float64, message string, extra map[string]any, isErr bool) {
	payload := map[string]any{"message": message}
	for k, v := range extra {
		payload[k] = v
	}
	sev := "INFO"
	if isErr {
		sev = "ERROR"
	}
	s.append(logparser.GCPLogEntry{
		InsertID:  s.nextID(),
		Timestamp: s.stamp(at),
		Severity:  sev,
		LogName:   synthLogAgents,
		Labels: map[string]string{
			"agent_id":      a.id,
			"scion.harness": "claude",
			"grove_id":      synthProject,
		},
		JSONPayload: payload,
	})
}

func (s *synth) emitTool(a *synthAgent, at float64, tool string, isErr bool) {
	payload := map[string]any{"message": msgToolCall, "tool_name": tool}
	switch tool {
	case "Read", "Edit", "Write", "Grep":
		payload["file_path"] = fmt.Sprintf("/workspace/src/%s/mod_%d.go", a.name, s.rng.Intn(6))
	}
	s.emitAgentPayload(a, at, payload, isErr)
}

func (s *synth) emitAgentPayload(a *synthAgent, at float64, payload map[string]any, isErr bool) {
	sev := "INFO"
	if isErr {
		sev = "ERROR"
	}
	s.append(logparser.GCPLogEntry{
		InsertID:  s.nextID(),
		Timestamp: s.stamp(at),
		Severity:  sev,
		LogName:   synthLogAgents,
		Labels: map[string]string{
			"agent_id":      a.id,
			"scion.harness": "claude",
			"grove_id":      synthProject,
		},
		JSONPayload: payload,
	})
}

// emitMessage writes the log rows for one message.
//
// A delivered message produces *two* rows, as a real Scion export does: the
// broker's "message dispatched" and the recipient's "message accepted
// (buffered)" some time later. That second row is what makes an arrival time
// measurable rather than guessed, and therefore what makes the arrow's slope
// real latency. Emitting only the dispatch would make the demo quietly
// understate what the digest can do on production logs -- and would leave the
// pairing path in the builder untested by the demo pipeline.
//
// A rejected message has no arrival: nobody took it.
func (s *synth) emitMessage(from, to *synthAgent, at float64, rejected bool) {
	msg := "message dispatched"
	if rejected {
		msg = "message rejected: recipient not accepting"
	}
	// Drawn before the early return so that the rejected path consumes the same
	// number of random values as the delivered one, keeping the stream (and so
	// the whole synthetic run) stable if the branch weighting ever changes.
	broadcast := s.rng.Float64() < 0.1
	msgType := synthMsgTypes[s.rng.Intn(len(synthMsgTypes))]
	content := synthContent[s.rng.Intn(len(synthContent))]
	latency := s.deliveryLatencyMs()

	row := func(at float64, message string) {
		s.append(logparser.GCPLogEntry{
			InsertID:  s.nextID(),
			Timestamp: s.stamp(at),
			Severity:  "INFO",
			LogName:   synthLogMessages,
			Labels: map[string]string{
				"sender":       "agent:" + from.name,
				"sender_id":    from.id,
				"recipient":    "agent:" + to.name,
				"recipient_id": to.id,
				"grove_id":     synthProject,
			},
			JSONPayload: map[string]any{
				"message":         message,
				"sender":          "agent:" + from.name,
				"sender_id":       from.id,
				"recipient":       "agent:" + to.name,
				"recipient_id":    to.id,
				"msg_type":        msgType,
				"message_content": content,
				"broadcasted":     broadcast,
			},
		})
	}

	// A small share of deliveries are never acknowledged -- the recipient dies,
	// or the export window clips the second row. Real exports show a couple of
	// percent; keeping some here means the demo also shows what an *inferred*
	// arrival looks like next to the measured ones.
	acked := s.rng.Float64() > 0.04

	row(at, msg)
	if rejected || !acked {
		return
	}
	row(at+latency, "message accepted (buffered)")
}

// deliveryLatencyMs samples the gap between dispatch and acceptance.
//
// Shaped to match what a real export looks like: a floor of a couple of
// hundred milliseconds, a long right tail from recipients that are mid-turn
// and do not check their inbox promptly, and a hard cap so no single arrow
// slopes off the bottom of the run.
func (s *synth) deliveryLatencyMs() float64 {
	const (
		floorMs = 200
		meanMs  = 900
		capMs   = 5000
	)
	d := floorMs + s.rng.ExpFloat64()*meanMs
	if d > capMs {
		d = capMs
	}
	return d
}

func (s *synth) emitServer(at float64, message string, payload map[string]any) {
	jp := map[string]any{"message": message}
	for k, v := range payload {
		jp[k] = v
	}
	s.append(logparser.GCPLogEntry{
		InsertID:    s.nextID(),
		Timestamp:   s.stamp(at),
		Severity:    "INFO",
		LogName:     synthLogServer,
		Labels:      map[string]string{"grove_id": synthProject},
		JSONPayload: jp,
	})
}

func (s *synth) emitRequest(at float64, agentName string) {
	s.append(logparser.GCPLogEntry{
		InsertID:  s.nextID(),
		Timestamp: s.stamp(at),
		Severity:  "INFO",
		LogName:   synthLogRequests,
		Labels:    map[string]string{"grove_id": synthProject},
		HTTPRequest: &logparser.HTTPRequestField{
			RequestMethod: "DELETE",
			RequestURL:    "https://scion.example/v1/agents/" + agentName,
		},
		JSONPayload: map[string]any{},
	})
}

func (s *synth) append(e logparser.GCPLogEntry) {
	s.entries = append(s.entries, e)
}

func (s *synth) stamp(ms float64) string {
	if ms < 0 {
		ms = 0
	}
	if ms > s.duration {
		ms = s.duration
	}
	return synthBase.Add(time.Duration(ms * float64(time.Millisecond))).Format(synthTimeFormat)
}

func (s *synth) nextID() string {
	s.seq++
	return fmt.Sprintf("synth-%07d", s.seq)
}

func (s *synth) uuid() string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(s.rng.Intn(256))
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
