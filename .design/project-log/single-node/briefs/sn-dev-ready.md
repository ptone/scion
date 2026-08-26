# Brief — sn-dev-ready

You report to **sn-impl-arch**. Message me with `scion message sn-impl-arch "..."` (keep messages
under 2000 characters — longer ones are silently dropped).

## Deadline, and why it is real

ptone is back at about **14:50 UTC** and expects to open a working instance and use it. Your work
has to be pushed by **14:00** so CI has time to build an image before I deploy and walk it. If you
are going to miss that, tell me at 13:50 rather than at 14:00 — a labelled stopgap that ships beats
a correct fix that does not.

## What is broken (all of this is measured, not theorised — do not re-derive it)

On a Cloud Run Instance, a sandbox agent that finishes leaves its sandbox running forever. The hub
keeps reporting `running`. Exit detection is supposed to work through a tmux hook shipped in the
template home (`pkg/config/embeds/templates/default/home/.tmux.conf`, the `pane-exited` hook near
line 85): agent exits → hook fires → `kill-session` → the entrypoint's `while tmux has-session`
loop returns → `sciontool init` runs its shutdown path → PID 1 exits → the sandbox stops.

I proved that whole chain works on the live platform at 12:54 by placing the file and sourcing it
by hand: the session died, `sciontool` reported `exitCode=0, crash=false`, the sandbox stopped, and
the hub row went to `phase=stopped`. **The design is settled. This is delivery work.**

Two independent causes stop it happening on its own. **Each alone is fatal — fixing one is not
progress.**

**Cause A — `HOME=/root` inside the sandbox.** Measured: `/proc/1/environ` has `HOME=/root`,
`SCION_HOST_UID=0`. The entrypoint log says `setupHostUser result: targetUID=0, targetGID=0,
rootless=false`. `pkg/sciontool/supervisor/supervisor.go:113` only sets `HOME` when
`(UID > 0 || Rootless)`, so it is skipped and root's `HOME` is inherited. tmux therefore reads
`/root/.tmux.conf`.

**Cause B — the template home is never applied to a sandbox agent home.** Measured on the launcher:
the template at `.../templates/global/default/home` holds `.tmux.conf`, `.zshrc`, `.gitconfig`,
`.gemini`. A provisioned home at `/scion/agents/<name>/home` holds `.bashrc`, `.claude`,
`.claude.json`, `.scion`, `agent-info.json`, `agent.log` — **none of the four**, for all four agents
on the instance. So `.tmux.conf` exists nowhere in the sandbox.

For contrast, a docker agent's `/home/scion` is a bind mount from the host and **does** contain all
four. Same intended path; the hosted one skips a step.

## Your three changes, ONE branch, ONE image

Branch from the current remote head of `scion/dev-rebase-1294` (`git fetch origin` first — it moves).
Name your branch `scion/sn-dev-ready`. **Do not merge anything into `scion/dev-rebase-1294`** — that
is ptone's gate. Push your own branch only.

### 1. #33 — `deploy-instance` is contaminated under service-account impersonation (do this first)

`cmd/deploy_instance.go`. gcloud writes `WARNING: This command is using service account
impersonation...` to stderr, and at least two call sites capture combined output and use it as
data. Measured consequences:

- the instance URL becomes `https://ssh-probe-WARNING: This command is using service account
  imperson....run.app`, so the IAP reconcile gate polls garbage and times out at 3m0s while blaming
  port 8080;
- worse, `diResolveProjectNumber` is contaminated, so the instance is **created** with
  `SCION_SERVER_AUTH_PROXY_IAP_AUDIENCE = "/projects/WARNING: This command is using...\n721899303052/locations/us-east4/services/<name>"`.
  That instance then exits 1 on boot.

Fix: capture **stdout only** (`Output`, not `CombinedOutput`, or `--format=value(...)` with stderr
discarded) and **validate every captured value before use** — the URL must parse as `https` with a
plausible host, the project number must match `^[0-9]+$`. Make the gate's failure message
distinguish "could not parse URL" from "instance not serving". Add a unit test that feeds
warning-prefixed gcloud output through each parser and asserts rejection rather than embedding.

This is on the critical path, not housekeeping: impersonation is the only identity we have in
`ptone-experiments`, so until it is fixed every deploy bakes a malformed audience.

### 2. Cause A — set `HOME` for the sandbox

Set `HOME`, `USER` and `LOGNAME` in the Cloud Run sandbox's `envFor()`
(`pkg/runtime/cloudrun_sandbox_runtime.go`, around `:486` where `SCION_HOST_UID` is written).
Sandbox-local by design.

**Do not** relax `supervisor.go:113` in this branch. It is the more correct fix and it is a separate
follow-up — it changes the environment of root-run children in docker, podman and k8s, and that
needs its own review. If you think it belongs here, message me rather than doing it.

While you are in the file, fix the comments at `cloudrun_sandbox_runtime.go:419` and `:573`. Both
assert a *"hardcoded HOME=/home/scion (supervisor.go:115)"*. That guarantee does not exist, and the
false comment is precisely why this defect survived code review. State what is actually true.

### 3. Cause B — apply the template home

**First, name the mechanism. Do not guess.** Find the function that materialises a template's
`home/` directory into an agent home on the docker path, find where the Cloud Run sandbox path
should call it, and find the condition under which it does not. Message me the three of those
before you write the fix — one short message, no essay.

One lead, offered as a lead and worth killing rather than confirming: the sandbox's PID 1 env has
`SCION_TEMPLATE_NAME=default`, and the launcher stores the template under
`.../templates/**global**/default/home`. A broker resolving in a project scope, missing, and
continuing silently would produce exactly this partial home.

**If you have not named it by 13:50**, stop investigating and ship a stopgap: materialise the
template home into the agent home at sandbox provisioning time when the files are absent. Label it
in the code as a stopgap with a one-line reason, and tell me, so I can file the root cause as its
own item. Shipping the stopgap knowingly is fine. Shipping it while calling it a fix is not.

## What NOT to widen into

- **Not** the credential helper or git identity. I briefly claimed hosted agents cannot commit or
  push; that was wrong — `sciontool init` writes the helper at `cmd/sciontool/commands/init.go:1688`
  and `:1831` when a token exists, and the agent I inspected had none.
- **Not** `relocateToScion`'s unconditional `os.RemoveAll` (#32). Real, latent, needs a volume mount
  to fire, already filed.
- **Not** the dev-auth fallback (#34) or the hub's session-metrics 400 (#35). Both filed, both
  ptone's call or a later pass.

## Verifying, if you need inside-the-sandbox evidence

```
gcloud beta run instances ssh e2e-walk-r2 --project ptone-experiments --region us-east4 \
  --container worker --iap-tunnel-url-override=wss://tunnel.cloudproxy.app/v4
# then, on the launcher:
/usr/local/gcp/bin/sandbox exec <sandbox-name> -- /bin/sh -c '<cmd>'
```

Impersonate with `CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT=scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`
— the env var only; the `--impersonate-service-account` flag is broken for this command. The
`--iap-tunnel-url-override` is required because gcloud 582 hardcodes the wrong SSH endpoint.
**Never print an access token.** Do not restart, redeploy or delete any Instance.

## Reporting

Message me when: (a) you have named Cause B's mechanism, (b) each change is pushed, (c) CI has an
image tag. I need the **image tag** — that is the handoff. If CI does not build images on your
branch, say so immediately; that changes my plan, not just yours.
