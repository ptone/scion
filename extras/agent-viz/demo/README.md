# seq-viz demo data

Sample runs for the sequence visualizer. Everything here is **synthetic** — see
[Honesty about this data](#honesty-about-this-data) at the bottom, which is the
most important section on this page.

## Quick start

```bash
cd extras/agent-viz
make seq                                            # build binary + frontend
./seq-viz --digest-file demo/sample/run.digest.json # open http://localhost:8090
```

Or skip the demo files entirely — the generator is built in:

```bash
make seq-run                     # synthesizes 60 agents / 45 min on the fly
```

## What's here

```
demo/
  sample/          committed
    run.log.json      raw Cloud Logging export, 310 entries
    run.digest.json   digest built from that log
  generated/       gitignored, produced by regenerate.sh
    default.digest.json
    wide.digest.json
    stress.digest.json
    idle.digest.json
```

```bash
./demo/regenerate.sh          # all scenarios (~5s)
./demo/regenerate.sh sample   # just the committed sample
```

### Why most of it isn't committed

The synthesizer is seeded and the digest builder is deterministic, so
`regenerate.sh` reproduces byte-identical files anywhere. Regenerating costs a
few seconds; a 2.5 MB JSON blob costs the repo forever. So only `sample/` is
committed — it is small, readable, and does double duty as the frontend test
fixture.

`sample/run.digest.json` is built **from `run.log.json` via the real parser**,
not straight from the synthesizer, so the committed digest is provably the
output of the actual parse path rather than a shortcut around it.

## Scenarios

Each one pressures a different axis of the design.

| Scenario | Shape | What it's for |
|---|---|---|
| `sample` | 14 agents / 6 min, 11 concurrent | Small enough to read end to end. Doubles as the test fixture. |
| `default` | 60 agents / 45 min, 12 concurrent | The headline case, and the shape the design targets: a handful of agents of interest on screen, drawn from a much larger population. 11× compression. |
| `wide` | 90 agents / 30 min, **~35 concurrent** | **Column pressure.** More columns than fit on screen — the only condition under which collapse, auto-fold and solo actually matter. |
| `stress` | 120 agents / 3 h | **Volume.** ~3,200 intervals and ~10,800 warp knots. Checks culling, the binary-search frame build, and that the payload stays sane (2.5 MB). |
| `idle` | 20 agents / 2 h, 5 concurrent | **Time pressure.** Mostly empty; 27× compression, velocity up to 107×. The express lane has to cross large dead stretches without the view feeling broken. |

`wide` and `idle` exist because the defaults hide the two failure modes most
likely to bite: too many columns, and too much nothing. If you change the
layout or the velocity planner, check those two before the pretty one.

## Things worth doing in the UI

Load `wide`, since it has the most going on:

```bash
./seq-viz --digest-file demo/generated/wide.digest.json
```

- **Space** plays. Watch the staging zone below the frame: it accelerates
  through gaps and has already slowed by the time a burst reaches the frame.
  That is the backward pass in the velocity planner, not a reaction.
- At speed, bars in the staging zone cross-fade into **motion streaks**. That is
  deliberate: "too fast to read" should look it rather than strobe like data.
- **Collapse a parent** in the left tree — its whole subtree folds into one
  composite column. **Solo** narrows to one subtree.
- Turn **auto-fold** off in the header and watch idle columns stop collapsing to
  stripes. Leave it on and note the axis is deliberately *slow* to change:
  recomputing per frame would make columns shimmer.
- **Click any bar** for details, including a Cloud Logging deep link (it carries
  project and a ±5 min window, so it lands on the right rows).
- **`+` / `-`** zoom the time axis. Bar heights scale exactly with duration at
  every zoom level — that invariant is asserted in the tests.
- Check the **minimap** on the right. It stays strictly linear in wall time
  while the main view's speed varies, which is what makes the compression
  legible rather than deceptive.

Look for hatched and faded bars: those are `inferred` and `open` confidence,
where an endpoint was reconstructed or never observed.

## Regenerating after a contract change

`internal/digest/types.go` and `web-seq/src/seq/core/types.ts` are a mirrored
frozen contract carrying a `SchemaVersion`. If you bump it:

```bash
./demo/regenerate.sh
```

`--digest-file` refuses to load a digest whose version doesn't match the
binary, rather than rendering a subtly wrong view. The frontend does the same
check on `/api/digest`.

## Honesty about this data

**No real agent run has ever been through this tool.** Everything here comes
from `internal/digest/synth.go`. That matters in four specific ways:

1. **It is partly circular.** The generator emits spawns shaped like the
   shell-call pattern the parser's parent-attribution heuristic looks for. The
   clean 4-layer ancestry in these demos partly reflects that it was fed data
   built to be understood. Real logs may produce a flatter or wrong tree — and
   the column tree is load-bearing for the whole design.
2. **The confidence mix is probably too optimistic.** `default` is 93%
   `measured`. Real hook-per-process telemetry leaves far more spans with one
   endpoint, and a mostly-hatched view reads very differently.
3. **Velocity defaults are fitted to invented density.** `--target-rate 6`,
   `--max-velocity 120` and `--max-accel 0.02` are tuned against burst/idle
   structure that was made up. Real runs are likely burstier.
4. **The event vocabulary may be incomplete.** Unhandled event types or payload
   keys in real exports would yield a much sparser view.

So this data demonstrates the *mechanism* honestly — the warp, slot recycling,
nesting and geometry provably work — but it supports no claim about how the
tool reads on a real run. Point `--log-file` at a genuine Cloud Logging export
to find out.
