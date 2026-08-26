# Brief: sn-e2e-walk — walk the whole §1 path on a live IAP Instance, tonight

**Dispatched by** `sn-impl-arch`, 2026-08-26 ~03:40 UTC. ptone offline, back in the AM.

## Why you exist

Everyone else tonight is working on *getting an operator to a login page*. **Nobody is
testing what happens after they log in.** §1's success criterion does not stop at a URL:

> An operator with a GCP project and alpha access runs one deploy command, opens the
> resulting `run.app` URL, **logs in, creates a project, starts a Claude agent, attaches
> to its terminal from the browser, and watches it commit to a git remote.**

Those last four clauses are the demo. They have been exercised piecemeal via CLI and
unit tests during P1–P5; **as far as I can establish, they have never been walked
end to end against a real IAP-fronted Cloud Run Instance.** If something in there is
broken, ptone finds out in the morning while showing it to someone. That is the outcome
you are here to prevent.

## Do not wait for the deploy command

`sn-impl-em3` is building it and will not be done for a while. **You do not need it.**
There is already a live IAP-enabled Instance called **`iap-demo`** in project
`ptone-experiments`, region `us-east4`, standing up specifically so ptone can browse it.
Use that. Decoupling "can we deploy it in one command" from "does it work once you're
in" is the whole point of running you in parallel.

**Do not delete or redeploy `iap-demo`** — ptone has not looked at it yet, and a redeploy
of a Tier 0 pure-ephemeral Instance is a factory reset of the entire fleet (§ Tier 0).
If you believe you need to change it, message me first.

## What to do

Walk the path in order and record, for each step, **worked / failed / not reachable**:

1. **Authenticate through IAP.** You are not a browser. Obtain an OIDC token for the
   IAP OAuth client and present it. `spike-iap` (still running, `blocked`) established
   how this works — **message it and ask, rather than re-deriving.** Reuse beats rework.
2. **Reach the hub UI/API behind IAP.** Confirm the hub sees an authenticated identity
   and that identity is treated as **admin** (§11.6 — `AdminEmails` seeding). An
   operator who logs in and is told they lack permission is a failed demo.
3. **Create a project.**
4. **Start a Claude agent** in a sandbox on that Instance.
5. **Attach to its terminal.** This is the PTY/WebSocket path (`pty_handlers.go`, +189
   lines in #1266 — one of only two real integration points in that PR, so it is exactly
   where I would expect a defect). Confirm bidirectional I/O, not just a socket that opens.
6. **Have the agent commit and push to a git remote.**

## The two steps I most expect to fail, and why

- **Step 5, terminal attach.** WebSocket upgrade through IAP is not the same code path as
  a plain request. If IAP or the Cloud Run edge interferes with the upgrade or with
  long-lived connections, this breaks — and it breaks in a way that unit tests cannot see.
- **Step 6, git push.** This needs **egress from inside the sandbox** and **credentials**.
  That is adjacent to **OQ-14** (Vertex/ADC under `--allow-egress`), which is open and
  unowned. If step 6 fails on egress, you have effectively answered OQ-14 — say so
  explicitly, it is worth more than the step itself.

Also worth knowing: if step 4 fails because the agent cannot reach the launcher, that is
**OQ-2**, which `spike-oq2` is testing right now. Compare notes with it before concluding
anything; do not duplicate its work.

## Working rules

- **Read-mostly.** You are validating, not implementing. If you find a defect, **report
  it with a reproduction — do not fix it.** Three agents tonight have been more useful
  by reporting precisely than they would have been by patching.
- **Do not merge, rebase, or force-push anything.** #1265 and #1266 are open; merging is
  ptone's gate.
- If you must create a branch for a test artifact, keep it out of
  `scion/dev-rebase-1294` and tell me. That is the single integration branch.
- Credentials come from the **metadata server**. **Never print an access token to
  stdout** — this has happened in this project before.

## Reporting

Message `sn-impl-arch`. **Report the first failure the moment you hit it — do not walk
the remaining steps first and batch it.** Knowing at 04:00 that the terminal attach is
broken is worth hours; knowing at 07:00 is worth nothing.

A truthful partial — "steps 1–4 work, step 5 fails like *this*" — is the single most
valuable artifact you can hand me tonight. **Verify what you claim.** Several reports
today asserted work that was not on disk, and I check.
