# Defect: PrincipalKindFromAddress case-fold masks kind-prefix validation

**Filed by:** ca-msg-em9  
**Date:** 2026-08-28  
**Origin:** em6 finding during DMConversationKey tightening audit  
**Status:** Audit complete — no code changes made  

---

## Summary

`PrincipalKindFromAddress` (pkg/messages/dm_key.go:75-85) applies
`strings.ToLower` to the kind prefix before validating against
`validDMKinds`. This means non-canonical prefixes like `"User:"` or
`"Agent:"` are silently accepted and mapped to their canonical forms.

The fold is semantically inert on the DM key itself — there is no
distinct `"User"` or `"Agent"` principal kind, so the aliasing maps to
the same entity. However, the fold masks a control flow inconsistency at
one of the four production call sites where case-sensitive upstream gates
make authorization decisions on the raw (unfolded) prefix.

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
correctly rejected, enabling an authorization bypass path.

**Practical exploitability note:** The SenderID will be empty (not
cached by the skipped user-lookup block). The `resolveDMConversation`
function (messagebroker.go:835) checks `msg.SenderID == ""` and returns
nil with a warning log, aborting DM resolution. This means the bypass
path is currently **inert in practice** — it hits a nil-guard before
causing harm. However, the control flow inconsistency remains a latent
defect: any future change that populates SenderID through an alternate
path would activate it.

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

**The aliasing on the DM key is semantically inert.** There is no
distinct `"User"` or `"Agent"` principal kind in the system. `"User:x"`
and `"user:x"` name the same principal. The fold maps to the only valid
canonical form, and `DMConversationKey` itself also applies `ToLower`
(line 41-42), so the key derivation is doubly normalized.

**However, the fold masks a control flow inconsistency at site 2.**
The case-sensitive `HasPrefix("user:")` gate makes authorization
decisions (ActionAttach check, SenderID resolution) based on the raw
prefix. The case-insensitive fold in `PrincipalKindFromAddress` then
"repairs" the prefix for DM key derivation. This means:

- `"user:alice"` → HasPrefix passes → authorized → fold → DM resolution
- `"User:alice"` → HasPrefix fails → **not authorized** → fold → DM resolution attempted (but currently aborted by SenderID nil-guard)

The aliasing itself is inert. The authorization bypass it enables is a
latent defect gated by a nil-guard that may not survive future changes.

---

## Recommendation (no code changes in this audit)

1. **Canonicalize early at site 2:** Add `strings.ToLower` normalization
   of the kind prefix in `handleBrokerInbound` before the HasPrefix gate,
   or reject non-canonical prefixes explicitly.
2. **Remove the fold from PrincipalKindFromAddress:** Make it
   case-sensitive to match the upstream gates. This surfaces rather than
   masks inconsistencies.
3. **Defense in depth:** The SenderID nil-guard in
   `resolveDMConversation` is currently the last line of defense. It
   should not be removed without ensuring the HasPrefix gate is
   case-insensitive or the prefix is pre-canonicalized.
