# A2A bridge serves federated users (phase 1, item 7)

**Date:** 2026-10-13
**Stacked on:** project visitors list and read agents, and read their own grants and DMs (item 5, part B)
**Tracked in:** ptone/scion#4603

## What changed (extras/scion-a2a-bridge only)

- **Credential kinds.** `decodeFederationToken` reads `email` and
  `scion_token_kind`. `classifyFederationClaims` decides the caller kind:
  - `agent`: an agent caller, built exactly as before.
  - `user`: a user caller. It is refused when it also carries an agent
    claim, or has no email.
  - No kind: an agent when the credential carries an agent claim
    (`project_id`, `agent_name`, `ancestry`, `root_user`). A user when it
    carries an email and no agent claim. Otherwise an agent, as before.
  - Any other kind, or a kind that is not a string: refused.
  - A user credential whose issuer has no host is refused too, because the
    task isolation key needs the host.
- **User caller path.** `CallerIdentity` gains `Subject` and
  `IsFederatedUser()`, which is nil-safe. The federation scheme sends a
  user caller to `GET /api/v1/auth/me` with the caller's credential in
  `X-Scion-Federation-Token`, plus the bridge's minted bearer, the same
  pair the federation client already sends.
  - `UserID` is the account the hub names. The email comes from the
    credential.
  - A hub 401 is answered with 401. A 403 or 404, or a hub answer that names
    an agent or no account, gets the uniform refusal (403, the hub's
    refusal text).
  - Sender label: `user:<account ID>`.
  - `CallerKey`: `fed:<issuer host>:<sub>`.
  - The SDK task store records `caller.UserID` (the account ID), so a reply
    on the user's own topic passes `validateTopicUser`.
- **Executor.** For a federated user:
  - The message is text only (no `a2aTaskId` metadata). The hub refuses
    anything else from a visitor. The reply is correlated through the
    user topic (the existing `subscribeAllUserTopics` path for user
    callers).
  - An unresolved agent and a hub refusal of the send both end the task
    with the uniform refusal, as a failed task rather than a JSON-RPC error.
  - Before the reply is forwarded, `recheckFederatedUserAccess` reads the
    DM (`GET /api/v1/chat/conversations/dm:agent:<agentId>:user:<accountId>/messages?limit=1`)
    with the caller's credential. A refusal ends the task with the uniform
    refusal. A hub error also stops the reply ("Unable to verify access to
    agent").
  - Cancel sends no interrupt for a federated user (the hub refuses
    interrupts from visitors).
- **Durable resubscribe.** `DurableRequestHandler.SetBridge` (wired in
  `main.go`). Before it streams a response event to a federated user, the
  handler runs the same re-check. Without a bridge, route or resolvable
  agent, it fails closed.
- **No invented access.**
  - `resolveContext` never auto-provisions an agent for a federated user.
  - The legacy `Bridge.SendMessage` and `SendStreamingMessage` entry points
    refuse federated users. The streaming one sends as the bridge admin,
    and neither has a production caller.
  - Legacy `CancelTask` sends no interrupt for them.
- **Agent cards.** New authenticated `GET /projects/{projectSlug}/agents`,
  for federated users only (404 for others):
  - The project ID comes from the caller's own `GET /api/v1/users/me/visitor-grants`
    (active grant with that slug).
  - Then `GET /api/v1/projects/{id}/agents?messageable=true`, narrowed to
    the project's `exposed_agents`.
  - Cards are built from the hub's projection only (name and slug, generic
    description) through the new `buildAgentCard`, which `GenerateAgentCard`
    now also uses (same output).
  - Every refusal is the uniform refusal. Responses are `private, no-store`.
- **Docs.** `docs-site/.../hosted/user/a2a-bridge.md` describes users from
  another hub.

## Decisions and notes

- Uniform refusal text = the hub's `visitorRouteRefusalMessage`, so bridge
  and hub answers cannot be told apart.
- The DM key is computed locally with `messages.DMConversationKey`, the
  same call the hub makes. `hubclient.MessageResponse` has no
  `conversation_key` field, and adding one would change the root module.
- The visitor calls (`auth/me`, `visitor-grants`, `messageable` list, DM
  read) are raw HTTP in the bridge. `hubclient` has no `messageable`
  option and no chat routes. No root module file changed.
- Bridge and hub differ on credentials without the claim. Bridge: email
  and no agent claim means user. Hub (item 6B): no claim always means
  agent. Every credential a hub mints carries the claim, so they agree in
  practice. For a disagreeing credential the hub's `auth/me` answer names
  an agent, and the bridge refuses.
- Identity issuers (`issuer_type: user`) issue credentials with an email
  and no agent claim. The bridge used to treat these as agent callers. Now
  they are user callers and are admitted only if the hub's `auth/me`
  answers. The hub's own handling of them is unchanged.

## Follow-ups

- `auth/me` runs on every request for a user caller (no cache). A
  short cache keyed by credential hash would cut hub calls during polling.
- Reply correlation without metadata stays ambiguity-closed per agent. Two
  callers with active tasks on the same agent drop the reply. This was
  already true for every caller; narrowing by the topic's user would help.
- Push notifications (legacy `PushDispatcher`) have no re-check. They are
  off in production wiring (`PushNotifications: false`).
- The docs page names the scheme `oidcFederation`; the config value is
  `federation`.
