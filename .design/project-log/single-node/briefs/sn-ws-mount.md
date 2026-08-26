# Brief — sn-ws-mount

You report to **sn-impl-arch**. Message me with `scion message sn-impl-arch "..."`. Keep messages
under **2000 characters** — longer ones are rejected outright.

## Why this is the only thing that matters right now

§1 of the design is the whole project's yardstick: *an operator with a GCP project runs one deploy
command, opens the resulting `run.app` URL, logs in, creates a project, starts a Claude agent,
attaches to its terminal from the browser, and watches it commit to a git remote.*

Steps 0 through 5 and step 7 now pass on a live hosted instance. **Step 6 — the commit — is the
last one unverified, and this ticket is what blocks it.** Nothing else on my list outranks it.

## What is measured. Do not re-derive any of this.

Measured live on the Cloud Run Instance `sn-walk` at 15:30 on 26-Aug. Two adjacent agent creates on
the same instance:

- project **without** a `gitRemote` → agent starts, `phase=running`. Fine, three times over.
- project **with** a `gitRemote` → **502, sandbox dead on arrival.**

Broker error: `sandbox dead on arrival after run returned rc=0 — all liveness probes failed`.

The entrypoint log (captured only because #22 landed — it earned its keep here):

```
[sciontool] INFO:  Falling back to scion user UID=0 GID=0 for git clone
[sciontool] ERROR: Git clone failed: git init failed: /workspace/.git: Permission denied
[sciontool] INFO:  Telemetry pipeline stopped
```

`sciontool init` treats the clone failure as fatal, PID 1 exits, the sandbox never comes up, every
liveness probe fails.

And from inside a *healthy* sandbox on the same instance, running as uid=0:

```
$ ls -ld /workspace
drwxr-xr-x 1 nobody nogroup 0 Aug 26 13:58 /workspace
$ touch /workspace/.probe
touch: cannot touch '/workspace/.probe': Permission denied
$ touch /home/scion/.probe
HOME_WRITABLE
$ grep /workspace /proc/mounts
(no entry; only "none / overlay rw")
```

**Read those three results carefully, because they constrain the answer.** Root is denied, so this
is not a permission-bits problem — 0755 on a root-owned directory is writable by root. `nobody:
nogroup` is what an unmapped UID looks like from inside a user namespace. And `/home/scion` *is*
writable, so the sandbox is not globally read-only; this is specific to `/workspace`.

My hypothesis, offered as a hypothesis and worth killing rather than confirming: the sandbox mounts
a host path whose owner has no mapping into the sandbox's user namespace, so root inside that
namespace has no authority over it. **I have not proven this and you should try to break it.**

## The gate: name the mechanism before you fix anything

**Message me with three things before you write a line of fix:**

1. Which component creates `/workspace` in a Cloud Run sandbox, and with what ownership.
2. What UID/GID mapping the sandbox runtime requests, and where that is specified.
3. Whether `/workspace` is *supposed* to be writable here — i.e. whether the docker path makes it
   writable and by what means, since git-linked agents demonstrably work there.

One short message. If you cannot answer 3 from the code, say so rather than inventing an intent.

I am insisting on this because three separate times today a fix was proposed off a plausible reading
and the plausible reading was wrong. A green test asserting the hop *before* the broken one has
already fooled us three times. Find the mechanism, then fix it.

## Likely places to look

`pkg/runtime/cloudrun_sandbox_runtime.go` is the runtime. Compare against whatever the docker
runtime does for the same directory — the docker path works, so the difference is the answer.
`cmd/sciontool/commands/init.go` is the consumer that dies. Adjacent and already filed: **#32**
(`relocateToScion`'s unconditional `os.RemoveAll` on a failed rename) also concerns what is mounted
where, and you may well pass through it — note anything you learn, but **do not fix it here**.

## A second defect you will pass through, and should report but not fix

`sciontool init` turning a clone failure into a dead sandbox with no surviving diagnostic is bad
behaviour independent of the mount. An operator whose git URL is simply wrong gets
`502 dead on arrival`, not "your clone failed". Tell me what you observe; I will file it. If the
right fix for *this* ticket happens to make it diagnosable, good, but do not widen the change to
chase it.

## Verifying on live hardware

```
gcloud beta run instances ssh sn-walk --project ptone-experiments --region us-east4 \
  --container worker --iap-tunnel-url-override=wss://tunnel.cloudproxy.app/v4
# then, on the launcher:
/usr/local/gcp/bin/sandbox exec <sandbox-name> -- /bin/sh -c '<cmd>'
```

Impersonate with the **env var** form:
`CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT=scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`
— the `--impersonate-service-account` flag is broken for this command (the sub-call does not
inherit it and you get a misleading PERMISSION_DENIED). The `--iap-tunnel-url-override` is required
because gcloud 582 hardcodes the wrong SSH endpoint.

**Never print an access token to stdout.** This has happened before in this project.

`sn-walk` is mine and you may use it. **Do not touch, restart, redeploy or delete `sn-ready`,
`e2e-omni`, `e2e-walk-r2`, `iap-demo` or `q2-control`.** `sn-ready` in particular is the live
instance the project owner is using right now.

## Branch and process

`git fetch origin` first. Branch from the current remote head of `scion/dev-rebase-1294` and name
your branch `scion/sn-ws-mount`. **Do not merge anything into `scion/dev-rebase-1294`** — that is
the owner's gate. Push your own branch only, and push as you go rather than at the end.

Write a test that fails on the current behaviour and passes on the fixed one. A unit test over the
mount/ownership decision is worth more than a live re-run, because the live re-run is what we
already have.

## Reporting

Message me when: (a) you have named the mechanism (the gate above), (b) the fix is pushed, (c) CI
has an image tag. **The image tag is the handoff** — without it I cannot verify on live hardware.
If CI does not build images for your branch, tell me immediately; that changes my plan, not just
yours.
