# wave01fixtures — offline stopped-slot fixture helper (Wave01 "G1")

Steward-only developer tool. **Not product behaviour.** It inserts fixture
agent rows (stopped / stopped+crashed / stopped+limits_exceeded / error /
created) and offline (B1) runtime-broker rows into an **isolated, stopped,
checkpointed clone** of a hub SQLite database, so a Wave01 layout slot can be
restarted on that clone and read back through the real API.

It uses only existing store interfaces (`entc.OpenSQLite` →
`entadapter.NewCompositeStore` → `Migrate`, the offline-writer pattern of
`cmd/hub_secret_migrate.go` and `cmd/server_recover_authz.go`). It changes
nothing under `pkg/`, adds no endpoint, starts no server, dispatches no agent,
issues no credential or join token and never calls `UpdateAgentStatus`.

Normative acceptance: `wave01-fixture-helper-acceptance-FROZEN-r1.md`
(7000 B, sha256 `d03ca22a0bdf73ec8246eb3daf9f56d79b51b08216a3713e64eba45ceae5ea40`).
Source contract: inventory-findings rev 2 §3.3–3.4 (`g1-store-contract.md`,
7336 B, sha256 `9749b93399e46941f91ead67c3708c67e55cf90ffec2f6f99a39a0ead64a62a1`).

## What it writes

Per agent, in recipe order (brokers are written first):

1. One `WithTx`: `CreateAgent` + `ReplaceAgentIdentityKeys(id, projectID, api.IdentityKeysFor(slug, slug))`.
2. Only when the recipe sets `exitCode`/`exitReason`: `GetAgent` (StateVersion 1)
   then `UpdateAgent` setting just those fields (CreateAgent does not write them).
3. `SetRunIntent(stopped)`, outside any transaction.

Fields written: `ID`, `Slug`, `Name = Slug`, `Template`, `ProjectID`, `Phase`,
`Activity`, `Message`, `TaskSummary`, `Labels`, `Detached = true`,
`CreatedBy = OwnerID = admin`, `Ancestry = [admin]`,
`AppliedConfig{AgentRole, CreatorName = admin email}`. Everything else keeps
its zero value or schema default: `LastSeen`, `LastActivityEvent`, `StartedAt`,
`RuntimeBrokerID`, all `launch_*`, `start_claim_*`, `deletion_*` and
reincarnation columns, and run IDs stay NULL or empty.

Per broker: `CreateRuntimeBroker{ID, Name, Slug, Version?, Labels?}`. Status
`offline`, connection `disconnected` and a NULL heartbeat come from the schema
defaults and **cannot be set from the recipe**.

## Allowed states (H3.4)

| phase   | activity          | required                    | forbidden               |
|---------|-------------------|-----------------------------|-------------------------|
| stopped | ∅                 | —                           | exitReason              |
| stopped | crashed           | exitReason=crashed, exitCode | —                      |
| stopped | limits_exceeded   | exitReason=limits_exceeded, exitCode | —              |
| error   | ∅                 | exitReason (valid), message | —                       |
| created | ∅                 | —                           | exitCode, exitReason    |

Every other pair is refused, including `stopped/completed`, any `running`
pair, and `provisioning`/`cloning`/`starting`/`stopping`. All agents get
RunIntent `stopped`.

## Recipe schema (`scion.wave01.fixtures/v1`)

JSON. It is decoded strictly: unknown fields and trailing data are rejected,
so `lastSeen`, `startedAt`, `runtimeBrokerId`, `runIntent`, broker `status`,
`lastHeartbeat`, `joinToken` and similar fields cannot be expressed.

```jsonc
{
  "schema": "scion.wave01.fixtures/v1",
  "admin":    { "userId": "<uuid>", "email": "<existing admin email>" },  // must exist; role admin; email ↔ id agree
  "projects": [ { "id": "<uuid>", "slug": "<stored slug>" } ],            // must exist; slug must match
  "agents": [ {
      "id": "<uuid>",               // canonical lowercase, unique across the recipe
      "projectId": "<uuid>",        // must be listed in projects
      "slug": "ledger-reconciler",  // ValidateDisplayName key == slug; unique per project; Name = Slug
      "template": "default",        // non-empty slug
      "agentRole": "baseline",      // none | readonly | baseline | full
      "phase": "stopped", "activity": "",           // see table above
      "exitCode": 137, "exitReason": "crashed",     // optional/required per table; exitCode 0-255
      "message": "...", "taskSummary": "...",       // trimmed; ≤400 / ≤600 chars
      "labels": { "team": "payments" }              // ≤8; key ≤63, value ≤128
  } ],
  "brokers": [ {
      "id": "<uuid>", "name": "build-runner-eu-west1-a", "slug": "build-runner-eu-west1-a",
      "version": "optional", "labels": { }          // slug unique and canonical; name unique (case-insensitive)
  } ]
}
```

Example (synthetic IDs only): [`examples/phase1-slice.json`](examples/phase1-slice.json).

## Fail-closed checks

The helper refuses to run, and writes nothing, when:

- the recipe fails validation (all problems are listed together);
- `--attest-stopped-clone` is empty;
- `--db` is not absolute, does not exist (it never creates a DB), is a symlink
  or non-regular file, or is empty;
- `<db>-wal` is non-empty, meaning the clone was not checkpointed;
- `<db>.wave01-fixtures.run` exists, meaning this clone was already used;
- preflight fails: the admin or a project is missing or mismatched, or any
  recipe agent ID, `(project, slug)`, identity key, broker ID, broker name or
  broker slug already exists.

Just before the first write it creates `<db>.wave01-fixtures.run` with
`O_EXCL`. The tool never removes it, so a clone can only ever be used by one
run.

On a write failure it stops, reports `outcome: partial-discard-clone`, and
lists the rows committed so far and the record that failed. **Discard that
clone. Do not rerun over it.** The rerun is refused anyway.

After writing, it reopens the store and verifies every row against the recipe
and the forbidden-field rules. These include identity keys, no agent
credential, no delegation edge, no broker secret and no join token. It then
runs `PRAGMA wal_checkpoint(TRUNCATE)` and records the after-digests.

The attestation is recorded as an **operator statement, not proof**. The flag
cannot show that no other process has the file open. The steward supplies
process and checkpoint evidence (H2).

## Build (provenance H1)

Build from the exact helper commit, in a normal clone so VCS stamping works:

```sh
git rev-parse HEAD                                   # helper commit
sha256sum hack/wave01fixtures/*.go                   # helper source digests
git rev-parse 4a253489ebe3298fcfe4d7271b3642a5578b2b31:pkg/ent/schema   # schema tree hash at the served backend
git diff --stat 4a253489ebe3298fcfe4d7271b3642a5578b2b31 HEAD -- pkg/   # must be empty
GOMEMLIMIT=6GiB go build -p 1 -trimpath -o /data/wave01fixtures ./hack/wave01fixtures
go version; sha256sum /data/wave01fixtures           # also self-reported in the manifest
```

This is the helper binary only. The served hub binary is not rebuilt or
replaced.

## Run (steward / slot owner only)

1. While the slot is running: test-login the admin and users, create projects
   through the API, and run the existing groups seed. Note the admin user ID
   and the project IDs and slugs for the recipe.
2. Stop the slot's hub. Record process evidence (no `scion server` or hub
   process for that slot; `fuser`/`lsof` on the DB is empty).
3. Checkpoint the slot DB and confirm `hub.db-wal` is absent or 0 bytes. Copy
   `hub.db` to a new **slot-owned** path, never shared with another slot.
   Record sha256 of the source and the clone.
4. Validate: `wave01fixtures -validate-only -recipe recipe.json`.
5. Run:
   ```sh
   /data/wave01fixtures \
     -db /data/<slot>/fixture-clone/hub.db \
     -recipe recipe.json \
     -attest-stopped-clone 'slot=<slot>; hub stopped <time>Z; checkpointed; isolated slot-owned clone' \
     -manifest /data/<slot>/fixture-clone/manifest.json
   ```
   Exit 0 with `outcome: ok` and `verified: true` is required. Any other
   outcome means: discard the clone.
6. Put the clone in place as the slot DB and restart the slot's existing hub
   binary (same `binarySha256`). Startup runs `BackfillRoleBindings` and the
   other marker-gated backfills.
7. API readback (H5) as the synthetic admin: `GET /api/v1/projects`,
   `/api/v1/projects/{id}/agents`, `/api/v1/agents/{id}` for each agent and
   `/api/v1/runtime-brokers`. Compare id, slug, project, phase, activity,
   exitReason and broker status with the recipe. Also confirm no `running`
   phase, no live activity, a null `lastSeen` and no online broker.

The manifest is non-secret. It holds the recipe sha256, before/after
DB/WAL/SHM digests, the checkpoint result, written IDs, state mix, the helper
binary digest, the Go version and the H6 divergences. It does not record the
source commit or the schema tree hash; the steward adds those from the build
step.

## Known divergences (H6, also in every manifest)

- No DelegationEdge and no mutation audit row. An edge needs a
  credential-derived EffectCeiling, so it cannot be honest offline. Fixture
  agents must never be started.
- No quota reservation: broker agentCount and project agent limits
  under-count. Project card counts are live and correct.
- No events are published.
- Created/Updated are the insertion time. Ages are honest only relative to the
  helper run, and updated-time order between helper rows is a **tie**, so
  oracles must not depend on it.
- Agents have no runtime broker. Brokers are offline with no heartbeat,
  registration, join token or secret.
- Agents with exit fields have StateVersion 2 (one `UpdateAgent`). The others
  have StateVersion 1.

## Tests

`go test ./hack/wave01fixtures` runs the helper against real on-disk SQLite
stores. Nothing is mocked; the admin and projects stand in for API-created
rows. The tests cover:

- the vertical slice and all five allowed kinds, with an independent raw-SQL
  readback (NULL heartbeat/live columns, `run_intent = stopped`, identity
  keys, no join token or secret);
- rerun refusal (by marker, and by preflight with the marker removed);
- binding mismatches;
- pre-existing slug, identity key and broker name;
- target preconditions (missing DB never created, relative path, symlink,
  non-empty WAL, missing attestation);
- honest partial-failure reporting, using an injected SQLite trigger;
- the full validation matrix and strict decoding of forbidden fields;
- negative checks showing `Verify` catches each forbidden state when it is
  written behind the helper's back.
