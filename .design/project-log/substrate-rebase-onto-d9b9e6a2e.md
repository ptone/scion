# Substrate rebase onto d9b9e6a2e

Rebased the substrate integration commits onto a newer upstream commit
(d9b9e6a2e). Most of the substrate commits applied without incident. Two
areas needed more than a textual merge to reconcile correctly, and one
upstream interface addition needed a real implementation rather than a
mechanical carry-forward.

## Stop-lookup error handling

Upstream landed its own fix for the same underlying bug substrate's own
restart-safety work had already fixed specially: a project-scoped stop
whose container lookup failed (a transient listing error, an ambiguous
match) was silently treated as "not found," reporting a false success
instead of surfacing the failure. Upstream's fix changed
`projectScopedTarget` to return `(string, error)`, propagating
`LookupContainerID`'s own error for every runtime.

`LookupContainerID` does not cover two properties substrate's own fix relies
on: auxiliary-runtime list-failure strictness (an auxiliary runtime's list
error is silently skipped in favor of the next one, so a transient failure
on the one auxiliary runtime that actually holds the agent can still look
like a genuine not-found) and manager coherence (the stop dispatches through
a manager re-resolved by a second, independent lookup, which can land on a
different runtime than the one that produced the matched container ID).

Resolution: non-prober brokers use upstream's `projectScopedTarget` exactly
as it ships. Brokers with a `RecordlessActorProber` runtime registered
(substrate) use `projectScopedTargetErr`, a stricter, independent
re-implementation that scans auxiliary runtimes in a fixed order (a match
found elsewhere is authoritative over an earlier auxiliary error) and
returns the manager whose list call actually produced the match, so `Stop`
is always dispatched through that same manager. Errors are typed the same
way upstream's are (a listing-failure sentinel, an unwrapped ambiguous-match
error, a not-found sentinel for a matched record with no container ID) so a
caller can use the same `errors.Is` check against either path's result. A
substrate-only sentinel that predated upstream's fix was dropped in favor of
upstream's own.

One behavior changed as a direct consequence: a matched agent record with no
resolvable container ID now folds into the idempotent "not found" response
on the prober path too, matching upstream's own handling of that case,
instead of reporting a failure. This is unrelated to the restart-safety
scenario the prober path exists for (a runtime process restart leaving a
record-less actor behind) — that path is unchanged: an ambiguous match, an
auxiliary listing failure, and a genuine record-less actor after a restart
all still produce their original results.

## ExecWithStdin

Upstream added `ExecWithStdin(ctx, id, cmd, stdin io.Reader) (string, error)`
to the shared runtime interface, so that a caller delivering a secret (the
reset-auth token) pipes it through the exec'd command's stdin instead of
embedding it in the command's argv, where it stays readable from the host
process's own command line for the life of the exec. Every other runtime
implementation gained this method; substrate's did not exist on the base
this interface change landed on.

Substrate's own exec transport is an HTTP call to the in-actor control
server (`ExecRequest{Argv, User, TimeoutS}`, no stdin field). Implemented
the same protection for substrate: `ExecRequest` gained an optional `Stdin`
byte field, and `ExecResponse` gained a `StdinSupported` flag the control
server sets on every request it handles. A non-empty `Stdin` is piped into
the exec'd command's standard input instead of being interpolated into the
command line, bounded by the same request-body size limit the rest of the
request already uses — no new unbounded read; the client's own stdin size
cap is derived arithmetically from that same server-side limit, exported for
exactly this purpose, rather than a second, independently-chosen number:
base64, which is how stdin travels in the JSON body, expands every three raw
bytes into four encoded ones, so the client reserves a fixed allowance out
of the server's limit for the rest of the request and caps raw stdin at
three quarters of what remains. The client never includes the content it
read in an error.

A control server old enough to predate the `Stdin` field would otherwise
silently ignore it, run the real command with nothing attached to its
standard input, and still report a clean exit — which for the reset-auth
write-then-rename script means an empty file gets renamed on top of a
working token, a destructive outcome an after-the-fact check on the real
exec's own response is already too late to prevent. The client therefore
sends an uncached, no-op probe exec (a single `true` with a one-byte
payload) before the real command every time, and requires that probe's own
response to confirm `StdinSupported` — the real command is never sent at
all if it doesn't. The check on the real exec's own response is kept as a
second layer regardless. Plain `Exec` sends no stdin and is unaffected by
either check.

The probe names version skew in its error only when that specific check —
the control server answered but never confirmed `StdinSupported` — is what
failed. A transport error reaching the probe, or an authorization rejection,
has nothing to do with an old image and is passed through unchanged rather
than being misreported as version skew.

A correction to an earlier note in this entry: the client-side capability
probe and cap derivation were first covered only against a fake control
server, on the stated (incorrect) premise that a real client-to-real-server
test wasn't reachable from this package without root — the real control
server's bootstrap path prepares a root-owned scratch directory. That premise
was wrong: the server package exports a seam for exactly this
(`SetPrivateRootTmpDirForTest`), already used by another package's tests, and
a missing hooks directory is already a no-op. A real client against a real
server, both over `httptest`, is now covered directly, including the probe's
own happy path (a real `true` invocation reaching a real server) and the
exact stdin cap accepted through the real server's own request-size limit,
not just the client's.

## Test fallout from the stop-lookup reconciliation

Running the full test suites (not the rebase mechanics themselves) surfaced
two more points the textual merge could not see:

- A test asserting that a non-prober broker's stop lookup failure produces
  the idempotent 202 was pinning the exact behavior the stop-lookup
  reconciliation above deliberately changed. Renamed and re-asserted against
  the corrected outcome (a real error), with the failing-lookup call count
  kept as a sanity check that the path under test actually ran.
- A file-and-line-keyed guard over root-context exec call sites had its
  tracked line number invalidated by the `ExecWithStdin` change, which added
  lines above the guarded call site. Updated the tracked line; no new
  unguarded exec call site was introduced.

## Verification

Full build passes. The runtime, runtime broker, sciontool, and sciontool
command packages all pass their existing and updated tests, including tests
for the stdin protocol: a non-empty `Stdin` reaches the exec'd command's
real standard input through the real HTTP handler and a real subprocess, on
both sides of the client/server boundary independently and together —
server-side (`pkg/sciontool/substrate`'s `TestExec_StdinRoundTripsThroughRealCommand`)
and end to end (`pkg/runtime`'s real-client-against-real-server test, which
also exercises the probe's own happy path); the secret never appears in the
argv the handler hands the process (`TestExec_StdinNeverReachesSpawnedArgv`,
which spies on the real exec-boundary seam while still running the real
handler and a real subprocess); the exact stdin cap is accepted both by the
client's own check and, separately, by the real server's own request-size
limit; a marshalled request at the cap is checked directly against the
server's byte limit, so a future change to either constant the cap is
derived from would be caught even if the end-to-end cases happened to still
pass; an oversize payload exactly one byte over the cap is rejected without
echoing it back, both server-side and client-side; a response missing
`StdinSupported` while stdin was sent is a client error, with the
version-skew wording confirmed present only for that specific failure and
absent for a transport error or an authorization rejection reaching the same
probe; and plain `Exec` sends no `Stdin` field. The probe's version-skew
detection itself is proven two ways: against the package's usual fake
control server, and separately against a hand-written handler that decodes
the real, shared `ExecRequest`/`ExecResponse` types the way an
implementation predating `Stdin` would, confirming that in either case the
real command is never sent — only the probe's own no-op reaches the control
server. The hub package's existing project-scoped agent authorization tests
pass unchanged on the rebased tree, and a live run of a standalone hub
server against a real database confirmed the property those tests guard: an
access token scoped to one project cannot list or read another project's
agent.

## Test hermeticity: the real-exec tests run as any user

The tests that drive a real exec through the control server's own handler —
the client-to-real-server test above and five server-side tests exercising a
real subprocess with `"scion"` as the exec user, either explicitly or via
the handler's own default — depend on the exec-as-user wrapper script's own
identity check: `if [ "$(whoami)" = "$1" ]; then exec sh -c "$2"; else exec
su - "$1" -c "$2"; fi`. The direct branch runs unconditionally on any user;
the `su` branch only succeeds unprivileged when the calling process already
is the target user, since switching to an arbitrary different user needs a
password. On an ordinary CI runner — some other, unrelated user — `whoami`
never matches `"scion"`, so every one of these tests fell into the `su`
branch and failed there. (The handler also validates the request's `User`
field against an allowlist of `"scion"` or `"root"`, but that check is not
what blocks these tests: they already send an allowed value. It only
matters for a value outside the allowlist, which none of these tests use.)

Both the value `whoami` reports and the paths `sh`/`su` resolve to come from
one package-level indirection, already provided for exactly this kind of
substitution. Installing a stand-in that reports `"scion"` for `whoami`
while leaving `sh`/`su` resolution untouched makes the wrapper take its
direct branch — the same branch that already runs when the process
genuinely is `"scion"` — regardless of who is actually running the test, so
these tests now run for real on any user instead of being skipped. The one
exception is the client-to-real-server test, which lives in a different
package with no reachable indirection of its own; a new, minimal, exported
hook was added for exactly that cross-package case, mirroring an existing
one-purpose test hook already in this codebase, with its own guard proving
no production code path calls it. Proven by actually running the whole
affected surface as a separate, unprivileged, no-`sudo` user: every one of
these tests, plus the client-to-real-server test, passes for real rather
than being skipped, and the surrounding packages report success. The
probe's own version-skew detection and the client-side cap enforcement are
unaffected either way — those are proven separately against fakes that
never depend on which OS user is asking or spawn a real subprocess at all.
