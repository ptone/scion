# #1620 GE/A2A auth + transport integration

Date: 2026-09-18. Branch: `scion/dev-integration-auth-transport`.

## Scope and dependency assembly

This phase implements only the taskstore-independent auth and transport layers. No
unaccepted taskstore commit was merged. Hub identity persistence uses its own real
Ent SQLite store; the bridge SDK's in-memory task store is only the local envelope
executor and is not claimed as HA task durability.

The exact dependency DAG is:

```text
370a0268ac1da762f04c76c2193e68cc7d11d6c5  true merge-base
├─ e9a4c8858cb4ffb88678f51d56a75043e9bf90d9  accepted transport
│  └─ 6874df7c30b62c3a4a1f0cdc35c3187207d9b771  foundation / first parent
└─ 346b1f74ceaf541b2a8838c7b14b6a68ec73baa3  auth base / exact main
   └─ 37a3489d0effcbc536cd465855499a85bd14bc4f  accepted auth

5b07fa156e3d271df8da18ae9c2ae7997aae833d  exact-auth assembly merge
  parents: d7643c388ffaa7c4f1dff4750807b2fe469477b3,
           37a3489d0effcbc536cd465855499a85bd14bc4f
```

The clone was shallow when work began, so the accepted auth series was initially
replayed onto the foundation. After deepening exposed the true merge-base, the exact
accepted auth tip was merged at `5b07fa15`; thus the immutable originals, rather than
only equivalent patches, are ancestors. Original-to-replay map, in order:

```text
5a0a9334->da4a636c  c248e1af->c8ec69bc  8285a732->b8be478a
3efcee80->f4f29978  c2416bd3->e74f7270  135d9d24->b8c66711
b61435e2->3c21b177  acf1cb21->86916e8a  12d99470->a67f0c87
48ecbd89->02a09656  d50cb34a->2e12ef8b  deee990e->41262fd0
58df399f->03cb6c2b  e099d854->6acd9d32  53dc31cb->50a763e7
4a65c451->6ca123be  37a3489d->7568b235
```

The exact-auth merge conflicted in 11 replayed auth files: `broker.go`,
`ge_exchange_validator.go`, `ge_exchange_validator_test.go`, `server.go`, and
`v0_compat_test.go` under the bridge; `ge_exchange.go`,
`ge_exchange_ratelimit_test.go`, `ge_exchange_test.go`, and
`google_credential_validator.go` under the Hub; and `store.go` plus
`entadapter/composite.go`. Each was resolved to the first-parent version because it
was the replayed accepted patch plus already-tested integration fixes. The clean
merge also duplicated `mockHubClient.Messaging`; the redundant definition was
removed. No conversation-architecture file was resolved or edited.

## Implemented behavior

All fake endpoints, network pinning, controllable credentials, clocks, capture
surfaces, and process modes live in `_test.go`. Production changes are limited to:

- making the configured GE exchange route reachable through the outer Hub auth
  middleware;
- rejecting enabled Hub exchange configs with absent/blank audiences or token TTLs
  outside 0..5 minutes; and
- rejecting bridge GE configs with a non-HTTP(S) Hub URL, implicit/unknown credential
  type, or cache TTL outside 0..5 minutes.

Every helper is a distinct OS process. The per-run allocator binds
`127.0.0.1:0`, duplicates the bound socket into `exec.Cmd.ExtraFiles`, and the child
serves the inherited fd. The parent closes its copies only after `Start`, eliminating
the former release/rebind gap and fixed ranges shared by independent `go test`
invocations. Cleanup cancels, kills when needed, waits/reaps, and releases listeners.

Hard assertions include:

- exchange counters 2 after one cold miss per replica, 4 after credential rotation,
  and at least 6 after explicit invalidation plus Hub-JWT expiry;
- killing the original Hub, rotating JWKS, reopening the same SQLite identity store,
  and resolving changed email/same subject to the original user from a cold bridge;
- envelope counters exactly 1 exchange + 1 authenticated Hub message;
- all six BrokerService unary methods accepted for the dual-header Hub principal,
  denied with `PermissionDenied` for GE app Authorization, and rejected with
  `Unauthenticated` for a stripped/serverless-only credential; and
- raw upstream credential, Google JWT, exchanged Hub bearer, and SHA-256
  hex/base64/base64url encodings absent from combined stdout, stderr, and structured
  observations while safe replica/outcome fields remain.

The acceptance JSON keeps `TestTwoReplicaUserLifecycle`,
`TestCrossReplicaStreamCursor`, and `TestCrashLeaseBoundary` false and
`blocked-on-taskstore`. The taskstore HA startup sublayer is likewise false. The
actual GE capture remains false and `external-live-only`; therefore the envelope and
combined-startup parent rows remain false/partial.

## Test determinism correction

An isolated 10x run initially failed after 82.377s (rc=1): one
`TestColdReplicaAndRotation` iteration observed initial exchange count 4, expected 2.
The test Hub used a one-second token TTL. JWT `exp` is encoded at integer-second
precision, so truncation can make the effective lifetime approach zero; the initial
four requests could cross expiry before the explicit expiry phase.

The correction is test-only: `testHubTokenTTL = 3 * time.Second` and the test Hub uses
that TTL. At the cache-prime observation boundary, the harness captures the actual
exchange response, cryptographically verifies and decodes the minted Hub JWT, proves
JWT `exp == expiresAt <= upstreamExpiresAt`, calculates remaining time from the
current clock, and waits only until actual `exp + 200ms`. The hard post-expiry
re-exchange and `>=6` counter assertion remain unchanged. No production cache, JWT,
or token lifetime changed. A concurrent race/repetition attempt that caused
fixed-port bind collisions was discarded as invalid evidence and is neither a product
failure nor a gate result.

After both the expiry and allocator changes, fresh semantic runs were sequential and
each printed `HELPER_SCAN=ZERO` before starting:

```text
named normal, count=1       PASS  integration 8.635s
named race, count=1         PASS  integration 18.371s
named repetition, count=10  PASS  integration 89.145s
full bridge, count=1        PASS  integration 9.273s; bridge 35.484s;
                                  state 0.197s
root build                   PASS  go build -buildvcs=false ./...
```

Two independent `go test -v ./integration -run '^TestColdReplicaAndRotation$'
-count=1` processes were also started concurrently. Both passed (4.054s and 4.041s)
without a lock or serialization. Their disjoint helper ownership was:

```text
run 1: 48453/43827 48454/45941 48455/45027 48456/35843 48457/44255
       48523/36139 48524/33757
run 2: 48479/42647 48480/41689 48481/44985 48482/44523 48483/37489
       48521/36583 48522/39785
```

Each pair is PID/port. All 14 helpers logged `reaped=true`; the subsequent scan was
`HELPER_SCAN=ZERO`. This green replaces the discarded concurrent fixed-port attempt,
which failed with bind collisions and is not a product gate result.

The focused Hub GE/Google suite passed in 0.263s. The full Hub package run was not
green: `go test ./pkg/hub -count=1` returned rc=1 after 482.109s. Exact candidate and
detached exact-base (`346b1f74`) targeted commands reproduced five unrelated failures:

```text
TestGetEffectivePermissions_PrincipalConstraintTargetingGroup  group invalid
TestBypassCensus                                               one unauthorized site
TestAgentMentions_DoNotCreateUserNotifications                 0 items, want 1
TestCreateMessageEnumeration                                   one unstamped site
TestHandleAgentOutboundMessage_DMSyncBackfill                  no webchat_dm table
```

The DMSync failure is pre-existing intermittent: it failed in the candidate targeted
run (0.618s), failed at exact base (0.580s), and passed on a candidate repeat while the
other four remained red. It is not a deterministic-baseline claim. The implicated
base/candidate blobs are identical and their aggregate diff is empty:

```text
authz_test.go                         b290b4fd110df0b291942f1207531404441d7f86
bypass_census_test.go                 45c06aebbf0ac307f8b7852d5ac8744244d44cf4
project_messaging_policy.go           6d1549f4f07e135332fecf0c805cac2834e89e6c
chat_notifications_test.go            7bf7aee3cd553d2ada9942604a4d97cb5f4dc0f9
create_message_enumeration_test.go     28c040b9013638102c9fd488d02711aedc6ec4c5
handlers_conversation_send.go          0c14bbfd9c3cd9108440da744af709ddc4902345
handlers_agent_messaging_test.go       20549cc04af5673b108776c2203c5bd9eeeb5864
webchannel_store.go                    f76abd598e3e5d6a3f203951255b8f5330f686d6
git diff --exit-code 346b1f74 HEAD -- <these paths>  rc=0
```

## Root CI baseline block

`make ci` is **BLOCKED**, never reported passing. It reaches and fails
`check-conversation-upsert-guard`. Running the identical guard on exact main/base
`346b1f74` and on the combined tip returns rc=1 with the identical four findings:

```text
pkg/hub/handlers_conversations.go:487  CreateConversation
pkg/hub/handlers_conversations.go:502  AddParticipant
pkg/hub/handlers_conversations.go:693  AddParticipant
cmd/conversation.go:597                AddParticipant
```

The guard and both reported source files are byte-identical Git blobs:

```text
cmd/conversation.go                         405d84f2b8bd1f95331399937dac9e541f243c94
pkg/hub/handlers_conversations.go           dbe0d07a0df6c69209856d669c9c129b426077e2
hack/check-conversation-upsert-guard.sh      1bb70ae0a2833a870ea64492ba7f09277abca677
git diff --exit-code 346b1f74 HEAD -- <three paths>  rc=0
```

The guard was neither changed nor skipped. This phase does not touch the unrelated
conversation architecture.
