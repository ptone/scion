# Switch Collapse Design

**Project:** ca-msg-arch
**Status:** DRAFT — awaiting gteam acceptance completion before dispatch
**Measured at:** `scion/tranche-g` = `0cff20a2b1ad5e46d4ce3e1a4cc1b7140221e651`
**Author:** ca-msg-arch (architect)

Origin directive (ptone, verbatim):

> "so for discord this is yet another switch and i've asked us to consolidate
> switches. please lets get gteam in as close to an end state as possible.
> all switches flipped on. then before final merge we want to collapse all
> switches."

and the binding upgrade directive:

> "we want to deliver this a SINGLE cut-over upgrade... when a hub is updated
> to a version that includes this completed refactor - migrations are auto-run,
> and all switches cut-over by default"

---

## 0. Correction to the premise (read this first)

The directive above says "for discord this is yet another switch." **It is not.
There is no Discord-side conversation or envelope switch.** Measured at
`0cff20a2b`:

| Thing that looks like a Discord switch | What it actually is |
|---|---|
| `mention_routing` (`extras/scion-discord/internal/discord/broker.go:69,278,294`) | Pre-existing broker feature flag for @-mention routing. Defaults true. Present identically in the **Teams** broker (`extras/scion-teams/internal/teams/broker.go:49,114,140`). Predates this refactor and is unrelated to it. |
| `conversation_context` table (`extras/scion-discord/internal/discord/store.go:108,218`) | A plugin-local **outbound routing cache** keyed `(discord_user_id, project_id, agent_slug) → last_channel_id`. It is a memo for "where do I post the reply", not a conversation identity. It shares a word with `conversations`, nothing else. |

`grep` for `conversation_envelope_switch`, `conversation_read_switch`,
`conversation_write_deny_switch` across `extras/` returns **nothing**.

So the collapse does not have to reach into the broker plugins at all. This
narrows the blast radius considerably, and it removes the one part of the job
that would have required a coordinated plugin+hub release.

**Assumption being surfaced (Rule 1052):** I am reading "yet another switch"
as a concern about switch *proliferation* generally, not as a claim that a
specific Discord switch exists. If ptone was pointing at something concrete I
have not found, this section is wrong and the design needs revisiting.

---

## 1. Problem & Goals

### Problem

Phase 9a already did one round of consolidation: `conversation_read_switch` and
`conversation_write_deny_switch` were merged into a single
`conversation_envelope_switch` that defaults ON. What remains is one live
switch, two stale keys, and an entire settings section + admin API endpoint
that exist solely to carry them.

Shipping that as the end state means every future operator sees a documented,
supported knob for "turn the conversation model off" — a configuration we do
not intend to support, will not test, and which after migration returns
pre-refactor behaviour over post-migration data.

### Goals

- **G1** — The conversation envelope/read/write model is **unconditional**. No
  runtime setting can disable it.
- **G2** — `MessagingSettings`, the `messaging` opsettings section, and
  `/api/v1/admin/messaging` are removed, because the switch was their only
  content.
- **G3** — No existing instance's settings document becomes unreadable as a
  result. An upgrade must not turn a live `hub_settings` row into a parse
  failure.
- **G4** — The collapse is a **code-only** change. No data migration, no row
  deletion, nothing irreversible at the storage layer.

### Success criteria

An operator upgrading to the collapsed build gets full conversation behaviour
with no configuration action, and an operator who had explicitly written
`conversation_envelope_switch: false` gets full conversation behaviour anyway
— with a log line telling them their setting is now ignored.

---

## 2. Non-Goals

- **Not** removing the divergence / bypass instrumentation
  (`pkg/messaging/divergence.go`, `SwitchBypassMetrics`, `DMAbsentMetrics`).
  See §5 — this is deliberately deferred, not forgotten.
- **Not** deleting the `messaging` row from `hub_settings` on any instance.
  Row deletion is irreversible and therefore ptone's call, not mine. Dead data
  is cheap.
- **Not** touching `mention_routing` or any broker plugin config. Per §0 these
  are unrelated.
- **Not** DEF-133 (the REST DTO vocabulary). Separate decision, separate batch.
- **Not** the fresh-cutover test itself — this design only fixes its
  *ordering* relative to the collapse (§6).

---

## 3. The current surface, measured

### 3.1 The one live switch

`conversation_envelope_switch`, read through
`OperationalSettings.ConversationEnvelopeSwitch()`
(`pkg/hub/operational_settings.go:1225`). Semantics today:

| Document state | Result |
|---|---|
| `messaging` section absent from DB | **ON** (compiled default) |
| section present, key omitted | **ON** (compiled default) |
| section present, key explicit | that value |
| document malformed / type-incompatible | **OFF** (fail-closed, DEF-92) |

### 3.2 Call sites (11 non-test)

**Behavioural (7)** — these are the collapse targets:

| Site | Guards |
|---|---|
| `handlers_messages.go:76` | agent-filtered read resolves DM conversation into the filter; 409/500 on failure |
| `handlers_messages.go:295` | agent-message read resolves DM or thread conversation; 409 family on failure |
| `handlers_chat_v2.go:1837` | counts `SwitchBypassMetrics.IncWcsNil()` — **instrumentation only** |
| `handlers_chat_v2.go:1872` | chat history queries by `ConversationID` instead of channel+thread; strict DM-key parse |
| `server.go:2164` | `writeDenyEnabled()` predicate |
| `server.go:2500` | closure injected into `NotificationDispatcher.writeDenyEnabled` |
| `server.go:2564` | closure injected into `MessageBrokerProxy.writeDenyEnabled` |

**Administrative (4)** — all in `admin_messaging.go` (`:63, :111, :119, :161`).
These vanish with the endpoint.

### 3.3 The stale keys

`MessagingSettings` (`pkg/config/opsettings/sections.go:137-143`) still carries
`ConversationReadSwitch` and `ConversationWriteDenySwitch` for deserialisation
of old rows. The registry schema (`registry.go:323-329`) deliberately omits
them so `additionalProperties: false` rejects them on write; they self-clean on
first PUT.

---

## 4. Proposed design

### 4.1 The trap: `ops != nil` is fused to the switch check

Every behavioural site reads:

```go
if ops := s.GetOperationalSettings(); ops != nil && ops.ConversationEnvelopeSwitch() {
```

Two conditions, not one. `ops == nil` means operational-settings init failed,
and today that forces **OFF** at every site — pre-refactor fallback.

The naive collapse ("the switch is always ON, delete the check") also deletes
the `ops != nil` arm, which silently changes behaviour on the init-failure
path from "pre-refactor fallback" to "full enforcement." Nobody asked for that
and it would land unremarked.

**Ruling:** deleting the whole condition is nevertheless *correct here*, but
only because of a fact that must be verified rather than assumed — **none of
the seven guarded blocks reads operational settings for any other purpose.**
They use `s.store`, `s.messageLog`, `s.webChatStore`. Once the switch is gone,
the conversation model has no dependency on opsettings at all, so `ops == nil`
is simply irrelevant to it rather than a reason to fall back.

This is an **acceptance criterion, not a footnote** (AC-3). A developer must
demonstrate it site by site, not assert it.

### 4.2 `writeDenyEnabled` — delete the predicate, do not rename it

Three of the seven sites are the `writeDenyEnabled` predicate and the two
closures injected into `NotificationDispatcher` and `MessageBrokerProxy`.

**DEF-131** currently asks to rename `writeDenyEnabled` →
`conversationEnvelopeEnabled` and add a test. **DEF-131 is superseded by this
design and should be closed, not worked.** You do not rename a symbol you are
about to delete, and doing both costs two dispatches and a merge conflict.

The collapse removes the `writeDenyEnabled` field from both consumers and makes
their guarded paths unconditional.

⚠️ Both injection sites sit inside `StartNotificationDispatcher` /
`StartMessageBroker`, which **already hold `s.mu.Lock()`**. The standing rule
applies: **no RLock in these functions.** Removing a closure that itself calls
`GetOperationalSettings()` actually *reduces* the risk here, but the developer
must not "tidy" by adding a lock.

### 4.3 Settings-layer removal, and why it is safe

Remove, in order:

1. `ConversationEnvelopeSwitch()` getter (`operational_settings.go:1216-1247`).
2. `MessagingSettings` struct (`sections.go:126-144`) and its section
   registration.
3. The `messaging` entry in the registry schema (`registry.go:323-329`).
4. `pkg/hub/admin_messaging.go` in full, plus its route registration.

**Why G3 holds.** Go's `encoding/json` ignores unknown fields by default, and
the ingest path (`operational_settings.go:239`) only type-checks a section when
`opsettings.SectionByName(row.Section)` returns non-nil. Once `messaging` is
unregistered, `SectionByName` returns nil, the type-check is skipped, and the
row is cached as non-malformed. **An existing `messaging` row survives the
upgrade inert.** No migration required. This is the single most important
property of the design and it is why G4 (code-only) is achievable.

`DisallowUnknownFields` is not used anywhere in `pkg/config` or `pkg/hub`
(verified by grep), so there is no strict-decode path to trip over.

**Why the schema entry cannot simply be emptied.** The `messaging` schema is
`{properties: {conversation_envelope_switch}, additionalProperties: false}`.
Deleting only the property leaves an object that permits *nothing* — every PUT
to the section would fail. The section must be removed entirely, not hollowed.

### 4.4 Telling the operator their setting is now ignored

An operator who explicitly set `conversation_envelope_switch: false` will, on
upgrade, silently get the opposite of what their config says. Silence here is
the wrong default: their config file becomes a lie and nothing tells them.

**Proposal:** a one-shot startup check, independent of the settings registry —
read the raw `messaging` row once at boot, and if it parses and contains
`conversation_envelope_switch: false`, emit a single `WARN`:

```
messaging.conversation_envelope_switch is set to false but is no longer
honoured; the conversation model is now unconditional. This setting can be
removed.
```

Deliberately a WARN and not a startup failure. Refusing to boot because of an
obsolete key would turn a cosmetic problem into an outage, and the safe
behaviour (enforcement ON) is what we are doing regardless.

This is the one piece of the design that adds code rather than removing it. It
is also the piece I would drop first if the developer finds it awkward — it is
a courtesy, not a correctness requirement. Flagged as such in the phases.

### 4.5 What the metric names now mean

`SwitchBypassMetrics` / `SwitchBypassCounter`
(`pkg/messaging/divergence.go:87`) were built to measure the switch's coverage
during rollout. With no switch, "bypass" names nothing. The counters still
measure something real (traffic taking a non-conversation path), but under a
name that will mislead every future reader.

Renaming is **out of scope here** and filed as a follow-on. Reason: see §5.

---

## 5. The instrumentation ordering decision

There is a genuine tension. The bypass/divergence counters exist to answer
"is the conversation model covering all traffic?" That question is *most*
valuable during the fresh-cutover test — which happens **after** the collapse
(§6). Deleting the instrumentation in the same change that removes the switch
would blind exactly the test the collapse is meant to enable.

**Ruling:** collapse the switch; **keep every counter**. Rename and prune the
instrumentation only after the fresh-cutover test passes. Cost is a badly-named
metric for one testing cycle; benefit is not flying the cutover blind. That
trade is obviously worth taking.

---

## 6. Sequencing: collapse BEFORE the fresh cutover

This is the load-bearing sequencing decision and it changes the currently
assumed order.

Standing plan has been: finish gteam acceptance → fresh cutover on a second
instance → collapse switches before final merge. ptone:
*"fresh cutover only happens after all other acceptance testing on gteam is
done"*.

That leaves the collapse **after** the fresh cutover, which is wrong, because:

- ptone's stated purpose for the fresh cutover is *"I want to similulate the
  more atomic (all switches flipped) upgrade path we will end up with for
  users."* The collapsed build **is** that upgrade path. Testing the
  uncollapsed build tests a configuration no user will ever run.
- gteam is already running all-switches-on, which is behaviourally identical to
  collapsed. **So collapsing does not invalidate any gteam acceptance result**
  — the two builds differ only in whether a knob exists, not in what the code
  does when the knob is ON.
- The collapse's own risk (an existing settings row becoming unreadable, §4.3)
  is precisely what a fresh-cutover test would catch. Running the cutover
  before the collapse tests the one build in which that risk is absent.

**Recommended order:** gteam acceptance completes → **collapse** → redeploy
gteam on the collapsed build and re-run a short regression → fresh cutover on
the second instance using the collapsed build → merge.

This is sequencing, not an irreversible act, so I am recording it as a decision
rather than escalating it. It does not contradict *"fresh cutover only happens
after all other acceptance testing on gteam is done"* — gteam acceptance still
comes first. It inserts the collapse into the gap.

---

## 7. Alternatives considered

**A — Keep the switch, hard-wire the default to ON and remove the ability to
set it OFF.** Rejected. Leaves the key in the schema and the endpoint in the
API, so operators still see a supported knob; the PUT path becomes a
write-that-does-nothing, which is worse than either extreme. Achieves none of
G2 and only a weak form of G1.

**B — Deprecate for one release: accept the key, ignore it, delete it later.**
Rejected on the explicit Q2 hard-cutover ruling and the single-cut-over
directive. There is no external consumer to protect: the web UI does not
reference the key (grep of `web/src/` returns nothing) and the endpoint is
admin-only. A deprecation window costs a second release and a second decision
for a key nobody reads. *Note:* §4.4's warning log is the useful 5% of this
alternative retained without the cost.

**C — Delete the `messaging` row from `hub_settings` via migration.**
Rejected. Irreversible, and unnecessary — §4.3 shows the row is harmless once
unregistered. This would also collide with the standing fixture rule that the
`messaging` section row must never be deleted or modified. If ptone later wants
the rows gone for tidiness, that is a separate, opt-in cleanup with its own
decision.

**D — Collapse the switch and the instrumentation together.** Rejected, §5.
Blinds the fresh-cutover test.

**E — Do nothing; ship with the switch.** Rejected by direct instruction, but
worth naming the real cost: the switch's OFF path returns pre-refactor
behaviour over *post-migration* data. That combination has never been tested
and is not a supported state. Shipping a knob whose OFF position we have not
validated is worse than shipping no knob.

---

## 8. Migration / rollout

**No data migration.** The entire change is code. §4.3 is the argument.

**Rollback.** Reverting the commit restores the switch, and any surviving
`messaging` row is picked up again by `SectionByName` on the next refresh —
including an explicit `false`, which would then take effect again. Rollback is
therefore clean *provided the row was never deleted*, which is exactly why
Alternative C is rejected. **This is the reason row deletion and switch removal
must not ship together.**

**Irreversibility note for ptone.** After this lands there is no runtime lever
to return to pre-refactor messaging behaviour; the only path back is a binary
downgrade. That is the intended end state and he has asked for it — recording
it so the trade is explicit rather than discovered.

---

## 9. Open questions

- **OQ-SC-1** — Does ptone want the §4.4 startup warning at all? It is the only
  additive code in the design. My read: keep it, it is ~15 lines and it stops a
  config file silently becoming a lie. **Not blocking** — developer should
  implement it, and it can be dropped in review.
- **OQ-SC-2** — Should the `messaging` rows eventually be deleted from
  instances? Irreversible, so his call, and explicitly **not** part of this
  change. Can be answered any time after the collapse ships.
- **OQ-SC-3** — §0 assumes "yet another switch" was about proliferation, not a
  specific Discord switch I failed to find. If that assumption is wrong the
  design needs revisiting. **This is the one I would want confirmed** before
  dispatch.

---

## 10. Implementation phases

Commit-sized, ordered, each independently green.

| Phase | Content | Files |
|---|---|---|
| **SC-1** | Make the 4 non-`writeDenyEnabled` behavioural sites unconditional. Delete the `ops != nil && ...()` condition, keeping the block body verbatim. Per §4.1, demonstrate per site that nothing else in the block reads opsettings. | `handlers_messages.go`, `handlers_chat_v2.go` |
| **SC-2** | Delete `writeDenyEnabled()` and both injected closures; make the guarded paths in `NotificationDispatcher` and `MessageBrokerProxy` unconditional. **No new locks in `Start*`.** Closes DEF-131 as superseded. | `server.go`, `notifications.go`, broker proxy |
| **SC-3** | Delete `admin_messaging.go` and its route registration; delete `ConversationEnvelopeSwitch()` getter; delete `MessagingSettings` + section registration + registry schema entry (whole entry, §4.3). | `admin_messaging.go`, `operational_settings.go`, `sections.go`, `registry.go` |
| **SC-4** | §4.4 startup warning for an explicit `false`. Droppable in review. | `server.go` or boot path |
| **SC-5** | Test sweep: delete or rewrite tests that set the switch. **Any test that exercised the OFF path must be deleted, not flipped to ON** — an OFF-path test rewritten as an ON-path test usually duplicates existing coverage while looking like it still guards something. | `*_test.go` |

**Standing constraints for whoever implements this:** never make a gate pass by
weakening the gate; report any red to me rather than tuning it away; stage only
named files, never `git add -A`; per-file numstat before push; push to the
explicit token-bearing ptone/scion URL and include raw `ls-remote` output in
the report.

---

## 11. Acceptance criteria

- **AC-1** — `grep -rn "conversation_envelope_switch\|ConversationEnvelopeSwitch\|writeDenyEnabled" --include='*.go' .` returns **only** test-fixture or changelog matches; zero live references.
- **AC-2** — `GET /api/v1/admin/messaging` returns **404**. There is no route.
- **AC-3** — For each of the 7 former call sites, the reviewer confirms in
  writing that the enclosing block reads no operational setting. §4.1 — this is
  the finding most likely to be skipped, so it is an explicit criterion.
- **AC-4** — **The G3 hazard.** A hub booted with a `hub_settings` row
  containing `{"conversation_envelope_switch": false}` starts cleanly, logs the
  §4.4 warning, and **enforces the conversation model**. Assert enforcement,
  not merely a clean boot.
- **AC-5** — A hub booted with a row containing the two **stale** keys
  (`conversation_read_switch`, `conversation_write_deny_switch`) starts cleanly
  and the settings cache does **not** mark the section malformed.
- **AC-6** — A hub booted with a **malformed** `messaging` document starts
  cleanly and enforces the conversation model. Note this **inverts** the
  previous fail-closed behaviour (malformed → OFF). That inversion is correct
  once the switch is gone — there is no longer a behaviour to fail closed *to* —
  but it must be asserted deliberately, because it contradicts DEF-92 as
  written and a reviewer who remembers DEF-92 will otherwise flag it as a
  regression.
- **AC-7** — Reverting the SC-1..SC-3 commits on an instance whose `messaging`
  row still exists restores switch-honouring behaviour, including an explicit
  `false`. This is the §8 rollback claim, tested rather than asserted.
- **AC-8** — `make test-fast` green. Full `pkg/hub` + `pkg/messaging` +
  `pkg/store` green, excluding the two known-environmental failures
  (`TestDeleteStopped_RequiresGroveContext`, the six `pkg/config` tests).
- **AC-9** — Counters in `pkg/messaging/divergence.go` still compile, are still
  wired, and still increment. §5 — the collapse must not silently take the
  instrumentation with it.
- **AC-10** — No file under `extras/` is modified. §0 — the plugins are not
  part of this change, and a diff touching them means the premise was wrong.
