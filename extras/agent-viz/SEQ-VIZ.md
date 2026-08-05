# seq-viz — sliding-window sequence visualizer

An experimental way to look at a multi-agent run: a **sequence diagram whose
vertical axis is metric wall-clock time**, viewed through a sliding window that
speeds up and slows down so that the *interesting* parts play at a readable
speed.

```
make seq-run          # build everything, serve a synthetic 60-agent run on :8090
```

Then open <http://localhost:8090>.

## Why not a trace, and why not a sequence diagram

A **flame graph / trace** lays time out left-to-right and stacks call depth
vertically. It is excellent for one request through many services, but it
destroys *actor identity*: if agent `planner` participates five times, it
appears as five unrelated boxes in five places.

A **UML sequence diagram** keeps one lifeline per actor, so an actor is a single
stable column no matter how often it participates. But it is conventionally
*untimed* — vertical position means "after", not "how long after" — and it is
static.

seq-viz takes the sequence diagram's actor-per-column axis and makes the
vertical axis **metric**. That single change is what makes the two views
compose:

- An activation box and a span become **the same object**. Nested activations
  (session > turn > tool) render as a flame graph running *down* each lifeline,
  so you get trace-style nesting without giving up actor identity.
- Message arrows become **sloped**, and the slope *is* the delivery latency.
  This is the Lamport space-time diagram, not UML.

## The two things worth understanding

### 1. Time is warped temporally, never spatially

A run is mostly idle. The obvious fix is to squeeze idle gaps in the layout —
but then a bar's height no longer means its duration, and every visual
comparison silently lies.

Instead `msPerPx` is **constant everywhere on the canvas**, and we vary how fast
the playhead *travels*. Boring stretches are crossed at high velocity; bursts
slow to 1x. Geometry stays honest; only the clock is elastic.

The mapping is a precomputed monotonic function `T_viewer → T_wall`
(`Warp`, a list of piecewise-linear knots). The scrubber, the minimap, the
timestamp readout and deep links are all *projections of this one function*, so
they cannot disagree with each other, and playback is deterministic.

The invariant the planner targets is **constant events per second of viewer
attention**, not constant wall-time per second: `v = R / density(t)`.

### 2. Acceleration is planned, not reactive

Snapping to a new velocity when a burst arrives is jarring and, worse, arrives
*late* — you are already inside the burst before slowing down.

The velocity profile is planned ahead like a CNC feed rate. The constraint
`|dv/dτ| ≤ A` is awkward because `dτ = dt/v`, but under the substitution
`u = v²/2` it collapses to simply `|du/dt| ≤ A`. A forward pass limits
acceleration and a **backward pass limits deceleration**, which is what
guarantees the view has already slowed down by the time a burst enters frame.

## The three zones

```
   ┌──────────────┐
   │     WAKE     │  above frame: already seen, fading out
   ├──────────────┤  ← frameTop
   │              │
   │    FRAME     │  readable, honest wall-clock
   │              │
   ├──────────────┤  ← frameBottom == playhead
   │   STAGING    │  below frame: the future, rushing up.
   └──────────────┘    at speed, bars cross-fade into motion streaks
```

Streaks are a deliberate signal: when the staging zone is moving too fast to
read, it *looks* too fast to read, rather than strobing as if it were data.

## Columns

- The column axis is a **tree**, keyed on each agent's persisted `Ancestry`. A
  collapsed parent absorbs its whole subtree into one composite column
  (Perfetto-style track groups), which is what makes solo/pin fall out for free.
- **Slot recycling**: columns are reused by non-overlapping lifelines via greedy
  interval-graph colouring, so 100 agents with 12 concurrent need ~12 columns,
  not 100.
- Idle subtrees auto-fold to a narrow stripe. This is throttled and hysteretic
  (see `LAYOUT_REFRESH_MS` in `seq-viz.ts`) — recomputing it every frame would
  make the axis shimmer and destroy object constancy.

Note the symmetry: **time** compresses *temporally* (velocity), **columns**
compress *spatially* (collapse/fold). Spatial compression is safe here only
because a column's horizontal position carries no metric meaning.

## Honesty features

These exist because agent telemetry is genuinely incomplete, and a view that
hides that is worse than useless for diagnosis.

| Confidence | Meaning | Rendered |
|---|---|---|
| `measured` | both endpoints observed | solid |
| `inferred` | an endpoint reconstructed from neighbouring events | hatched |
| `open` | started, never ended (still running at window edge) | faded, unterminated |

A duration is **never fabricated**. In hook-per-process deployments many tool
spans would otherwise collapse to zero duration; they are marked, not invented.

**Edge stubs**: when a message's peer is offscreen, hidden or more than 8
columns away, the arrow is replaced by a labelled stub rather than dropped. A
cropped window that silently omits edges lies about connectivity.

### Known limitation: sloped arrows are only half-real

The Cloud Logging message stream carries a single timestamp per message, so true
send→receive latency is not available. The digest infers arrival as the
recipient's next observed activity and marks it `inferred`; where nothing can be
inferred the edge stays horizontal and dashed. Sourcing the digest from the
`messages` table instead (which has both `created` and `dispatched_at`) would
upgrade these to genuinely `measured`.

## Live vs replay

There is no separate live mode. "Live" is just the playhead pinned to
`now - 5min` at 1x. The lag budget is a resource the express lane spends to
catch up, which dissolves the usual live/replay dual-mode complexity into a
single scrubbable timeline.

## Layout

```
cmd/seq-viz/               entrypoint
internal/digest/           digest builder + velocity planner (the contract lives in types.go)
internal/seqserver/        static server + /api/digest
web-seq/
  src/seq/core/            DOM-free: warp, clock, columns, frame, render
  src/seq/components/      Lit components (canvas host, transport, minimap, tree, detail, legend)
```

`src/seq/core/` deliberately has **no DOM dependency** — it is plain functions
over plain data, unit-tested without a browser, which is also what makes it
portable into the main web UI later.

`internal/digest/types.go` and `web-seq/src/seq/core/types.ts` are a **frozen
mirrored contract** (`SchemaVersion = 1`). Change them together.
`web-seq/src/seq/core/integration.test.ts` runs a real Go-generated digest
through the TypeScript read path, which is the only place drift between the two
is actually caught.

## Development

```bash
make seq-test                       # typecheck + vitest + go test
make seq-dev                        # frontend on :3100, proxies /api to :8090
go run ./cmd/seq-viz --dev          # backend, serving web-seq from disk

go run ./cmd/seq-viz --log-file run.json      # a real GCP log export
go run ./cmd/seq-viz --synth-agents 80        # synthetic, no logs needed
```

Tuning flags: `--frame`, `--target-rate`, `--max-velocity`, `--max-accel`.

Regenerate the integration fixture after any contract change:

```bash
go run ./cmd/seq-viz --synth-agents 14 --synth-duration 6m --port 8098 &
curl -s localhost:8098/api/digest -o web-seq/src/seq/core/__fixtures__/synthetic-digest.json
```

## Promotion into the main web UI

`web-seq/` is a separate Vite project, but its dependency ranges and resolved
versions are matched to `/workspace/web` (lit 3.3.2, Shoelace 2.20.1,
TypeScript 5.3.3, Vite 7.3.2), it uses the same `--scion-*` design tokens, and
it follows the same conventions (`NodeNext` resolution, so relative imports
carry `.js` extensions). Components should move over without a rewrite.

## Deferred

The velocity profile is currently driven by raw event density. The more
interesting version drives it by **relevance to a selected causal chain** — pick
a failure, and the view automatically lingers on what contributed to it and
races through what did not. The warp is already pluggable; only the planner
input would change.
