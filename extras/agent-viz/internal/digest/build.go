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
	"math"
	"os"
	"sort"
	"strconv"
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
	if o.MaxBodyLen == 0 {
		o.MaxBodyLen = def.MaxBodyLen
	} else if o.MaxBodyLen < 0 {
		// Explicitly disabled. clipRunes treats any non-positive limit as "clip
		// everything", so normalise to exactly zero.
		o.MaxBodyLen = 0
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
	// idsByName holds every lifeline sharing a name, in creation order. Names
	// are not unique: a restarted agent reuses its name with a fresh ID.
	idsByName map[string][]string
	firstSeen map[string]float64

	intervals []Interval
	edges     []Edge

	slotCount    int
	droppedEdges int
	beforeOrigin int
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

	// The origin is normally the first entry, but a caller rebuilding a digest
	// as more log arrives pins it, so that an entry turning up from before the
	// previously-first one lengthens the run at the top instead of sliding every
	// other event backwards.
	b.start = stamps[0].t
	if !b.opts.Origin.IsZero() {
		b.start = b.opts.Origin
	}
	b.end = stamps[len(stamps)-1].t
	if b.end.Before(b.start) {
		b.end = b.start
	}
	b.durationMs = float64(b.end.Sub(b.start)) / float64(time.Millisecond)
	if b.durationMs < 0 {
		b.durationMs = 0
	}

	b.events = make([]logEvent, 0, len(stamps))
	for _, s := range stamps {
		e := &entries[s.idx]
		ms := float64(s.t.Sub(b.start)) / float64(time.Millisecond)
		// An entry older than a pinned origin is evidence we asked not to see,
		// but dropping it would lose a session start or a spawn that the rest of
		// the digest depends on. Clamp it to the origin instead.
		if ms < 0 {
			ms = 0
			b.beforeOrigin++
		}
		b.events = append(b.events, logEvent{
			entry:  e,
			ms:     ms,
			stream: logStream(e.LogName),
			msg:    payloadStr(e.JSONPayload, "message"),
		})
	}
	if b.beforeOrigin > 0 {
		log.Printf("digest: %d entries predate the pinned origin %s; clamped to t=0",
			b.beforeOrigin, b.start.Format(time.RFC3339))
	}
	return nil
}

// initLifelines creates one lifeline per agent, preferring the parser's names
// and colors and backfilling anything the parser missed.
func (b *builder) initLifelines(parsed *logparser.ParseResult) {
	b.byID = make(map[string]*Lifeline)
	b.idByName = make(map[string]string)
	b.idsByName = make(map[string][]string)
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
		b.idsByName[name] = append(b.idsByName[name], id)
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
			// Resolve as of the child's birth: whoever created it had to be
			// alive at that moment, which disambiguates a recycled name.
			if pid := b.resolveAgentRefAt(lc.RequestedBy, "", child.BirthMs); pid != "" && pid != child.ID {
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

// lifetime is one lifeline's occupancy of a column, half-open: [birth, death).
type lifetime struct{ birth, death float64 }

// overlaps reports whether two occupancies of the same column would collide.
// Half-open, so a lifeline may be born at the instant its predecessor died.
func (o lifetime) overlaps(p lifetime) bool {
	return o.birth < p.death && p.birth < o.death
}

// assignSlots performs greedy interval-graph coloring over lifetimes so that
// non-overlapping lifelines share a rendered column.
//
// Options.PinnedSlots makes the result stable across rebuilds of a session that
// is still being read. Left to itself the coloring is a pure function of the
// whole lifetime set, which means a newly discovered agent -- or an existing
// agent's death moving in from "still open" to a measured time -- can reseat an
// agent that has been sitting in the same column on screen for ten minutes.
// Pinned lifelines are placed first, at the column they already had; everything
// else is colored around them.
func (b *builder) assignSlots() {
	idx := make([]*Lifeline, len(b.lifelines))
	copy(idx, b.lifelines)
	sort.SliceStable(idx, func(i, j int) bool {
		if idx[i].BirthMs != idx[j].BirthMs {
			return idx[i].BirthMs < idx[j].BirthMs
		}
		return idx[i].Order < idx[j].Order
	})

	occupied := map[int][]lifetime{}
	maxSlot := -1
	take := func(l *Lifeline, slot int) {
		occupied[slot] = append(occupied[slot], lifetime{l.BirthMs, l.DeathMs})
		if slot > maxSlot {
			maxSlot = slot
		}
		l.Slot = slot
	}
	fits := func(l *Lifeline, slot int) bool {
		want := lifetime{l.BirthMs, l.DeathMs}
		for _, o := range occupied[slot] {
			if o.overlaps(want) {
				return false
			}
		}
		return true
	}

	pending := idx
	if len(b.opts.PinnedSlots) > 0 {
		pending = pending[:0:0]
		for _, l := range idx {
			slot, pinned := b.opts.PinnedSlots[l.ID]
			if !pinned || slot < 0 {
				pending = append(pending, l)
				continue
			}
			// A pin can only conflict if a lifetime grew across rebuilds, which
			// the watermark is meant to prevent. Honor the earlier-born claim and
			// recolor the other rather than draw two agents down one column.
			if !fits(l, slot) {
				log.Printf("digest: lifeline %s could not keep column %d; recoloring", l.ID, slot)
				pending = append(pending, l)
				continue
			}
			take(l, slot)
		}
	}

	for _, l := range pending {
		slot := 0
		for ; !fits(l, slot); slot++ {
		}
		take(l, slot)
	}
	b.slotCount = maxSlot + 1
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
	uniquify(len(b.intervals), func(i int) string { return intervalKey(b.intervals[i]) },
		func(i int, id string) { b.intervals[i].ID = id })
}

// msKey renders a millisecond offset for use inside an identifier. Sub-
// millisecond precision distinguishes nothing a reader can see, and rounding
// keeps the identifier readable.
func msKey(ms float64) string {
	return strconv.FormatInt(int64(math.Round(ms)), 10)
}

// intervalKey derives an interval's identity from the log entry that opened it.
//
// The obvious alternative -- numbering intervals by position, as this did until
// the identities had to survive a growing digest -- is only stable for a run
// that is already over. Insert one earlier event and every later interval takes
// the identity of its neighbour, which silently moves the reader's selection,
// the open message reader and any deep link onto a different event. Identity
// therefore comes from the log, which cannot be renumbered: `insertId` is
// already carried through the whole pipeline as `LogID`.
//
// The kind is part of the key because one entry can legitimately produce two
// intervals -- a lifeline whose only evidence is a session row gets both a
// session bar and the synthesised lifecycle bar that contains it.
func intervalKey(iv Interval) string {
	if iv.LogID != "" {
		return "iv." + string(iv.Kind) + "." + iv.LogID
	}
	// A lifeline with no lifecycle rows at all still gets one synthesised
	// lifecycle bar, which no entry produced. There is exactly one per lifeline,
	// so the lifeline names it -- and deliberately without its start time, which
	// is only "first seen" and moves whenever earlier evidence turns up.
	if iv.Kind == KindLifecycle {
		return "iv.lifecycle." + iv.LifelineID
	}
	// Anything else without an entry (an export with no insert ids) falls back
	// to position in time, which is stable only as far as the origin is.
	return "iv." + string(iv.Kind) + "." + iv.LifelineID + "@" + msKey(iv.StartMs)
}

// edgeKey derives an edge's identity the same way, per kind.
func edgeKey(e Edge) string {
	// A spawn edge is the child's birth, so the child names it. Its LogID is the
	// lifeline's own entry, which is empty for manifest-derived agents.
	if e.Kind == EdgeSpawn {
		return "e.spawn." + e.ToID
	}
	if e.LogID != "" {
		return "e." + string(e.Kind) + "." + e.LogID
	}
	// Destroy edges come from the parser's event stream, which does not carry an
	// insert id.
	return "e." + string(e.Kind) + "." + e.FromID + ">" + e.ToID + "@" + msKey(e.SendMs)
}

// uniquify assigns each item its key, disambiguating any repeat with a suffix.
//
// Keys are meant to be unique and the tests assert that they are on real and
// synthetic runs alike, but a duplicate must not silently collapse two events
// into one identity -- an id is what selection, deep links and delta updates
// are matched on. The suffix is applied in the caller's existing sort order, so
// it is deterministic for a given set of events.
func uniquify(n int, key func(int) string, set func(int, string)) {
	seen := make(map[string]int, n)
	for i := 0; i < n; i++ {
		k := key(i)
		seen[k]++
		if c := seen[k]; c > 1 {
			k = k + "~" + strconv.Itoa(c)
		}
		set(i, k)
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

	// closeNested ends every still-open span deeper than depth at end.
	//
	// A span cannot outlive the thing that contains it: a turn ending is itself
	// evidence that any tool call inside it finished, whatever the log did or
	// did not say. Without this a dropped tool result leaves the call floating
	// until some unrelated later event happens to close it, and the bar draws
	// hundreds of seconds outside its parent -- which in a per-lifeline flame
	// graph reads as nonsense. The end is inferred, not measured: all we know is
	// that it happened no later than this.
	closeNested := func(depth int, end float64) {
		var nested []*openSpan
		for k, st := range stacks {
			if kindDepth[k] <= depth {
				continue
			}
			nested = append(nested, st...)
			stacks[k] = nil
		}
		if len(nested) == 0 {
			return
		}
		// Deepest first so an enclosing span is emitted after what it contains,
		// and deterministically ordered: `stacks` is a map, and interval IDs are
		// assigned by position, so an unstable order would make regeneration
		// non-reproducible.
		sort.SliceStable(nested, func(i, j int) bool {
			a, c := nested[i], nested[j]
			if da, dc := kindDepth[a.kind], kindDepth[c.kind]; da != dc {
				return da > dc
			}
			if a.startMs != c.startMs {
				return a.startMs < c.startMs
			}
			return a.logID < c.logID
		})
		for _, sp := range nested {
			e := end
			if e < sp.startMs {
				e = sp.startMs
			}
			emit(sp.kind, sp.startMs, e, sp.label, sp.logID, ConfidenceInferred, sp.err)
		}
	}

	// popEnd finds the open span that an end event closes, plus any spans left
	// stranded above it on the stack.
	//
	// For tools the name matters. Results go missing often enough that a
	// name-blind pop will close an `Edit` using a `Bash`'s result, inventing a
	// duration and attaching it to the wrong label. When both sides name the
	// tool we only pair like with like; a result naming a tool nobody called is
	// left to the dangling-end path, which infers a start instead.
	popEnd := func(kind IntervalKind, ev logEvent) (sp *openSpan, stranded []*openSpan, ok bool) {
		st := stacks[kind]
		if len(st) == 0 {
			return nil, nil, false
		}
		idx := len(st) - 1
		if kind == KindTool {
			if name := payloadStr(ev.entry.JSONPayload, "tool_name"); name != "" {
				idx = -1
				for i := len(st) - 1; i >= 0; i-- {
					if st[i].label == name {
						idx = i
						break
					}
				}
				if idx < 0 {
					return nil, nil, false
				}
			}
		}
		sp = st[idx]
		stranded = append(stranded, st[idx+1:]...)
		stacks[kind] = st[:idx]
		return sp, stranded, true
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

		if sp, stranded, matched := popEnd(kind, ev); matched {
			end := ev.ms
			if sp.childEnd > end {
				end = sp.childEnd
			}
			// Calls that were still open on this stack never got a result of
			// their own; they cannot have outlasted the one that just closed.
			for _, s := range stranded {
				e := end
				if e < s.startMs {
					e = s.startMs
				}
				emit(s.kind, s.startMs, e, s.label, s.logID, ConfidenceInferred, s.err)
			}
			closeNested(kindDepth[kind], end)
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
		// Anything still open inside this one must have begun after it did, so
		// pull the inferred start back far enough to actually contain them.
		for k, st := range stacks {
			if kindDepth[k] <= kindDepth[kind] {
				continue
			}
			for _, sp := range st {
				if sp.startMs < start {
					start = sp.startMs
				}
			}
		}
		closeNested(kindDepth[kind], ev.ms)
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

	pairs := b.pairDeliveries()

	for _, ev := range b.events {
		if ev.stream != "scion-messages" {
			continue
		}
		if strings.Contains(ev.msg, "rejected") {
			continue
		}
		// A paired acknowledgement is not a message of its own: it is the
		// arrival half of a dispatch we are about to emit. Folding the two rows
		// into one edge is what stops every message being drawn twice.
		if pairs.consumedAcks[ev.entry.InsertID] {
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
		if ackMs, ok := pairs.arrivalMs[ev.entry.InsertID]; ok {
			// The log told us when the recipient actually took the message.
			// This is the only place an arrival time is ever measured rather
			// than guessed, and it is what makes the arrow's slope honest.
			recv, conf = ackMs, ConfidenceMeasured
		}
		body, bodyCut := clipRunes(meta.content, b.opts.MaxBodyLen)
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
			Urgent:         meta.urgent,
			Body:           body,
			BodyTruncated:  bodyCut,
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
			t, err := logparser.TimestampToTime(ev.Timestamp)
			if err != nil {
				continue
			}
			ms := float64(t.Sub(b.start)) / float64(time.Millisecond)
			fromID := b.resolveAgentRefAt(lc.RequestedBy, "", ms)
			if fromID == "" || fromID == target.ID {
				continue
			}
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
	uniquify(len(b.edges), func(i int) string { return edgeKey(b.edges[i]) },
		func(i int, id string) { b.edges[i].ID = id })
}

// Message rows in scion-messages come in two phases. The broker logs the
// hand-off to the recipient's container, and then the recipient's side logs
// that it took the message into its inbox. Both rows carry the same endpoints
// and the same content, so without pairing they look like two separate
// messages -- which is why an unpaired build reports roughly twice as many
// edges as there were messages.
type msgPhase int

const (
	phaseOther msgPhase = iota
	phaseSend           // "message dispatched"
	phaseAck            // "message accepted (buffered)"
)

func messagePhase(msg string) msgPhase {
	switch {
	case strings.Contains(msg, "dispatched"):
		return phaseSend
	case strings.Contains(msg, "accepted"):
		return phaseAck
	}
	return phaseOther
}

// deliveryPairs is the result of matching dispatch rows to acknowledgement
// rows: the measured arrival time for each dispatch, and the set of
// acknowledgements that have been folded into one and must not be drawn again.
type deliveryPairs struct {
	arrivalMs    map[string]float64 // dispatch InsertID -> arrival offset (ms)
	consumedAcks map[string]bool    // ack InsertID
}

// pairDeliveries matches each "message dispatched" row with the
// "message accepted" row for the same message.
//
// There is no message ID in the export, so the key is (sender, recipient,
// content) and matching is FIFO within that key -- a repeated message is
// matched oldest-dispatch-first, which is the only ordering consistent with a
// queue. Pairs are rejected if the acknowledgement precedes the dispatch or
// falls outside PairDeliveryWindowMs.
//
// Exports that carry only one of the two phases pair nothing and fall through
// to the inference path unchanged, so this is safe on partial logs.
func (b *builder) pairDeliveries() deliveryPairs {
	pairs := deliveryPairs{
		arrivalMs:    make(map[string]float64),
		consumedAcks: make(map[string]bool),
	}
	if b.opts.PairDeliveryWindowMs <= 0 {
		return pairs
	}

	type pending struct {
		insertID string
		ms       float64
	}
	// Keyed on the logical message; each key holds dispatches still awaiting
	// an acknowledgement, oldest first.
	waiting := make(map[string][]pending)

	for _, ev := range b.events {
		if ev.stream != "scion-messages" || strings.Contains(ev.msg, "rejected") {
			continue
		}
		phase := messagePhase(ev.msg)
		if phase == phaseOther {
			continue
		}
		fromID, toID, meta := b.messageEndpoints(ev)
		if !meta.valid || fromID == "" || toID == "" {
			continue
		}
		key := fromID + "\x00" + toID + "\x00" + meta.content

		switch phase {
		case phaseSend:
			waiting[key] = append(waiting[key], pending{ev.entry.InsertID, ev.ms})
		case phaseAck:
			q := waiting[key]
			// Discard dispatches that have aged out: their acknowledgement
			// was never logged, so they should keep an inferred arrival.
			for len(q) > 0 && ev.ms-q[0].ms > b.opts.PairDeliveryWindowMs {
				q = q[1:]
			}
			if len(q) == 0 {
				waiting[key] = nil
				continue
			}
			pairs.arrivalMs[q[0].insertID] = ev.ms
			pairs.consumedAcks[ev.entry.InsertID] = true
			waiting[key] = q[1:]
		}
	}
	return pairs
}

// messageMeta carries the non-endpoint fields of a message log entry.
type messageMeta struct {
	valid     bool
	msgType   string
	content   string
	broadcast bool
	urgent    bool
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
		urgent:    payloadBool(jp, "urgent"),
	}
	return b.resolveAgentRefAt(sender, senderID, ev.ms),
		b.resolveAgentRefAt(recipient, recipientID, ev.ms),
		meta
}

// resolveAgentRef maps an agent reference (an ID, a name, or an "agent:name"
// token) onto a lifeline ID, without regard to when the reference was made.
// Prefer resolveAgentRefAt where a timestamp is available.
func (b *builder) resolveAgentRef(ref, id string) string {
	return b.resolveAgentRefAt(ref, id, math.NaN())
}

// resolveAgentRefAt maps an agent reference onto a lifeline ID, preferring the
// explicit ID and otherwise resolving the name as of time ms.
//
// The timestamp matters because a name is not a unique key. An agent that
// fails to start and is recreated leaves two lifelines called "planner", and
// most message rows carry only the name -- so a name-only lookup will happily
// attribute an hour of traffic to a lifeline that lived for three seconds.
// Resolving against the reference time keeps those arrows on the instance that
// was actually alive to send or receive them.
func (b *builder) resolveAgentRefAt(ref, id string, ms float64) string {
	if id != "" {
		if _, ok := b.byID[id]; ok {
			return id
		}
	}
	name := strings.TrimPrefix(ref, "agent:")
	if _, ok := b.byID[name]; ok {
		return name
	}
	if lid := b.pickByName(name, ms); lid != "" {
		return lid
	}
	if id != "" {
		if lid := b.pickByName(id, ms); lid != "" {
			return lid
		}
	}
	return ""
}

// pickByName chooses among the lifelines sharing a name. With a usable
// timestamp it prefers one that was alive then, then the nearest by time;
// otherwise it falls back to the first seen, which is the historical behaviour.
func (b *builder) pickByName(name string, ms float64) string {
	ids := b.idsByName[name]
	switch len(ids) {
	case 0:
		return ""
	case 1:
		return ids[0]
	}
	if math.IsNaN(ms) {
		return ids[0]
	}
	best, bestGap := "", math.Inf(1)
	for _, id := range ids {
		l, ok := b.byID[id]
		if !ok {
			continue
		}
		if ms >= l.BirthMs && ms <= l.DeathMs {
			return id
		}
		gap := l.BirthMs - ms
		if gap < 0 {
			gap = ms - l.DeathMs
		}
		if gap < bestGap {
			best, bestGap = id, gap
		}
	}
	if best == "" {
		return ids[0]
	}
	return best
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
		switch e.RecvConfidence {
		case ConfidenceInferred:
			s.InferredEdges++
		case ConfidenceMeasured:
			s.MeasuredEdges++
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
	out, cut := clipRunes(strings.TrimSpace(strings.ReplaceAll(s, "\n", " ")), maxLabelLen)
	if cut {
		return out + "…"
	}
	return out
}

// clipRunes truncates to at most n runes, reporting whether anything was
// removed. Runes rather than bytes: slicing a UTF-8 string by byte count
// happily cuts a multi-byte character in half and emits a replacement glyph.
// A limit of 0 clips everything, which is how bodies are switched off.
func clipRunes(s string, n int) (string, bool) {
	if n <= 0 {
		return "", s != ""
	}
	if len(s) <= n {
		// Bytes >= runes, so this is a safe fast path.
		return s, false
	}
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}
