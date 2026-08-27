# Brief: rebase the P0 dev-auth guard branch onto current main

Author: sn-impl-arch (architect). Date: 2026-08-27.
Authorised by ptone, 00:15.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find on disk, **stop and message me** — do not improvise
around it. That instruction has already paid for itself once on this project: the last developer
caught a wrong file classification in my brief because they checked instead of complying.

---

## 1. What you are doing, in one sentence

Rebase `scion/security-fix-p0-s1` onto current `main` and force-push it, so that upstream PR
**GoogleCloudPlatform/scion#1307** stops being `CONFLICTING`.

## 2. What you must NOT do

Hard constraints, not style preferences.

- **Do not change what the guard does.** This is a rebase, not a redesign. The security semantics
  must come through unchanged. If the only way you can see to resolve a conflict is to weaken or
  broaden the guard, **stop and message me**.
- **Do not touch any other branch.** Not `scion/sn-tier`, not `scion/dev-rebase-1294`, not
  `scion/sn-dev-ready`, not `scion/sn-ws-mount`. Force-push `scion/security-fix-p0-s1` and nothing
  else.
- **Do not merge anything.** Merging is ptone's gate.
- **Do not open, close, or edit any PR.** #1307 updates itself when you force-push.
- **Do not delete any Cloud Run Instance or agent.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`,
  `q2-control` and `sn-ready` are explicitly do-not-delete.
- **Do not remove `.design/project-log/p0-security-fixes.md`.** I have flagged it to ptone
  separately as something that probably should not land upstream. It is his call, not ours, and
  not part of this task.

## 3. What the branch does, so you can judge a conflict resolution

`devAuthMiddleware` auto-logs-in **every cookieless request as admin**. Bound to `0.0.0.0` — the
default in hosted mode — that is a publicly reachable unauthenticated admin UI.

The fix blocks that combination at startup, in two places:

- `cmd/server_foreground.go` — `initWebServer` returns an error.
- `pkg/hub/web.go` — `NewWebServer` fails fatally.

Both layers matter. **Do not collapse them into one** on the grounds that they look redundant.

## 4. Facts I verified, so you do not re-derive them

Checked against the remote at 00:12–00:15 on 2026-08-27.

- #1307: `mergeable=CONFLICTING`, `mergeStateStatus=DIRTY`, head `scion/security-fix-p0-s1`,
  base `main`, author ptone.
- Branch vs upstream main: `ahead=1, behind=3`. Merge base **`d663025b`**.
- The branch is **6 files, +259**.

**Main gained 3 commits since `d663025b`:**

| Commit | What |
|---|---|
| `25714622` (#1297) | antigravity harness API key auth |
| **`1d1e4d76` (#1300)** | **WebServer reads live operational settings via `AccessSettingsProvider`** |
| `d6fd3204` (#1296) | caller verification on broker-scoped handlers |

**The conflict is #1300.** It rewrote `pkg/hub/web.go` by **+74 −31** and reworked the area around
`NewWebServer` — exactly where this branch installs its fatal guard. It also changed:

- `cmd/server_foreground.go` **+3 −15**
- `pkg/hub/web_test.go` **+109 −15**

All three overlap with the branch's own changes. **This is a semantic rebase, not a textual one.**

### What #1300 actually did, so the resolution is informed

It added an `AccessSettingsProvider` interface (`web.go:117`) with `AdminEmails()`,
`AuthorizedDomains()` and `UserAccessMode()`; a nil-safe field (`:170`); private accessors
(`:574`, `:583`, `:592`); and `SetAccessSettingsProvider` (`:602`). `NewWebServer` is at `:438`.

`WebServer` no longer holds a by-value config snapshot for those three fields — it reads them live
through the provider. **That is the shape you are rebasing onto.**

## 5. The steps

1. Fetch and rebase `scion/security-fix-p0-s1` onto current `origin/main`.
2. Resolve the conflicts in the three overlapping files. Keep the guard's behaviour identical.
3. Build.
4. `go test ./...`. It must pass, or fail only in ways you can **show** are pre-existing on `main`.
   `internal/fixturegen` is known pre-existing — verify rather than assume.
5. **Confirm the guard still actually fires.** The branch ships tests for exactly this
   (`cmd/server_foreground_test.go`, `pkg/hub/web_test.go`). They must still exist and still pass.
   If a conflict resolution silently dropped a test, that is the failure mode I most want caught —
   it happened on this project once already.
6. Force-push `scion/security-fix-p0-s1`.
7. Confirm #1307 flips off `CONFLICTING`.

## 6. Report back

Message `sn-impl-arch` with:

- The conflict resolution you chose in `pkg/hub/web.go`, in a sentence or two. This is the one I
  want to see, because it is the one with judgement in it.
- `git diff --stat` against main.
- Test results.
- #1307's `mergeable` / `mergeStateStatus` after the push.

If the resolution required any judgement call about the guard's placement or scope, **say so
explicitly** rather than burying it. I would rather hear "I had to make a choice here" than
discover it in review.
