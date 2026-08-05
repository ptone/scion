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
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/logparser"
)

// fallbackColors mirrors the log parser's palette and is used for lifelines
// that the parser never saw (which can happen when an agent only ever appears
// as a message endpoint).
var fallbackColors = []string{
	"#4e79a7", "#f28e2b", "#e15759", "#76b7b2",
	"#59a14f", "#edc948", "#b07aa1", "#ff9da7",
	"#9c755f", "#bab0ac",
}

// kindDepth maps an interval kind to its nesting depth within a lifeline.
var kindDepth = map[IntervalKind]int{
	KindLifecycle: 0,
	KindSession:   1,
	KindTurn:      2,
	KindTool:      3,
}

// Log payload messages that delimit intervals.
const (
	msgPreStart  = "agent.lifecycle.pre_start"
	msgPostStart = "agent.lifecycle.post_start"
	msgPreStop   = "agent.lifecycle.pre_stop"
	msgSessStart = "agent.session.start"
	msgSessEnd   = "agent.session.end"
	msgTurnStart = "agent.turn.start"
	msgTurnEnd   = "agent.turn.end"
	msgToolCall  = "agent.tool.call"
	msgToolRes   = "agent.tool.result"
)

// maxLabelLen bounds message labels so the digest stays small; the frontend
// only ever shows a preview.
const maxLabelLen = 140

// BuildFromFile reads a Cloud Logging JSON export, parses it with the shared
// log parser, and produces a digest.
//
// Both the raw entries and the parser's result are needed: the parser resolves
// agent names, colors and the requestedBy parent attribution, but it flattens
// everything into state transitions and drops the Cloud Logging insertId that
// the digest uses for deep links.
func BuildFromFile(logPath, fsLogPath string, maxDepth int, opts Options) (*Digest, error) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil, fmt.Errorf("reading log file: %w", err)
	}
	var entries []logparser.GCPLogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing log JSON: %w", err)
	}
	parsed, err := logparser.ParseLogFile(logPath, fsLogPath, maxDepth)
	if err != nil {
		return nil, err
	}
	return Build(entries, parsed, opts)
}

// Build produces a digest from raw log entries plus the parser's result.
//
// parsed may be nil, in which case lifeline names, colors and parent
// attribution are derived from the raw entries alone.
func Build(entries []logparser.GCPLogEntry, parsed *logparser.ParseResult, opts Options) (*Digest, error) {
	opts = normalizeOptions(opts)

	b := &builder{opts: opts}
	if err := b.loadEntries(entries); err != nil {
		return nil, err
	}
	b.initLifelines(parsed)
	b.resolveBounds()
	b.resolveAncestry(parsed)
	b.assignOrder()
	b.assignSlots()
	b.buildIntervals()
	b.buildEdges(parsed)

	d := &Digest{
		Version:    SchemaVersion,
		StartedAt:  b.start.Format(time.RFC3339Nano),
		EndedAt:    b.end.Format(time.RFC3339Nano),
		DurationMs: b.durationMs,
		Lifelines:  make([]Lifeline, 0, len(b.lifelines)),
		Intervals:  b.intervals,
		Edges:      b.edges,
	}
	if parsed != nil {
		d.ProjectID = parsed.Manifest.ProjectID
		d.ProjectName = parsed.Manifest.ProjectName
	}
	for _, l := range b.lifelines {
		d.Lifelines = append(d.Lifelines, *l)
	}
	sort.Slice(d.Lifelines, func(i, j int) bool { return d.Lifelines[i].Order < d.Lifelines[j].Order })

	d.Density = computeDensity(d.Intervals, d.Edges, d.DurationMs, opts)
	d.Warp = planWarp(d.Density, d.DurationMs, opts)
	d.Stats = computeStats(d, b.slotCount)

	if b.droppedEdges > 0 {
		log.Printf("digest: dropped %d message edge(s) with unresolvable endpoints", b.droppedEdges)
	}
	return d, nil
}

// normalizeOptions fills in zero-valued fields with defaults and repairs
// nonsensical combinations so Build never divides by zero.
func normalizeOptions(o Options) Options {
	def := DefaultOptions()
	if o.FrameMs <= 0 {
		o.FrameMs = def.FrameMs
	}
	if o.TargetEventsPerViewerSecond <= 0 {
		o.TargetEventsPerViewerSecond = def.TargetEventsPerViewerSecond
	}
	if o.MinVelocity <= 0 {
		o.MinVelocity = def.MinVelocity
	}
	if o.MaxVelocity <= 0 {
		o.MaxVelocity = def.MaxVelocity
	}
	if o.MaxVelocity < o.MinVelocity {
		o.MaxVelocity = o.MinVelocity
	}
	if o.MaxAccel <= 0 {
		o.MaxAccel = def.MaxAccel
	}
	if o.DensityBucketMs <= 0 {
		o.DensityBucketMs = def.DensityBucketMs
	}
	if o.InferRecvWindowMs < 0 {
		o.InferRecvWindowMs = def.InferRecvWindowMs
	}
	return o
}

// logEvent is a log entry with its timestamp resolved to a run-relative offset.
type logEvent struct {
	entry  *logparser.GCPLogEntry
	ms     float64
	stream string
	msg    string
}

type builder struct {
	opts Options

	events     []logEvent
	start      time.Time
	end        time.Time
	durationMs float64

	lifelines []*Lifeline
	byID      map[string]*Lifeline
	idByName  map[string]string
	firstSeen map[string]float64

	intervals []Interval
	edges     []Edge

	slotCount    int
	droppedEdges int
}

// loadEntries filters out unparseable timestamps, sorts chronologically and
// establishes the run's time origin.
func (b *builder) loadEntries(entries []logparser.GCPLogEntry) error {
	type stamped struct {
		idx int
		t   time.Time
	}
	var stamps []stamped
	for i := range entries {
		t, err := logparser.TimestampToTime(entries[i].Timestamp)
		if err != nil {
			continue
		}
		stamps = append(stamps, stamped{idx: i, t: t})
	}
	if len(stamps) == 0 {
		return fmt.Errorf("digest: no log entries with parseable timestamps")
	}
	sort.SliceStable(stamps, func(i, j int) bool { return stamps[i].t.Before(stamps[j].t) })

	b.start = stamps[0].t
	b.end = stamps[len(stamps)-1].t
	b.durationMs = float64(b.end.Sub(b.start)) / float64(time.Millisecond)
	if b.durationMs < 0 {
		b.durationMs = 0
	}

	b.events = make([]logEvent, 0, len(stamps))
	for _, s := range stamps {
		e := &entries[s.idx]
		b.events = append(b.events, logEvent{
			entry:  e,
			ms:     float64(s.t.Sub(b.start)) / float64(time.Millisecond),
			stream: logStream(e.LogName),
			msg:    payloadStr(e.JSONPayload, "message"),
		})
	}
	return nil
}

// initLifelines creates one lifeline per agent, preferring the parser's names
// and colors and backfilling anything the parser missed.
func (b *builder) initLifelines(parsed *logparser.ParseResult) {
	b.byID = make(map[string]*Lifeline)
	b.idByName = make(map[string]string)
	b.firstSeen = make(map[string]float64)

	add := func(id, name, harness, color string) *Lifeline {
		if id == "" {
			return nil
		}
		if l, ok := b.byID[id]; ok {
			return l
		}
		if name == "" {
			name = shortID(id)
		}
		if color == "" {
			color = fallbackColors[len(b.lifelines)%len(fallbackColors)]
		}
		l := &Lifeline{
			ID:       id,
			Name:     name,
			Harness:  harness,
			Color:    color,
			Ancestry: []string{},
		}
		b.byID[id] = l
		b.lifelines = append(b.lifelines, l)
		if _, ok := b.idByName[name]; !ok {
			b.idByName[name] = id
		}
		return l
	}

	if parsed != nil {
		for _, a := range parsed.Manifest.Agents {
			add(a.ID, a.Name, a.Harness, a.Color)
		}
	}

	// Backfill any agent that appears in the raw entries but not the manifest.
	for _, ev := range b.events {
		if ev.stream != "scion-agents" {
			continue
		}
		if aid := ev.entry.Labels["agent_id"]; aid != "" {
			add(aid, "", ev.entry.Labels["scion.harness"], "")
		}
	}
}

// resolveBounds computes birth/death for every lifeline from lifecycle events,
// defaulting to first appearance and the end of the run.
func (b *builder) resolveBounds() {
	type bounds struct {
		birth, death float64
		hasBirth     bool
		died         bool
		logID        string
	}
	bs := make(map[string]*bounds, len(b.byID))
	get := func(id string) *bounds {
		if v, ok := bs[id]; ok {
			return v
		}
		v := &bounds{}
		bs[id] = v
		return v
	}

	touch := func(id string, ms float64) {
		if id == "" {
			return
		}
		if _, ok := b.byID[id]; !ok {
			return
		}
		if prev, ok := b.firstSeen[id]; !ok || ms < prev {
			b.firstSeen[id] = ms
		}
	}

	for _, ev := range b.events {
		switch ev.stream {
		case "scion-agents":
			aid := ev.entry.Labels["agent_id"]
			if _, ok := b.byID[aid]; !ok {
				continue
			}
			touch(aid, ev.ms)
			v := get(aid)
			switch ev.msg {
			case msgPreStart:
				if !v.hasBirth {
					v.hasBirth = true
					v.birth = ev.ms
					v.logID = ev.entry.InsertID
				}
			case msgPreStop:
				v.died = true
				v.death = ev.ms
			}
		case "scion-messages":
			from, to, _ := b.messageEndpoints(ev)
			touch(from, ev.ms)
			touch(to, ev.ms)
		}
	}

	for _, l := range b.lifelines {
		v := get(l.ID)
		switch {
		case v.hasBirth:
			l.BirthMs = v.birth
			l.LogID = v.logID
		default:
			if fs, ok := b.firstSeen[l.ID]; ok {
				l.BirthMs = fs
			}
		}
		if v.died {
			l.Died = true
			l.DeathMs = v.death
		} else {
			l.DeathMs = b.durationMs
		}
		if l.DeathMs < l.BirthMs {
			l.DeathMs = l.BirthMs
		}
	}
}

// resolveAncestry sets ParentID from the parser's requestedBy attribution and
// walks the resulting forest to fill in Ancestry and Depth. Cycles and missing
// parents are tolerated.
func (b *builder) resolveAncestry(parsed *logparser.ParseResult) {
	if parsed != nil {
		for _, ev := range parsed.Events {
			if ev.Type != "agent_create" {
				continue
			}
			lc, ok := ev.Data.(logparser.AgentLifecycleEvent)
			if !ok || lc.RequestedBy == "" {
				continue
			}
			child, ok := b.byID[lc.AgentID]
			if !ok || child.ParentID != "" {
				continue
			}
			if pid := b.resolveAgentRef(lc.RequestedBy, ""); pid != "" && pid != child.ID {
				child.ParentID = pid
			}
		}
	}

	for _, l := range b.lifelines {
		seen := map[string]bool{l.ID: true}
		var chain []string
		cur := l.ParentID
		for cur != "" && !seen[cur] {
			seen[cur] = true
			p, ok := b.byID[cur]
			if !ok {
				break
			}
			chain = append(chain, cur)
			cur = p.ParentID
		}
		// chain is [parent, grandparent, ..]; Ancestry is [root, .., parent].
		anc := make([]string, 0, len(chain))
		for i := len(chain) - 1; i >= 0; i-- {
			anc = append(anc, chain[i])
		}
		l.Ancestry = anc
		l.Depth = len(anc)
	}
}

// assignOrder numbers lifelines by a depth-first traversal of the ancestry
// forest so that children sit next to their parents.
func (b *builder) assignOrder() {
	children := make(map[string][]*Lifeline)
	var roots []*Lifeline
	for _, l := range b.lifelines {
		parent, hasParent := b.byID[l.ParentID]
		// Treat a lifeline whose ancestry walk did not reach it (cycle) as a root.
		if l.ParentID == "" || !hasParent || parent == l {
			roots = append(roots, l)
			continue
		}
		children[l.ParentID] = append(children[l.ParentID], l)
	}
	byBirth := func(s []*Lifeline) {
		sort.SliceStable(s, func(i, j int) bool {
			if s[i].BirthMs != s[j].BirthMs {
				return s[i].BirthMs < s[j].BirthMs
			}
			if s[i].Name != s[j].Name {
				return s[i].Name < s[j].Name
			}
			return s[i].ID < s[j].ID
		})
	}
	byBirth(roots)
	for _, c := range children {
		byBirth(c)
	}

	order := 0
	visited := make(map[string]bool, len(b.lifelines))
	var walk func(l *Lifeline)
	walk = func(l *Lifeline) {
		if visited[l.ID] {
			return
		}
		visited[l.ID] = true
		l.Order = order
		order++
		for _, c := range children[l.ID] {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	// Anything still unvisited is part of a parent cycle; emit it in birth order
	// rather than looping forever.
	var leftovers []*Lifeline
	for _, l := range b.lifelines {
		if !visited[l.ID] {
			leftovers = append(leftovers, l)
		}
	}
	byBirth(leftovers)
	for _, l := range leftovers {
		walk(l)
	}
}

// assignSlots performs greedy interval-graph coloring over lifetimes so that
// non-overlapping lifelines share a rendered column.
func (b *builder) assignSlots() {
	idx := make([]*Lifeline, len(b.lifelines))
	copy(idx, b.lifelines)
	sort.SliceStable(idx, func(i, j int) bool {
		if idx[i].BirthMs != idx[j].BirthMs {
			return idx[i].BirthMs < idx[j].BirthMs
		}
		return idx[i].Order < idx[j].Order
	})

	var slotFree []float64 // death time of the current occupant of each slot
	for _, l := range idx {
		assigned := -1
		for s, free := range slotFree {
			if free <= l.BirthMs {
				assigned = s
				break
			}
		}
		if assigned < 0 {
			slotFree = append(slotFree, 0)
			assigned = len(slotFree) - 1
		}
		slotFree[assigned] = l.DeathMs
		l.Slot = assigned
	}
	b.slotCount = len(slotFree)
}

// openSpan is an interval whose start has been seen but whose end has not.
type openSpan struct {
	kind     IntervalKind
	startMs  float64
	label    string
	logID    string
	err      bool
	evIdx    int     // index within the lifeline's event slice
	childEnd float64 // latest end of any interval closed while this was open
}

// buildIntervals pairs start/end events per lifeline, labelling each interval
// with how much of its duration was actually observed.
func (b *builder) buildIntervals() {
	perLifeline := make(map[string][]logEvent)
	for _, ev := range b.events {
		if ev.stream != "scion-agents" {
			continue
		}
		aid := ev.entry.Labels["agent_id"]
		if _, ok := b.byID[aid]; !ok {
			continue
		}
		perLifeline[aid] = append(perLifeline[aid], ev)
	}

	for _, l := range b.lifelines {
		b.intervals = append(b.intervals, b.lifelineIntervals(l, perLifeline[l.ID])...)
	}

	sort.SliceStable(b.intervals, func(i, j int) bool {
		a, c := b.intervals[i], b.intervals[j]
		if a.StartMs != c.StartMs {
			return a.StartMs < c.StartMs
		}
		if a.Depth != c.Depth {
			return a.Depth < c.Depth
		}
		return a.LifelineID < c.LifelineID
	})
	for i := range b.intervals {
		b.intervals[i].ID = fmt.Sprintf("iv%d", i)
	}
}

func (b *builder) lifelineIntervals(l *Lifeline, evs []logEvent) []Interval {
	var out []Interval
	stacks := make(map[IntervalKind][]*openSpan)
	sawLifecycle := false

	// noteClose records an interval end against the open spans that enclose it,
	// so an enclosing span can never be inferred to finish before its children
	// do. Only shallower spans are enclosing ones.
	noteClose := func(kind IntervalKind, end float64) {
		depth := kindDepth[kind]
		for _, st := range stacks {
			for _, sp := range st {
				if kindDepth[sp.kind] >= depth {
					continue
				}
				if end > sp.childEnd {
					sp.childEnd = end
				}
			}
		}
	}

	emit := func(kind IntervalKind, start, end float64, label, logID string, conf Confidence, isErr bool) {
		if end < start {
			end = start
		}
		out = append(out, Interval{
			LifelineID: l.ID,
			Kind:       kind,
			Depth:      kindDepth[kind],
			StartMs:    start,
			EndMs:      end,
			Label:      label,
			Confidence: conf,
			Error:      isErr,
			LogID:      logID,
		})
		if kind == KindLifecycle {
			sawLifecycle = true
		}
	}

	for i, ev := range evs {
		kind, isStart, ok := classifyEvent(ev.msg)
		if !ok {
			continue
		}
		if isStart {
			stacks[kind] = append(stacks[kind], &openSpan{
				kind:    kind,
				startMs: ev.ms,
				label:   intervalLabel(kind, l, ev),
				logID:   ev.entry.InsertID,
				err:     entryIsError(ev.entry),
				evIdx:   i,
			})
			continue
		}

		st := stacks[kind]
		if n := len(st); n > 0 {
			sp := st[n-1]
			stacks[kind] = st[:n-1]
			end := ev.ms
			if sp.childEnd > end {
				end = sp.childEnd
			}
			emit(kind, sp.startMs, end, sp.label, sp.logID, ConfidenceMeasured, sp.err || entryIsError(ev.entry))
			noteClose(kind, end)
			continue
		}

		// An end with no matching start: infer the start from the previous event
		// on this lifeline rather than collapsing it to zero duration.
		start := l.BirthMs
		if i > 0 && evs[i-1].ms > start {
			start = evs[i-1].ms
		}
		if start > ev.ms {
			start = ev.ms
		}
		emit(kind, start, ev.ms, intervalLabel(kind, l, ev), ev.entry.InsertID, ConfidenceInferred, entryIsError(ev.entry))
		noteClose(kind, ev.ms)
	}

	// Flush spans that never closed, innermost first so that an inferred end
	// always covers everything nested inside it.
	var leftover []*openSpan
	for _, st := range stacks {
		leftover = append(leftover, st...)
	}
	sort.SliceStable(leftover, func(i, j int) bool {
		di, dj := kindDepth[leftover[i].kind], kindDepth[leftover[j].kind]
		if di != dj {
			return di > dj
		}
		return leftover[i].startMs > leftover[j].startMs
	})

	// endByDepth tracks how far the deeper (already flushed) spans reach, so an
	// enclosing span is never inferred to end before something nested in it.
	// Siblings at the same depth deliberately do not influence each other.
	endByDepth := map[int]float64{}
	for _, sp := range leftover {
		if sp.kind == KindLifecycle {
			// An agent with no pre_stop is genuinely still alive at the end of the
			// captured window; that is exactly what ConfidenceOpen means.
			emit(KindLifecycle, sp.startMs, l.DeathMs, sp.label, sp.logID, ConfidenceOpen, sp.err)
			continue
		}
		depth := kindDepth[sp.kind]
		bound := sp.childEnd
		for dd, e := range endByDepth {
			if dd > depth && e > bound {
				bound = e
			}
		}
		next, hasNext := 0.0, false
		if sp.evIdx+1 < len(evs) {
			next, hasNext = evs[sp.evIdx+1].ms, true
		}
		conf := ConfidenceOpen
		end := l.DeathMs
		if hasNext || bound > sp.startMs {
			conf = ConfidenceInferred
			end = bound
			if hasNext && next > end {
				end = next
			}
		}
		if end > l.DeathMs {
			end = l.DeathMs
		}
		if end < sp.startMs {
			end = sp.startMs
		}
		if end > endByDepth[depth] {
			endByDepth[depth] = end
		}
		emit(sp.kind, sp.startMs, end, sp.label, sp.logID, conf, sp.err)
	}

	// Every lifeline gets a lifecycle bar, even when no lifecycle event was
	// captured at all. Its bounds are known only as "somewhere in the window".
	if !sawLifecycle {
		conf := ConfidenceOpen
		if l.Died {
			conf = ConfidenceInferred
		}
		emit(KindLifecycle, l.BirthMs, l.DeathMs, l.Name, l.LogID, conf, false)
	}
	return out
}

// classifyEvent maps a payload message to the interval it opens or closes.
func classifyEvent(msg string) (kind IntervalKind, isStart bool, ok bool) {
	switch msg {
	case msgPreStart:
		return KindLifecycle, true, true
	case msgPreStop:
		return KindLifecycle, false, true
	case msgSessStart:
		return KindSession, true, true
	case msgSessEnd:
		return KindSession, false, true
	case msgTurnStart:
		return KindTurn, true, true
	case msgTurnEnd:
		return KindTurn, false, true
	case msgToolCall:
		return KindTool, true, true
	case msgToolRes:
		return KindTool, false, true
	}
	return "", false, false
}

func intervalLabel(kind IntervalKind, l *Lifeline, ev logEvent) string {
	switch kind {
	case KindLifecycle:
		return l.Name
	case KindTool:
		if n := payloadStr(ev.entry.JSONPayload, "tool_name"); n != "" {
			return n
		}
		return "tool"
	}
	return ""
}

// buildEdges turns the message stream into sloped edges and adds spawn/destroy
// edges derived from lifecycle attribution.
func (b *builder) buildEdges(parsed *logparser.ParseResult) {
	startsByLifeline := make(map[string][]float64, len(b.byID))
	for _, iv := range b.intervals {
		startsByLifeline[iv.LifelineID] = append(startsByLifeline[iv.LifelineID], iv.StartMs)
	}
	for id := range startsByLifeline {
		sort.Float64s(startsByLifeline[id])
	}

	inferRecv := func(toID string, sendMs float64) (float64, Confidence) {
		starts := startsByLifeline[toID]
		i := sort.SearchFloat64s(starts, sendMs)
		if i < len(starts) && starts[i]-sendMs <= b.opts.InferRecvWindowMs {
			return starts[i], ConfidenceInferred
		}
		return sendMs, ConfidenceOpen
	}

	for _, ev := range b.events {
		if ev.stream != "scion-messages" {
			continue
		}
		if strings.Contains(ev.msg, "rejected") {
			continue
		}
		fromID, toID, meta := b.messageEndpoints(ev)
		if !meta.valid {
			continue
		}
		if fromID == "" || toID == "" {
			b.droppedEdges++
			continue
		}
		recv, conf := inferRecv(toID, ev.ms)
		b.edges = append(b.edges, Edge{
			Kind:           EdgeMessage,
			FromID:         fromID,
			ToID:           toID,
			SendMs:         ev.ms,
			RecvMs:         recv,
			RecvConfidence: conf,
			MsgType:        meta.msgType,
			Label:          truncateLabel(meta.content),
			Broadcast:      meta.broadcast,
			LogID:          ev.entry.InsertID,
		})
	}

	// Spawn edges: parent -> child at the moment the child was born.
	for _, l := range b.lifelines {
		if l.ParentID == "" {
			continue
		}
		if _, ok := b.byID[l.ParentID]; !ok {
			continue
		}
		b.edges = append(b.edges, Edge{
			Kind:           EdgeSpawn,
			FromID:         l.ParentID,
			ToID:           l.ID,
			SendMs:         l.BirthMs,
			RecvMs:         l.BirthMs,
			RecvConfidence: ConfidenceMeasured,
			Label:          "spawn " + l.Name,
			LogID:          l.LogID,
		})
	}

	// Destroy edges: only where the parser attributed a requester.
	if parsed != nil {
		for _, ev := range parsed.Events {
			if ev.Type != "agent_destroy" {
				continue
			}
			lc, ok := ev.Data.(logparser.AgentLifecycleEvent)
			if !ok || lc.RequestedBy == "" {
				continue
			}
			target, ok := b.byID[lc.AgentID]
			if !ok {
				continue
			}
			fromID := b.resolveAgentRef(lc.RequestedBy, "")
			if fromID == "" || fromID == target.ID {
				continue
			}
			t, err := logparser.TimestampToTime(ev.Timestamp)
			if err != nil {
				continue
			}
			ms := float64(t.Sub(b.start)) / float64(time.Millisecond)
			b.edges = append(b.edges, Edge{
				Kind:           EdgeDestroy,
				FromID:         fromID,
				ToID:           target.ID,
				SendMs:         ms,
				RecvMs:         ms,
				RecvConfidence: ConfidenceMeasured,
				Label:          "destroy " + target.Name,
			})
		}
	}

	sort.SliceStable(b.edges, func(i, j int) bool {
		if b.edges[i].SendMs != b.edges[j].SendMs {
			return b.edges[i].SendMs < b.edges[j].SendMs
		}
		if b.edges[i].FromID != b.edges[j].FromID {
			return b.edges[i].FromID < b.edges[j].FromID
		}
		return b.edges[i].ToID < b.edges[j].ToID
	})
	for i := range b.edges {
		b.edges[i].ID = fmt.Sprintf("e%d", i)
	}
}

// messageMeta carries the non-endpoint fields of a message log entry.
type messageMeta struct {
	valid     bool
	msgType   string
	content   string
	broadcast bool
}

// messageEndpoints resolves a message entry's sender and recipient to lifeline
// IDs. An empty ID means the endpoint is not a known lifeline (a user, say).
func (b *builder) messageEndpoints(ev logEvent) (fromID, toID string, meta messageMeta) {
	jp := ev.entry.JSONPayload
	sender := firstNonEmpty(payloadStr(jp, "sender"), ev.entry.Labels["sender"])
	recipient := firstNonEmpty(payloadStr(jp, "recipient"), ev.entry.Labels["recipient"])
	if sender == "" || recipient == "" {
		return "", "", messageMeta{}
	}
	senderID := firstNonEmpty(payloadStr(jp, "sender_id"), ev.entry.Labels["sender_id"])
	recipientID := firstNonEmpty(payloadStr(jp, "recipient_id"), ev.entry.Labels["recipient_id"])

	meta = messageMeta{
		valid:     true,
		msgType:   firstNonEmpty(payloadStr(jp, "msg_type"), ev.entry.Labels["msg_type"]),
		content:   payloadStr(jp, "message_content"),
		broadcast: payloadBool(jp, "broadcasted"),
	}
	return b.resolveAgentRef(sender, senderID), b.resolveAgentRef(recipient, recipientID), meta
}

// resolveAgentRef maps an agent reference (an ID, a name, or an "agent:name"
// token) onto a lifeline ID, preferring the ID.
func (b *builder) resolveAgentRef(ref, id string) string {
	if id != "" {
		if _, ok := b.byID[id]; ok {
			return id
		}
	}
	name := strings.TrimPrefix(ref, "agent:")
	if _, ok := b.byID[name]; ok {
		return name
	}
	if lid, ok := b.idByName[name]; ok {
		return lid
	}
	if id != "" {
		if lid, ok := b.idByName[id]; ok {
			return lid
		}
	}
	return ""
}

func computeStats(d *Digest, slotCount int) Stats {
	s := Stats{
		LifelineCount: len(d.Lifelines),
		IntervalCount: len(d.Intervals),
		EdgeCount:     len(d.Edges),
		MaxConcurrent: slotCount,
	}
	for _, iv := range d.Intervals {
		switch iv.Confidence {
		case ConfidenceMeasured:
			s.MeasuredIntervals++
		case ConfidenceInferred:
			s.InferredIntervals++
		default:
			s.OpenIntervals++
		}
	}
	for _, e := range d.Edges {
		if e.RecvConfidence == ConfidenceInferred {
			s.InferredEdges++
		}
	}
	s.CompressionRatio = 1
	if d.Warp.TotalTauMs > 0 && d.DurationMs > 0 {
		s.CompressionRatio = d.DurationMs / d.Warp.TotalTauMs
	}
	return s
}

func logStream(logName string) string {
	parts := strings.Split(logName, "/")
	return parts[len(parts)-1]
}

func payloadStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func payloadBool(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		return false
	}
	return b
}

// entryIsError reports whether a log entry describes a failure.
func entryIsError(e *logparser.GCPLogEntry) bool {
	switch strings.ToUpper(e.Severity) {
	case "ERROR", "CRITICAL", "ALERT", "EMERGENCY":
		return true
	}
	jp := e.JSONPayload
	for _, key := range []string{"error", "is_error", "failed"} {
		switch v := jp[key].(type) {
		case bool:
			if v {
				return true
			}
		case string:
			if v != "" && strings.ToLower(v) != "false" {
				return true
			}
		}
	}
	if v, ok := jp["success"].(bool); ok && !v {
		return true
	}
	switch strings.ToLower(payloadStr(jp, "status")) {
	case "error", "failed", "failure":
		return true
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncateLabel(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= maxLabelLen {
		return s
	}
	return s[:maxLabelLen] + "…"
}
