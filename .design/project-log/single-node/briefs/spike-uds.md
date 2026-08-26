# Brief: spike-uds — empirical AF_UNIX test on Cloud Run Sandboxes

**Dispatched by** `sn-impl-arch` on ptone's explicit instruction, 2026-08-25.
**You are a verification spike.** You do not implement anything. You run tests,
record results, and report. No production code, no branches, no PRs.

## Why you exist

The Cloud Run platform team told us AF_UNIX sockets cannot cross the sandbox
boundary: *"We would need to run runsc/gvisor with `--host-uds` enabled which we
don't."* We took that as settled and redesigned around it.

**The engineering team has since corrected it:** `--host-uds=host` **is** set. So
the original design may work after all. ptone has asked for an empirical test.

**This decides which design we build.** It is blocking.

## Read first

`/scion-volumes/scratchpad/projects/single-node/cloudrun-instances-sandboxes.md`

- **§4.4-rev** — the correction, the `open`/`create`/`all` distinction, and the
  `SCM_RIGHTS` risk. Read this before touching anything.
- **§10a** — the test plan. Ground rules, then Tier A (T1, T2, T3a–e). **You are
  running Tier A only.** Do not start Tier B unless I tell you to.
- **§3.2c** — sandbox CLI facts you will need (empty `PATH` inside a sandbox,
  `delete --force`, `--env` repeatable).

`/scion-volumes/scratchpad/projects/single-node/deploy-instance-with-sandbox.md`
— how to create an Instance with `sandboxLauncher: true`. Note the scope banner:
raw REST is for verification spikes **only**, which is exactly what you are.

`/scion-volumes/scratchpad/projects/single-node/ac0-results.md` — the previous
spike's results and format. Append yours; match the format.

## Ground rules — these are not optional

1. **Real Cloud Run Sandbox on a real Cloud Run Instance.** Not `unshare`, not
   local Docker, and **specifically not a locally installed `runsc`** — a
   self-installed gVisor may have different `--host-uds` settings and would give
   you a confident, reproducible, wrong answer. This project has already been
   burned once by exactly this: an earlier spike tested with `unshare`, reported
   PASS, and was wrong, because `unshare` shares the host VFS outright while
   gVisor proxies through a gofer.
2. **Write the pass/fail predicate down before you run each test.** The earlier
   false positive happened because nobody had written down what PASS meant.
3. **Characterize negatives.** "Failed" is not a result. Capture the exact error
   text, the errno if available, and whether it failed immediately or hung.
4. **Capture raw output**, not just your conclusion, so the next reader can
   re-derive it rather than trust it.
5. **Report a partial pass as a partial pass.** Do not average T3a–e into one
   verdict. §4.4-rev explains why a partial pass is the *likely* outcome and why
   it is a good one.

## What to run

Tier A from §10a, in order: **T1, T2, then T3a–e.**

The single most important structural point: **T1 needs `--host-uds` to permit
*create*** (the tmux server runs inside the sandbox and binds the socket), while
**T2 needs *open***. Run both regardless of how T1 goes — if T1 fails and T2
passes, the mode is `open`-only, which is a different and important finding.

**T3d is the one most likely to fail on its own.** `tmux attach` passes the
client's terminal fd over the socket via `SCM_RIGHTS`; gVisor may proxy the byte
stream without proxying ancillary data. If T3a–c pass and T3d fails, say so
precisely — that outcome makes the design a hybrid and is worth more than a clean
yes or no.

**T3e:** record the uid inside the sandbox and the uid of the launcher-side
process. tmux creates `tmux-<uid>/` mode 0700 and refuses sockets it does not
own, so a uid mismatch produces a failure that looks like a socket problem and
is not one. Do not misattribute it.

If you can determine the **actual `--host-uds` value** in effect, capture it. Do
not spend long on it — behaviour beats configuration, and the tests above measure
behaviour directly.

## Credentials

Metadata server; no key file. Container SA is
`scion-my-grove@deploy-demo-test.iam.gserviceaccount.com`, holding Token Creator
on `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.
Project `ptone-experiments`, region **`us-east4`** — `us-central1` is
capacity-exhausted for Instances.

**Do not print access tokens to stdout.** This has happened before in this
project.

## Reporting

Write results into `ac0-results.md`, then message `sn-impl-arch` with a per-test
verdict (T1, T2, T3a, T3b, T3c, T3d, T3e), the exact failure text for anything
that failed, and the uid pair from T3e. **Raise a blocker immediately** if you
cannot get an Instance up — do not spend an hour working around it silently.

Also message ptone (`user:ptone@google.com`, channel `discord`, thread
`1534555192450748456`) with the headline result once you have it. He asked for
this directly and is waiting on it.

## Termination

Complete when Tier A results are recorded and reported. Do not proceed to
Tier B, and do not restore any of the removed tmux code — that decision is
mine to make on your results.
