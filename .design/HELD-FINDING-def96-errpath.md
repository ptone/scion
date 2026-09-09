# Held finding — DEF-96 @ `2f3beca21`, `handlers_chat_v2.go:2605-2612`

**Held deliberately.** `cr-msg-def96` is reviewing right now and their brief item 4
points at these exact lines without naming this. Whether they catch it
independently is calibration data on the whole review, and sending it now
destroys that signal. Release when their report lands, either as corroboration
or as an addition.

## The code

```go
var directConvID string
directConv, convErr := s.store.GetConversationByExternalRef(ctx, "native", key)
if convErr == nil && directConv != nil {
    directConvID = directConv.ID
}
// ErrNotFound is fine — pre-conversation-model hub. Any other error is
// also tolerable: the legacy arm will still match thread_id rows.
```

`ErrNotFound` appears in this file **only in comments** (grep: lines 2601 and
2611, both comment text). The code never tests for it. Every error — transient
DB failure, context deadline, pool exhaustion — takes the same branch as a
genuine miss.

## Why "also tolerable" is backwards

The comment justifies itself with *"the legacy arm will still match thread_id
rows."* The C2 correction exists **because that arm matches almost nothing**:
measured on the live hub, 22 of 6,476 messages in `kind='direct'` conversations
carry a `thread_id`. The consoling clause asserts the precise fact the
correction was written to refute.

So on a transient lookup error, promotion:

1. proceeds with `directConvID = ""`,
2. the guard correctly suppresses arm 1, so **arm 1 matches nothing**,
3. arm 2 matches ~0 rows,
4. **step 4 deletes the `webchat_dm` registry row**,
5. commits,
6. returns **200** with `messageCount: 0`.

The DM disappears from the user's DM list, the new thread is empty, and the API
reported success. That is DEF-96 restored — reached not by a latent bug but by
the code's documented handling of a transient error, on a path a user can hit.

## The asymmetry that makes it worse

The `? <> ''` guard is a **store**-level defence against an empty
`directConvID`. It does its job: it stops the wildcard. But it converts a
lookup failure from *dangerous* into *silently inert*, and nothing upstream
distinguishes inert-because-absent from inert-because-broken. The guard
protects the rest of the hub's data and leaves this DM to fail quietly.

Note the contrast with `dmKey`, which cannot be empty: the handler requires a
`dm:` prefix and a non-empty `parseAgentDMKey`. Arm 2 needs no guard because its
empty value is unreachable. Arm 1 needs one because its empty value is a
**normal, expected** path. That asymmetry is correct. The defect is that the
same code treats "normal and expected" and "something went wrong" as one case.

## Proposed correction

Split the two:

- `errors.Is(convErr, store.ErrNotFound)` → `directConvID = ""`, proceed. This
  is the pre-conversation-model hub and it is fine.
- any other non-nil `convErr` → **refuse the promotion**, 503, no transaction,
  no registry deletion.

Refusal is the recoverable direction: the user retries and gets a correct
promotion. A committed promotion that moved nothing and deleted the registry row
is not recoverable by the user, and the 200 means nobody knows to look.

Also worth pinning: `directConv == nil` with `convErr == nil` is a third
outcome the code folds into the same silent branch. It should be impossible;
if it is, say so, and if it is not, it is a lookup failure and belongs with the
refusal case.

## Minor, same read (not held — fold into the same reply)

`webchannel_store_postgres.go:1679-1695`: the `else` arm of the
`topic.ConversationID != ""` branch is **unreachable**. Line 1622 mints an id
unconditionally when empty, so the condition is always true by the time it is
tested. In sqlite the same `else` is live, because `hasConv` can be false. The
two stores read as parallel and are not. Worth a comment saying the pg branch is
retained only for shape-parity with sqlite — or deleting it.

## Predicates I checked myself, both stores, both clear

- The `? <> ''` / `$N <> ''` guard is present on arm 1 in **both** stores and
  the two predicates are semantically identical, arm for arm.
- `GetConversationByExternalRef` is called at `handlers_chat_v2.go:2606`,
  outside and before `PromoteDM`, so before either store's `BeginTx`.
  U-TX-1 holds.
- `PromoteKeys` is used at the one call site; the two fields are named, not
  positional.
- The lookup is `GetConversationByExternalRef` only — no create-on-miss.
