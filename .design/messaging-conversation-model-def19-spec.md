# DEF-19 — Phase 7 validation breaks `group[]` messaging

**Release blocker. Gates tranche D (Phase 7) in the incremental landing plan.**
Spec written 2026-08-27 18:45Z. Prior art grepped and cited (rule 16).

---

## 1. The defect

`group[]` is a **shipped, documented CLI feature on `main`** — `cmd/message.go:64` documents it,
`:71` gives a worked example, `:82`–`:257` define its flag-conflict errors. Our Phase 7 validation
choke point rejects it.

**Proved by direct probe, not by reading** (I ran this; reproduce it before you start):

| Recipient | `ValidateLegacyMessage` result |
|---|---|
| `group[agent:reviewer,user:alice]` | `addressee[0]: principal_kind must be user or agent, got "group[agent"` |
| `group[reviewer,deploy-bot]` | `addressee[0]: principal_kind must be user or agent, got "system"` |
| `agent:reviewer` | `nil` (passes) |

**Why it is live:** `messaging.ValidateLegacyMessage` is called at
`pkg/hub/handlers_agent_messaging.go:630`. The `group[]` fan-out dispatch is at `:669`.
**Validation runs first**, so every `group[]` message through the outbound path returns 400 and
never reaches `handleGroupMessage`.

**Why it is ours, not pre-existing:** `ValidateLegacyMessage` **does not exist on `origin/main`**
(verified at `b09e7f49`). On main the handler reaches `handleGroupMessage` at `:585` with no
validation gate.

## 2. Root cause

`buildAddressees` (`pkg/messaging/envelope_compat.go:229-250`) treats `old.Recipient` as **exactly
one** principal reference:

```go
if old.Recipient != "" {
    ref := buildPrincipalRef(old.Recipient, old.RecipientID)   // splits on ":"
    addrs = append(addrs, Addressee{ PrincipalKind: ref.PrincipalKind(), ... })
}
```

`buildPrincipalRef` splits on `:`, so `group[agent:reviewer,...]` yields the principal kind
`group[agent`. Validation then correctly rejects a kind that is neither `user` nor `agent`.

**The validator is not wrong. The mapper is incomplete.** Do not loosen the validator — that is
rule 20 (widening a funnel to hide a broken sink) and it would let genuinely malformed kinds
through.

## 3. The fix

**`group[]` is a multi-addressee form, and the new envelope was designed for exactly that.**
`NewEnvelopeToLegacy`'s own comment (`envelope_compat.go:268`) says the new format captures
"multiple addressees" that the old format cannot represent. The legacy mapper simply never wired
`group[]` to that capability. So the fix is not a special case — it is the mapper finally
expressing what the target model already supports.

`buildAddressees` detects the `group[` form and emits **one `Addressee` per member**.

**Reuse the existing parser. Do not write a second one.**
`messages.ParseGroupRecipient(s string) ([]GroupRecipient, error)` already exists at
`pkg/messages/message_group.go:72` and is what `handleGroupMessage` uses (`:1099` produces
`invalid group[] recipient: ...` from it). A second group-parsing implementation that agrees by
convention is precisely how DEF-8 happened.

Sketch — illustrative, not production:

```go
if old.Recipient != "" {
    if members, err := messages.ParseGroupRecipient(old.Recipient); err == nil && isGroupForm(old.Recipient) {
        for _, m := range members {
            addrs = append(addrs, Addressee{ MessageID: msgID, PrincipalKind: ..., PrincipalID: ..., Via: via, DeliveryState: DeliveryPending })
        }
        return addrs
    }
    // existing single-principal path unchanged
}
```

**Open sub-question for the implementer to settle by test, not by assumption:** what `Via` do
group members get? `ViaExplicit` is the obvious reading — they were named explicitly. Confirm no
existing consumer distinguishes them, and say which you chose and why.

**`RecipientID` does not apply to the group form** (it names a single principal). State what
happens to it — ignored, or an error if set alongside `group[]`.

## 4. Non-goals

- **Do not change `ValidateLegacyMessage`'s principal-kind rule.**
- Do not touch `handleGroupMessage`'s fan-out behaviour. This defect is entirely upstream of it.
- Do not fix this in a merge resolution. It is its own change with its own tests.

## 5. Acceptance criteria

- **AC-19-1** `ValidateLegacyMessage` accepts `group[agent:reviewer,user:alice]` and
  `group[reviewer,deploy-bot]`, producing one addressee per member with correct kind and id.
  **This is the reproduction; it must fail before the fix.**
- **AC-19-2** **A `group[]` message survives the full HTTP handler path** —
  `POST /agents/{id}/outbound-message` with a `group[]` recipient reaches `handleGroupMessage` and
  fans out. *This is the test whose absence hid the defect: no test on the branch exercises
  `group[]` through the handler.* A unit test on `buildAddressees` alone **does not satisfy this
  AC** — that is exactly the bypass that concealed DEF-19 (rule 30).
- **AC-19-3** Malformed group forms are still rejected with a diagnostic naming the offending
  member: `group[]`, `group[`, `group[bogus:x]`, `group[,]`.
- **AC-19-4** Single-recipient behaviour is unchanged. Paired positive/negative (rule 29):
  `agent:reviewer` still passes, `bogus:x` still fails.
- **AC-19-5** Mutation-verified: disable the group branch in `buildAddressees` and confirm
  **AC-19-2 fails**. If only AC-19-1 fails, the handler-level test is not reaching the code and
  the coverage hole is still open.
- **AC-19-6** No second group-parsing implementation is introduced. `messages.ParseGroupRecipient`
  is the only parser. Assert by grep in review.

## 6. Verification note for whoever reviews this

The DEF-19 probe was run against `457149b9`. **Re-run it on your own tree before starting** — a
base is a claim (rule 24), and this defect's whole history is one of being observed and filed as
someone else's concern.
