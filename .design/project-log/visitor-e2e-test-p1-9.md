# End-to-end test of project visitors across two hubs (phase 1, item 9)

**Date:** 2026-10-18
**Stacked on:** the A2A bridge item for federated users (item 7)
**Tracked in:** ptone/scion#4603

## What changed

- **New test:** `TestFederatedVisitorEndToEnd` in
  `extras/scion-a2a-bridge/integration/federated_visitor_e2e_test.go`. It
  uses only test code and exported pkg/hub and bridge API. No production
  file changed.
- **Topology, all in one process:**
  - Hub A runs with its OIDC provider on and `user_token_audiences` set to
    hub B.
  - Hub B trusts hub A as an `issuer_type: hub` issuer with
    `accept_user_tokens` and `allow_visitor_grants`.
  - Both hubs have the `federation.project_visitors` experiment on, run on
    SQLite stores, and listen on `httptest` loopback ports.
  - The A2A bridge (`Auth.Scheme: federation`) sits in front of hub B. It
    is a spoke of hub B's fan-out event bus, with the broker proxy as its
    host callbacks, the same wiring `cmd/server_foreground.go` uses for a
    broker plugin.
  - The client is plain JSON-RPC, decoded with a2a-go types.
- **The agent:** hub B's dispatcher is a test type. It records each
  delivered message and answers an error for every other dispatch. The
  test replies as the agent through `POST
  /api/v1/agents/{id}/outbound-message`, with an agent credential that
  hub B signed and recorded.
- **Steps:**
  1. Alice obtains a user access credential from hub A with a session
     credential.
  2. Hub B's owner grants her by email. The grant is pending.
  3. Her first task through the bridge is delivered and completes with the
     agent's reply. The grant is then active, and one `visitor_linked`
     audit record exists. The bridge lists the agent's card.
  4. With `accept_user_tokens` off (applied through `ApplySnapshot`), the
     bridge answers HTTP 401 and nothing is delivered. Turned back on, the
     next task completes, and the grant is unchanged.
  5. After the grant is revoked, the next task is refused.
  6. A visitor rule for `partner.example` admits Bob on his first request,
     bound by the rule. After the rule is removed, his next task is
     refused.
- **Uniform refusal:** the refusals in steps 5 and 6 must be identical, in
  task state and message text, to the answer for an agent the bridge does
  not expose. That answer is taken in the same run.
- **Waits:** every wait is a `select` on the delivery channel or the A2A
  answer, bounded by a 60 s context. There are no sleeps.

## Notes

- **CI:** the test runs in the Extras Modules CI matrix (`go test ./...`
  for the bridge module) and in the A2A integration job (`-v`, `-race`,
  `-count=3`). The test never skips, because that job fails on skipped
  tests. It is not added to `testdata/acceptance_layers.json`, the census
  of the GE suite.
- **Subscriptions:** the fan-out bus publishes every message to every
  spoke, and the bridge's `BrokerServer.Publish` does not filter by
  subscription. The bridge's topic subscriptions are therefore bookkeeping
  in this wiring (and with the plugin RPC adapter). A test cannot observe
  a missing subscription.
- **Compile:** nothing was compiled or run while writing the test. A gate
  agent compiles and runs it after review.
