# Defect: PrincipalKindFromAddress case-fold masks kind-prefix validation

**Filed by:** ca-msg-em9  
**Date:** 2026-08-28  
**Updated:** 2026-08-28 (v2 — corrected after architect review)  
**Origin:** em6 finding during DMConversationKey tightening audit  
**Status:** Audit complete — live authorization bypass confirmed — no code changes made  

---

## Summary

`PrincipalKindFromAddress` (pkg/messages/dm_key.go:75-85) applies
`strings.ToLower` to the kind prefix before validating against
`validDMKinds`. This means non-canonical prefixes like `"User:"` or
`"Agent:"` are silently accepted and mapped to their canonical forms.

At call site 2 (handlers_broker_inbound.go:276), the fold is a necessary
link in a **live authorization bypass chain** on upstream/main. A broker
plugin sending `"User:alice"` as Sender bypasses the case-sensitive
`HasPrefix("user:")` gate that performs ActionAttach authorization and
SenderID resolution. The fold then repairs the prefix downstream,
allowing DM conversation resolution to proceed with an
attacker-controlled SenderID. The bypass is not latent — it is
exploitable now.

**v1 error corrected:** The original audit incorrectly concluded the
bypass was "inert in practice, gated by a SenderID nil-guard." That
guard is at messagebroker.go:835 (call site 4) — a different function on
a different call path. It does not run at call site 2. Furthermore,
SenderID is decoded from the request JSON body
(`json:"sender_id,omitempty"`) and is attacker-controlled; the nil-guard
would not have helped even if it ran on this path.

## Function under audit

```go
func PrincipalKindFromAddress(address string) (string, bool) {
    idx := strings.IndexByte(address, ':')
    if idx < 0 { return "", false }
    kind := strings.ToLower(address[:idx])   // ← the fold
    if validDMKinds[kind] { return kind, true }
    return "", false
}
```

`validDMKinds` contains only `"user"` and `"agent"`.

---

## Call-site provenance audit (upstream/main)

### Site 1 — handlers_agent_messaging.go:1578

```go
senderKind, sOK := messages.PrincipalKindFromAddress(mentionMsg.Sender)
```

**Provenance:** `mentionMsg.Sender` ← `originalMsg.Sender` ←
`structuredMsg.Sender` ← B5 auth override (lines 540-559).

B5 override constructs Sender from authenticated context:
- `"user:" + user.DisplayName()` or `"user:" + user.Email()`
- `"agent:" + agentIdent.ID()`
- fallback: `"user:unknown"`

All prefixes are string literals in lowercase.

**Classification: INTERNAL.** Server-constructed, always canonical. The
fold is identity-preserving. No non-canonical prefix can reach this site.

### Site 2 — handlers_broker_inbound.go:276

```go
senderKind, sOK := messages.PrincipalKindFromAddress(req.Message.Sender)
```

**Provenance:** `req.Message.Sender` comes from the JSON body of a
broker plugin HTTP POST. Transport is HMAC-authenticated (line 58), but
the Sender field content is controlled by the broker plugin.

**Upstream gate (lines 126-146):** A case-sensitive
`strings.HasPrefix(req.Message.Sender, "user:")` check controls:
1. User lookup via `store.GetUserByEmail`
2. `SenderID` caching on the message
3. `ActionAttach` permission check for the resolved user

If Sender is `"User:alice"` (capital U):
- `HasPrefix("user:")` → **false** — skips user lookup, SenderID cache,
  and ActionAttach permission check
- `PrincipalKindFromAddress` at line 276 → folds `"User"` to `"user"`,
  returns `("user", true)` — proceeds into DM conversation resolution

This creates a path where DM conversation resolution proceeds with
`senderKind = "user"` but without the ActionAttach authorization that
normally gates user-originated messages.

**Classification: EXTERNAL.** Broker plugin controls Sender content. The
fold repairs a non-canonical prefix that the upstream case-sensitive gate
correctly rejected, enabling an authorization bypass.

**This bypass is live, not latent.** The full chain:

**Step 1 — Gate bypass (handlers_broker_inbound.go:126-146).** The
case-sensitive `HasPrefix("user:")` gate does three things: (a) resolves
the sender via `store.GetUserByEmail`, (b) runs the ActionAttach
permission check, and (c) overwrites `req.Message.SenderID` with the
resolved user's ID. Sender `"User:alice"` (capital U) causes HasPrefix
to return false. All three operations are skipped.

**Step 2 — Attacker-controlled SenderID survives.** `SenderID` is a
JSON-decoded field (`json:"sender_id,omitempty"` in
pkg/messages/types.go:131). Because the gate at :126 was skipped, the
body-supplied `sender_id` is not overwritten with a server-resolved
value. The attacker controls it.

**Step 3 — DM ownership check passes (#1322, line 165-170).** The
ownership check compares `dmUserID` (parsed from the attacker-supplied
`thread_id`) against `senderID` (the attacker-supplied `sender_id`).
Both values are attacker-controlled. They trivially match.

**Step 4 — SenderID used under false invariant (line 241).** The
assignment `senderUserID := req.Message.SenderID` carries a comment:
*"The sender user ID was resolved and cached in req.Message.SenderID
during the upstream permission check for 'user:' senders."* This comment
asserts exactly the invariant that the case-sensitive gate fails to
guarantee. The attacker's body-supplied value is used as if it were
server-resolved.

**Step 5 — Fold enables DM resolution (line 276).** At this point,
`PrincipalKindFromAddress("User:alice")` folds `"User"` to `"user"` and
returns `("user", true)`. Without the fold, it would return `("", false)`
and the forged message would never reach `ResolveOrCreateDMConversation`.
The fold is not the root cause, but it is a necessary link in the chain.
Removing it shrinks blast radius; it does not close the bypass.

**Step 6 — DM resolution proceeds (line 279).** The call to
`ResolveOrCreateDMConversation` runs with `senderKind = "user"` and an
attacker-controlled `senderUserID`. Note that this is a direct call to
`messaging.ResolveOrCreateDMConversation`, NOT through
`resolveDMConversation` in messagebroker.go. The nil-guard at
messagebroker.go:835 (call site 4) is on a completely different call path
and does not run here.

**Root cause:** The case-sensitive `HasPrefix("user:")` gate at line 126.
The fold in `PrincipalKindFromAddress` is a contributing factor that
enables the worst outcome (DM resolution with forged identity).

**Existing broker plugins:** The Slack broker plugin constructs Sender as
`"agent:" + slug` using lowercase string literals with case-sensitive
HasPrefix checks. No known first-party plugin sends non-canonical
prefixes. Third-party plugins are unconstrained.

### Site 3 — messagebroker.go:465

```go
if recipientKind, rOK := messages.PrincipalKindFromAddress(msg.Recipient); rOK {
```

**Provenance:** `msg.Recipient` arrives via eventbus subscription
(`subscribeProjectUserMessages`). Published by hub handlers:
- `handleAgentOutboundMessage` sets Recipient = `"user:" + name`
  (server-constructed, lines 150/158/164)
- `handleBrokerInbound` does not publish through this path

All publishers use lowercase string literal prefixes.

**Classification: INTERNAL.** Server-constructed, always canonical. The
fold is identity-preserving.

### Site 4 — messagebroker.go:839

```go
senderKind, ok := messages.PrincipalKindFromAddress(msg.Sender)
```

**Provenance:** Called from `resolveDMConversation`, which is invoked by
`deliverToUser` and `deliverToAgent`. Messages arrive via eventbus,
published by hub handlers where Sender is always B5-forced (`"user:" +
name/email`) or agent-constructed (`"agent:" + slug`). All lowercase
string literal prefixes.

**Classification: INTERNAL.** Server-constructed, always canonical. The
fold is identity-preserving.

---

## Answers to architect's questions

### Q1: Can a non-canonical kind prefix originate outside the trust boundary?

**Yes, at exactly one call site.** Site 2 (handlers_broker_inbound.go:276)
receives Sender from a broker plugin's JSON body. The broker plugin is
HMAC-authenticated at transport level but the Sender field content is not
canonicalized or validated before reaching `PrincipalKindFromAddress`.

Sites 1, 3, and 4 are all server-constructed with lowercase string
literal prefixes. No non-canonical prefix can reach them.

### Q2: Is the fold-induced aliasing semantically inert, or does it matter?

**The aliasing on the DM key is semantically inert in isolation.** There
is no distinct `"User"` or `"Agent"` principal kind in the system.
`"User:x"` and `"user:x"` name the same principal. The fold maps to the
only valid canonical form, and `DMConversationKey` itself also applies
`ToLower` (line 41-42), so the key derivation is doubly normalized.

**But the fold matters because it is a necessary link in a live
authorization bypass at site 2.** Without the fold,
`PrincipalKindFromAddress("User:alice")` would return `("", false)` and
the B15 dual-write block would not reach `ResolveOrCreateDMConversation`.
The fold repairs the prefix that the case-sensitive gate correctly
rejected, enabling the forged message to complete DM resolution with an
attacker-controlled SenderID.

The full chain at site 2:

- `"user:alice"` → HasPrefix passes → authorized, SenderID resolved →
  fold → DM resolution with server-verified identity
- `"User:alice"` → HasPrefix fails → **not authorized, SenderID
  attacker-supplied** → fold repairs to `"user"` → DM resolution with
  attacker-controlled identity

The aliasing on the key is inert. The fold's role in enabling the bypass
is not.

---

## Reasoning error in v1 (corrected)

The v1 audit carried a safety argument across call sites: the nil-guard
at messagebroker.go:835 (call site 4, `resolveDMConversation`) was cited
as protecting call site 2 (handlers_broker_inbound.go:276). These are
different functions on different call paths. Site 2 calls
`messaging.ResolveOrCreateDMConversation` directly at line 279 — it does
not go through the `resolveDMConversation` wrapper in messagebroker.go.

Even if the nil-guard ran on this path, it checks
`msg.SenderID == ""`. SenderID is decoded from the request JSON body
(`json:"sender_id,omitempty"` in pkg/messages/types.go:131). An attacker
exercising this bypass controls the body and would not leave SenderID
empty.

The lesson: when carrying a safety argument across call sites, the guard
must be on the path being cleared. Write the guard's file and line next
to the site it protects — the mismatch becomes immediately visible.

---

## Recommendation (no code changes in this audit)

Routing is with the user. Architect has recommended a standalone hotfix
ahead of the refactor.

1. **Root cause — fix the gate at site 2:** Make the `HasPrefix("user:")`
   check at handlers_broker_inbound.go:126 case-insensitive, or
   canonicalize the kind prefix before the gate runs. This closes the
   bypass regardless of the fold.
2. **Contributing factor — remove the fold from PrincipalKindFromAddress:**
   Make it case-sensitive to match the upstream gates. This does not close
   the bypass on its own (the gate is still the root cause) but removes a
   necessary link in the worst version of the chain, shrinking blast
   radius.
3. **Do not rely on the SenderID nil-guard at site 4** as defense for
   site 2. It is on a different call path and tests a condition the
   attacker controls.
