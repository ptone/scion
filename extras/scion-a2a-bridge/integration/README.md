# GE/A2A deterministic auth + transport integration

This directory is the test-only composition point for the combined #1620
suite. All fake endpoints and process helpers are in `_test.go` files, so no production binary,
flag, environment variable, route, validator override, or public configuration is
added by this package.

The executable auth+transport phase provides:

- real subprocess topology with distinct PIDs, deterministic reserved loopback
  ports, TCP readiness, shared cancellation, process reaping, sanitized logs, and
  structured observations;
- an HTTP reverse-proxy process that alternates new sequential requests across two
  backends while a streaming response remains on the backend selected when that
  request began;
- synthetic identity and A2A envelope category fixtures with their schema sources;
- credential redaction for bearer values and their stable SHA-256 encodings;
- unique PostgreSQL run/schema naming and reverse-order teardown hooks; and
- a machine-readable status map for the eight planned deterministic test layers.

The real-process tests compose a production Hub exchange handler over durable
SQLite identity bindings, a pinned fake Google JWKS process, two independent
bridge processes, the production A2A SDK handler/executor, authenticated gRPC
control transport, and the deterministic alternator. Run the proven layers with:

```sh
go test ./integration -run 'Test(GEEnvelopeCompatibility|ColdReplicaAndRotation|ControlPlanePrincipalIsolation|CombinedStartupMatrix|CredentialRedaction)$'
```

Run the foundation without cloud credentials:

```sh
go test ./integration
```

The PostgreSQL socket test is optional and skips unless `TEST_DATABASE_URL` is set:

```sh
TEST_DATABASE_URL='postgres://...' go test ./integration -run TestPostgreSQLSchemaAllocator
```

That test creates and drops only its uniquely named schema. `DatabaseName` is a
deterministic name available to a future database-level provisioner; this foundation
does not assume permission to create databases. The schema models bridge task/event
storage only. Hub identity persistence remains an independent dependency and must not
be inferred to share this connection, schema, transaction, or lifecycle.

`testdata/acceptance_layers.json` records local auth+transport proof separately
from external-live and taskstore-dependent work. The lifecycle, stream cursor,
crash/lease, and taskstore startup rows remain `blocked-on-taskstore` and false.
The envelope and combined-startup parent rows also remain false because their
external-live/taskstore sublayers are not part of this phase.
