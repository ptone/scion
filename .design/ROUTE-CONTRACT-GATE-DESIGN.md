# Route-contract gate — catching client paths the server does not serve

Status: **design ready**, not staffed. Approved for design by ptone (2026-09-04).
Defect class: `DEFECTS.md` [^85] (DEF-145). Instance that provoked it: [^82] (DEF-142).
Base: `scion/tranche-g` @ `eb59ea98f`.

---

## Problem & Goals

Seven client call sites target HTTP paths `pkg/hub` does not serve. One of them — `POST /api/v1/agent/secrets`, called at container init and its error swallowed — means agent secret overrides have **silently never resolved**. Another, `scion reset-auth`, is a CLI command that has never once worked.

The audit that found them replayed all 165 `s.mux.HandleFunc` patterns through a real `http.ServeMux`, then hand-checked each prefix match against its sub-router's accepted action set. That procedure is mechanical, which is the evidence this gate is buildable: **it has already been executed once by hand, and it found all seven.**

The root cause is not that the seven authors were careless. It is that **no test in this repository exercises a client path against a real server mux.** Three of the seven are mock-backed and have *passing tests asserting the exact missing path*. A mock cannot disagree with the server about what exists, because it never meets it.

**Goals.** (G1) A client path literal that no server pattern serves fails CI. (G2) The gate covers prefix-matched routes by checking the sub-router's action set, not merely the prefix. (G3) **Paths the gate cannot statically resolve are reported, never silently skipped.** (G4) The gate is cheap enough to run on every PR.

## Non-Goals

- Not verifying request/response *schemas*. This gate answers "does this path reach a handler?" and nothing about what the handler expects. Schema drift is a separate, larger problem.
- Not covering non-Go clients in the first cut. The web UI is in scope for reporting (G3) but not for enforcement — see Phasing.
- Not fixing the seven defects. Those are routed to the coordinator; this gate exists so the eighth does not happen.
- Not a runtime concern. This is a build-time gate.

## Proposed Design

### 1. The server's routes become data

Today the route table exists only as 165 imperative `s.mux.HandleFunc` calls, which means the only way to know the route set is to run the registration function. That is fine — *use* it rather than parse it:

```go
// pkg/hub/routes_export.go
// RegisteredPatterns returns every pattern this server registers.
// Populated by the same registration pass the server uses, so it cannot
// drift from reality — there is no second list to keep in sync.
func RegisteredPatterns() []string
```

Implemented by having `registerRoutes` write to a recording mux in test/tooling mode. **The critical property is that there is exactly one route table.** A hand-maintained mirror would be a second source of truth and would rot; this is the same failure mode as the mocks, one level up.

### 2. Prefix routes declare their action sets

Go's `ServeMux` prefix semantics are why four of the seven defects are invisible to naive matching: `/api/v1/broker/` matches `/api/v1/broker/callback`, so a path-level check says "served" while the sub-router 404s. Prefix handlers must therefore publish what they accept:

```go
// A prefix handler that dispatches on a path segment or an action field
// declares its accepted set. The gate consults this; the handler's switch
// and this list are checked against each other by a unit test.
var brokerActions = ActionSet{"inbound", "projects"}
```

This is the one place the design tolerates a second list, because there is no way to extract a `switch`'s cases without parsing. The mitigation is local and enforceable: a test in the same package asserts the declared set equals the switch's cases. A drift there is a compile-adjacent failure in one file, not a silent 404 in a client.

### 3. Client paths are enumerated at the shared constant, not scraped

The audit's harder half was enumeration, not matching. 120 of 261 web paths carry a `${…}` segment; ~14 vary in the *first* segment and are statically unresolvable. Regex-scraping call sites would therefore produce a gate whose coverage is unknown and whose gaps are invisible — the exact property that makes the current mocks dangerous.

So: **Go clients reference route constants, and the gate reads the constants.**

```go
// pkg/api/routes/routes.go — imported by BOTH pkg/hub and every Go client.
const (
    AgentOutboundMessage = "/api/v1/agents/{id}/outbound-message"
    BrokerInbound        = "/api/v1/broker/inbound"
)
```

A client that uses a constant is verified by construction. A client that hand-builds a path string is caught by (4).

### 4. Un-enumerable paths must be loud — this is the load-bearing rule

A static gate over a dynamic language always has a tail it cannot resolve. The failure mode to design against is **not** missing that tail; it is missing it *quietly* and thereby reporting a coverage it does not have.

So the gate emits three buckets, and the third is not optional:

| Bucket | Meaning | Gate behaviour |
|---|---|---|
| `served` | resolves to a pattern, and for prefixes to an accepted action | pass |
| `unserved` | resolves to no pattern, or to a prefix that rejects it | **fail** |
| `unresolvable` | a path literal the extractor could not fully resolve | **fail unless listed in an explicit, reviewed exceptions file** |

`unresolvable` must be a **separate bucket from `unserved`** — the same rule the Tranche G evidence report follows, and for the same reason: collapsing "I checked and it's broken" into "I couldn't check" destroys the only signal that tells you how much you actually verified. The exceptions file carries one line per entry with a reason, and it is reviewed like code. A growing exceptions file is a visible fact; a growing blind spot is not.

### 5. What the gate would have caught

All seven. Concretely: `reset-auth` and `set_message_mode` fail on the action set (§2); `agent/secrets`, the Slack routes, `broker/callback`, `auth/ws-ticket` and hub-scoped `SubmitEnv` fail on pattern match (§1). This is not a prediction — the manual audit performed exactly this procedure and produced exactly this result.

## Alternatives Considered

**A. Regex-scrape client path literals; no shared constants.** Rejected as the primary mechanism, though it is much the cheapest and is what the manual audit did. It cannot resolve the dynamic tail, and — decisively — it cannot *tell you* it failed to resolve it. It would ship a gate with an unknown false-negative rate and a green light, which is the DEF-145 pathology reproduced in the tool built to prevent DEF-145. Retained as a *transitional* mechanism in P1 with mandatory `unresolvable` reporting.

**B. Integration test per client path against a real mux.** Rejected as the general mechanism, kept for high-value paths. It does not generalise (N tests for N paths, each needing a fixture), it verifies only paths someone remembered to write a test for, and "someone remembered" is precisely the property that failed seven times. As a targeted device it is still right: DEF-142's AC-7 requires exactly one of these for the CLI reference path, because there the wiring *is* the thing under test.

**C. Runtime 404 telemetry — log unmatched requests, alert on them.** Rejected as a gate, **recommended as a complement**. It is the only mechanism that covers dynamically-constructed and non-Go paths, and it would have caught `agent/secrets` on the first container start. But it is reactive: it reports after shipping, it needs traffic on the path, and `reset-auth` proves traffic is not guaranteed. Its real value is covering the `unresolvable` bucket that §4 makes visible — the two mechanisms are complementary precisely because their blind spots differ. Worth filing separately.

**D. Generate clients from an OpenAPI spec.** Rejected for now on migration cost, and honestly the "right" long-run answer. It subsumes this gate and the schema problem too. It is a multi-quarter change across Go and TypeScript; proposing it here would trade a shippable gate for a plan. Recorded so the next person knows it was considered, not overlooked.

## Migration / Rollout

Additive and reversible throughout. No schema change, no runtime behaviour change, no switch.

The sequencing constraint that matters: **the gate must be introduced non-blocking, with its full report visible, before it is made blocking.** Turning on a failing gate and fixing the fallout in the same change makes the fallout invisible — nobody can tell which failures were pre-existing. Land it reporting-only, publish the three buckets, let the coordinator's DEF-145 fixes land against it, then flip to blocking when `unserved` is empty. The flip is the only irreversible-feeling step and it is one line.

Route constants are adopted incrementally: a client not yet migrated reports as `unresolvable`, which is visible and non-blocking during P1–P3 and blocking after. That gives migration pressure without a big-bang refactor.

## Open Questions

- **OQ-RCG-1** — should the web UI be enforced or only reported? My read: **reported in this design, enforced later.** TypeScript path extraction is a separate toolchain, and 120 of 261 paths are templated. Reporting them puts the number in front of us, which is what decides the next step. Non-blocking.
- **OQ-RCG-2** — do the `extras/` brokers count as first-party for enforcement? Two of the seven live there. My read: **yes, enforce** — they ship in the same repo and the Slack flow is entirely dead. Flagging because it widens the migration surface.
- **OQ-RCG-3** — who owns the gate once landed? It is infrastructure, not messaging. My read: same owner as the existing reachability gate ([^56], PR #1410), for consistency. Ptone's call.

## Implementation Phases

1. **P1** — `RegisteredPatterns()` via a recording mux, plus a test asserting the count matches the live registration pass. No client side yet.
2. **P2** — `ActionSet` declarations for every prefix handler, each with a same-package test asserting the declared set equals the switch's cases.
3. **P3** — the matcher: replay a list of paths through a real `ServeMux` loaded with `RegisteredPatterns()`, then action-check prefix landings. Unit-tested against the seven known defects as fixtures — **the gate must go red on all seven before it is trusted.**
4. **P4** — `pkg/api/routes` constants; migrate `pkg/hubclient` and `pkg/sciontool/hub/client.go`. Report-only.
5. **P5** — CI wiring, report-only, three buckets published.
6. **P6** — flip to blocking once `unserved` is empty. Separate commit, separate review.

## Acceptance Criteria

- **AC-1** — Fed the seven DEF-145 paths, the gate reports all seven as `unserved`. Asserted individually as fixtures, not as a count.
- **AC-2** — Fed the full current client path set, the gate reports zero **false positives**. A gate that cries wolf gets disabled.
- **AC-3** — `RegisteredPatterns()` cannot drift: a test registers routes twice, once through the server and once through the recorder, and asserts set equality. Verify by mutation — add a route to the server only and confirm the test fails.
- **AC-4** — Every prefix `ActionSet` matches its handler's switch cases. Verify by mutation — add a case to one switch without updating its set and confirm that package's test fails.
- **AC-5 (the G3 property)** — a path the extractor cannot resolve appears in `unresolvable` and **never** in `served`. Verify by mutation: feed a deliberately dynamic path and assert it lands in `unresolvable` and that the gate's exit status reflects it. A gate that silently drops what it cannot parse is the defect it was built to prevent.
- **AC-6** — the exceptions file is empty at P6, or every entry carries a written reason. Enforced by the gate's own parser rejecting a reasonless entry.
- **AC-7** — gate runtime under 10s on the full tree, so it can run on every PR (G4).
