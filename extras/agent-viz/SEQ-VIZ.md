# seq-viz — sliding-window sequence visualizer

An experimental view of a multi-agent run: a **sequence diagram whose vertical
axis is metric wall-clock time**, seen through a sliding window that speeds up
and slows down so the interesting parts play at a readable pace.

```bash
cd extras/agent-viz
make seq-run          # build everything, serve a synthetic 60-agent run
```

Open <http://localhost:8090>. No log file or credentials needed — it
synthesizes a run.

---

## Contents

- [Why this exists](#why-this-exists)
- [Core ideas](#core-ideas)
- [Using it](#using-it)
- [Command line](#command-line)
- [Demo data](#demo-data)
- [Architecture](#architecture)
- [The digest format](#the-digest-format)
- [Development](#development)
- [Testing](#testing)
- [Live sessions (design)](#live-sessions-design)
- [Promotion into the main web UI](#promotion-into-the-main-web-ui)
- [Limitations](#limitations)
- [Deferred ideas](#deferred-ideas)

---

## Why this exists

Two existing visualizations each solve half the problem.

A **flame graph or distributed trace** lays time out left-to-right and stacks
call depth vertically. It is excellent for one request through many services,
but it destroys *actor identity*: if agent `planner` participates five times, it
appears as five unrelated boxes in five places. You cannot ask "what was the
planner doing all afternoon?"

A **UML sequence diagram** keeps one lifeline per actor, so an actor is a single
stable column no matter how often it participates. But conventionally its
vertical axis is *ordinal*, not metric — position means "after", not "how long
after" — and it is a static picture.

seq-viz keeps the sequence diagram's actor-per-column axis and makes the
vertical axis **metric**. That one change is what lets the two compose:

- An activation box and a span become **the same object**. Nested activations
  (session > turn > tool) render as a flame graph running *down* each lifeline,
  so you get trace-style nesting without giving up actor identity.
- Message arrows become **sloped**, and the slope *is* the delivery latency.
  This is the Lamport space-time diagram, not UML.

Everything else in the design follows from committing to a metric time axis.

## Core ideas

### 1. Time is warped temporally, never spatially

A run is mostly idle. The obvious fix is to squeeze the idle gaps in the layout
— but then a bar's height no longer means its duration, and every visual
comparison silently lies.

Instead **`msPerPx` is constant everywhere on the canvas**, and we vary how fast
the playhead *travels*. Boring stretches are crossed at high velocity; bursts
slow toward 1×. Geometry stays honest; only the clock is elastic. The minimap
stays strictly linear in wall time, so the compression is always legible
alongside the truth.

The mapping is a precomputed monotonic function `T_viewer → T_wall` (`Warp`, a
list of piecewise-linear knots). The scrubber, minimap, timestamp readout and
deep links are all *projections of this one function*, so they cannot disagree
with each other, and playback is deterministic — the same digest plays the same
way for everyone.

The invariant the planner targets is **constant events per second of viewer
attention**, not constant wall-time per second:

```
v(t) = target_rate / density(t)
```

### 2. Acceleration is planned, not reactive

Snapping to a new velocity when a burst appears is jarring, and worse, it
arrives *late* — you are already inside the burst before the view slows.

So the velocity profile is planned ahead, like a CNC feed rate. The natural
constraint `|dv/dτ| ≤ A` is awkward because `dτ = dt/v`. Under the substitution
`u = v²/2` it collapses to:

```
|du/dt| ≤ A
```

which is linear and solvable in two sweeps. A **forward pass** limits
acceleration; a **backward pass** limits deceleration. The backward pass is the
important one: it is what guarantees the view has *already* slowed down by the
time a burst reaches the frame.

Density is also smoothed with a frame-sized kernel before planning, so
deceleration begins before a burst enters the viewport rather than as it lands.

### 3. Three zones

```
   ┌──────────────┐
   │     WAKE     │  above frame: already seen, fading out
   ├──────────────┤  ← frameTop     (30% of height)
   │              │
   │    FRAME     │  readable, honest wall-clock
   │              │
   ├──────────────┤  ← frameBottom  (78%) == playhead
   │   STAGING    │  below frame: the future, rushing up.
   └──────────────┘    at speed, bars cross-fade into motion streaks
```

Future below, past above. Streaks are a deliberate signal: when the staging zone
is moving too fast to read, it should *look* too fast to read, rather than
strobing as though it were data you were meant to parse.

### 4. The column axis is a tree

- Columns are keyed on each agent's persisted `Ancestry`. A **collapsed parent
  absorbs its whole subtree into one composite column** (like Perfetto track
  groups), which is what makes solo and pin fall out for free.
- **Slot recycling**: columns are reused by non-overlapping lifelines via greedy
  interval-graph colouring, so 100 agents with 12 concurrent need ~12 columns,
  not 100.
- **Auto-fold** collapses idle subtrees to a narrow stripe. This is throttled
  and hysteretic — recomputing per frame would make the axis shimmer and destroy
  object constancy. See `LAYOUT_REFRESH_MS` and `ACTIVITY_WINDOW_FACTOR` in
  `seq-viz.ts`.

- **Adaptive width**: layout runs once at the minimum column width to learn how
  many columns survive folding, then widens them to fill the canvas (clamped to
  104–240px). A run with seven agents should not leave half the window empty; a
  run with forty should not try.

Note the symmetry: **time** compresses *temporally* (velocity); **columns**
compress *spatially* (collapse/fold). Spatial compression is only safe on the
column axis because horizontal position carries no metric meaning there —
which is also why widening columns to fill the canvas costs no honesty.

### 5. Confidence is a first-class field

Agent telemetry is genuinely incomplete. A view that hides that is worse than
useless for diagnosis.

| Confidence | Meaning | Rendered |
|---|---|---|
| `measured` | both endpoints observed | solid |
| `inferred` | an endpoint reconstructed from neighbouring events | hatched |
| `open` | started, never ended (still running at the window edge) | faded, unterminated |

**A duration is never fabricated.** In hook-per-process deployments many tool
spans would otherwise collapse to zero; they are marked, not invented.

Inference is also constrained to preserve nesting: an end is only propagated
from a strictly deeper kind to a shallower one, never between siblings, so an
inferred parent end always still contains its children.

Containment runs the other way too. Ends go missing often enough that a
name-blind stack pop would close an `Edit` with a `Bash`'s result, and a tool
call left open would float until some unrelated later event closed it —
drawing a bar hundreds of seconds outside the turn it belonged to. So tool
results pair by `tool_name` when both sides carry one, and anything still open
when its enclosing span ends is closed at that boundary and marked `inferred`.
A turn ending *is* evidence that the tools inside it finished. The invariant
"a child that starts inside a parent also ends inside it" is asserted on the
Go side and again on the generated digest from the frontend tests.

### 5b. Arrival times are measured where the log allows it

A real Scion export writes two rows per delivered message: the broker's
`message dispatched`, and the recipient's `message accepted (buffered)` when it
takes the message into its inbox. The digest pairs them — FIFO, keyed on
endpoints plus content, within a bounded window — and folds the pair into one
edge.

This matters twice over. Not pairing them draws **every message twice**. Pairing
them makes the arrival time `measured` rather than a guess, which is what makes
the arrow's slope real latency instead of a plausible-looking fiction. On a real
49-minute run: 1490 message rows → 758 edges, 744 of them with measured arrival.

Where the acknowledgement is missing — the recipient died, or the export window
clipped it — the edge falls back to the inferred arrival described above. The
legend states the measured share for the loaded run, because slope is the most
seductive thing on the canvas: it looks like latency whether or not anyone
measured it.

Names are also not unique keys. A restarted agent reuses its name with a fresh
UUID, and most message rows carry only the name — so name→ID resolution happens
*as of the row's timestamp*, preferring the instance that was actually alive
then. Without that, one real run attributed 112 edges and six child agents to an
instance that had lived for three seconds.

### 6. Edge stubs

When a message's peer is offscreen, hidden by a collapse, or more than 8 columns
away, the arrow is replaced by a labelled stub rather than dropped. A cropped
window that silently omits edges lies about connectivity. Stub reasons:
`offscreen`, `hidden`, `distant`.

### 7. Live and replay are the same thing

There is no separate live mode. "Live" is just the playhead pinned to
`now − 5 min` at 1×. The lag budget is a resource the express lane spends to
catch up. This dissolves the usual live/replay dual-mode complexity into a
single scrubbable timeline.

## Using it

```
┌──────────────────────────────────────────────────────────┬────┐
│ header: run name · counts · auto-fold · zoom · ratio     │    │
├──────────────┬───────────────────────────────────────────┤ m  │
│              │                                           │ i  │
│  lifeline    │            canvas                         │ n  │
│  tree        │            (the three zones)              │ i  │
│              │                                           │ m  │
│              │   [detail panel appears here on click]    │ a  │
│  legend ↖    │                                           │ p  │
├──────────────┴───────────────────────────────────────────┴────┤
│ transport: play · scrubber · clock · rate · velocity          │
└───────────────────────────────────────────────────────────────┘
```

### Keyboard

| Key | Action |
|---|---|
| `Space` | play / pause |
| `↓` / `↑` | step forward / back 1 s of viewer time |
| `+` / `-` | zoom the time axis (in / out) |
| `Esc` | clear selection |

Shortcuts are ignored while typing in an input.

### Mouse

- **Click a bar** — open the detail panel: kind, duration, confidence, and a
  Cloud Logging deep link carrying the project and a ±5 min window around the
  event, so it lands on the right rows rather than a default last-hour view.
- **Click an arrow** — inspect the message: type, endpoints, and whether the
  arrival time was measured or inferred.
- **Hover an arrow** — the arrow highlights and a chat bubble appears at its
  midspan. **Click the bubble** to read the message itself: the body, both
  endpoints, send and arrival times, latency, arrival confidence, and any
  broadcast/urgent flags. Playback pauses when the reader opens, because reading
  takes seconds and the arrow would otherwise be gone by the time you closed it.
  `Esc` closes it.
- **Click a stub** — jump the playhead to the peer end of that message.
- **Wheel over the canvas** — scrub time. The gesture is metric like the axis
  is: N pixels of wheel moves the diagram by exactly the N pixels' worth of wall
  time it would take to cover that distance on screen, so scrolling feels like
  dragging the paper rather than turning an abstract dial. Scrolling pauses
  playback — you have taken the wheel. Line- and page-mode deltas (mouse wheels,
  page keys) are normalised to pixels first, or a wheel notch would advance the
  view by three milliseconds.
- **Ctrl/⌘ + wheel** — zoom the time axis, matching the pinch-zoom convention
  every map and browser already trained the reader on.
- **Drag the minimap window** — seek. It is a drag, not a click: see below.

Hit-testing follows paint order — bubble, then arrows and stubs, then bars.
Arrows are drawn *over* the bars they cross and nearly every arrow crosses one,
so resolving to the bar first made most arrows unreachable. The cost is a ~5px
band along each arrow where the bar underneath cannot be clicked.

### Left tree

Toggle collapse on any parent, or solo a subtree. Active lifelines are
highlighted; the tree is the affordance for drilling into a run that starts
mostly collapsed (anything below depth 1 with children begins collapsed,
because a hundred columns at once is unreadable).

### Transport

Play/pause, a scrubber over viewer time, the wall clock, and a rate selector
(0.25× – 8×). The rate multiplies the planned profile; it does not replace it.

The clock readout blurs and glows in proportion to `log(velocity)`, with an
"N× express" pill, so you can tell at a glance that time is being skipped.

### Run overview (the minimap)

A 200px rail down the right-hand side, in **true linear wall time** — the one
place in the UI that is not warped, so it is the honest answer to "where am I
and what did I skip". Three bands:

| band | shows |
|---|---|
| time gutter | `mm:ss` ticks at a readable spacing |
| density heat | overall event intensity, the input to the velocity planner |
| lifeline lanes | one lane per lifeline, coloured where that agent was active |

The lanes are the reason it needs the width: knowing *something* was busy at
32:00 is much less useful than seeing it was `kael` and `scribe` and nobody
else. Collapse the whole rail to 22px when the canvas needs the room. The static
bands are rastered to an offscreen canvas and blitted, so hover and the viewport
rectangle cost nothing per frame.

Seeking means **grabbing the highlighted viewport window and dragging it**, and
nothing else in the rail responds to a press. The rail is full-height along the
edge of the window, so the pointer crosses it constantly on the way somewhere
else; when a bare click seeked, a slip of the hand threw the playhead minutes
away with no undo. The window is the handle, it keeps its grip (the drag applies
a delta, so the point you grabbed stays under the cursor rather than snapping to
centre), and it carries a 7px margin so that a sub-pixel window — a 6s viewport
on a 49-minute run — is still catchable. The cursor turns to `grab` over it, and
the window brightens and grows edge grips, so the affordance is visible before
the press.

The hover readout waits ~220ms for the pointer to stop. Passing through should
not raise a tooltip; asking should. It also stays off the window itself, where
it would cover the handle to report a time the transport clock is already
showing.

### Header

Lifeline/interval/edge counts, an **auto-fold** toggle, zoom controls with the
current scale (e.g. `1.2s/100px`), and the overall compression ratio.

## Command line

```bash
./seq-viz [flags]
```

### Input (pick one; defaults to synthetic)

| Flag | Default | Meaning |
|---|---|---|
| `--log-file` | — | GCP Cloud Logging JSON export |
| `--fs-log` | — | fs-watcher NDJSON log (optional, pairs with `--log-file`) |
| `--digest-file` | — | precomputed digest JSON; skips parsing and synthesis |
| `--max-depth` | `3` | max directory depth for file graph nodes (`0` = unlimited) |

### Synthetic run

| Flag | Default | Meaning |
|---|---|---|
| `--synth-agents` | `60` | agents over the whole run |
| `--synth-duration` | `45m` | wall-clock length |
| `--synth-seed` | `1` | same seed ⇒ byte-identical run |
| `--synth-concurrency` | `0` | agents alive at once (`0` = default 8–12); raise it to pressure the column axis |

### Velocity planner

| Flag | Default | Meaning |
|---|---|---|
| `--frame` | `1m` | wall-clock duration visible at velocity 1 |
| `--target-rate` | `6` | target events per second of viewer attention |
| `--max-velocity` | `120` | ceiling, in wall-ms per viewer-ms |
| `--max-accel` | `0.02` | max rate of change of `v²/2` w.r.t. wall time |

Tuning notes:

- Playback feels rushed → lower `--target-rate`.
- Idle stretches drag → raise `--max-velocity`.
- Speed changes feel abrupt or nauseating → lower `--max-accel`.
- Bars are too cramped to read → raise `--frame`.

### Output and serving

| Flag | Default | Meaning |
|---|---|---|
| `--dump-digest` | — | write the digest to a path and exit |
| `--dump-log` | — | write the synthetic log export to a path and exit |
| `--port` | `8090` | port |
| `--dev` | `false` | serve web assets from disk instead of the embedded bundle |

### HTTP endpoints

| Path | Returns |
|---|---|
| `/api/digest` | the whole precomputed digest, as JSON |
| `/api/healthz` | `ok` |
| anything else | the SPA (`index.html`), so deep links survive a reload |

There is no WebSocket and no server-side playback engine. The digest is computed
once and the client owns the clock — which is exactly why a shared link lands on
the same moment for everyone.

## Demo data

See **[demo/README.md](demo/README.md)**. Short version:

```bash
make seq-demo              # serve the committed sample
./demo/regenerate.sh       # regenerate all scenarios (~5s)
```

Scenarios cover column pressure (`wide`, ~35 concurrent), volume (`stress`,
3,200 intervals), and time pressure (`idle`, 27× compression). If you change the
layout or the planner, check `wide` and `idle` before the pretty one.

## Architecture

```
cmd/seq-viz/              entrypoint, flags
internal/digest/
  types.go                the contract (SchemaVersion, all wire structs)
  build.go                logs -> digest: lifelines, intervals, edges, slots
  velocity.go             density -> warp knots (the two-pass planner)
  synth.go                deterministic synthetic runs
internal/seqserver/       static assets + /api/digest
demo/                     sample data + regenerate.sh
web-seq/
  src/seq/core/           DOM-free logic
    types.ts              mirror of the Go contract
    warp.ts               T_viewer <-> T_wall
    clock.ts              playback state machine
    columns.ts            ancestry forest, collapse/solo/fold, slot layout
    frame.ts              digest + layout + time -> a drawable FrameModel
    render.ts             FrameModel -> canvas; hit testing
  src/seq/components/     Lit components
    seq-viz.ts            root: state, rAF loop, wiring
    seq-canvas.ts         thin canvas host
    seq-transport.ts      play/scrub/rate
    seq-minimap.ts        honest linear overview
    seq-lifeline-tree.ts  ancestry tree, collapse/solo
    seq-detail-panel.ts   selection details + Cloud Logging deep link
    seq-legend.ts         confidence and edge legend
```

Data flow:

```
GCP logs ──parse──> entries ──build──> Digest ──JSON──> browser
                                 │
                            density ──plan──> Warp
                                                 │
   clock.tick ─> τ ─> warp.wallAt(τ) ─> wallMs ──┴─> buildFrame ─> renderFrame
```

Two boundaries are load-bearing:

- **`src/seq/core/` has no DOM dependency.** Plain functions over plain data,
  unit-testable without a browser, and portable into the main UI unchanged.
- **`internal/digest/types.go` and `core/types.ts` are a mirrored frozen
  contract.** Change them together and bump `SchemaVersion`.

Rendering is canvas, not SVG or DOM: 50–100 columns × thousands of intervals
must hold 60fps, and a node per interval will not.

## The digest format

One JSON document, computed once. Abridged:

```jsonc
{
  "version": 1,
  "projectId": "...", "startedAt": "2026-03-22T16:00:00Z", "durationMs": 2700000,

  "lifelines": [{
    "id": "...", "name": "planner", "color": "#...",
    "parentId": "...", "ancestry": ["root", "...", "parent"],
    "depth": 1, "order": 3, "slot": 2,      // slot = recycled column
    "birthMs": 0, "deathMs": 812000, "died": true
  }],

  "intervals": [{
    "id": "...", "lifelineId": "...",
    "kind": "tool",                          // lifecycle|session|turn|tool
    "depth": 3,                              // nesting within the lifeline
    "startMs": 1000, "endMs": 1400,
    "confidence": "measured",                // measured|inferred|open
    "error": false, "logId": "..."           // logId -> Cloud Logging deep link
  }],

  "edges": [{
    "id": "...", "kind": "message",          // message|spawn|destroy
    "fromId": "...", "toId": "...",
    "sendMs": 1200, "recvMs": 1350,          // slope == latency
    "recvConfidence": "inferred",
    "msgType": "instruction", "broadcast": false, "urgent": false,
    "body": "...", "bodyTruncated": false,   // capped by --max-body
    "logId": "..."
  }],

  "density": { "bucketMs": 1000, "samples": [...], "peak": 4.2 },

  "warp": {                                  // the T_viewer -> T_wall function
    "knots": [{ "tauMs": 0, "wallMs": 0, "velocity": 16.2 }],
    "totalTauMs": 241198, "minVelocity": 1.3, "maxVelocity": 75.3
  },

  "stats": { "lifelineCount": 59, "maxConcurrent": 12, "compressionRatio": 11.19 }
}
```

All times are **milliseconds since `startedAt`**, never absolute — so the whole
document is relative and the client only needs one absolute anchor.

**IDs are derived, not positional.** An interval is
`iv.<kind>.<insertId>`, an edge `e.<kind>.<insertId>`, and a spawn arrow
`e.spawn.<childLifelineId>`, so the same event keeps the same name in every
rebuild of a session that is still being read. Where no log row produced the
row — the synthesised lifecycle bar a lifeline gets when it has no lifecycle
events at all — the fallback is `iv.lifecycle.<lifelineId>`, deliberately
without a time, because that bar's start is only "first seen" and moves whenever
earlier evidence turns up. Everything else falls back to
`<kind>.<lifelineId>@<startMs>`, and any residual collision gets a `~2`, `~3`
suffix in sort order. Treat IDs as opaque; the format is not a wire contract.

Payload sizes: ~100 KB for 14 agents / 6 min, ~650 KB for 60 agents / 45 min,
~2.5 MB for 120 agents / 3 h. Warp knots dominate at long durations (one per
density bucket, merged only where velocity is exactly equal).

Message bodies are inlined so the reader needs no round trip, but they are not
free: 738 messages in one real 49-minute export came to 342 KB of text. They are
capped at 2,000 characters each (`--max-body`, `-1` to omit them entirely), and
`bodyTruncated` says so explicitly rather than letting a clipped body pass for
a whole one. An absent `body` means "not exported", never "empty message" — the
reader states that distinction rather than guessing.

## Development

```bash
make seq            # build frontend + binary
make seq-web        # frontend only (emits into internal/seqserver/dist)
make seq-test       # typecheck + vitest + go test
make seq-run        # serve a synthetic run
make seq-demo       # serve the committed demo sample
make seq-demo-data  # regenerate all demo datasets
```

Live-reload loop — two terminals:

```bash
go run ./cmd/seq-viz --dev     # backend on :8090
make seq-dev                   # vite on :3100, proxies /api to :8090
```

Then use <http://localhost:3100>.

Notes:

- `web-seq/tsconfig.json` uses `NodeNext` resolution, so **relative imports need
  `.js` extensions** even in TypeScript.
- The built bundle is embedded via `//go:embed dist/*`, which needs at least one
  file present — hence the committed `internal/seqserver/dist/.gitkeep`, which
  `npm run postbuild` restores after Vite empties the directory.

## Testing

```bash
make seq-test
```

| Suite | Covers |
|---|---|
| `internal/digest/build_test.go` | interval nesting, confidence inference, slot recycling, edge resolution, cycle safety |
| `internal/digest/identity_test.go` | interval and edge IDs surviving an earlier insertion and an interval closing; uniqueness on a real 1,134-interval export |
| `internal/digest/stability_test.go` | pinned columns surviving a new agent and a death arriving; unhonourable pins dropped; pinned origin holding offsets still; pre-origin entries clamped |
| `internal/digest/velocity_test.go` | warp monotonicity, round-trip inverse, accel limits, decelerate-before-burst, uniform-density linearity |
| `internal/seqserver/server_test.go` | SPA fallback, asset serving, API 404s, digest round-trip |
| `core/*.test.ts` | warp, clock, columns, frame geometry |
| `components/*.test.ts` | transport and tree rendering and events |
| **`core/integration.test.ts`** | **a real Go-generated digest driven through the whole TypeScript read path** |

The integration test deserves the emphasis. Every other test uses hand-written
fixtures, so they would all keep passing while the Go writer and the TypeScript
reader drifted apart. That test reads the committed demo sample directly — the
same bytes a person opens in the viewer — and asserts referential integrity,
nesting containment, `recvMs >= sendMs`, warp/velocity agreement across the
language boundary, and that bar height equals `duration / msPerPx` on real data.

## Live sessions (design)

Not built yet. This is the plan for starting from a project and a hub instead of
a file, and it is written down first because the naive version — poll, rebuild,
re-render — breaks the two things the tool is actually for: a stable playhead
and honest confidence.

### A session is not a run

A file is a run: it has a beginning, an end, and a full density profile the
planner can see all of. A live feed has none of those, so the unit becomes a
**viewing session**: a fixed origin (the moment you opened it, minus a seeded
history window of 10–15 minutes), an open end, and a scale that only grows. The
session's `startedAt` is chosen once and never moves. Everything downstream —
every `*Ms` offset in the digest, the minimap's coordinate system, the tau axis
— is anchored to it, so re-anchoring would shift the entire diagram under a
reader who is looking at it. A date-range chooser, later, is the same machinery
with a closed end; a log file is a session that was already over when it opened.

### The lag budget is the whole design

Section 7 states the principle: live is the playhead pinned to `now − 5 min` at
1×. That lag is not a UI preference, it is the resource that makes every other
part legal, and its size is determined by the planner's own constants.

The acceleration limiter works in `u = v²/2` and moves at most
`MaxAccel × bucketMs` per bucket, so decelerating from express speed to reading
speed takes a fixed amount of wall time, and the two are the same knob:

```
v_max = sqrt(2 · MaxAccel · lagMs)
```

At the shipped `MaxAccel = 0.02`, a 6-minute lag buys exactly the 120× cap the
planner already uses; a 5-minute lag buys 110×; a 1-minute lag buys 49×. **You
cannot have both a short lag and a fast express lane** — with less runway the
profile physically cannot have slowed to reading speed by the time a burst
arrives, which is the one promise section 2 makes.

Three other windows need lookahead and all fit inside that budget: the density
smoothing kernel is symmetric with radius `FrameMs/2` (30 s), receive-time
inference looks ahead `InferRecvWindowMs` (120 s), and dispatch/ack pairing
spans `PairDeliveryWindowMs` (300 s).

So the session has a **watermark** at `now − lag`, and three regions:

| region | wall time | status |
|---|---|---|
| settled | `< watermark` | immutable; warp knots frozen; the playhead lives here |
| provisional | `watermark … now` | fetched and drawn in the staging zone, but every unresolved end is `open` and may be revised |
| unknown | `> now` | empty staging, honestly |

The invariant that holds the whole thing together: **the playhead never crosses
the watermark**, so the warp is only ever replanned ahead of where the reader
is. Already-consumed tau never shifts, and the playhead never teleports. This is
also why `WarpFn` needing a rebuild is harmless — appending knots past the last
consumed tau leaves the existing prefix mapping identical.

The provisional region is not a fudge. An in-flight turn genuinely has no end
yet and a just-sent message genuinely has no arrival yet; the confidence model
already has `open` for exactly that, and the live tail is simply the part of the
run where `open` is the truth rather than a gap in the export. Events resolve
`open → inferred → measured` as more log arrives, which is the same promotion
the file path already performs, just observed happening.

### Polling, on both hops

The transport question answers itself once the lag budget exists: with the
playhead five minutes behind, **latency below a few seconds is worth nothing**.
So neither hop needs streaming.

- **Cloud Logging → server.** Poll `entries.list` every ~3 s, filtered to
  `scion-agents` and `scion-messages` for the hub, asking for
  `timestamp >= cursor − overlap`. Dedupe on `insertId`, which the pipeline
  already carries end-to-end as `logId`. The overlap (~2 min) covers late
  arrival; anything later than that still lands in the provisional region rather
  than rewriting settled history. `TailLogEntries` is available and
  lower-latency, but it drops entries under `RATE_LIMIT` and reports only a
  count — a bad trade for a tool whose premise is not lying about what it saw.
  It is worth revisiting only if the lag budget ever needs to shrink.
- **Server → browser.** Poll a delta endpoint on the same cadence. SSE would
  work and there is a proven pattern for it in `pkg/hub/handlers_logs.go`, but a
  long-lived connection buys nothing here and costs reconnection handling.

Reuse `pkg/hub.LogQueryService` (`Query`, `Tail`, `BuildLogFilter`, ADC auth)
rather than writing a second filter-and-credentials implementation — noting that
`Query` currently caps at 1000 entries and would need its `NextPageToken`
plumbed through to backfill a 15-minute seed.

### Rebuild the digest; do not make the builder incremental

`Build` is already a pure function of `([]entry, *ParseResult, Options)`, and on
the 49-minute reference export — 3,700 entries, 8 lifelines, 1,134 intervals,
758 edges — it takes **11 ms**. Scaling is roughly linear, so an eight-hour
session is on the order of 100 ms per rebuild, or ~3% of one core at a 3-second
cadence.

That number kills the hard problem. Making the builder incremental would mean
incrementalising slot colouring, DFS ordering, ancestry attribution, receive
inference and delivery pairing — every one of which is a whole-run pass today,
and several of which exist specifically to *revise* earlier conclusions. Instead
the server keeps the accumulated entries and re-derives the whole digest each
poll, which makes late arrivals and retroactive resolution free and correct by
construction, then diffs against the previous digest to send the client a delta.

Three changes to the builder were required first, all of them worth making
regardless of ingestion, and **all three are now in**. Each is the same bug in a
different coordinate: something the reader is looking at moves because of news
about something else.

1. **Stable identity.** Interval and edge IDs were positional (`iv%d`, `e%d`),
   so a single insertion renumbered everything after it — silently moving the
   reader's selection, the open message reader, and any future deep link onto a
   different event. They are now derived from `insertId`, which the digest
   already carried as `logId`. See *The digest format* for the scheme.
2. **Stable columns.** Slot assignment is greedy interval-graph colouring over
   the full lifetime set, so a newly discovered agent — or an existing agent's
   death arriving and shrinking its lifetime from "still open" to a measured
   time — could reseat an agent that had been in the same column for ten
   minutes. `Options.PinnedSlots` (fed from `SlotsOf(previous)`) places pinned
   lifelines first, at the column they already had, and colours everything else
   around them. Nil pins reproduce the old colouring exactly, so a one-shot
   export is unaffected. A pin that cannot be honoured without overlapping is
   dropped with a log line rather than allowed to draw two agents down one
   column.
3. **A pinned origin.** `b.start` was `stamps[0]`, so one late entry predating
   the current first one shifted every offset in the digest at once.
   `Options.Origin` pins t=0; the zero value keeps the "first entry" behaviour.
   Entries older than a pinned origin are clamped to 0 and counted rather than
   dropped — they may be the session start a later interval depends on.

Still open, and belonging with the ingestion work rather than the builder: DFS
`order` is stable only in the relative sense (a new agent inserted mid-forest
renumbers its successors), which is fine because `order` only sorts, and a
**horizon** input so that an idle session's `durationMs` keeps up with the clock
instead of stopping at the last entry.

### The minimap anchors and grows; it never rolls

The rail could scroll like a terminal, keeping a fixed window and letting the
beginning fall off the top. It should not. Its entire contract is "where am I
and what did I skip", and a session that discards its own beginning cannot
answer that. Memory is not the constraint that makes the decision — the
reference run is 1,134 intervals and a 1.13 MB digest, so even a long session is
a rounding error — so the honest default is to keep everything and let the rail
compress, exactly as it already does for a long file.

What growth actually costs is redrawing. `durationMs` is the rail's whole
coordinate system, so every increase rescales it: lanes shift, tick granularity
jumps, the cached static raster is thrown away, and a drag in progress finds the
scale moving under it. The fix is to **grow the declared span in quantised
steps** — round up to the next 5 minutes, with headroom — so the rail rescales a
few times an hour instead of continuously, the raster survives in between, and
new intervals can be drawn straight into the existing layer. Freeze the span for
the duration of an active drag.

If a session ever does run long enough to be useless at that scale, trimming
should be an explicit user action with a visible consequence, never a silent
eviction.

### Where the playhead starts, and what "following" means

Start it at the beginning of the seeded history, not at the live edge. The
express lane is precisely the mechanism for catching up — fifteen minutes of
mostly-idle history plays in a minute or two — so the reader sees how the run
got to its current state and arrives at the live edge naturally. **The velocity
planner is the catch-up controller**; no new machinery is needed.

On reaching the watermark the session is *following*: velocity clamps to 1× and
the view rides the edge. Scrubbing away drops out of follow; a "jump to live"
affordance re-enters it. Two clock behaviours are wrong for this and must change:
`tick()` pauses on reaching `totalTauMs`, and `play()` restarts from zero when
already at the end — which would throw a following viewer back to the start of
the session. `atEnd` needs to split into "end of a closed run" and "at the live
edge", where the latter stalls while remaining `playing`.

### Client-side merge

`adopt()` resets tau to 0, rebuilds every id map, recomputes the default collapse
set and restarts the loop; calling it on each poll would reset the reader's
world every three seconds. A live session needs a `merge()` that preserves tau,
collapse, solo and selection, and:

- pushes into the frame index instead of discarding it — the `WeakMap` in
  `frame.ts` is keyed on digest identity and its `prefixMaxEnd`/`prefixMaxT`
  arrays are append-friendly, so monotone appends are four `push`es rather than
  an O(n log n) rebuild;
- swaps `WarpFn`/`PlaybackClock` (both hold `readonly` refs) while carrying over
  tau, rate and playing;
- refreshes `stats`, `durationMs`, `totalTauMs` and `maxVelocity` together,
  since the header and transport read them straight off the wire object;
- memoises `markers()`, which currently rescans every interval inside `render()`.

The other known hotspot is `activeLifelineIds`, an unindexed full scan of all
intervals and edges that auto-fold runs ~2.5×/s.

### Open questions

- Which "project" scopes a session: the GCP project owns the log, but
  `labels.hub` and the Scion project id both narrow it, and the right primary
  key for a session is probably (hub, Scion project) with the GCP project as
  where to look.
- Whether `extras/agent-viz` should require the root module (a `replace ../..`)
  to reuse `pkg/hub`'s log query, or vendor a minimal client of its own. The
  former is the right end state given promotion into the main web UI, where this
  ingestion becomes a hub endpoint.

## Promotion into the main web UI

`web-seq/` is a separate Vite project, but it is deliberately not divergent:

- Dependency ranges **and resolved versions** match `/workspace/web`
  (lit 3.3.2, Shoelace 2.20.1, TypeScript 5.3.3, Vite 7.3.2).
- Same `--scion-*` design tokens, same dark-theme defaults.
- Same conventions: `NodeNext`, `experimentalDecorators`, strict mode,
  `exactOptionalPropertyTypes`.
- All logic lives in DOM-free `core/`; components are thin.

Promotion should be a move plus a router entry, not a rewrite. The likely work
is swapping `/api/digest` for the real backend route and adding auth.

## Limitations

**Validated against one real run, not many.** A 49-minute, 8-agent production
export has been through the whole pipeline end to end, and it is what found the
double-counted messages, the recycled-name misattribution and the mispaired tool
spans. But it is still one run, from one project, with one harness. The
synthetic generator remains partly circular: it was written to satisfy the
parser's parent-attribution heuristic, so the clean ancestry in the demos proves
less than it looks like it does. See the honesty section in
[demo/README.md](demo/README.md).

**Slope is measured only where the recipient acknowledged.** With both message
phases present the arrival time is real (744 of 758 edges on the run above).
Without them — an export carrying only `message dispatched`, or a recipient that
died before acknowledging — arrival falls back to the recipient's next observed
activity, marked `inferred`; where nothing can be inferred at all the edge stays
horizontal and dashed. The legend reports the split for whatever is loaded.

**Only `scion-messages` and `scion-agents` are required.** The fs-watcher log is
optional and usually absent; without it the file-graph-derived detail is simply
missing and everything else works. Runs with no `agent.lifecycle` rows fall back
to manifest-derived lifelines.

**Warp payload is exact, not decimated.** One knot per density bucket, merged
only on exactly equal velocity — ~10,800 knots for a 3-hour run, roughly half
the payload. Error-tolerant decimation would shrink it, at the cost of slightly
changing the mapping.

**No dropped-edge stat.** Edges whose endpoints cannot be resolved are logged
but have nowhere to go in the frozen `Stats` struct.

**Not yet built:** URL deep links into a specific moment or selection (the warp
makes this straightforward — it is just `τ` plus a selection id), and any
persistence of collapse/solo state across reloads.

## Deferred ideas

The velocity profile is currently driven by raw event density. The more
interesting version drives it by **relevance to a selected causal chain**: pick
a failure, and the view automatically lingers on what contributed to it and
races through what did not. The warp is already pluggable — only the planner's
input changes.

That is the point where this stops being a nicer timeline and starts being a
debugger.
