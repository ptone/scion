## What this changes

Three changes, all from ptone's review of the published single-node tutorial.

**1. `scion deploy-instance` is removed from the CLI surface.** The single-node tier now deploys
only via `scripts/single-node/deploy.sh`. That script was previously a 94-line wrapper whose last
line was `exec "$SCION_BIN" deploy-instance "$@"`; it now carries the deploy itself.
`cmd/deploy_instance.go` and its registrations in `cmd/root.go` and `cmd/cli_mode.go` are deleted.

The translation is direct: the Go command used no GCP client library — every call was a `gcloud`
subprocess or a plain HTTP request — so the bash implementation performs the same eight steps in
the same order (resolve identity; resolve project number; create the Instance via the `gcloud` v1
surface, which is required for `sandboxLauncher`; enable IAP via REST v2 `PATCH`; wait for IAP
reconcile; bind the IAP policy at region level; assert the perimeter; print the URL).

The nine flag names, their defaults, and the three required flags are unchanged.

**2. The tutorial no longer references a private image.** Six references to an image in a personal
project — including a pinned digest — are removed. The page now shows a placeholder in the
reader's own project.

**3. The tutorial gains build-your-own steps**, using the existing
`image-build/cloudbuild-omni.yaml` via `gcloud builds submit`.

## Two things worth a reviewer's attention

**The perimeter assertion is the point of the script.** This tier runs with
`invokerIamDisabled: true`, so IAP is the sole network perimeter. The deploy's final gate sends an
unauthenticated request and requires that it does not get through — it classifies the redirect
target and the IAP response header rather than checking a status code, and distinguishes an open
Instance (`UNPROTECTED`) from a dead container (Cloud Run's own `502`/`503` error page, where the
operator needs to be told the problem is their image, not their auth).

**`deploy.sh` is deliberately written as sourceable functions with a main guard**, with no side
effects at file scope. That is not style: it is the seam that keeps the deleted Go tests alive.
`cmd/deploy_instance_test.go` held 28 tests, five of which pinned the perimeter classifier against
stub HTTP responses; those cases are ported against the shell functions rather than lost. The IAP
audience format is still pinned against the hub's own `isSupportedIAPAudience`, now by reading the
format string out of the script so there is one authoritative copy.

## Testing

- `go build ./...` — pass
- `go test ./cmd/...` — pass
- The deleted Go test file had 28 tests. 22 carry over against the shell functions (several
  renamed), 4 are new, 26 total. Four of the six that did not carry over are Go-only helpers or are
  subsumed by the pin tests. Two are a real coverage gap: the step-6 IAP-binding print had two
  `doesn't panic` tests and now has none. Step 6 is informational and is not a gate.
- The audience pin was verified to **fail** when the format string in `deploy.sh` is wrong:
  changing `services` to `instances` turned 4 of 5 pin tests red; restoring it turned them green
- `shellcheck scripts/single-node/deploy.sh` — clean
- Live walk on a throwaway Cloud Run Instance in a test project: all eight deploy steps, health
  endpoint, API through IAP, project create, agent create, agent reaches `running` — pass. Instance
  deleted afterwards.
- The perimeter gate was exercised against all five response shapes with a stub server, and against
  two real unauthenticated public URLs, which it correctly rejects as `UNPROTECTED`. On the live
  deploy it passed from a real unauthenticated probe returning `302` to `accounts.google.com` with
  `x-goog-iap-generated-response: true`.

Failures in the full `go test ./...` run (`TestFixtureCoverage`, and in one run a `pkg/hub`
timeout) reproduce on the base commit and are not introduced here.

## Notes

The tutorial's Go toolchain prerequisite is removed: `deploy-instance` was the only use of the
`scion` binary in the page, so a reader now needs only `git` and `gcloud`. That also removes the
stale-binary failure mode the page previously had to document.
