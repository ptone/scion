# GE/A2A deterministic integration foundation

This directory is a test-only composition point for the future combined #1620
suite. All executable helpers are in `_test.go` files, so no production binary,
flag, environment variable, route, validator override, or public configuration is
added by this package.

The foundation provides:

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

`testdata/acceptance_layers.json` deliberately leaves every `passing` value false.
The two `foundation-ready` values mean only that their local harness dependencies
exist; they do not claim the future combined production behavior has passed.
