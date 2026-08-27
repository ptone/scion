# QA Walkthrough — messaging conversation model

**Branch:** `scion/messaging-v2` @ `ebf8cc27` · **Written:** 2026-08-27

**What this branch changes.** Messages now resolve to a *Conversation* — a first-class row
identifying the venue a message belongs to — instead of relying on an optional
`--channel` + `--thread-id` pair that could be forgotten. `scion message` takes a conversation
reference as a positional argument. The old flags still work and now warn.

**What it does not change yet.** Nothing routes *by* conversation. `conversation_id` is
stamped onto the message row; delivery still goes to an agent. Read Part 5 before you
conclude anything is broken.

**Time:** about 15 minutes.

---

## Part 0 — Before you start

Confirm the read switch is **off**. It is default-off and should stay off for this pass; Part 4
explains why.

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  https://<hub>/api/v1/admin/messaging/divergence | jq
```

Expect `"read_switch_enabled": false`. Note the starting `matches` / `mismatches` / `fallbacks`
numbers — they are process counters, not persisted, so they reset on restart and you want the
baseline.

---

## Part 1 — What should work

**1. Message an agent by conversation reference.** The main new path.

```bash
scion message @<some-agent> "QA check one"
```

Expect: `Message delivered to agent '<some-agent>' (conversation <uuid>).`
The conversation UUID in that line is the new part — previously there was nothing to print.
Save it; you will use it in Part 2.

**2. Confirm the agent actually received it.** The point is delivery, not the message printed
by the sender.

```bash
scion logs <some-agent> | tail
```

**3. Send the same agent a second message.** The conversation UUID printed should be
**identical** to step 1 — the conversation is resolved, not recreated.

```bash
scion message @<some-agent> "QA check two"
```

*If the UUID differs, stop and tell me. That is a resolve-or-create bug and it is not one I
have predicted.*

**4. Legacy addressing still works.** This is the compatibility guarantee.

```bash
scion message <some-agent> "QA check three — legacy form"
```

**5. From inside an agent container, message a user by email.**

```bash
scion message @ptone@google.com "QA check four — from an agent"
```

Expect delivery. This form requires `SCION_AGENT_NAME`, so it only works from inside a
container — from your laptop it will fail, and that is correct behaviour, not a bug.

---

## Part 2 — What should fail, and exactly how

These two must produce a clean error and a non-zero exit. **A silent success here is the worst
possible outcome** — the original defect this project exists to fix was a message that reported
success and went nowhere.

**6. A conversation ID.** Use the UUID from step 1.

```bash
scion message conv:<uuid-from-step-1> "should be refused"
echo "exit=$?"
```

Expect exit 1 and:
`conversation reference "conv:<uuid>" is not yet supported in the CLI; use @<agent-name> to message an agent`

**7. A thread reference.**

```bash
scion message '#general' "should be refused"
echo "exit=$?"
```

Expect the same error shape and exit 1.

---

## Part 3 — Deprecation warnings

Every deprecated flag must warn on stderr **and still work**. Warnings go to stderr, so they do
not corrupt piped output.

```bash
scion message <some-agent> "QA check five" --channel discord --thread-id 123 2>&1 >/dev/null
```

Expect two `Warning: --<flag> is deprecated` lines, and the message still delivered.

Worth spot-checking that a named replacement actually runs — this is the class of bug that cost
S5 three rounds:

```bash
scion schedule create --agent <some-agent> --message "later" --in 30m
```

---

## Part 4 — The divergence board, and how to read it

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  https://<hub>/api/v1/admin/messaging/divergence | jq
```

### You will see mismatches. Most of them are an instrumentation artifact, not a routing bug.

I found this while writing this document, and it is important you know it before you look at the
board, because otherwise the honest reading is "the new model is badly broken."

Every `scion message @<agent>` send registers as a **mismatch** with reason
`routing-type-mismatch`. Here is why. The CLI resolves the conversation up front and puts the ID
on the message. The Hub sees a supplied ID and skips re-resolution — correctly, it should not do
the work twice — but it builds its result object by hand and leaves the `external_ref` field
empty (`handlers_agent_messaging.go:828-832`). The divergence comparison then compares the old
routing key against an empty string and, finding no `dm:` prefix, records a mismatch
(`divergence.go:176`). **The two models actually agree. The comparison is being fed a blank.**

This is logged as **DEF-11**.

**How to tell artifact from real:**

| Reason string | Meaning |
|---|---|
| `routing-type-mismatch: old=sender-recipient:… new=` — **empty after `new=`** | DEF-11 artifact. Ignore. |
| `dm-routing-mismatch: old=… new=dm:…` | **Real.** Both models produced a DM key and they disagree. Tell me. |
| `thread-routing-mismatch` | **Real.** Tell me. |
| `no-new-routing` | The new model produced nothing. Counted as a fallback. Expected for some legacy paths. |

**Do not enable the read switch during this pass.** The documented gate is non-zero matches,
zero mismatches, and near-zero fallbacks. DEF-11 makes zero mismatches unreachable, so the gate
cannot be satisfied and would have to be overridden by judgement — which is exactly the kind of
override that turns a safety criterion into a formality.

---

## Part 5 — Known broken. Please do not spend time filing these.

All six are verified, logged, and have owners. They are gaps *between* the delivered sections,
not failures within them — each section did what it was asked; nobody was asked to join them up.

| Ref | What you would notice |
|---|---|
| **DEF-7** | `#<thread>` could never work even if ungated. It matches a conversation display name, and no code ever writes one. |
| **DEF-8** | Your agent DM exists as **two** conversation rows — one created by the legacy stamping path, one by the CLI resolver — with different IDs. They cannot find each other. Design §2.4.2 specs the fix. |
| **DEF-9** | The addressee table is never written; `DefaultAgentID` is never read. So "post to a room without tasking anyone" and "wake the room's default agent" do not happen. |
| **DEF-10** | Agent DMs are project-scoped; the design says direct conversations are global. |
| **DEF-11** | The divergence artifact above. |
| **DEF-12** | Messages sent *before* this deploy have no `conversation_id` and never will. The backfill service is written but nothing calls it. |

**A concrete thing worth checking, if you have DB access.** It would turn my DEF-8 code reading
into evidence:

```sql
SELECT id, external_ref, project_id,
       (SELECT count(*) FROM conversation_participants p
         WHERE p.conversation_id = c.id) AS participants
FROM conversations c
WHERE kind = 'direct' AND deleted_at IS NULL
ORDER BY created_at DESC LIMIT 20;
```

I predict two rows per agent DM: one with `external_ref` like `dm:<id>:<id>`, null `project_id`,
**0 participants**; one with an **empty** `external_ref`, a non-null `project_id`, and
**2 participants**. If instead you see one row per pair, I have misread the code and DEF-8
should be closed — I would rather find that out from you than defend the reading.

---

## What to report back

1. Any step in Parts 1–3 that behaved differently from the stated expectation.
2. Any divergence reason string **not** in the artifact row of the Part 4 table.
3. The output of the SQL in Part 5, if you can get it.
4. Anything that succeeded silently when it should have failed. That is the highest-priority
   class of bug in this project and the reason Part 2 exists.
