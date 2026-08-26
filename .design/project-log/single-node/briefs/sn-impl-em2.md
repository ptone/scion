# Brief: sn-impl-em2 — phases P4, P4a, P5

## Role

You are the **engineering manager** for phases **P4, P4a and P5** of the Cloud Run
Instances + Sandboxes single-node runtime. You own the dev/review cycle: dispatch
developers, dispatch fresh reviewers, land commits. You do not write the design —
it exists, it is authoritative, and it is now **fully empirically validated**.

Dispatched by `sn-impl-arch` on ptone's instruction, 2026-08-26.

**You are the second EM on this project.** `sn-impl-em` owned P0–P3, completed, and
exited. Nothing is inherited except the branch and the design doc — read both.

## Where you are starting from

**Branch: `scion/dev-rebase-1294`, head `8a7852f2`.** This is the integration branch.
It contains P0–P3 rebased onto upstream `a34deb91` (PR #1294). The rebase was
verified end-to-end: zero conflicts, build clean, the P3 stopgap intact.

One caveat you will hit and should not chase: `internal/fixturegen/fixturegen_test.go`
fails with `expectedTableCount = 42` against a 46-table fixture. **This is a
pre-existing upstream failure** — it fails identically on unmodified `a34deb91`. Do
not let a developer "fix" it as part of your phases; it is not ours.

**The branch has been sitting since 2026-08-25 and decays as upstream moves.** If the
rebase is stale by the time you start, redo it before dispatching anyone — do not
have three developers each resolve the same conflicts.

## The design document is the authority

`/scion-volumes/scratchpad/projects/single-node/cloudrun-instances-sandboxes.md`

**Read §0 first** — the revision log, and specifically the "State of the design,
2026-08-26" paragraph, which tells you what has changed and what is still open.
Then the sections your phases depend on:

- **§4.4a-rev** — the resolved control-plane design. This is P4. It is the single
  most important section for you and it *supersedes* §4.4 and §4.4a; the struck text
  above it is history, not instruction.
- **§4.5** — state store. P4 deletes a field from it; see below.
- **§4.8 / §4.8b** — the `RuntimeCommand()` leak and the `pty_handlers.go` branch.
- **§5.2** — what Tier 0 requires us to build. This is P5.
- **§9** — the phase table, including P4a's rationale for existing separately.
- **§10** — acceptance criteria.

**If the design is wrong or underspecified, do not improvise — message
`sn-impl-arch`.** Design changes go through the architect and get written back into
the doc, so the next person reads the decision rather than rediscovering it. This has
already paid for itself twice on this project.

## Your scope

### P4 — the `sandbox exec` control plane

`Attach` / `GetLogs` / `Exec`, and the `pty_handlers.go` branch, each carried into the
sandbox via `sandbox exec`. **Browser terminal works** at the end of this.

**This phase got simpler after empirical validation, not harder.** The design
originally called for a tmux socket bind-mount (dead — AF_UNIX does not cross the
boundary) and then for an inner `script -qfc` PTY wrapper (unnecessary — a
launcher-side PTY propagates on its own). What survives is smaller than either:

```
sandbox exec <id> --env TERM=xterm-256color -- tmux attach -t scion
```

driven by the launcher's existing `pty.StartWithSize`. **No mount, no `TMUX_TMPDIR`,
no `script`, no `util-linux` dependency, no double PTY.** It is simpler than the
Docker path.

Five things that are load-bearing and will cost you a debugging cycle each if missed:

1. **`--env TERM=xterm-256color` is required.** Without it the inner tmux sees
   `TERM=dumb` and exits with *"terminal does not support clear"* — which reads like
   a PTY failure and is not one. Require a code comment saying so; the next person to
   see that error will otherwise re-litigate the whole PTY question.
2. **SIGWINCH does not cross.** PTY *fds* propagate; PTY *signals* do not. Resize is
   a **second, out-of-band exec**: `tmux resize-window -t scion -x <W> -y <H>`,
   driven by the launcher's SIGWINCH handler. **Not `refresh-client -C`** — that
   needs a control-mode client and is the wrong tool; the spike confirmed it.
3. **Delete `sandboxStateEntry.TmuxSocket`** (`json:"tmux_socket"`), along with
   `scionPaths.tmuxDir` and the `mkdir` that creates it. That path will now never be
   populated, and a persisted field holding a plausible-looking wrong path is worse
   than an absent one — P4's own attach code would read it, find nothing, and
   misdiagnose. No migration cost: Tier 0 is pure ephemeral.
4. **Drop `TMUX_TMPDIR` entirely, not just the mount.** Keeping the env var while
   removing the mount still *works*, and is the worst of the three options: identical
   behaviour plus a misleading signal, and a divergence from every other runtime
   (`common.go:480`, `k8s_runtime.go:934` both use the default socket).
5. **`PATH` is empty inside a sandbox** (§3.2c). Use absolute paths in anything you
   exec.

Measured, so you do not need to re-derive them: interactive keystroke latency p50
32 ms / **p95 37 ms** (threshold was 150 ms); per-exec-call p95 114 ms; an idle
attached exec survived **32 minutes** unimpaired. If your implementation is
dramatically worse than those numbers, that is an implementation problem, not a
platform limit.

### P4a — timeout-bounded `Delete`

**Split from P4 deliberately, and I want it to stay split.** It is a
teardown-correctness fix on the *normal* Tier 0 lifecycle — every redeploy deletes the
entire fleet — and attaching it to the terminal feature is how it slips as "polish."

`sandbox delete --force` **never returns.** Not on a busy sandbox, not on one running
only `sleep 3600`, not with or without an exec attached. This is a platform defect,
reported to the Cloud Run team; full write-up in
`defect-sandbox-delete-hang.md` (revision 2 — read it, it is short).

The implementation:

- Issue `--force`, **bound it with a timeout, and treat the timeout as success.**
  Deletion *is* effective despite not returning — the sandbox really is gone
  (`sandbox exec` on it reports "not running").
- **Reap the orphaned `runsc … delete --force` process** rather than waiting on it.
  Orphans do become zombies and are reaped eventually, but do not rely on that.
- **Never fall back to plain `delete`.** It refuses in 209 ms *and kills the sandbox
  anyway*, leaving live `runsc-gofer`/`runsc-sandbox` processes behind a CLI that
  reports "not running". A caller that correctly handles the error and retries is
  operating on a sandbox the CLI has already disowned. This is the more dangerous of
  the two defects because it does not announce itself.
- The timeout value is **being picked blind** — we have no data on the distribution
  because it never completes. Make it configurable, log when it fires, and say in the
  code comment that the number is a guess.

**OQ-16 is open and it is yours to close or explicitly accept:** every observation is
a *serial* delete. Fan-out is our actual pattern. If the hang is contention-related,
concurrent teardown could be qualitatively worse rather than N× slower. Either test it
on a throwaway Instance before P4a ships, or ship with a documented concurrency cap
and say you chose not to test it. Do not ship silently assuming it composes.

### P5 — Tier 0 honesty

Small. Permit workspace writes under an explicit ephemeral profile, add the UI banner,
keep the ephemeral-path warning (§5.2). The point of this phase is that the tier
should be honest about what it loses on redeploy — §5.1 has the exact inventory, and
it belongs in user-visible text, not only in the design doc.

## Not yours

**P6 and P7 are not dispatched.** P7 is conditional on OQ-2 (hairpinning) which is
still open with the Cloud Run team.

**Do not touch the IAP question.** A separate spike (`spike-iap`) is empirically
testing whether `Instance.iapEnabled` is honoured (OQ-15), running in parallel and
deliberately independent of your work. If it comes back positive it simplifies §4.9a,
which is a P6 concern, not a P4/P4a/P5 one. It should not perturb you; if you think it
does, message `sn-impl-arch` rather than waiting.

## Conventions

- **Review cycle:** review → fixes → **fresh** reviewer. Never send fixes back to the
  reviewer who reviewed the previous round. Fresh `scion start`, no reuse.
- **Non-blocking findings** must be fixed or explicitly declined with reasoning. Never
  silently dropped. Note the standing lesson in
  `/scion-volumes/scratchpad/coordinator-conventions.md`: "pre-existing" is not the
  same as "not our problem" — if your change makes a pre-existing defect newly
  reachable, it is in scope.
- **Push your own work branches; never push the integration branch.** Merging to
  shared ground is your gate, exercised deliberately.
- Full standing rules: `/scion-volumes/scratchpad/coordinator-conventions.md`.

## Cloud access

Project `ptone-experiments`, region **`us-east4`** (`us-central1` is
capacity-exhausted for Instances). Credentials come from the **metadata server** — no
key file. Container SA `scion-my-grove@deploy-demo-test.iam.gserviceaccount.com` holds
Token Creator on `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.

**Do not print access tokens to stdout** — this has happened before on this project.

Agent containers ship a stale gcloud. Before any `gcloud run instances` command:
`bash /scion-volumes/scratchpad/update-gcloud.sh` (2–4 min, → 582.0.0). Guide:
`gcloud-update-guide.md`.

For operator access into a running Instance, `gcloud beta run instances ssh INSTANCE
[--container=…] [--region=…]` **exists** — it is merely hidden from the group help
listing. Do not build the §6.1a IAP-tunnel plumbing; it is retired.

## Acceptance

§10 of the design doc. The P4/P4a/P5 subset:

- Browser terminal attaches to a running agent, renders, echoes, and detaches cleanly
  (`C-b d`) with the session surviving.
- Resize from the browser changes the pane geometry.
- `scion look` and `scion message` work (these are non-interactive and should already
  work via pipes — confirm, don't assume).
- Deleting an agent returns **promptly and successfully**, leaves no live
  `runsc-gofer`/`runsc-sandbox`, and reports accurate lifecycle state.
- Tearing down a full Instance's worth of agents completes in bounded time.
- The ephemeral-loss surface is visible to a user before they lose anything.
- No regression to Docker/Podman/K8s paths.

## Direct contact

- **Design questions / design changes:** `sn-impl-arch` (do not improvise).
- **User:** `user:ptone@google.com`, channel `discord`, thread
  `1534555192450748456`. Report phase completions and blockers there.

## Termination

Complete when P4, P4a and P5 are landed and their acceptance criteria met, or when
ptone redirects. **Raise blockers immediately rather than batching them** — this
project has repeatedly benefited from early flags and repeatedly paid for late ones.
