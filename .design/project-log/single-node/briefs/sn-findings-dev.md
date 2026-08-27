# Brief: file three measured findings as tracking issues before their evidence is torn down

Author: sn-impl-arch (architect). Date: 2026-08-27. Tasks #65, #67.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find, **stop and message me** — do not improvise. That rule
has caught several of my own errors this week, including three yesterday and today.

**Why now:** two stress instances are about to be torn down. These three findings were measured on
them. Once they are gone the evidence is gone with them, and a finding that exists only in an
agent's transcript is a finding we have lost.

---

## 1. The reference trap — read this before you write a single word

**Fork and upstream issue numbers are completely independent.** The same bare number is a different
thing in each repo. There are **five known collisions**, two of which we created today:

| bare ref | in `ptone/scion` (fork) | in `GoogleCloudPlatform/scion` (upstream) |
|---|---|---|
| `#1273` | Hosted hub drops template identity | PR: populate file_secret_files |
| `#1274` | `GitCloneConfig.Depth` | PR: accept text files with unusual control chars |
| `#1281` | Session metrics lost | PR: stop syncBuiltImage mutating config.yaml |
| `#1301` | deploy-instance OAuth client ID gap | PR: Permissions Foundation Phase 1 |
| `#1302` | gcloud ssh impersonation bug | PR: Cloud Run Instances runtime |

**Fully qualify every cross-repo reference.** `ptone/scion#1274`, never a bare `#1274`. A bare number
resolves against whichever repo the text is rendered in, which is exactly how a design doc that now
lives upstream ended up citing eighteen fork issues that resolve to unrelated PRs.

Do **not** write `Fixes #N` or `Closes #N`. These are trackers, not fixes.

**File on `ptone/scion`. Issues are fork-only in this project — never open an issue upstream.**
An upstream write returns 403 anyway.

## 2. Search before filing each one

I believe none of these are filed. **Search anyway before each.** My search was keyword-based. If you
find a genuine duplicate, do not file — tell me.

---

## 3. Issue 1 — exceeding the agent ceiling destroys the entire Instance and all state, after
returning HTTP 201

**This is the most serious finding of the project and it is a real defect, not a non-goal.**

Measured on two Instance sizes, both running the omni image, both with idle agents added **one at a
time** with explicit `template: "default"` and `harnessConfig: "claude"`:

| size | ceiling (exec-verified alive) | what happened past it |
|---|---|---|
| 4 CPU / 8 GiB (the **default**) | 17-18 | at N=19, SIGBUS (signal 7) ~8s after a create returned **HTTP 201**; total loss |
| 8 CPU / 32 GiB (the **maximum**) | 51-52 | at N≈53, a ~2-minute cascade of sandbox deaths, then Cloud Run terminated the Instance |

Facts that must appear in the body, because each one blocks a wrong conclusion:

- **The create SUCCEEDS.** It returns 201. The destruction follows seconds later. The operator's last
  signal before losing everything is a success message.
- **Total loss.** Every agent, every project, every workspace, the SQLite control plane. New Hub ID,
  new broker ID, new signing keys.
- **The service self-recovers in ~25-30 seconds, healthy and completely empty.** Nothing looks
  broken. This is worse than an outage, because an outage announces itself.
- **There is ZERO `exit_code=137` at either size.** The Linux OOM killer is **not** the mechanism. Do
  not write that it is. Say the mechanism is not established.
- **The two sizes failed by two DIFFERENT mechanisms**, so there is no single signature to document
  and you must not synthesise one.
- **The ceiling is not linear in memory.** 4x memory and 2x CPU bought only 3x agents. Fitting a
  per-agent constant plus fixed overhead across the two points yields a **negative** overhead, which
  is impossible — so the model is wrong and something other than memory binds at the larger size.
  **State that no per-GiB rule can be derived from this data.**
- **Repeatability is UNMEASURED.** Each ceiling is a single observation. Say so plainly. Do not imply
  the numbers are stable, and do not present them as thresholds to design against.

Relate it to, but distinguish it from, the by-design item "no per-agent resource limits"
(`ptone/scion#1287` covers that). Sharing a budget is the accepted design. **Destroying the Instance
when the budget is exhausted is not**, and that distinction is the point of this issue.

Point at `.design/hosted/cloud-run-single-node.md` §5 and §9.1 **by path and section number**. Note
neutrally that §5 frames loss around redeploy ("A redeploy loses both", "disposable") and that this
framing is incomplete: **a redeploy is chosen, an overload is not.** That framing error is mine; say
so neutrally rather than blaming the document.

## 4. Issue 2 — `getStats` returns hardcoded zeros, so there is no CPU or memory observability for
any runtime on any tier

`pkg/runtimebroker/handlers.go:1958` on `origin/main`:

```go
func (s *Server) getStats(w http.ResponseWriter, r *http.Request, id, projectID string) {
	// TODO: Implement real stats from runtime
	// For now, return placeholder data
	writeJSON(w, http.StatusOK, StatsResponse{
		CPUUsagePercent:  0.0,
		MemoryUsageBytes: 0,
	})
}
```

**Verify this yourself on `origin/main` before filing, including the line number.** The agent who
first reported it placed it in the hub at a different line; widening the check is what revealed the
real scope.

Two things make this bigger than it looks, and both belong in the body:

1. **It is in `runtimebroker`, not in the Cloud Run runtime.** So it returns zeros for **every
   runtime on every tier**, not just this one. Do not file it as a single-node tier gap — doing so
   would bury a product-wide problem inside a tier's backlog.
2. **The fix is close at hand.** `pkg/hub/server_dispatcher.go:34` documents that the broker is
   co-located with the Hub in the main container. It therefore already has instance-scoped
   `/proc/meminfo` access. This is not an architectural change.

Consequence, and it is the reason this matters now: combined with Issue 1, **an operator has no
instrument that would let them see the ceiling approaching.** Five separate measurement instruments
were tried during the stress test and all five were dead or wrongly scoped — Cloud Monitoring's
memory and CPU utilization metrics only work for `cloud_run_revision` (Services), not for
`cloud_run_instance`, and agent sandboxes are gVisor processes invisible to it.

## 5. Issue 3 — `sshd` is absent from the omni image, so SSH is advertised as enabled and every
connection fails

The platform reports `SSH: enabled`. Connections fail. The root cause is that `sshd` is not present
in the omni image.

**Write this one as a fact and a question, not as a recommendation.** Whether to ship `sshd` in the
image is a security decision and it is ptone's, not ours. The issue should state:

- the advertised capability,
- the observed failure,
- the root cause,
- and that the resolution is a decision between **shipping `sshd`** and **not advertising SSH as
  enabled**.

**Do not recommend adding `sshd`.** Do not describe the absence as a bug to be fixed by installing
it. We were one sentence away from recording this as a permanent platform limitation, which it is
not; the opposite error — treating a security decision as a packaging oversight — is just as easy.

The related `gcloud` impersonation defect found alongside it is **already filed as
`ptone/scion#1302`**. Reference it; do not re-file it.

---

## 6. What you must NOT do

- **Do not fix any of these.** All three are tempting. Issue 2 in particular is close to a one-line
  change and you must not make it. You are filing, not repairing.
- **Do not open an issue upstream.** Fork only.
- **Do not write bare cross-repo references.** See §1.
- **Do not touch any branch, PR, or code.** This task produces issues, nothing else.
- **Do not touch or delete any Instance or agent.** `sn-stress-def` and `sn-stress-max` are running a
  live experiment. `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-ready`, `sn-adminseed-t`,
  `sn-adminfix-t` are all **do-not-delete**. `sn-ready` is ptone's live Instance — do not touch,
  restart or delete it. Keep `iap-demo` up.
- **Do not add, round, or extrapolate any number** beyond what is written above. Especially: do not
  turn the two ceilings into a recommendation or a per-GiB rule. §3 explains why that is not
  available from this data.

## 7. Report back

Message `sn-impl-arch` with:

- The issue number and title for each, as `ptone/scion#NNNN`.
- **Your independent verification of the `getStats` file and line number**, and whether it matched
  what I wrote here.
- Any duplicate you found and did not file.
- Anything in this brief you think I have described wrongly. Several of these facts I am relaying
  from other agents' reports rather than having measured myself, and **relayed facts are exactly
  where my errors have concentrated this week.** If something looks wrong, say so — a developer
  refusing a number I asserted has already corrected me three times on this project.
