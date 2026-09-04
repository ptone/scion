# DEF-142 — Wire conversation references into the send path

Status: **design ready**, not staffed. **Must land together with DEF-141** (`DEF-141-DESIGN.md`).
Defect: `DEFECTS.md` [^82] (CONFIRMED by probe).
Base: `scion/tranche-g` @ `eb59ea98f`.

---

## Problem & Goals

`cmd/message.go:746` resolves every reference form — `conv:<uuid>`, `#<thread>`, `@<agent>`, `@<email>` — by POSTing `/api/v1/conversations/resolve`. **That route does not exist.** Probe evidence in [^82]: unmatched-pattern 404 on both verbs, against a control that returns 200 and a structured 405. The "obvious typo" hypothesis is dead too — `/api/v1/chat/conversations/resolve` 404s for an independent reason (`handleChatConversationRoutes` requires two path segments and admits seven fixed actions). No resolve endpoint exists under any prefix.

**But the resolver does.** `messaging.Resolve` (`pkg/messaging/resolve.go:169`) is complete and careful: it parses, resolve-or-creates, and performs post-resolution authorization with a deliberate not-found/boundary-violation split to prevent existence leakage. It has **zero non-test callers**. What is missing is not logic — it is the wiring.

**Goals.** (G1) All four reference forms work against a real hub. (G2) Exactly one authorization choke point on the send path. (G3) No new HTTP endpoint that discloses conversation existence. (G4) A reference can never silently resolve to one of several candidates.

## Non-Goals

- No change to the reference grammar. `ParseReference`'s four forms stand.
- Not fixing what a *bare* `user:` recipient derives — that is [^69], and remains a DM.
- Not building a general-purpose conversation lookup API. If a UI later needs one, it is a separate design with its own leakage analysis.

## Proposed Design

### 1. Resolve at send, not before it

Delete the two-step. Add `ConversationRef` to the send request DTO and its `pkg/hubclient` mirror:

```go
// ConversationRef is a conversation reference in the §2.6 grammar
// (conv:<uuid>, @<agent>, @<email>, #<thread>). The hub resolves it against
// the AUTHENTICATED sender. Mutually exclusive with ConversationID.
ConversationRef string `json:"conversation_ref,omitempty"`
```

The CLI stops calling `ResolveConversation`, passes `ref.Raw` on the send request, and `ResolveConversation` is removed from `pkg/hubclient` along with the mock at `cmd/message_convref_test.go:60`. **Delete the mock in the same commit that deletes the method** — leaving it is what let this defect live.

Note how small this is: the CLI already sends the raw reference string to the server (`Reference: ref.Raw`). Resolution was always server-side. This moves the string onto a request that already exists instead of one that does not.

Handler order in `handleAgentOutboundMessage`, and the order is load-bearing:

1. Reject if both `ConversationRef` and `ConversationID` are set → `400`. Two ways to name one thing must never both be honoured; silently preferring one is how the next DEF-141 gets written.
2. **Validate the message.** `Resolve` can create a row; a validation failure after creation orphans it. This is DEF-48's rule, and folding into one handler is what makes it enforceable rather than a comment.
3. `messaging.Resolve(ctx, store, req.ConversationRef, rctx)` with `rctx` built **only** from the authenticated caller (G-1). No field of `rctx` may come from request JSON.
4. Feed the resulting id into the **existing DEF-138 authorization block**, unchanged.
5. Set `asserted = true` (DEF-141) and continue down the explicit branch.

### 2. One authorization choke point

`Resolve` performs its own post-resolution auth, and DEF-138's block authorizes an asserted `conversation_id`. Do **not** treat the former as the send gate. A resolved reference produces an id, and that id goes through the identical block every other asserted id passes. `Resolve`'s internal check stays — it protects resolve-or-create — but the invariant that matters is unchanged and unweakened: *no messaging path reaches send without passing the same authorization.* Adding a second, parallel authorization implementation on the send path is precisely what the prohibition list exists to prevent.

Newly-created conversations (`result.Created == true`) are exempt from `Resolve`'s own check because both parties were just added as participants. They are **not** exempt from step 4. An AC covers this.

### 3. Error mapping must not re-widen what the resolver narrowed

`ResolutionError` distinguishes `IsNotFound()` from `IsBoundaryViolation()` **deliberately**: when the sender does not belong to the owning project, the resolver returns *not-found* rather than *forbidden*, so the response cannot be used to probe which conversation ids exist. The HTTP layer must map these mechanically and **must not add detail** — no "conversation exists but you lack access", no project id, no display name. This is AC-33's rule applied to resolution: the refusal may name what was rejected, never what it belongs to.

### 4. Ambiguous thread names must fail closed — P4 is a blocker, not a follow-up

`resolveThread` (`:430-470`) matches on `c.DisplayName == name` and **returns the first hit** it encounters while paginating. `display_name` carries no unique constraint — the only unique index on the table is `(surface, external_ref)` (`pkg/ent/schema/conversation.go:89-90`). So two group conversations in a project may share a name, and which one `#general` resolves to depends on pagination order.

Today that is latent because nothing calls `Resolve`. **This change makes it live**, and the consequence is a message delivered to a different audience than intended — group authorization is project membership, so both candidates pass the auth check while having different participant sets. That is a disclosure, bounded within a project.

**It is not hypothetical, and the reason is uncomfortable: we just built the machine that produces it.** DEF-140 made conversation identity `(surface, external_ref)`, with the accepted consequence that a live thread **forks** into `(native, thread:P:T)` and `(discord, thread:P:T)` ([^80]). Two rows, same project, same kind, and nothing stops them carrying the same display name. The fix we merged manufactures the duplicate that this resolver cannot see.

The remedy is already designed into the types and never produced: `UnresolvedRef` documents `Reason: "ambiguous"` with a `Candidates []string` field, and no code path emits it. `resolveThread` must collect **all** matches across all pages, and:

- exactly one → resolve;
- zero → `not-found` (unchanged);
- two or more → `ResolutionError` with `Reason: "ambiguous"` and `Candidates` populated with disambiguating forms (`conv:<uuid>` per candidate, with surface). Refuse the send.

**Do not resolve ambiguity by picking the newest, the oldest, or the native one.** A rule that silently chooses is the affinity pattern ptone objected to, relocated into the resolver. Explicit routing means an ambiguous address is an error.

## Alternatives Considered

**A. Implement `POST /api/v1/conversations/resolve` and keep the two-step.** Rejected, though it is the smaller diff and matches the code already written. It keeps a round trip whose only product is a conversation id; it creates rows before the message is known to be sendable (the orphan risk DEF-48 worked around rather than removed); and it adds a public endpoint whose entire purpose is to answer "does this reference name a conversation you may see?" — an existence oracle needing its own leakage analysis and rate limiting. Since `Resolve` is directly callable from the send handler, the endpoint buys nothing the fold does not.

**B. Fix the client URL to the chat prefix.** Rejected as impossible, not merely wrong. The probe shows `/api/v1/chat/conversations/resolve` 404s from inside `handleChatConversationRoutes`, which requires two path segments and admits seven fixed actions. There is no handler behind either URL.

**C. Have the CLI derive conversations locally and send `conversation_id`.** Rejected on security grounds. Resolution performs authorization; moving it client-side makes the client the authority for its own access decision, and the resulting id would arrive as an unverifiable assertion. It also fails G-1.

**D. Keep `#thread` first-match and file ambiguity separately.** Rejected. The whole point of this change is to make references usable; shipping a form that can silently address the wrong audience, in the same tranche that created the duplicates, is not a deferral but a regression.

## Migration / Rollout

No schema change and no migration. `conversation_ref` is additive; `conversation_id` is untouched, so DEF-138's asserted-id path keeps working. Removing `ResolveConversation` from `pkg/hubclient` removes a method that cannot succeed against any hub, so nothing that works today stops working. Old CLI against new hub: unchanged failure (it calls the absent route). New CLI against old hub: the send request carries an unknown field, which is ignored, and the reference is silently dropped — **so the mutual-exclusion check in step 1 must live server-side and the CLI must not assume**. No switch, per the single-cutover directive. Fully reversible by revert.

## Open Questions

- **OQ-142-1** — should `#<thread>` resolve-or-**create** when no thread matches, as `@<agent>`/`@<email>` do? Today it returns `not-found`. My read: **keep not-found.** Creating a group conversation as a side effect of a typo is expensive and hard to undo, and the asymmetry is defensible — a DM's participants are fully determined by the reference, a thread's are not. Non-blocking; flagging because the inconsistency will look like an oversight to the next reader.
- **OQ-142-2** — after this lands, should the bare `user:<uuid>` form warn that it will derive a DM? Relates to ptone's position that affinity belongs in agent context, not a memory system. Deferred to skill work.

## Implementation Phases

1. **P1** — `resolveThread` collects all matches and fails closed on ≥2 with `Reason: "ambiguous"` and populated `Candidates`. Tests first; this is the only phase with a disclosure consequence.
2. **P2** — add `ConversationRef` to the send DTO and the `pkg/hubclient` mirror. No behaviour yet.
3. **P3** — handler: mutual-exclusion rejection, validate-before-resolve, `Resolve` call with an authenticated-only `ResolveContext`, resolved id through the existing DEF-138 authorization block, `asserted = true`.
4. **P4** — error mapping with no added detail.
5. **P5** — CLI: drop the two-step, pass the reference on the send request. Delete `ResolveConversation` from `pkg/hubclient` **and** the mock, same commit.
6. **P6** — a test that exercises the CLI reference path against a **real `pkg/hub` mux**, not a mock. See AC-7.

## Acceptance Criteria

- **AC-1** — Each of `conv:<uuid>`, `@<agent>`, `@<email>`, `#<thread>` sends successfully against a real hub and the message persists in the referenced conversation. Four cases, asserted individually.
- **AC-2** — `conversation_ref` + `conversation_id` together → `400`, no message sent, no conversation created.
- **AC-3** — A `conv:<uuid>` naming a direct conversation the sender is not party to is refused, and the response body is byte-identical to the response for a non-existent id. Assert equality of the two responses; a test that merely checks "both fail" does not test the leakage property.
- **AC-4** — Two group conversations in one project sharing a display name → `#<name>` is **refused** with `ambiguous` and both candidates listed. Build the fixture via the DEF-140 fork path (`native` and `discord` rows for the same thread), since that is how it will actually occur.
- **AC-5** — A message that fails validation creates **no** conversation row. Assert the row count is unchanged across the failed request.
- **AC-6** — A reference that resolves to a newly created conversation still passes the DEF-138 authorization block. Verify by mutation: remove the block from the ref path and confirm a test fails.
- **AC-7 (structural)** — a test drives the CLI reference path against a real `pkg/hub` mux. Its value is not the assertion but the wiring: it must **fail if the route/handler is absent**. Verify by mutation — remove the resolve call from the handler and confirm this test goes red. Without that mutation the test is another mock.
- **AC-8** — `grep` shows zero occurrences of `conversations/resolve` in the tree after P5.
- **AC-9 (mutation, mandatory)** — semantic failure with the build green, clean restore ([^76]): revert `resolveThread` to first-match → AC-4 fails; drop the mutual-exclusion check → AC-2 fails; source any `ResolveContext` field from request JSON → a test asserting sender identity fails.
- **AC-10** — full gate reported, per-file numstat re-run before push. Known-environmental failures are container-scoped ([^1061]) — report yours, do not inherit mine.
