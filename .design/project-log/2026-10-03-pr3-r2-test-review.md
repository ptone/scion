# PR3 Substrate Runtime Integration — Round-2 Test Review

**Date:** 2026-10-03
**Branch:** scion/review-pr3-r2-test (reviewing origin/scion/substrate-pr3-restack-wip @ 60ae25d9, range ee26f1275..60ae25d9)

## Summary

The round-2 QA review of the final substrate integration PR returned **REQUEST CHANGES**. The full report is in `scratchpad/projects/substrate-integration/reviews/review-pr3-r2-test.md`.

## Tip-vs-main triage

I ran the scrubbed `go test` on the tip and on a detached `origin/main` (2220c46de). The failing sets are identical: both trees show only the default 10-minute package timeout in `pkg/hub`. **There are no failures on the tip that main does not also have.** The new packages `pkg/runtime/substrate` and `pkg/sciontool/substrate` pass.

## Findings

- **HIGH:** commit `afbfd8d0b` removed the `fakeWhoamiAsScion` fixture. As a result, 8 tests in `pkg/sciontool/substrate` exec require a real `scion` OS account and will fail on `ubuntu-latest` CI. The sandbox hides this because its user is named `scion`. I reproduced it by stubbing `execUserLookup`.
- **HIGH:** the `reflect.DeepEqual` branch of `ValidateOperatorOnlySubstrateProfile` has no test guarding it. That branch catches a project that overrides an operator-defined substrate runtime (audit-H1). A one-token mutation leaves the suite green. Production behaviour is correct, which I verified with a probe test.
- **MEDIUM:** the A2 timeout test does not check that grandchildren are killed. It passes only because its 5s threshold equals `execWaitDelay`. With `Cancel` removed and `WaitDelay` set to 2s, the test passes while `sleep 30` grandchildren survive.
- **LOW:** the reaper regression test does not fail when the fix is reverted. The A5 refuse-to-start call site is untested. There is no broker-level restart test for an agent with no record. Some edge-case rows (Vertex region, inet_aton IP literals) are missing. Supplementary groups are now dropped, which is a behaviour change and untested.

## Supplementary run

I re-ran `pkg/hub` with `-timeout 60m` on both trees. It passes on both (tip: 16,245 passing test events; main: 16,468) with zero failures.

## Learnings

- Run substrate exec tests as a non-`scion` user, or stub `execUserLookup`, before trusting a green sandbox run. Tests that depend on the identity of the host user pass in Scion containers and fail on GitHub runners.
- The brief's triage command hits the default 10-minute timeout on `pkg/hub` on both trees. To compare at test level inside `pkg/hub`, add `-timeout 60m` and run it separately.
