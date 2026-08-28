# Tasks #37/#48 round 2 — Fix B stays withdrawn. Test Fix F instead.

Author: sn-impl-arch (architect). Date: 2026-08-28. **Dispatched. Start now.**
Continues `briefs/sn-harnesscfg-dev.md`. **Keep your measurement harness — you will extend it.**

**TOUCH NO CLOUD INSTANCE.** Unchanged from round 1.

## Your withdrawal is accepted, and your correction of me is right

Row 4 moves, so B is dead. I am not asking you to revisit it.

**You were also right about the sentence I built the brief on.** I wrote *"this change is about stamping
identity, not about changing defaults."* You answered: the name is the same, but the rank moves from
rung 7 to rung 3/1, and **the rank change IS the default change.** That is correct and it is the
sharper statement. A default is not a string; it is a string *at a precedence*. I had smuggled the
precedence out of the definition so the change would look inert. **Please keep doing that** — it is the
second time today a brief of mine has been corrected by the agent executing it, and both times the
correction was load-bearing.

## Where the measurement actually points

Your remediation option (1) — *"the hub stamps only ID/hash without the name"* — sent me back to the
broker, and there is a fifth option there that none of A–E covered. All five of my candidates tried to
get the **hub** to supply something. None asked why the **broker** cannot resolve what it already
correctly computes.

Look at `hydrateHarnessConfig` (`pkg/runtimebroker/handlers.go:992-1024`). Two things sit three lines
apart:

```go
// :993 — the guard
if cfg == nil || (cfg.HarnessConfigID == "" && cfg.HarnessConfigHash == "") {
    return "", nil            // -> broker falls back to ON-DISK search -> 502
}

// :998 — the capability
if conn.LocalStorage != nil {
    ref := cfg.HarnessConfigID
    if ref == "" {
        ref = cfg.HarnessConfig          // <-- resolves by NAME
    }
    path, err := s.resolveLocalResource(ctx, storage.ResourceKindHarnessConfig, ref, conn)
```

**The broker can already resolve a harness-config by NAME from the local storage backend.** That code
is written, and on this tier it is unreachable, because the guard above it demands an ID or hash.

The function's own doc says it exists *"to make harness-configs usable from a broker that lacks the
config on its local filesystem"* — which is precisely and exactly our situation.

### But the obvious one-line widening does NOT work, and here is why

Widening the guard to admit `cfg.HarnessConfig != ""` is useless on its own: **the hub sends an empty
name.** The name `antigravity` is invented *later*, by
`resolveHarnessConfigNameForBroker` (`handlers.go:2421-2458`), which **returns a string and never writes
it back into `cfg`.** So at hydration time `cfg.HarnessConfig` is still `""`.

**That gap is the whole defect, stated properly:**

> The broker already knows how to resolve a harness-config *name* through its 7-rung ladder, and already
> knows how to resolve a *ref* against local storage. It never connects the two. Hydration uses the
> **hub's** name — which is empty — instead of the **ladder's** resolved name.

## Fix F — the hypothesis to test

In the harness-config hydration block (`start_context.go:474-494`), when `HarnessConfigID` and
`HarnessConfigHash` are both empty, resolve the name via the broker's own ladder and use **that** as the
hydration ref against local storage. Fall through to the existing on-disk search only if it misses.

**Why this should leave row 4 alone, and why that is the point:** the ladder is untouched. F does not
supply a name, does not change a rank, and does not add a rung. It changes only **where an
already-resolved name is looked up** — store first, then disk. A broker profile at rung 6 still beats
settings at rung 7, because F never touches the code that decides that. No template change, no hub
change, no default change on any tier.

If that reasoning is right, F fixes the diagnosis directly — *"resolution moved from disk to store"* —
at the place the resolution lives, instead of working around it from the hub.

## Do not trust the paragraph above. Measure it.

**I have been wrong twice today by reasoning from a code read** — I asserted hosted mode has no shared
`settings.yaml` (false, I misread a skip), and I called a postgres-only change "one line in the right
keyspace" (your round-1 finding). **Treat Fix F as my third candidate for being wrong.** Three
preconditions carry it, and if any fails, F dies and you say so:

1. **Is `conn.LocalStorage != nil` in single-node hosted mode?** Hub and broker are co-located in one
   process, so I expect yes. **If it is nil, F is dead** — the store path is unreachable and the
   `resolver.Resolve` fallbacks below it both require an ID. Check this FIRST; it is cheapest and it can
   kill the whole thing.
2. **Does `resolveLocalResource(ResourceKindHarnessConfig, "antigravity", conn)` actually find the
   store-seeded resource by name?** Round 1 of the investigation established `antigravity` IS seeded
   into the store on a fresh deploy. Being in the store and being findable *by name through this
   function* are different claims.
3. **Does row 4 stay put?** Re-run your existing row 4 under F. My reasoning says untouched. Reasoning
   is what failed last round.

### One more thing, and it is a rule-19 flag

**The ID/hash guard appears three times** — `handlers.go:509`, `handlers.go:993`, and structurally at
`start_context.go:478`. A condition written three times is usually deliberate. **Find out why before you
widen it.** If there is a reason — a security boundary, a remote-broker assumption, an
untrusted-name concern — that reason outranks F, and I want it quoted rather than worked around. Ask
what breaks if a broker hydrates from a name it resolved itself rather than one the hub authenticated.

## Rows to add

Keep all seven. Add:

| # | Scenario | Predicted under F |
|---|---|---|
| 8 | Hosted/SQLite fresh, no overrides, **F applied** | resolves `antigravity` from the **store**; agent starts |
| 9 | **Row 4 re-run under F** | **unchanged — profile still wins** |
| 10 | Remote (non-co-located) broker, `LocalStorage == nil`, name only | unchanged: on-disk fallback, no new behaviour |
| 11 | Name resolves to something absent from the store (the row-7 residual) | legible failure, not a bare 502 |

**Row 9 is the new withdrawal condition.** If F moves row 4 too, then every available fix moves it, and
the problem is structural — that escalates to ptone rather than to another round with me. Say so plainly
and stop.

Row 11 is Fix D, which stays in scope for whichever fix survives.

## Mutation standard

Unchanged. Mutate every pin, read **why** it went red. Named mutation: **revert F and confirm row 8 goes
red with the actual 502 from the disk lookup**, not some unrelated failure.

## Constraints

Unchanged from round 1. Additive commits on your existing branch
`sn-harnesscfg-dev/blast-radius-measurement`; no rebase, amend, or force-push. Push to `ptone/scion`
only, no upstream PR. `golangci-lint` and `gofmt` clean before you report green. Never print an access
token. Local is `task #37`/`task #48`; GitHub is `owner/repo#NNNN`.

## Report

Rows 8-11 measured; the three preconditions answered individually; **what the triple guard is for**; and
the named mutation with why it went red.

**And tell me what in this brief is wrong.** You have now corrected me twice — once on the rank/default
conflation, once on the postgres gate. Assume there is a third error and go find it. If precondition 1
fails in the first ten minutes, message me immediately rather than completing the rest; that result
alone changes the whole plan.
