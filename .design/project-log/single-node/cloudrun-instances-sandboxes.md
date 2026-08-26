# Single-Node Hosted on Cloud Run Instances + Sandboxes

**Status:** Draft — in active discussion with ptone (Discord thread `1534555192450748456`)
**Author:** `sn-impl-arch` (architect)
**Branch:** `scion/sn-impl-arch`
**Code HEAD when written:** `4b68362`
**Last revised:** 2026-08-25 (post-AC-0, post-UDS ruling)

---

## 0. Revision log — what changed most recently

Read this first if you have seen an earlier copy. Everything below is from
2026-08-25/26, after AC-0 ran against a real Instance and after several platform
answers and corrections from ptone. Sections are annotated in place; nothing was deleted, superseded material
is struck through and kept.

| § | Change | Status |
|---|---|---|
| **§3.2z** | Platform **injects** `/usr/local/gcp/bin/sandbox` when `sandboxLauncher: true` — **VERIFIED**, not inferred. `unshare`/`bubblewrap` fallbacks struck. | Resolved |
| **§3.2b** | **PREMISE FALSIFIED.** I had inferred from `--mount string` in the help text that the flag was single-valued. It is repeatable; so is `--env`; both mount syntaxes work. The inference was wrong — pflag's type rendering is not an arity contract. | Resolved |
| **§3.2b-r** | Consequence: **reversal to per-agent mounts.** The single shared `/scion` mount root that the false premise forced would have exposed every agent's home, workspace and tmux socket to every other sandbox, writable. Landed in P3. | Resolved |
| **§3.2c** | New sandbox-CLI facts: `PATH` is empty inside a sandbox (use absolute paths or set `--env`), `delete` needs `--force` while running, `--publish`/`tar`/`fork` exist. | Resolved |
| **§4.2a** | Omni-image should **chain** the existing harness Dockerfiles (all are `ARG BASE_IMAGE`/`FROM ${BASE_IMAGE}`), not hand-transcribe their install steps. The transcription that merged in P2 is correct but unanchored — it will silently drift. **Landed** (`f06d6cbf`): 125-line transcription → ~15-line hub layer over the harness chain. But see **§4.2a-ci** — the same change gated omni to `--builder local-docker`, leaving the tier's deployment artifact with no CI build path. Open P5 item. | Landed, with a follow-up |
| **§4.2b** | **Thick base adopted** — Cloud Workstations `predefined/base` via the merged `image-build/thick-prep` (PR #1087). Size objection withdrawn: Instances use image streaming, so a 2 GB base is not paid at startup. | Resolved |
| **§4.4** | **❌ DEAD.** The tmux-socket-over-bind-mount design is ruled out: *"We would need to run runsc/gvisor with `--host-uds` enabled which we don't."* Original preserved as **§4.4-orig**. | Resolved (negative) |
| **§4.4a** | Replacement: tmux client *and* server stay inside the sandbox; `sandbox exec` carries each operation in. Non-interactive control is unaffected. ~~Interactive attach has a trap — the PTY must be allocated **inside** (`script -qfc`).~~ | Superseded by §4.4a-rev |
| **§4.4a-rev** | **Tier B ran; the PTY problem is smaller than §4.4a described.** A **launcher-side** PTY *does* propagate — gVisor passes the host TTY fd through, so `isatty()` is true inside. Production path is `sandbox exec <id> --env TERM=xterm-256color -- tmux attach`: **no `script`, no util-linux**. `TERM` is load-bearing. SIGWINCH still does not cross (out-of-band `resize-window` stands). p95 latency **37 ms**. **New risk:** `sandbox delete --force` hangs >90 s and orphans a `runsc` process — P4's `Delete` must be timeout-bounded. | Resolved by test |
| **§4.9a** | Instances have **no direct IAP**; the gap is closed with a Cloud Run service running IAP as an auth proxy. For the sandbox→hub hairpin, five options tabled. **Do not mimic the IAP header** — it is a verified ES256 JWT assertion, not a trusted header, so mimicry means either forging a signature or adding a bypass. Use the agent token, which is already Step 1 of hub auth. | Resolved |
| **§8 / AC-0** | Six AC-0 items struck as answered; the tmux item is **ANSWERED-NO**; four new items added covering the `script` PTY trick, `util-linux` presence, resize, and exec latency. | Updated |
| **§10a** | **New — empirical test plan** for AF_UNIX and its replacement, with pass/fail predicates and anti-substitution ground rules. Reverses my "no test needed" call on the UDS ruling. | New |
| **§4.11 / S4** | Broker authz gap found in `handlers_runtime_brokers.go` — three handlers trust the path `{id}` without checking the caller; the heartbeat's "security check" compares path-vs-agent, not caller-vs-path. **Filed as ptone/scion#1263, owned by `auth-lead`. Out of scope for this design.** | Handed off |
| **§4.9b** | **`gcloud beta run instances ssh` EXISTS** (hidden from the group listing; verified on 582.0.0). **Retires the §6.1a IAP-tunnel plumbing** for operator access. Does *not* answer the TTY question — it reaches the launcher, not a sandbox. Second instance of the same error shape: reporting an absence from a *listing*. | Resolved |
| **§4.9a** | **❌ PREMISE FALSE — `iapEnabled` on Instances is LIVE** (`spike-iap`, 2026-08-26). Edge-enforced, verified on genuine Instances, assertion is hub-compatible. Title struck; the **header-mimicry ruling stands unchanged** and is why the section is kept. **Four consequences the liveness result does not remove:** the open-config footgun *moves* rather than disappearing; dropping the proxy is what puts IAP on the *agents'* path; **OQ-2 becomes decisive** (A2 no longer works as written); and — per OQ-17 — **the proxy may have to come back anyway**, because only a Cloud Run *Service* has per-resource IAP IAM. | Resolved by test; **P6 scope decision open** |
| **OQ-17** | **ANSWERED — NO** (§10b.1). IAP and the Instance invoker check **cannot coexist**: IAP's `x-serverless-authorization` carries a `services`-path audience that the invoker check rejects, while the **URL** and **`instances`**-path forms both return 200. Isolated to one variable, confounders ruled out at 5m15s. **`invokerIamDisabled: true` is mandatory under IAP**, so IAP is the sole perimeter and the I4 open-config footgun becomes the *supported* configuration. **Changes S2.** | Resolved by test (negative) |
| **§10b.1** | **New — the IAP *IAM* surface, and it is the half-delivered case after all.** IAP **enforces** on Instances but has **no per-Instance IAM resource**: `getIamPolicy` on `iap_web/cloud_run-{region}/services/{instance}` returns **404** even holding `iap.admin`, while the same call on real Cloud Run **Services returns 200**. Only a **region-level** binding is available, which is **too broad to ship** — it grants hub access to everything in the region. **Re-opens a P6 design choice** (dedicated project / the §4.9a proxy after all / documented broad scope). Also: project-level `iap.httpsResourceAccessor` inherits invisibly and **retro-weakens I6**. | New — decision needed |
| **§4.4-rev** | The `--host-uds` ruling was **corrected, then re-confirmed by test**, all on the same day. Eng team said the flag *is* set; `spike-uds` then found **every socket-crossing test fails** on a real Sandbox. Config and behaviour are reconciled by scope (host-native sockets ≠ gofer-mediated bind mounts). **§4.4 is dead, §4.4a is the design, the P3 removals stand.** | Resolved by test |

| **P4a / OQ-16** | **Validated on a live Instance and closed — no code changes** (`sn-impl-em2`, 2026-08-26; `validation/delete-timeout-validation-results.md`). Sandboxes are unreachable at **<1s**, serially *and* under 5-way fan-out, so the 10s timeout is ~10× margin rather than a guess; **OQ-16 answered** — 5 concurrent deletes bind independently, no contention. Two orphan scares both resolved *away* from the code: `runsc state` is an artifact of the **test's own** `sandbox exec` probing (0/4/7 probes → 0/4/7 processes, tracks probes not concurrency), and `sandbox wait` shells out to `runsc wait`, which **exits cleanly** when the watcher is cancelled — identical orphan counts with the watcher absent, killed-first, and killed-simultaneously. `isOrphanedRunscProcess` is correct as written. | Resolved by test |

**Still open and worth your attention:** ~~**OQ-15**~~ — **resolved by test, IAP is
live** (§10b); what remains of it is **OQ-17**, awaiting one browser load against the
live `iap-demo` Instance. ~~**OQ-16**~~ — **answered; P4a validated and closed** (see
above). ~~**§4.2a-ci**~~ — **re-diagnosed 2026-08-26 (§4.2a-ci-rev); the original entry was wrong in both directions.** CI *can* build omni (the default builder is `local-docker` and the runner is one); the actual gap is that **nothing publishes any image automatically**, which breaks the one-command deploy. Five small changes, one disk risk to verify. ~~**OQ-2**~~ — **ANSWERED by
measurement; the sandbox reaches the launcher locally at 1.64 ms, so `P7` is deleted**
(§11.10). It leaves behind two things that are *not* closed: **`--allow-egress` is
mandatory and all-or-nothing** (no sandbox can reach its launcher without also reaching
the internet), and the **GCE metadata server is unreachable from a sandbox even with
egress on**. **OQ-14** (Vertex/ADC from inside a sandbox) is therefore **open, unowned,
and now the most consequential remaining question** — ADC-via-metadata appears simply
unavailable, so agents likely need launcher-minted credentials. Plus the three questions
to the platform team in `control-plane-uds-writeup.md`.

**State of the design, 2026-08-26.** Both empirical tiers have run and **nothing
material is unverified any more.** §4.4 (socket bind-mount) is dead by test; §4.4a-rev
is the design and is simpler than what it replaced; latency, resize, idle stability and
teardown all have measurements rather than predictions. **P0–P3 are landed and rebased
onto upstream `a34deb91`** (branch `scion/dev-rebase-1294`, zero conflicts, stopgap
intact). ~~**P4/P4a are unblocked and unowned**~~ — **dispatched 2026-08-26**, see
below. The remaining risk in this document is concentrated in teardown (P4a) and in
packaging (§4.2a-ci), not in the runtime mechanism.

**Two workstreams dispatched 2026-08-26 (ptone's instruction), deliberately
independent of each other:**

- **`sn-impl-em2`** — EM for **P4, P4a, P5**, from `scion/dev-rebase-1294` @
  `8a7852f2`. Brief: `briefs/sn-impl-em2.md`. P6/P7 remain undispatched.
- **`spike-iap`** — empirical test of **OQ-15**. Brief: `briefs/spike-iap.md`. Runs in
  parallel and touches no implementation branch; its outcome is a **P6** concern
  (§4.9a), not a P4 one, so it cannot block the EM.

---

## 1. Problem & Goals

Scion's tier spine is Local → Workstation → **Single-node hosted** → HA hosted. The
single-node hosted tier is the one we have designed on paper (see
`single-node-packaging.md`, `single-node-auth.md`) but never had a first-class
hosting substrate for: it needs one long-lived node that runs the hub, the broker,
and N agent containers, with a filesystem all three can see.

ptone has directed that we shape this tier onto two new Cloud Run capabilities:

- **Cloud Run Instances** (alpha) — individually addressable long-lived compute
  with its own `run.app` URL and built-in SSH. *Acts as the VM.*
- **Cloud Run Sandboxes** (public preview) — a `sandbox` CLI invoked *from inside*
  a container to launch nested, isolated workloads. *Acts as the Docker runtime.*

### The core framing

**The Cloud Run Instance is the node.** This is not a new tier and it is not a new
configuration axis. It is the existing single-node hosted tier with two component
substitutions:

| Single-node hosted (generic) | This design |
|---|---|
| A VM | A Cloud Run Instance |
| Docker daemon | `sandbox` CLI |
| Combo server (hub + embedded broker in one process) | unchanged |
| Embedded SQLite | unchanged |
| Bind-mounted workspaces on local disk | unchanged (Instance's ephemeral disk) |

Everything Scion already knows about the single-node tier continues to apply. What
we are adding is **one new `Runtime` implementation** and the packaging around it.

### Goals

- **G1.** A single container image ("omni-image") that, deployed to one Cloud Run
  Instance, yields a working Scion hub able to launch agents.
- **G2.** Agents run in Cloud Run Sandboxes, driven through the existing tmux
  control plane, with attach/exec/logs working from the hub UI.
- **G3.** No new abstraction layer. Implement `runtime.Runtime`; change nothing
  about how `pkg/agent` constructs a launch.
- **G4.** Deployment is one command against a GCP project, with no registry setup,
  no NFS, no Filestore, and no Kubernetes.
- **G5.** Cheap and fast to stand up and tear down. Explicitly trading durability
  for this (see §2, §5.1).

### Success criteria

An operator with a GCP project and alpha access runs one deploy command, opens the
resulting `run.app` URL, logs in, creates a project, starts a Claude agent, attaches
to its terminal from the browser, and watches it commit to a git remote.

---

## 2. Non-Goals

- **HA, failover, or multi-node.** One Instance. It is a single point of failure by
  design.
- **Durability of agent workspaces.** Workspaces live on the Instance's ephemeral
  filesystem and are lost on redeploy. (What happens to the *control plane* — the
  SQLite DB — is a live question; see §5.1 and Open Question **OQ-1**.)
- **Per-agent resource isolation guarantees.** See **OQ-4**.
- **Replacing the Cloudflare Tunnel design.** That work (`cloudflare-tunnel.md`,
  owned by `cf-tunnel-arch`) targets ingress for self-hosted nodes. A Cloud Run
  Instance has its own `run.app` ingress and does not need a tunnel. The two designs
  are siblings, not competitors.
- **Templated Sandboxes.** Not yet available. §4.2 explains why their absence forces
  the omni-image, and §7 notes what changes when they ship.
- **The `cloudrun` / `cloudrun-instances` runtime stubs' original intent** —
  agent-per-Cloud-Run-service. That is a *different* design; see §6.1.

---

## 3. What the existing code already gives us

Three findings materially reduce the scope. All verified at `4b68362`.

### 3.1 There is exactly one launch construction site

`pkg/agent/run.go:934-1041` is the only non-test `runtime.RunConfig{}` literal in
the repo, and `run.go:1043` is the only non-test `Runtime.Run` caller. **Implement
`runtime.Runtime` (15 methods, `pkg/runtime/interface.go:107-128`) and you receive
one fully-populated `RunConfig`.** No caller changes.

### 3.2 The deepest assumption in the codebase happens to be *true* here

The broker assumes it holds the agent's filesystem locally. This shows up in at
least four places:

- `localBackend.Resolve` requires a host-discovered `ProjectDir`
  (`workspace_backend_local.go:40-72`)
- `SCION_HOST_UID` is unconditionally `os.Getuid()` (`common.go:305-321`)
- worktree provisioning shells out to `git` on the broker's own FS
  (`provision/provision.go:325,472`)
- the entire agent home — harness config, skills, `agent-info.json`, `.gitconfig`,
  the auth token — is **staged onto the broker's filesystem** and delivered by bind
  mount (`pkg/agent/provision.go:807-975`)

This assumption is what makes remote runtimes hard. K8s works around it by rsyncing
the home into the pod after creation; the NFS backend works around it by computing
paths from IDs.

**In this topology the assumption holds — for reads.** The broker and the sandbox
share one filesystem: the sandbox is launched from inside the launcher container with
`--rootfs /`, and bind mounts resolve against the launcher's own filesystem. **Writes
are a different story — see §3.2a, which qualifies every bullet below.** So:

- No path translation. `/home/scion/.scion/projects/...` means the same thing on
  both sides.
- No UID mismatch. Same kernel, same passwd file, `os.Getuid()` is meaningful.
- No rsync-the-home step. Bind mount it, as Docker does.
- Token refresh's file write (`<agentHome>/.scion/scion-token`) is free.

Two of the three sharp edges that blocked "Strategy C" in `single-node-packaging.md`
vanish here, and they vanish structurally rather than by workaround.

### 3.2z ⚠️ FOUNDATIONAL: the sandbox binary is platform-injected, and gcloud can't request it on Instances

**Found 2026-08-25 by the AC-0 spike, diagnosed same day. Read this before anything
else in §3 or §4.**

The spike deployed a Cloud Run Instance and found **no `/usr/local/gcp/bin/sandbox`,
no `runsc`, nothing** — `find / -name sandbox -type f` returned zero. Kernel cmdline
showed `SANDBOX_GRPC_LOCALPATH_ENABLED=1` and `RIPTIDE_LOCALPATH_ENABLED=1`.

**The finding is correct. The obvious conclusion is wrong.** Guide line 18, which
this design skimmed past for three revisions:

> "Once access is granted for your project, the sandbox binary is granted on **any
> Cloud Run container where the `--sandbox-launcher` flag is enabled**. This feature
> works on Cloud Run services, jobs, and worker pools."

**The binary is injected by the platform at deploy time and is never in the image.**
An Instance deployed without the flag having no sandbox binary is the *expected*
result, not evidence the feature is unavailable. The kernel params were the tell:
the backend is present, the launcher was simply never requested.

**Why it is not straightforward.** `gcloud alpha run instances create` has **no
`--sandbox-launcher` flag** — verified against the full synopsis. In gcloud 575.0.0
the flag exists *only* on `gcloud beta run deploy` (services); not jobs, not
worker-pools, not instances. The guide's own list — "services, jobs, and worker
pools" — omits Instances too.

**But the API supports it.** From the Cloud Run v2 discovery document:

```
GoogleCloudRunV2Instance.containers[]  ->  GoogleCloudRunV2Container
GoogleCloudRunV2Container.sandboxLauncher : boolean
  "Indicates that this container can act as a sandbox supervisor and launch sandboxes."
```

Instances carry Containers; Container carries `sandboxLauncher`. **The capability is
in the API surface for Instances — `gcloud` merely does not expose it.**

**Consequence for the design: deployment must go through the REST API**, setting
`containers[0].sandboxLauncher: true`, not through `gcloud alpha run instances
create`. This lands in §4.10/deploy tooling, not in the runtime. The guide already
anticipates REST deployment for Templated Sandboxes (line 382), so it is not exotic.

**RESOLVED 2026-08-25.** ptone confirms `--sandbox-launcher` is being added to
`gcloud beta run instances` and asked for raw REST in the meantime. I verified the
API accepts it on Instances using `validateOnly=true` (no billable resource):
**HTTP 200, and `"sandboxLauncher": true` is echoed back in
`metadata.containers[0]`.** So the field is honoured on Instances, not merely
tolerated by the shared Container message. **The tier's premise holds.**

Full tested recipe, gotchas, and the remaining verification checklist:
**`deploy-instance-with-sandbox.md`** (same directory).

**Scope of the REST path — clarified by ptone 2026-08-25: raw REST is for the
verification spikes only, not for deploy tooling.** So §4.10 targets
`gcloud beta run instances create --sandbox-launcher` and **does not build a REST
client or a probe-and-fallback path.** I had proposed exactly that; it is
unnecessary complexity for a flag that is days away, and it would have meant
shipping and maintaining a second deploy mechanism to cover a temporary gap.

The practical consequence is a **sequencing dependency, not a design one**: deploy
tooling that shells out to `gcloud` cannot be completed until the flag ships. That
affects P5, not P3 — the runtime invokes the `sandbox` CLI *inside* an
already-deployed Instance and is indifferent to how that Instance was created.

**✅ BINARY INJECTION VERIFIED 2026-08-25** by the AC-0 re-test. A real Instance
(`sbx-test`, us-east4, `sandboxLauncher: true` via REST) was created and entered:

```
-rwxr-xr-x 1 root root 55820984 Aug  4 10:23 /usr/local/gcp/bin/sandbox
```

Sandboxes were then launched, `exec`'d into, and deleted. **The hypothesis is now a
fact and §3.2z is fully closed.** The chain that mattered — flag accepted → binary
injected → sandboxes actually run — is verified end to end, not inferred.

**The fallbacks are dead.** `unshare`-based isolation and bundled `bubblewrap` were
retained only against injection failing. Injection does not fail. **Do not build
them**; if a future finding revives the question, revive it from git history rather
than carrying speculative branches in a design doc.

**Two deployment facts the re-test surfaced, both P5-relevant:**

1. **The container must run a long-lived foreground process** or the Instance
   terminates almost immediately — "Instance completed successfully" after ~21s on
   the first attempt. Cloud Run Instances do not idle-hold a container that exits.
   The omni-image entrypoint (§4.2) must be the hub itself, in the foreground. This
   is normal container discipline, but it is a same-day failure if missed and the
   error message reads like success.
2. **`launchStage: ALPHA` is normalised to `BETA`** in the response. Do not assert
   on it round-tripping.

**Two other AC-0 results that stand regardless:**

1. ⚠️ **tmux socket: NOT actually verified. Do not treat §4.4 as closed.** The first
   spike measured a tmux socket over a bind mount into an **`unshare` child mount
   namespace** and reported PASS — and I recorded that as §4.4's assumption holding.
   **It does not establish the claim.** That spike ran before the sandbox binary was
   available, so it could only test the substitute mechanism it had. A real Cloud Run
   sandbox is **gVisor**, whose filesystem is a gofer/9p-style proxy, not a plain bind
   into a namespace. Unix-domain-socket passing is exactly where those two diverge:
   `unshare` shares the host VFS, so a socket is trivially the same inode; gVisor
   proxies it, and whether a socket *file* is honoured across that proxy is the open
   question. **A PASS on `unshare` is close to no evidence about gVisor.**

   The 2026-08-25 re-test, which *did* have a real sandbox, **skipped this check** —
   `python:3.11-slim` has no tmux. So the strongest test we have is still the weaker
   one, and the gap has now been marked resolved twice by different agents.

   **This remains the single biggest risk in the design** and it gates P4 entirely
   (§4.4 has the fallback). **Re-run it on a real sandbox with a tmux-bearing image**
   — the omni-image (P2) has tmux, so the cheap sequencing is to run it against the
   first omni-image deploy rather than building a bespoke test image.
2. **stdout/stderr is NOT routed to Cloud Logging on an Instance** — unlike Cloud Run
   services. Only `/var/log` files are, via a `loggingfs` FUSE mount, with no
   duplication. This **inverts §4.6 row 5**, which predicted double-ingestion: the
   real problem is the opposite, and logging must write to `/var/log` rather than
   rely on stdout.
3. **Hostname is always `localhost`**, never instance-derived — confirming §4.6
   row 4's concern. `hub_id` must be pinned explicitly; the fallback is unusable.

### 3.2a The qualification: `--rootfs /` is read-only, and writes go to a private overlay

**Found 2026-08-25.** §3.2 as originally written overstated the case, in a way that
would have cost P3 a confusing debugging session. The guide is precise and the two
flags do different things:

- `--rootfs PATH` — "Map the root filesystem (default `/`, **read-only**)". Writes to
  inherited paths land in the sandbox's **private writable overlay** (`rootfs-upper`,
  the thing `sandbox tar` exports). The launcher cannot see them.
- `--write` — "Allow **mounted** filesystems to be writable." It applies to
  `--mount` bind mounts. It does *not* make the inherited rootfs writable.

So the launcher and sandbox share a filesystem **for reading**. For writing they are
separate unless a path is *explicitly* bind-mounted **and** `--write` is passed.

**The trap this sets:** a developer reads §3.2, sees `--rootfs /`, and reasonably
concludes the agent home is already present so no mount is needed. It *is* present —
readable, correct, complete. But `agent-info.json` written by the agent then
disappears into the overlay, the broker reads the stale host-side copy forever, and
status silently never updates. Note this survives adding `--write`: the flag can't
help a path that was never mounted. Same symptom as the §4.3b `--write` omission,
different cause, and it would be debugged twice.

**Therefore the mount list is a hard requirement, not an optimisation.** Everything
the launcher must read back has to be an explicit `--mount ... --write`:

| Path | Why it must be mounted, not inherited |
|---|---|
| Agent home (`<agentHome>`) | `agent-info.json` — the entire self-reported Phase/Activity path (§9.1) |
| Workspace | The agent's actual work product |
| Shared dirs | Cross-agent visibility |
| `TMUX_TMPDIR` dir | The tmux socket (§4.4). A socket created on an overlay path is invisible to the launcher — this makes the mount load-bearing for the whole control plane, not merely convenient |

**A second question this raises, for AC-0:** is the read-only rootfs view *live* or a
*snapshot* taken at launch? §3.2 claims token refresh is free because the launcher
writes `<agentHome>/.scion/scion-token` and the sandbox reads it. That only works if
the view is live. If it is a snapshot, token refresh silently stops working after the
first rotation and P7 changes shape. The agent home is bind-mounted anyway per the
table above, which likely makes this moot — but "likely" is not "verified".

### 3.2b ~~`--mount` and `--env` appear to be single-value~~ — **PREMISE FALSIFIED; and the mitigation had a security cost**

> **✅ RESOLVED 2026-08-25 by the AC-0 re-test. Both flags ARE repeatable.**
> Two `--mount` flags both took effect (`/check-a` and `/check-b` each present with
> correct content). Two `--env` flags both took effect (`FOO=bar` and `BAZ=qux` both
> in the environment). **Both key syntaxes work** — `source=`/`destination=` *and*
> `src=`/`dst=`. The help text renders `string` because the flag is almost certainly
> a `StringArrayVar` with a custom `Type()`; the rendered type was misleading, which
> is exactly the residual risk I flagged below and could not close from docs.
>
> **My inference was wrong, and it was wrong in the direction that costs the most.**
> I read a pflag type string as authoritative about arity. It is a *rendering* choice,
> not a contract. The same reasoning would misjudge any custom `pflag.Value` — and I
> said so in "Certainty" below, then designed as if the risk were the likely case.
>
> **⚠️ Action required — reverse the single-mount-root, and not for tidiness.**
> See §3.2b-r immediately below. The mitigation is not merely now-unnecessary; it
> carries a cross-agent isolation defect that I priced at zero because I believed I
> had no alternative.

**Found 2026-08-25, continuing the flag audit.** §3.2a's *requirement* stands
unchanged; the *mechanism* it prescribed does not.

From the authoritative `-h` output (guide lines 245-253):

```
-e, --env string      Environment variables to set in the sandbox
    --mount string    Mounts for the sandbox (e.g. type=bind,source=SRC,destination=DST)
```

Both are declared **`string`, not `stringArray`**. In cobra/pflag, `StringArrayVar`
renders as `stringArray` and `StringSliceVar` as `strings`; plain `string` is
`StringVar` — **single value, last occurrence wins**. If that reading holds, both
loops in §4.3's `Run` pseudocode are broken: four `--mount` flags would yield one
mount, and N `--env` flags one variable.

**This contradicts §3.2a as I first wrote it.** That section told the developer to
mount agent home, workspace, shared dirs and `TMUX_TMPDIR` *separately*. If
`--mount` takes one value, that is not merely inefficient, it is impossible.

**Certainty: strong evidence, not proof.** A custom `pflag.Value` whose `Type()`
returns `"string"` could still accumulate. The guide contains no repeatable flag
anywhere to calibrate against, so the docs cannot settle it. Delegated to AC-0.

**Resolution — design so arity does not matter.** Both changes are better designs
independent of how the check lands, so P3 is not blocked on the answer:

1. **Do not use `--env` at all.** Pass the environment through the command:
   `-- /usr/bin/env KEY1=V1 KEY2=V2 … sciontool init -- sh -c '<tmux cmd>'`.
   `env` is coreutils, already in the omni-image, and accepts unlimited
   assignments. This also sidesteps quoting ambiguity in a packed `--env` string.
2. **Collapse to a single mount root.** We own the omni-image layout (§4.2), so
   place everything the launcher must read back under one parent:

   ```
   /scion/agents/<slug>/home       agent home, incl. agent-info.json
   /scion/agents/<slug>/workspace
   /scion/agents/<slug>/tmux       TMUX_TMPDIR (§4.4)
   /scion/shared/...               shared dirs
   ```

   Then a single `--mount type=bind,source=/scion,destination=/scion --write`
   satisfies §3.2a whatever the arity turns out to be.

**Minor, same area:** the guide is internally inconsistent on mount keys — line 145
shows `src=`/`dst=`, line 250 shows `source=`/`destination=`. §4.3b chose the latter,
from the `-h` output. ~~AC-0 confirms.~~ **Resolved: both work.** Keep
`source=`/`destination=` — it matches `-h`, and the aliases buy nothing.

### 3.2b-r ⚠️ REVERSAL: mount per-agent, not one `/scion` root

**This supersedes §3.2b point 2 and is a change to work already in flight (P3).**

The single-mount-root was adopted because I believed `--mount` accepted one value.
It does not. With that constraint gone, the mitigation should be re-examined rather
than kept out of inertia — and re-examining it exposes a defect:

> **`--mount type=bind,source=/scion,destination=/scion --write` mounts *every*
> agent's home and workspace into *every* sandbox, writable.**

Layout `/scion/agents/<slug>/…` means one mount of `/scion` gives agent A a
read-write view of agent B's home, workspace, `agent-info.json`, and — because
§4.4 puts `TMUX_TMPDIR` under the same tree — **B's tmux control socket.** That last
one is not a data-confidentiality issue; it is a **control-plane** issue. A sandbox
holding another agent's tmux socket can inject keystrokes into that agent's session.
The sandbox boundary would be intact and irrelevant, bypassed through a path we
mounted ourselves.

I did not price this when I wrote §3.2b, because I had concluded the alternative was
*impossible*. That is the actual error worth recording: **an arity constraint I had
not verified silently became a security argument I never made explicitly.** When a
constraint forces a design, the forced design still needs its own threat model — and
if the constraint later evaporates, the design must be revisited, not inherited.

**Corrected mount set** — per sandbox, all `--write`:

| Source | Destination | Notes |
|---|---|---|
| `/scion/agents/<slug>/home` | agent home | `agent-info.json` (§9.1) |
| `/scion/agents/<slug>/workspace` | workspace | |
| ~~`/scion/agents/<slug>/tmux`~~ | ~~`TMUX_TMPDIR`~~ | ❌ **DROP — §4.4 is dead (§4.4a).** AF_UNIX does not cross the boundary, so there is no socket to share and no reason to mount this. |
| `/scion/shared/<name>` | per shared dir | one mount each; only dirs this agent is entitled to |

Shared directories are *deliberately* shared and stay mounted — the point is that
sharing becomes **explicit and per-entitlement** rather than a side effect of the
mount root. This also makes shared-dir access enforceable later without re-plumbing.

**Cost of the reversal: low, and lower now than after P3 lands.** It is a change to
one function — the `mountsFor(agent)` helper in §4.3's `Run` — from returning one
descriptor to returning N. The on-disk layout does not change. Callers do not change.
`--write` semantics do not change. This is precisely the kind of change that is cheap
while the code is warm and expensive once it is a documented invariant, so it should
land in P3 rather than as a follow-up.

**Keep from §3.2b:** the `/scion/...` on-disk layout itself is good and should stay.
Grouping per agent is what makes per-agent mounting a one-liner.

### 3.2c Three further re-test findings that change implementation detail

None are shape-changing; all are same-day debugging traps.

1. **PATH is empty inside a sandbox.** `HOME=/root` is set; `PATH` is not. Every
   command in the argv we construct must be **absolute**, or we must pass
   `--env PATH=/usr/local/bin:/usr/bin:/bin`. **Prefer setting PATH explicitly** —
   the agent's own child processes (harness → git → node → …) will assume a working
   PATH, and absolute-argv only fixes the first hop. §4.3's argv builder must not
   emit a bare `sciontool` or `tmux`.
2. **`sandbox delete` requires `--force` for a running sandbox.** §4.3's `Delete`
   must pass it, otherwise deletion silently fails for exactly the sandboxes we most
   need to delete. Treat "delete a live agent" as the normal case, not the exception.
3. **`--env` is now the recommended mechanism**, superseding §3.2b point 1's
   `/usr/bin/env` trick. We pass argv directly via `exec.Command` with no shell, so
   there is no quoting hazard to dodge. Residual unknown, minor: whether an `--env`
   *value* containing `=` or a comma survives parsing. Values we control (PATH, hub
   URL, slug) do not exercise it; if arbitrary agent env is ever forwarded, test it.

**Two capabilities the re-test surfaced that we are not using yet** — recorded so
they are not rediscovered:

- **`-p, --publish`** exists on `run`/`do`/`fork` (not `exec`). This is directly
  relevant to **OQ-2** (§8) — the sandbox can expose ports without us hand-rolling
  networking. Whoever answers OQ-2 should start here.
- **`--import-tar` / `--export-tar` / `--sync-tar`** on `sandbox do`, plus
  `sandbox tar` and `sandbox fork`. Together these are a ready-made persist/restore
  primitive, which is the missing piece in §5.4's durability ladder above Tier 0.
  **Not in scope**; noted because it materially cheapens the follow-up.

### 3.3 `WorkspaceBackend` already anticipates Cloud Run

`MountDescriptor` documents a `cloudrun-volume` type and
`SelectWorkspaceBackend` already dispatches it
(`workspace_backend.go:110-113,152-174`). We do **not** need it for the default
path — the Instance's local disk is a normal filesystem and `localBackend` is
correct. It becomes the lever for OQ-1's durable variant.

---

## 4. Proposed Design

### 4.1 Topology

```
┌─ Cloud Run Instance ────────────────────────────────────────────┐
│  omni-image, single container                                   │
│                                                                 │
│  PID 1: scion server start --foreground --enable-hub            │
│    ├── hub (HTTP, :8080)  ──────────────► run.app ingress       │
│    ├── embedded broker (in-process)                             │
│    └── runtime = "cloudrun-sandbox"                             │
│                                                                 │
│  /home/scion/.scion/projects/<slug>/     (ephemeral disk)       │
│      agents/<name>/home/                 ── bind ──┐            │
│      workspace/                          ── bind ──┤            │
│      agents/<name>/tmux/                 ── bind ──┤            │
│                                                     │            │
│  $ sandbox run --rootfs / --write --allow-egress  ◄──┘            │
│         │                                                       │
│         ├── sandbox: scion--proj--agent-1                       │
│         │     PID 1 = sciontool init -- sh -c "tmux new-session…"│
│         ├── sandbox: scion--proj--agent-2                       │
│         └── …                                                   │
└─────────────────────────────────────────────────────────────────┘
```

The hub, the broker, and every agent share one filesystem and one image.

### 4.2 The omni-image

ptone's direction, and structurally required rather than a workaround: `--rootfs /`
means a sandbox inherits *the launcher's* filesystem read-only with a writable
overlay. A sandbox therefore cannot have a different image from its launcher. Until
Templated Sandboxes ship, one image must contain both the hub and every harness.

**Manifest — OQ-11 RESOLVED (ptone, 2026-08-25):** five harnesses —
**antigravity, claude, codex, opencode, grok**. *Not* the "all harnesses" default;
copilot, gemini-cli and hermes are excluded. Note the source directory for grok is
`harnesses/grok-build`, not `harnesses/grok`.

**Base image — settled by construction, not separately asked.** Every harness
Dockerfile and `image-build/hub/Dockerfile` are `ARG BASE_IMAGE` / `FROM
${BASE_IMAGE}` layered on **scion-base**. The omni-image is `FROM scion-base` plus
the union of the five harness install steps plus the hub layer.

```dockerfile
# image-build/omni/Dockerfile  (illustrative)
# ⚠️ SUPERSEDED — see §4.2a. This sketch is what produced a transcription fork
# of five harness Dockerfiles. Build the omni image by CHAINING the harnesses.
FROM scion-base            # scion user @1000, sciontool ENTRYPOINT, /opt/scion/bin
RUN install harness binaries: antigravity(agy), claude, codex, opencode, grok
COPY scion /usr/local/bin/scion
CMD ["scion","server","start","--foreground","--enable-hub", ...]
```

`image-build/hub/Dockerfile` already proves the layering direction — the hub image
extends `scion-base`, which is also the harness base. The omni-image is the union,
not a new construction.

**The image is binaries-only — the harnesses do not collide.** Worth stating because
it is the first thing that looks like a blocker and is not. All five harnesses'
`config.yaml` point provisioning at the *same* path,
`/home/scion/.scion/harness/provision.py`. Five harnesses cannot own one path in one
image — but they never do, because **provision.py is staged at runtime, not baked
in**: `container_script_harness.go:1046` builds `bundleDir := filepath.Join(agentHome,
".scion", "harness")` and `:432-439` copies the *selected* harness's provision.py in
per-agent at launch. The image carries only harness binaries; per-harness
provisioning arrives with the agent.

Two mechanical consequences for the P2 developer:

- **Drop every per-harness `CMD`** (`codex`, `opencode`, `agy`, `grok`). They are
  mutually exclusive and irrelevant: PID 1 is `sciontool init` and the harness
  command comes from the config.yaml `command:` key.
- **Home subdirs do not overlap** (`~/.codex`, `~/.local/share/opencode`,
  `~/.gemini/antigravity-cli`), so the mkdir/chown steps concatenate cleanly. Four of
  the five install via npm global — dedupe that layer.

Because the bundle is staged into the *agent home*, which is bind-mounted, and
`--rootfs /` shares the launcher's filesystem (§3.2), this mechanism works unchanged
under Sandboxes — the same property that makes the self-reported status path work
(§9.1).

**Consequences, all simplifying:**

- `ImageExists` / `PullImage` / `ImageID` / `RemoveImage` collapse to no-ops or
  trivial identity checks. There is one image and it is already here.
- **`requireImageRegistryForBroker` (`server_foreground.go:2618`) stops applying.**
  `single-node-packaging.md` calls this hard startup gate "the single most opaque
  prerequisite" for onboarding. This design deletes the requirement rather than
  documenting it.
- Per-agent image selection (templates choosing a base image) is **lost** until
  Templated Sandboxes ship. This is a real capability regression and belongs in the
  release notes, not buried.

Cost: a large image. Mitigated by the fact that the Instance pulls it once.

### 4.2a ⚠️ CORRECTION: chain the harnesses; do not transcribe them

**My error, found 2026-08-25 by reading what P2 actually built.** §4.2 above says the
omni-image is "the union of the five harness install steps" and sketches a single
Dockerfile. That is what got built — `image-build/omni/Dockerfile` on the integration
branch — and "union" turned out to mean **hand-transcription**. It now carries its own
copies of `GIT_DELTA_VERSION=0.18.2`, `ZSH_IN_DOCKER_VERSION=1.2.0`,
`AGY_VERSION=1.1.17`, the npm package list, the dbus/keyring bashrc snippet,
`init-firewall.sh` + sudoers, and the grok install incantation.

**That is a maintenance fork of five files with no drift detection.** When
`harnesses/claude/Dockerfile` bumps a version or adds a tool, omni keeps the old one
and *nothing fails*. The resulting defect class is the worst kind available here: an
agent behaves differently on single-node than on every other tier, with no error, no
failing test, and no diff to point at. Note the code is not wrong today — it is
*correct and unanchored*, which is precisely why it will rot quietly.

**The mechanism that avoids it was already there and I missed it.** All eight harness
Dockerfiles are `ARG BASE_IMAGE` / `FROM ${BASE_IMAGE}` — the same property §4.2
cites when arguing the layering direction works. It does not merely permit layering
*onto scion-base*; it permits **stacking harnesses on each other**:

```
scion-base -> claude -> codex -> opencode -> antigravity -> grok-build -> omni
```

`image-build/scripts/lib/targets.sh` already models exactly this shape:
`step_parent()` and `step_build_args()` thread a chain today
(`core-base|thick-prep -> scion-base -> harness`), and the file's own comment says
the orchestrator "thread[s] :short-sha through chained builds". **So the omni image
should be a target definition, not a Dockerfile**, and the harness definitions stay
single-source.

**Gate it on `OMNI_BUILD`, following the exact precedent `THICK_BUILD` set in
#1087** (`build-images.sh:108-111`, `targets.sh:184-189`). That keeps every existing
build path byte-identical, which answers the one serious objection below.

**Objections, none fatal, all named so this is a decision and not an instruction:**

| Objection | Weight |
|---|---|
| `targets.sh` is shared by every tier's build; the standalone Dockerfile has zero blast radius | Real — **mitigated by the `OMNI_BUILD` gate** |
| ~~Loses the deduped single `npm install -g`; three npm layers instead of one~~ | **Void.** Image streaming (§4.2b) means unfetched bytes cost nothing. |
| Five sequential build steps instead of one | Slower periodic build; not a runtime cost |
| Harness Dockerfiles were never *designed* to stack | **The real risk.** `CMD` is last-wins (irrelevant — omni sets its own), but **`USER` state is order-dependent**: `grok-build` deliberately ends as root ("Do NOT switch back to USER scion"). Pin `USER` explicitly at the end of the chain. |

**Recommendation: chain, gated on `OMNI_BUILD`** — drift across five duplicated files
is a permanent silent tax, while every objection is one-time or bounded. **But build
the chain before committing to it**; my confidence here is from reading, not running.
**If the build fights back, the transcription is a legitimate fallback — on condition
it gains a CI check diffing its pinned versions against the harness Dockerfiles**,
because the failure mode is silence and silence needs a detector.

### 4.2b Base image: thick (Cloud Workstations) — pending one question

**ptone proposed 2026-08-25:** use the **thick base** from
`GoogleCloudPlatform/scion#1087` (MERGED, already on our `main`) rather than
`core-base`. `image-build/thick-prep/Dockerfile` adapts
`cloud-workstations-images/predefined/base` for scion-base: `userdel ubuntu` (UID
1000 clash), installs zsh/fzf/gh/ripgrep, creates `/usr/local/share/npm-global`.

**Leaning strongly yes:**

- **Zero new build infrastructure** — `--target thick` already routes scion-base to
  thick-prep, and #1087 reports all 10 images built and verified.
- **amd64-only is a non-constraint *here*.** thick-prep is explicit that the
  workstations base has no arm64 variant. Cloud Run Instances are amd64, and the
  omni-image is specific to this tier — it never runs on Apple silicon. This
  limitation would block a general base-image switch; it does not block ours.
- thick-prep's compat fixes are things omni would otherwise re-solve itself.

**✅ RESOLVED 2026-08-25 — ptone: "instances use GCP's image streaming technology, so
there is no penalty in pre-fetching the large image." Adopt the thick base.**

The question was whether the workstations base — **2.01 GB compressed** (19 layers,
largest 696 MB; call it 5-6 GB uncompressed) plus ~1-2 GB of harnesses — would be
pulled in full on every Instance cold start. With streaming, layers are fetched
lazily on access, so the size never lands as startup latency. That mattered because
**Tier 0 is pure ephemeral (§5), which makes redeploy the normal lifecycle** — a
per-pull cost would have recurred rather than amortised. It does not.

**Three consequences that reach beyond the base-image choice:**

1. **§4.2's closing line — "Cost: a large image. Mitigated by the fact that the
   Instance pulls it once" — is now wrong in its reasoning but right in its
   conclusion.** It is not mitigated by pulling once (lifetimes are short by design);
   it is mitigated by never pulling in full at all.
2. **Unused harnesses are never fetched.** Streaming makes the omni-image cost
   roughly "what you actually execute", which removes the main argument against
   bundling all five harnesses — and against bundling more later.
3. **It voids the layer-size objection to §4.2a's chain.** Three separate npm layers
   instead of one deduped layer costs nothing if the bytes are never fetched. See the
   revised objection table there.

**Residual, non-blocking:** streaming is lazy, so **first access** to a given file
pays a fetch. Two places that could surface: first launch of a given harness, and
sandboxes reading the streamed filesystem through `--rootfs /` — the gVisor gofer
reading from a lazily-materialised backing store is a combination we have not
exercised. Expected to be fine and **not worth gating on**; worth watching for
first-run latency spikes rather than assuming a bug elsewhere.

**Sharpening the cost, in thick's favour:** `--rootfs /` means sandboxes inherit the
filesystem with **no copy**, so image size **does not touch per-agent launch latency
at all** — only Instance startup. The blast radius of being wrong here is one
number on one operation, not a per-agent tax.

**Second, smaller unknown:** does the workstations base ship services or an entrypoint
expecting the workstations environment? We override `CMD`, but confirm nothing
background-starts and then fails noisily in a non-workstations context.

**Sequencing:** §4.2a and §4.2b touch the same files. **No image has been built or
pushed yet**, so both are free right now and get monotonically more expensive. Do
them as one change, once.

**Disposition (2026-08-25):** `sn-impl-em` initially deferred §4.2a to "P5+ or a
dedicated P2-rework workstream", on the grounds that the merged transcription is
functional and the rework is outside P0–P3. Recorded because the reasoning recurs:

- **P2 is inside that scope** — the EM brief assigns "Omni-image.
  `image-build/omni/Dockerfile` + build wiring". *Merged* is not *out of scope*.
- **"Functional" is not the objection.** The transcription works; that is why the
  defect is dangerous. It is correct and unanchored, and nothing fails when it drifts.
- **The deferral does not buy what it is meant to buy.** If §4.2b lands, the image is
  rebuilt and re-reviewed regardless — so the chain rides along nearly free now, or
  costs a second rebuild and a second review later. **Deferring is the more expensive
  option here, not the cheaper one.**

**Resolved — compromise accepted by `sn-impl-em` the same day.** P3 proceeds against
the transcription image so nothing is held up (the chain yields the same image
contents, so P3 is indifferent to which produced it). **§4.2a + §4.2b land together
as a single change and are a P2 exit criterion, tracked as an open P2 item — not a
P5 follow-up.** Trigger: ptone's answer on image streaming (§4.2b).

The contingency stands in case the chain does not build: **if the transcription is
kept, it must gain a CI check** diffing its pinned versions against the harness
Dockerfiles (§4.2a). Silence is the failure mode, so it needs a detector.

**Landed 2026-08-25** (`f06d6cbf`, merged at `632c230`). The omni Dockerfile dropped
from 125 lines of transcription to a ~15-line hub layer over
`scion-base → claude → codex → opencode → antigravity → grok-build`, gated on
`OMNI_BUILD`, with omni implying thick (correct — the thick base has no arm64
variant). `cloudbuild-omni.yaml` was deleted as dead, which is right: it built the
transcription.

#### 4.2a-ci ⚠️ Open P5 item: omni has no CI build path

The same change **gated the omni target to `--builder local-docker`**, with
`cloud-build.sh` now returning an error for it. Recording this because it is a
capability regression that arrived inside a `chore`/`feat` commit rather than as a
decision, and it should not be inherited silently.

The stated reason is that the omni target "chains harness images and must be built
locally." **That is a property of the implementation, not of Cloud Build.** A chain is
six sequential build steps, each passing the previous image as `--build-arg
BASE_IMAGE` — ordinary Cloud Build. Nothing prevents it; the yaml simply was not
written.

Three consequences, in ascending order of seriousness:

1. **The deployment artifact for this entire tier has no CI build path.** The omni
   image is not an optional convenience; it is what an Instance boots.
2. **Thick base is amd64-only** (§4.2b). A maintainer on an arm64 laptop cannot build
   it natively, and emulating a 2 GB base plus five harness layers is punishing at
   best. "Build it locally" quietly assumes an amd64 builder.
3. **It partially reintroduces the very problem the chain rework solved.** §4.2a
   existed because the transcription would drift silently when harnesses bumped
   versions. Chaining fixes drift *by construction* — but only for images that
   actually get rebuilt. An artifact nobody can rebuild in CI is one that stops
   tracking its inputs for a different reason. We removed drift-by-transcription and
   left drift-by-nobody-rebuilds.

**Not a P0–P3 blocker** — building locally once to validate is fine, and P2's exit
criterion (chain + thick) is genuinely met. **This belongs to P5 (deploy tooling)** as
an explicit item: write `cloudbuild-omni.yaml` as a chained multi-step build, or state
on the record why local-only is acceptable for this artifact. What must not happen is
that the gate becomes the de-facto answer because nobody revisited it.

##### 4.2a-ci-rev — resolved 2026-08-26. The diagnosis above was **half wrong**, and the real gap is larger

Audited `.github/workflows/` and `image-build/scripts/` rather than reasoning from the
commit message. Two corrections, in opposite directions.

**Correction 1 — omni is *not* locked out of CI. It never was.**
`build-images.sh` defaults to `BUILDER="local-docker"`, and `build-images.yml` runs on
`ubuntu-latest` **without passing `--builder`**. The GitHub runner *is* an amd64 Docker
builder. So the `cloud-build.sh` gate blocks exactly one backend — Cloud Build — and
does nothing to CI as a whole.

The **only** thing preventing a CI omni build today is that `omni` is missing from the
`target` choice list in `build-images.yml` (both `workflow_dispatch` and
`workflow_call`). That is a one-line enum edit, not the chained-yaml project §4.2a-ci
assumed. **The error text is what misled us** — *"omni target chains harness images and
must be built locally"* reads as a property of the artifact; it is a property of one
backend. Retitle it: *"the omni chain has no Cloud Build yaml; use `--builder
local-docker` (the default), which works in CI."*

**Correction 2 — and this is worse than what we thought we had. *No* image is built
automatically, by anything.** `build-images.yml` has only `workflow_dispatch` and
`workflow_call` triggers, and **`build-release.yml` never calls it.** Every image in
this project is published by a human remembering to dispatch a workflow.

For most of the project that may be a deliberate cost trade. **For this tier it is
fatal to the success criterion**: §1 says the operator *runs one deploy command* — which
requires a published, versioned omni image at a predictable location. A deploy command
that cannot name an image that exists is not one command.

So the real §4.2a-ci item is not "write a cloudbuild yaml". It is:

| # | Change | Size |
|---|---|---|
| 1 | Add `omni` to the `target` enum in `build-images.yml` (both trigger blocks). | One line ×2 |
| 2 | **Trigger it on release.** `build-release.yml` calls `build-images.yml` with `target: omni` on tag push; tag the image with both the release tag and `latest`. | Small |
| 3 | **`platform: linux/amd64` for omni, explicitly.** Cloud Run runs amd64, and the thick base is amd64-only anyway (§4.2b) — an arm64 leg is impossible for this target and pointless for this tier. Do not inherit the `all` default. | One line |
| 4 | Fix the misleading `cloud-build.sh` error text (above). | One line |
| 5 | Decide and document the **registry + tag convention** the deploy command defaults to, so `--image` is optional. | Decision |

**⚠️ The one real risk, to be verified rather than assumed: disk.** The omni chain is
six sequential images over an amd64-only thick base of ~2 GB. A standard `ubuntu-latest`
runner has roughly 14 GB free. This may simply not fit. **Verify before committing to
the release trigger**; if it doesn't fit, the options are a free-disk-space step, a
larger runner, or reinstating Cloud Build for this target after all — which is where a
`cloudbuild-omni.yaml` would finally earn its keep. Note commit `eb3bb709` deleted an
orphaned `cloudbuild-omni.yaml`; if we come back to Cloud Build, that is the starting
point rather than a blank page.

**Lesson worth keeping.** §4.2a-ci was written from a commit message and an error
string, and it produced a plausible, confidently-worded diagnosis that was wrong in both
directions — it overstated the blocker (CI *could* build omni) and understated the gap
(nothing builds *anything* automatically). Same failure mode as the `gcloud`
path-construction hypothesis in §10b.1: inferring a mechanism from a message rather than
reading the thing.

### 4.3 The `cloudrun-sandbox` runtime

New file `pkg/runtime/cloudrun_sandbox_runtime.go`. Shells out to
`/usr/local/gcp/bin/sandbox`, in the same spirit as `DockerRuntime` shelling out to
`docker`. It must **not** reuse `buildCommonRunArgs` — that emits Docker CLI grammar.
It follows the K8s precedent of translating `RunConfig` into its own vocabulary.

```go
// Illustrative only.
type CloudRunSandboxRuntime struct {
    bin     string            // /usr/local/gcp/bin/sandbox
    state   *sandboxStateStore // see §4.5 — the CLI has no `list`
}

func (r *CloudRunSandboxRuntime) Name() string     { return "cloudrun-sandbox" }
func (r *CloudRunSandboxRuntime) ExecUser() string { return "scion" }

func (r *CloudRunSandboxRuntime) Run(ctx, cfg RunConfig) (string, error) {
    // NOTE: the verb is `run`, not `create` — there is no `create` verb.
    // Mounts are read-only unless --write is passed; the agent home MUST be
    // writable or agent-info.json cannot be written (see §4.3b).
    // --allow-egress is REQUIRED: sandboxes have no network at all without it,
    // so neither inference endpoints nor the hub are reachable (§4.3c).
    args := []string{"run", sandboxName(cfg), "--detach",
        "--rootfs", "/", "--write", "--allow-egress"}
    // mountsFor MUST be exhaustive. --rootfs / makes these paths *readable*, but
    // writes to inherited paths land in the sandbox's private overlay and never
    // reach the launcher (§3.2a). Anything the launcher reads back — agent-info.json,
    // the workspace, the tmux socket — has to be an explicit bind mount.
    for _, m := range mountsFor(cfg) {          // home, workspace, shared dirs, tmux
        args = append(args, "--mount",
            "type=bind,source="+m.src+",destination="+m.dst)
    }
    for _, e := range envFor(cfg) {             // incl. TMUX_TMPDIR
        args = append(args, "--env", e)
    }
    args = append(args, "--", "sciontool", "init", "--", "sh", "-c", tmuxCmd(cfg))
    // …exec, then record in r.state
}
```

#### 4.3a `ExecUser()` cannot be honoured by a flag — it must be honoured by wrapping

**Found 2026-08-25 while reviewing P2.** The stub above returns `"scion"` like every
other runtime, but unlike every other runtime the sandbox CLI has **no `--user`
flag**. `sandbox exec`'s complete flag set is `-e/--env`, `--workdir`, `-h/--help`.
Docker, Podman and Apple all hardcode `exec --user scion`
(`docker.go:264,267,373`); `ExecUser()` is a Runtime-interface method
(`podman.go:125`, `k8s_runtime.go:99`, `cloudrun_runtime.go:73`, …) that the
sandboxes runtime **cannot satisfy natively**.

This interacts with a P2 decision. The omni-image correctly drops `USER scion` before
`CMD` — consistent with `image-build/hub/Dockerfile`, which also ends at `USER root`
— because PID 1 must be root: `sciontool init` drops privileges *for the child* via
`syscall.Credential` (`supervisor/supervisor.go:100-107`). So the agent process runs
as scion, but the sandbox's PID 1 is root.

Consequence if unaddressed: `Exec` and `Attach` enter as **root**, where on every
other runtime they enter as scion. Two harms, and the second is a functional break
rather than a hardening nit:

1. The browser terminal and `scion look`/exec hand the user a root shell — a
   privilege escalation relative to Docker.
2. Files created in that shell land root-owned in the bind-mounted agent home and
   workspace, which the scion-uid agent process then cannot write. Because
   `--rootfs /` makes launcher and sandbox share one filesystem (§3.2), this
   contaminates host-side state, not just a container overlay.

**Required:** the runtime must wrap rather than flag. Every `Exec`/`Attach`
invocation drops privileges inside the command it sends:

```
sandbox exec <id> -- setpriv --reuid=1000 --regid=1000 --clear-groups <cmd>
    # or: su scion -c "<cmd>"   (scion-base is Debian-derived; both available)
```

`ExecUser()` then stays truthful — it reports the user the runtime actually enters
as, and the wrapping is what makes the report true. **This belongs in P4's brief**
(and in P3 if `Exec` lands there); a developer implementing against the stub alone
will ship a root shell without noticing.

#### 4.3b Three CLI corrections — verified against the user guide, all land in P3

The pseudocode above was corrected 2026-08-25 after re-checking it against the
sandbox CLI's documented surface. All three would have been hit on P3's first run.

1. **There is no `create` verb.** The complete verb list is `completion`, `delete`,
   `do`, `exec`, `fork`, `help`, `run`, `tar`, `wait`. Creating a persistent sandbox
   is `sandbox run <name> --detach`.
2. **`--mount` does not take `src:dst`.** The documented form is
   `type=bind,source=SRC,destination=DST`.
3. **`--write` is mandatory for us, and its absence is the dangerous one.** Mounts
   are **read-only by default**; `--write` is what makes mounted filesystems
   writable. Without it the agent home is read-only, `agent-info.json` cannot be
   written, and the entire self-reported Phase/Activity path (§9.1) silently
   produces nothing. This would present as "the agent runs but never reports
   status" — a slow, confusing failure rather than a loud one.

**Residual ambiguity for AC-0 to settle:** `sandbox run -h` documents the usage as
`run [command-to-execute] [flags]`, but the guide's own worked example is
`sandbox run my-session-1 --detach` followed by `sandbox exec my-session-1` — i.e.
the positional is the *name*. Meanwhile `do` has an explicit `--sandbox-name` flag
that `run` does not list. P3 must verify which the positional actually binds to
before relying on it; if `run` has no name parameter, sandbox identity has to come
from `--sandbox-name` (if accepted) or from parsing the CLI's output, which changes
§4.5's state store keying.

#### 4.3c `--allow-egress` is mandatory — and it does not buy GCP access

**Found 2026-08-25, continuing the §4.3b audit.** The Run invocation was missing
`--allow-egress`. Sandboxes have **no egress network access whatsoever** by default
(guide, "Security / isolation"). Without the flag, a sandboxed agent can reach
neither its inference endpoint nor — under the hairpin topology of §4.9 — the hub
itself. Nothing works. It is now in the Run args.

**The more consequential half is what `--allow-egress` explicitly does *not* grant.**
The guide is unambiguous: sandboxes "are not granted access to any Google Cloud
Platform services and cannot use the service account associated with this Cloud Run
resource." So:

- Public inference endpoints over HTTPS (`api.anthropic.com`, `api.openai.com`, …)
  work with `--allow-egress`. This is consistent with ptone's Q3 answer that we can
  reach inference endpoints.
- **Vertex AI via ADC does not.** Neither does any auth path that mints credentials
  from the metadata server or borrows the Instance's service account.

That is a **capability regression specific to this tier**, and it is not small:
`harness-auth` supports `vertex-ai`, and `gcloud-adc` appears as an auth option in
the config.yaml of three of our five shipped harnesses (claude, antigravity, grok).
Under Sandboxes those modes are structurally unavailable — only API-key and
OAuth-token modes function.

**Consequences to carry:**

- P3 must not offer `vertex-ai`/`gcloud-adc` on this runtime; it should reject them
  at launch with a clear message rather than let the agent fail obscurely at first
  inference call.
- Belongs in release notes beside the per-agent-image regression (§4.2).
- **Needs confirmation from ptone** that "we can reach inference endpoints" means
  public API endpoints rather than Vertex — the two readings differ materially and
  the design currently assumes the former.

#### 4.3d `sandbox wait`'s exit-code semantics are undocumented — and the whole exit-code design rests on them

The guide lists `wait` as "Wait for a sandbox to exit" with **no flags and no
documented output or return semantics**. §4.5 and §9.1 both assume it yields the
child's exit status — that assumption is what makes `ExitCode *int` populatable at
all, and it is the argument I gave `agent-status-lead` for making the heartbeat
level-triggered.

If `wait` merely blocks and exits 0, this runtime has **no runtime-observed exit code
source whatsoever**. The consequences are contained but real:

- `ExitCode` would arrive only via sciontool's own direct crash report (PID 1 is
  `sciontool init`, which reports to the hub independently). That path already
  exists and the hub already tolerates it — `handlers_runtime_brokers.go` comments
  that its derivation "works even if sciontool's own crash report never reached the
  hub", i.e. the two paths are deliberately redundant.
- What is lost is the case sciontool cannot report: SIGKILL, OOM, and anything that
  kills PID 1 before it can speak. Those are precisely the interesting crashes.
- It does **not** invalidate #1257 — Docker/K8s still populate the field, and the
  level-triggered wire format is still correct. It would mean this runtime reports
  `Phase=stopped, ExitCode=nil`, which is exactly why D2 ("nil means unknown, not
  clean exit") was worth insisting on.

**AC-0 must settle this.** If `wait` gives no code, §4.5's watcher goroutine still
has a job (detecting the transition) but records `nil`, and the release notes should
say crash exit codes are unavailable on this tier.

**Method-by-method contract:**

| Method | Implementation |
|---|---|
| `Run` | `sandbox run <name> --detach --rootfs / --write --allow-egress` + record state |
| `Stop` | `sandbox delete` — K8s sets the precedent that Stop==Delete when the platform has no pause verb. **See the grace-period note below.** |
| `Delete` | `sandbox delete` + drop state |
| `List` | **from local state store**, not the CLI (§4.5) |
| `Exec` | `sandbox exec <name> -- <cmd>` — maps directly; `Runtime.Exec` is request/response and merges stderr into stdout (`common.go:554-567`) |
| `Attach` | *not* via the CLI — via the shared tmux socket (§4.4) |
| `GetLogs` | **fallback only** — see below |
| `ImageExists`/`PullImage`/`ImageID` | no-ops (§4.2) |
| `RemoveImage` | no-op |
| `Sync` | no-op — filesystem is shared |
| `GetWorkspacePath` | the launcher-side path, verbatim — it is the same path |

**Logs are almost free.** The primary log source is not the container runtime: the
broker reads the host-side `agent.log` from the bind-mounted agent home and only
falls back to `Runtime.GetLogs` (`handlers.go:1897-1955`, fallback at `:1946`).
Since we bind-mount the home, we get the primary path for nothing. `GetLogs` may
return an explanatory error, as the Cloud Run stub already does.

The one real gap: **a sandbox that dies before `sciontool init` writes `agent.log`
leaves no trace** — i.e. exactly the mount/entrypoint failures you most want logs
for. Mitigation: capture `sandbox run`'s own stderr at launch and persist it
host-side.

**`ContainerStatus` is a leaky abstraction — being cleaned up as a prerequisite
workstream.** Today `List` would have to synthesize Docker-format strings (`"Up 3
minutes"`, `"Exited (137) 2 seconds ago"`) because the hub regex-parses them for
exit codes (`common.go:962`, consumed at
`handlers_runtime_brokers.go:720-728,758-792`). Emit the wrong shape and crash
detection silently breaks.

**ptone has directed (2026-08-25) that this be fixed in a separate workstream before
implementation begins.** Agreed — a brand-new backend fabricating fake `docker ps`
prose to satisfy a regex is exactly the wrong place to pay this debt. See §9.1 for
scope; `cloudrun-sandbox` should be written against the *structured* contract and
never grow a string synthesizer.

**Grace period — we must adapt to the platform, not the other way round.**
Q9 (ptone, 2026-08-25): `sandbox delete` **does** signal the workload, and it has
**only a `--force` flag — no configurable grace period.**

That is good news and a constraint. Good: we are not stuck with the K8s defect
(`k8s_runtime.go:1826-1851` hard-codes `gracePeriod: 0`, skipping the shutdown
sequence so agents report `offline` instead of `stopped`). Constraint: we cannot
*lengthen* the window.

PID 1 traps SIGTERM and runs a shutdown sequence ending in a final status report
(`init.go:880-1010`), gated by `--grace-period`, **default 10s**. Since we cannot
extend the platform's window, we must fit inside it:

- **Set `SCION_GRACE_PERIOD` in the omni-image** (`init.go:83-87`) to sit safely
  under the platform's implicit grace. If that window turns out to be short, 5s is
  the sane default — the shutdown sequence is a handful of local writes and one HTTP
  POST, not slow work.
- **Never pass `--force` on `Stop`.** Reserve it for `Delete` escalation after the
  graceful attempt has been given its chance.

This is the same class of bug the Docker backend already has and nobody noticed:
Docker's `Stop` passes no `--time`, so its 10s default exactly equals sciontool's
own 10s and the final report is frequently lost to a race. **We should not
replicate it.** AC-0 must measure the platform's actual pre-SIGKILL window, since
it is undocumented and now load-bearing.

### 4.4 ⚠️ STATUS REVERSED TWICE — READ §4.4-rev FIRST

#### 4.4-rev Correction, 2026-08-25 (same day): `--host-uds` IS set

**ptone, relaying an engineering-team correction:** *"the eng team came back with a
correction that `--host-uds=host` is set — so the original approach MAY work."*

> ### ✅ RESOLVED BY TEST, 2026-08-25 — §4.4 is dead after all; §4.4a is the design
>
> `spike-uds` ran Tier A against a real Sandbox on a real Instance (`us-east4`,
> `sandboxLauncher: true`). **Every socket-crossing test failed.** Full raw output in
> `ac0-results.md`.
>
> | Test | Result | Failure mode |
> |---|---|---|
> | **T1** create inside → connect from launcher | ❌ FAIL | `ENOENT` — the socket inode is **not propagated at all**. Regular files on the same mount *are* visible. |
> | **T2** create on launcher → connect from inside | ❌ FAIL | `ECONNREFUSED` — socket file **is** visible with correct metadata (`type=socket`), but the connection is not proxied. |
> | **T3a–d** tmux over the socket | ❌ FAIL | Socket never visible; tmux never reached. |
> | **T3e** uid check | ✅ PASS | uid=0 both sides — **not** the failure cause. |
>
> **The two directions fail differently, and that is the useful part.** Create fails at
> the inode layer; open fails at the connect layer. The gofer proxies regular file I/O
> on bind mounts and does not proxy AF_UNIX operations in either direction.
>
> **Reconciling this with `--host-uds=host` being set.** The spike's hypothesis, which
> I find convincing: the setting plausibly governs access to **host-native** sockets on
> the VM (a `/var/run/docker.sock`-shaped use case) and does not extend to sockets on
> **gofer-mediated bind mounts**. Configuration and behaviour are not actually in
> conflict; they are about different paths. **Behaviour is what we design against.**
>
> **Two residual unknowns — do not record these as answered:**
>
> 1. **`SCM_RIGHTS` fd passing was never tested.** T3d failed before reaching it. The
>    §4.4-rev concern about `tmux attach` needing ancillary-data passing is *untested*,
>    not *disproven*. Any future revival of §4.4-orig must still answer it.
> 2. **uid=0 on both sides is not representative.** Production agents run as uid 1000.
>    T3e passed trivially and the failure was not uid-related, so this does not affect
>    the conclusion — but a revival must retest at 1000.
>
> **Not pursued, and why.** One variant remains untested: a socket on a path inherited
> via `--rootfs /` rather than on an explicit bind mount, which the spike's hypothesis
> leaves marginally open. I am **deliberately not chasing it yet.** §4.4-orig's only
> remaining advantages over §4.4a are latency and avoiding the PTY wrapper — and
> Tier B's T6 and T8 measure exactly those. If the `script` trick works and exec
> latency is acceptable, the variant is worth nothing even if it works. **Sequence
> Tier B first; revisit only if Tier B disappoints.** Chasing a dead design's
> variants before measuring whether the live one is adequate is how spikes multiply.

**~~The earlier ruling is withdrawn.~~** ~~§4.4 is **not** dead; it is **unresolved
pending test**.~~ **Superseded — the withdrawal itself was withdrawn.** The original
ruling was correct; the correction to it was not. §4.4a is the design. What follows
records the reasoning while it was open.

**Status of everything downstream:** the P3 removals (tmux mount, `TMUX_TMPDIR`,
`TmuxSocket` state field) were correct *given the information at the time* and are
cheap to restore — they were three small commits, all recorded. **Do not restore them
yet.** Restore on a T3 pass, not on the correction alone; that is the whole point of
§10a.

##### The distinction that decides this

`--host-uds` is not a boolean. In runsc it selects *which operations* on host unix
sockets are permitted from inside the sandbox:

| Value | Sandboxed process may… |
|---|---|
| `none` | nothing |
| `open` | **connect to** an existing host socket |
| `create` | **bind/create** a host socket |
| `all` | both |

**§4.4-orig needs `create`.** The tmux *server* runs **inside** the sandbox and
**binds** the socket on the bind mount; the launcher is native and merely connects.
If the enabled mode is `open` only, our direction fails and the *reverse* topology
(server on the launcher, client inside) would be the one that works — a different
design, not the original one. **T1 and T2 exist precisely to separate these**, so run
both regardless of what the flag is reported to be.

##### The second-order risk: fd passing

Even with `create`, a socket that connects is not the same as tmux working.
**`tmux attach` passes the client's terminal file descriptor to the server over the
socket via `SCM_RIGHTS`**, and tmux also relies on socket ownership/`SO_PEERCRED`-style
checks. A gVisor host-UDS implementation may proxy byte streams while not proxying
ancillary data or credentials across the boundary.

**The plausible outcome is therefore a partial pass**: `has-session`, `send-keys` and
`capture-pane` — all simple request/response — succeed, while `attach` fails. That
would still be a very good result (it restores the three control operations at native
latency and leaves only attach on §4.4a's `sandbox exec` path), but **it must be
detected rather than averaged into a single PASS/FAIL.** T3 is subdivided accordingly
in §10a.

**Also check uid mapping.** tmux creates `$TMUX_TMPDIR/tmux-<uid>/` mode 0700 and
refuses sockets it does not own. Inside the sandbox the agent runs as uid 1000; if the
launcher-side process has a different uid, tmux will refuse on ownership grounds — a
failure that looks like a socket problem and is not one.

---

### 4.4 ~~The tmux socket bind-mount — the central mechanism~~ ~~❌ DEAD — see §4.4a~~ **← superseded by §4.4-rev above; ruling withdrawn**

> **❌ REFUTED 2026-08-25, definitively, by the Cloud Run platform team via ptone:**
>
> > *"This won't work. We would need to run runsc/gvisor with `--host-uds` enabled
> > which we don't."*
>
> **AF_UNIX sockets do not cross the sandbox boundary.** The mechanism below is dead
> and must not be implemented. §4.4a replaces it.
>
> **This is the outcome the design flagged as its single largest risk**, and the
> answer arrived before anyone built on it — which is the whole return on having
> re-opened it after the `unshare` spike was mistakenly recorded as a PASS. Had that
> "PASS" stood, P4 would have been built against a mechanism the platform cannot
> support.
>
> **Scope this precisely — it is a socket finding, not a mount finding.** Bind mounts
> work; the AC-0 re-test verified files crossing them in both directions. §3.2a,
> §3.2b-r and the whole `agent-info.json` status path are **unaffected**. What fails
> is exclusively `AF_UNIX`. Do not let this finding metastasise into doubt about
> mounts.
>
> **Second casualty:** §4.9a option **B** (hub API over a unix socket) dies by the
> same sentence. One answer closed both questions, which is why it was worth asking
> in platform terms rather than tmux terms.

### 4.4-orig The refuted proposal (retained for context only)

**The problem.** Every way Scion drives an agent goes through `tmux` inside the
container: `tmux send-keys` to deliver a task, `tmux has-session` for liveness,
`tmux attach` for the browser terminal. Today that is reached with
`docker exec -it --user scion <id> tmux attach -t scion`. The sandbox CLI has
`exec`, but it lacks `attach`, `logs`, `list`, and `inspect` — and the interactive
PTY path (`pkg/runtimebroker/pty_handlers.go`) builds a Docker-grammar command line
directly.

**The proposal.** Don't reach *into* the sandbox for tmux. Bring the tmux socket
*out*, and speak to it from the launcher.

tmux places its socket at `$TMUX_TMPDIR/tmux-<uid>/<name>`, defaulting to `/tmp`.
Scion currently uses the default socket (`common.go:480`, `k8s_runtime.go:934`), so
`TMUX_TMPDIR` is an unused lever. Therefore:

1. Launcher creates `<agentDir>/tmux/`.
2. Sandbox is created with that dir bind-mounted at `/scion-tmux` and
   `TMUX_TMPDIR=/scion-tmux`.
3. The agent's tmux server, running as uid 1000 inside the sandbox, binds
   `/scion-tmux/tmux-1000/default`.
4. The launcher sees the same socket at `<agentDir>/tmux/tmux-1000/default`.

Every tmux operation now runs **as a plain local process in the launcher**:

| Operation | Command from the launcher |
|---|---|
| attach (PTY) | `tmux -S <sock> attach -t scion` |
| send task | `tmux -S <sock> send-keys -t scion:agent …` |
| liveness | `tmux -S <sock> has-session -t scion` |
| logs | `tmux -S <sock> capture-pane -p -t scion:agent` |

This substitutes for all four missing CLI verbs at once, and it removes the sandbox
CLI from the latency-sensitive interactive path entirely.

> **Load-bearing and unverified.** This depends on a Unix domain socket working
> across the sandbox's bind mount. If sandboxes are gVisor-backed, UDS across a
> gofer mount may not work. **This is the single highest-value thing to test on day
> one of alpha access** (see AC-0).

**If the socket does not cross the mount**, two fallbacks, in order of preference:

1. **`sandbox exec` with a TTY** — `sandbox exec <name> -- tmux attach-session -t
   scion`, driven through `pty.StartWithSize` exactly as `startDockerExec` does.
   Requires the CLI's `exec` to allocate a TTY (**OQ-6**). Slower, and puts the
   sandbox CLI in the interactive path, but a small change.
2. **Accept degradation and refuse attach.** There is a first-class precedent:
   `managed:` runtimes already decline with *"attach is not supported for managed
   agents — use scion message and scion look"* (`cmd/attach.go:158-160`). Crucially,
   **`scion look` and `scion message` keep working over non-interactive exec** —
   `look` is `tmux capture-pane`, `message` is `tmux send-keys` / `paste-buffer`,
   both plain `Runtime.Exec`. So the loss is precisely the live terminal (CLI
   `attach` and the web terminal page), not agent control.

That third option is worth naming explicitly because it means **§4.4 failing does
not sink the design** — it costs a feature, not the tier.

### 4.4a ✅ REPLACEMENT: tmux stays *inside*; `sandbox exec` is the transport

**This supersedes §4.4 and re-scopes P4. It is fallback 1 above, promoted — the
design anticipated this branch, so this is a re-scope, not a redesign.**

**The reframing that makes it work:** §4.4 tried to move the *socket* across the
boundary. Nothing needs to cross except a *command*. tmux client and server both live
**inside** the sandbox, the socket never leaves, and `sandbox exec` carries each
operation in. The socket's confinement stops being a problem and becomes the design.

| Operation | Command from the launcher |
|---|---|
| send task | `sandbox exec <id> -- tmux send-keys -t scion:agent …` |
| liveness | `sandbox exec <id> -- tmux has-session -t scion` |
| logs / `scion look` | `sandbox exec <id> -- tmux capture-pane -p -t scion:agent` |
| attach (PTY) | see the PTY problem below |

**The removal has a third limb: the state store.** §4.5's `sandboxStateEntry` carries
a `TmuxSocket string` field (`json:"tmux_socket"`), populated from the launcher-side
`tmuxDir`. That path is now permanently empty — no socket will ever appear there.
**Delete the field**, along with `scionPaths.tmuxDir` and the `mkdir` that creates the
directory. A persisted field holding a plausible-looking wrong path is worse than an
absent one: it survives into the on-disk state file, and P4's attach implementation
will read it, find nothing, and diagnose the symptom as "tmux failed to start" rather
than "this field was never meaningful." There is no migration cost — the state store
is preview-stage and Tier 0 is pure ephemeral, so no state file outlives a redeploy.

**Drop `TMUX_TMPDIR` entirely, not just the mount.** With no socket crossing the
boundary there is nothing to relocate, so let tmux use its default. Keeping
`TMUX_TMPDIR` pointed at a no-longer-mounted path still *works* — the path exists via
`--rootfs /` inheritance and the socket lands in the private overlay — but it is a
vestige that reads as load-bearing to the next person, and it introduces a divergence
from every other runtime, all of which use the default socket (`common.go:480`,
`k8s_runtime.go:934`). **Removing the mount but keeping the env var is the worst of
the three options**: same behaviour, plus a misleading signal.

The first three are non-interactive: stdio is wired as pipes (`--stdin/--stdout/
--stderr`, default true) and pipes are sufficient. **`scion message` and `scion look`
therefore work with no new mechanism.** Agent *control* is fully preserved; only the
live terminal is at issue.

> ## ✅ RESOLVED BY TEST, 2026-08-26 — the PTY problem is smaller than described
>
> **`spike-uds-b` ran Tier B (T4–T9, T11) against a real Sandbox on a real Instance.
> The paragraphs below over-state the problem and prescribe an unnecessary fix.**
> Read this block; treat the struck text as history.
>
> **T5 was written as a negative control and it failed to be negative — which is the
> valuable result.** The *baseline* is as predicted: a bare
> `sandbox exec <id> -- tmux attach` gives `not a terminal`, and `test -t 0`/`test -t 1`
> both report false. But **a launcher-side PTY does propagate**:
>
> ```
> script -qfc 'sandbox exec <id> --env TERM=xterm-256color -- tmux attach -t scion' /dev/null
> ```
>
> renders the tmux UI, escape sequences and status bar included. Cross-checked with a
> python `pty.spawn` wrapper (T5a) against ptone's `script` formulation (T5b); the two
> **agree**, so this is a property of the boundary, not of `script`.
>
> **Mechanism**, from the spike's fd analysis — this is the part worth understanding,
> because it predicts what else will and will not work:
>
> | Launcher side | fds seen inside the sandbox |
> |---|---|
> | no PTY | `host:[720]`, `host:[721]`, `host:[722]` — pipes |
> | launcher PTY | `host:[716]` ×3 — the **host PTY fd itself** |
> | inner `script` | `/dev/pts/12` — a sandbox-local PTY device |
>
> `sandbox exec` passes launcher fds through gVisor's host-fd mechanism. With a
> launcher PTY the inner process holds a reference to a real host TTY, so **`isatty()`
> returns true**; `ttyname()` still fails, because the device path is not mapped
> inside. tmux needs the former and not the latter. Hence: *fd characteristics cross;
> device paths do not.*
>
> **The production path is therefore:**
>
> ```
> sandbox exec <id> --env TERM=xterm-256color -- tmux attach -t scion
> ```
>
> with the launcher's existing `pty.StartWithSize`. **No `script`, no double PTY, no
> `util-linux` dependency** — T4 drops from critical to irrelevant. This is *simpler
> than the Docker path*, not harder.
>
> **`--env TERM=xterm-256color` is load-bearing, and its absence misleads.** Without
> it the inner tmux sees `TERM=dumb` and exits with *"terminal does not support
> clear"*. That reads like a PTY failure and is not one. Name it in the code comment.
>
> **What did NOT survive: SIGWINCH.** T7 confirms PTY *signals* do not propagate even
> though PTY *fds* do — consistent with the mechanism above, since no `TIOCSWINSZ`
> ever reaches a sandbox-local terminal. The out-of-band resize below stands, with
> one correction: use `tmux resize-window -t scion -x <W> -y <H>`, which the spike
> verified. `refresh-client -C` requires a control-mode client and is the wrong tool
> here.
>
> **Latency (T8): p95 = 37 ms** for interactive keystrokes (p50 32 ms), against a
> 150 ms threshold. Per-exec-call p50 100 ms / p95 114 ms; control round-trip p95
> 301 ms. This closes the last argument for revisiting the dead socket design
> (§4.4-rev) — there is no performance case left to make.
>
> **Idle stability (T10): PASS** — an exec attached and idle for 32 minutes remained
> fully responsive. No idle timeout on the exec channel at that horizon, which is what
> the browser-terminal path needs.
>
> **⚠️ New risk, T9 + controls C1–C3 — `sandbox delete` is broken on *both* paths.**
>
> The half that works: deleting a sandbox with an exec attached causes the
> launcher-side exec to exit in ~1 s with a non-zero code. No leak on the exec side.
>
> **`delete --force` never returns — on any sandbox.** I initially recorded this as a
> hang seen "with an exec attached", which invited the reading that the exec caused it.
> The controls kill that reading: **C1** (tmux, no exec) and **C2** (bare `sleep 3600`,
> nothing else) both hang, at 120 027 ms and 120 028 ms — one millisecond apart, i.e.
> the test's own timeout, not any property of the workload. The claim is simply:
> `--force` delete does not complete.
>
> **The non-force path is arguably worse (C3).** `sandbox delete` without `--force`
> refuses correctly and fast (209 ms, *"cannot delete container that is not stopped
> without --force flag"*) — **and then kills the sandbox anyway**, leaving
> `runsc-gofer` and `runsc-sandbox` alive, one at 19 % CPU. A caller that correctly
> handles the error and retries is operating on a sandbox the CLI has already
> disowned. **Do not use non-force as a polite first attempt.**
>
> **Mitigating, and it is what makes P4a tractable:** deletion is *effective* despite
> not returning. The sandboxes really are gone (`sandbox exec` → "not running"), and
> the orphaned `runsc delete` processes become zombies and are reaped. So `--force` is
> a **termination/reporting failure, not a state leak** — which means "issue it, bound
> it, treat the timeout as success, reap the orphan" is defensible rather than
> reckless.
>
> Versions, for anyone re-testing: `runsc` **google-958767651, spec 1.2.1**; both
> binaries dated **Aug 4 2026**; the `sandbox` CLI has **no `--version` flag**.
>
> This is a `sandbox`-CLI defect rather than ours, but it lands squarely on this tier,
> where teardown is the *normal* lifecycle (§5) and a redeploy deletes every sandbox at
> once. See **P4a**. Written up for the platform team as
> `defect-sandbox-delete-hang.md` (revision 2), shared by ptone 2026-08-26.
>
> **Untested and the most obvious gap: concurrent deletes.** Fan-out is our actual
> pattern and every observation above is serial. If the hang is contention-related it
> could be worse at fleet scale, not merely N× slower.
>
> Full per-test detail and raw output: `ac0-results.md`, Tier B section.

~~**⚠️ The PTY problem, and why the obvious fix fails.** `sandbox exec` has **no
`-t`/`--tty` flag** — the re-test captured its full flag set (`-e/--env`, `-w/
--workdir`, `--stdin/--stdout/--stderr`). So the Docker pattern does not port:
`pty.StartWithSize` on the launcher gives a PTY to the *`sandbox` CLI process*, while
the process **inside** still sees pipes. `tmux attach` will fail with *"open terminal
failed: not a terminal"*. This is the trap to name loudly, because the launcher-side
code will look correct and the failure appears one layer in.~~

**Still true:** `sandbox exec` has no `--tty` flag (T11 confirms; OQ-6 resolved).
**Wrong:** the conclusion drawn from it. The absent flag does not imply an absent
TTY — the fd passthrough supplies one without the flag's help.

~~**Fix: allocate the PTY inside the sandbox**, and let pipes carry the byte stream:~~

```
sandbox exec <id> -- script -qfc 'tmux attach -t scion' /dev/null   # ← works (T6), but UNNECESSARY
```

~~`script` creates a PTY in the sandbox; the launcher keeps its existing
`pty.StartWithSize` plumbing for the outer process. Requires `util-linux` in the
omni-image — near-certain, but **verify** rather than assume.~~ T6 confirms this does
work and does allocate `/dev/pts/N` inside — it is simply redundant now. Kept only as
a fallback if the fd-passthrough behaviour ever regresses.

**Resize needs an out-of-band path.** SIGWINCH does not propagate (T7 confirms), so
the existing resize path is inert. Send tmux an explicit command instead —
`sandbox exec <id> -- tmux resize-window -t scion -x <W> -y <H>` — as a separate call
on each resize event. Slightly chattier; behaviourally equivalent.

**Costs, stated plainly:** the sandbox CLI is now in the latency-sensitive
interactive path (§4.4 avoided that deliberately), and every control operation is a
process spawn rather than a local socket write. For send-keys/has-session at
Scion's cadence this is not material; for keystroke-latency on attach it needs
measuring, since it is one persistent `exec` rather than per-keystroke spawns.

**Degradation ladder if the PTY trick also fails** — unchanged from §4.4 and still
the reason this does not sink the tier: refuse `attach` with the existing
`managed:` precedent (`cmd/attach.go:158-160`), keeping `scion message` and
`scion look` working. **Cost is one feature, not the tier.**

**Rejected: a TCP shim.** `socat TCP-LISTEN:…,fork UNIX-CONNECT:<tmux.sock>` inside
the sandbox, exposed via `-p/--publish` (which the re-test confirmed exists), with
the launcher connecting over TCP. It would work, but it adds a listening port and a
socat process per agent, and it puts the tmux control channel on a network surface
purely to avoid a process spawn. **Not worth it** — but recorded, because
`--publish` makes it genuinely available if `sandbox exec` latency ever proves
unacceptable.

**Knock-on to §4.8:** I argued there that our `pty_handlers.go` branch would be
*simpler* than Docker's because it had "no container runtime in the command at all".
**That rationale is now void** — the command is `sandbox exec <id> -- …`, structurally
the same shape as `docker exec -it <id> …`. Recommendation **(b)** (add a third
branch alongside `isK8s`) still stands, and is in fact better supported now: we are
adding a case that closely mirrors the existing Docker one.

### 4.5 State: the launcher remembers, because the CLI does not

`DockerRuntime.List` parses `docker ps`. With no `sandbox list`, the runtime keeps
its own record. Scope it deliberately small:

- **Store:** a JSON file under the Scion state dir, written on `Run`/`Delete`.
- **Contents:** sandbox name → agent ID, project ID, labels, created-at, tmux socket
  path.
- **Truth:** the *record* is authoritative for identity; **liveness is always
  probed** via `tmux -S <sock> has-session`. Never report a sandbox as running
  because the file says so.
- **Reconciliation on startup:** for each record, probe; drop the dead.

This is deliberately not a database. If the Instance restarts, the sandboxes are
gone anyway (§5.1) and the file is discarded.

### 4.6 Runtime selection: detect the capability, not the product

> **`K_SERVICE` is NOT set on Cloud Run Instances** (confirmed by ptone,
> 2026-08-25). This is the single most consequential platform fact in the design,
> and it is counter-intuitive: an Instance is a Cloud Run resource that does not
> announce itself as one.

Five places in the codebase use `K_SERVICE` as a proxy for "am I on Cloud Run." On
an Instance every one of them evaluates **false**. Two of the three problems I
originally expected therefore disappear, and three new ones appear:

| # | Site | Behaviour on an Instance | Verdict |
|---|---|---|---|
| 1 | `factory.go:91-93` autodetect | Falls past the Cloud Run branch, `LookPath("docker")` fails, **defaults to `docker` anyway** (`:101-103`) → a `DockerRuntime` that fails on first use with daemon errors | **Still broken**, but a different failure than I predicted |
| 2 | `factory.go:117-124` | `type: docker` is **not** swapped for the stub | ✅ Problem gone |
| 3 | `project_workspace_handlers.go:72-85` | `isCloudRunEnv()` false → **writes permitted** | ✅ Behaviour we want — but see §5.2, we get it for the wrong reason |
| 4 | `config/hub_config.go:144` | `hub_id` cannot derive from `K_SERVICE`; **falls back to hostname** | ⚠️ New — hostname stability across redeploys is unverified. **Set `server.hub.hub_id` explicitly in the deploy.** |
| 5 | `logging/otel.go:106`, `logging/message_log.go:76` | Both conclude "not on Cloud Run" and stand up their own Cloud Logging client — but an Instance's stdout **is** already captured by Cloud Logging | ⚠️ New — likely **duplicate log ingestion** (double billing, double entries). Verify in AC-0. |

**Correction, 2026-08-25 (ptone).** The platform *does* offer a signal — it is just
not `K_SERVICE`:

```sh
[ -n "$CLOUD_RUN_INSTANCE" ] && echo 'on CRI'
```

`CLOUD_RUN_INSTANCE` is set on a Cloud Run Instance. This removes the claim below
that "the platform offers *no* signal at all" — but it does **not** change the design,
for a reason worth being precise about.

**`CLOUD_RUN_INSTANCE` answers a different question than the one selection asks.**
It says *where we are running*. Selection needs to know *what we can launch*. Those
came apart the moment the sibling landed: on a Cloud Run Instance there are now **two
defensible autodetect answers** —

| Answer | Meaning |
|---|---|
| `cloudrun-sandbox` (this design) | one Instance hosts the hub; agents are sandboxes *inside* it |
| `cloudrun-instances` (the sibling, §6.1) | one Cloud Run Instance *per agent*, provisioned via the Instances API |

`CLOUD_RUN_INSTANCE` is true for both, so it cannot discriminate. The binary probe
can: sandboxes require a launcher present in *this* container, and the sibling does
not. **Use both, in this order** — `CLOUD_RUN_INSTANCE` to establish the environment,
the binary probe to pick the runtime within it. The env var is the cheaper and more
honest way to satisfy the first half, and it replaces the hostname sniffing row 4
would otherwise need.

The functional probe therefore stands, and Q7 still strengthens the case for it:

```go
// pkg/runtime (illustrative)
func SandboxLauncherAvailable() bool {
    _, err := os.Stat("/usr/local/gcp/bin/sandbox")
    return err == nil
}
```

- **`factory.go` autodetect:** gate on `CLOUD_RUN_INSTANCE != ""`, then probe the
  launcher binary — both *before* the `K_SERVICE` branch, which is for Cloud Run
  *Services* and will not fire here anyway. Binary present → `cloudrun-sandbox`;
  absent → fall through to the sibling. This fixes row 1 and is the only reason
  autodetect works on an Instance at all.
- **Rows 4 and 5 are deploy-time concerns, not runtime-selection concerns.** Pin
  `hub_id` in the deploy config; investigate the logging duplication in AC-0.
- Probing the binary also degrades correctly if sandboxes ever ship elsewhere, and
  it does not collide with the sibling `cloudrun-instances` runtime (§6.1), which
  has its own selection path.

### 4.7 Registration surface (the boring but mandatory part)

Adding a runtime name touches more places than the factory. There is no registry —
these are hardcoded allowlists, and missing one produces a confusing runtime failure
rather than a compile error. Full known list:

- `pkg/runtime/factory.go:48` — profile-name fallback allowlist; `:66-104`
  autodetect; `:110-181` the type switch
- `pkg/config/schemas/settings-v1.schema.json:253-276` — the runtime `type` enum.
  **`additionalProperties: false` and already stale** (missing `cloudrun-instances`),
  so new config is silently rejected. Fix while here.
- `pkg/runtimebroker/handlers.go:174` — `isLocalOnlyRuntime`
- `pkg/hub/system_handlers.go:122-126`
- `pkg/hub/harness_config_handlers.go:43-44`
- `cmd/server_broker.go:227`
- `cmd/server_helpers.go:87`
- `pkg/agent/run.go:350`
- `pkg/config/runtime_detect.go:34-38`
- `pkg/runtimebroker/pty_handlers.go` — see §4.8

A worthwhile side-quest for whoever does P1: replace these with one table. Out of
scope, but the eighth hardcoded list is the argument for it.

### 4.8 The `RuntimeCommand()` leak

`Server.RuntimeCommand()` (`pkg/runtimebroker/server.go:1330-1339`) returns
`runtime.Name()` and hands it to `exec.CommandContext` as a **host binary name**
with Docker CLI grammar (`pty_handlers.go:88,165,541-554,921-939`). K8s escapes only
by string comparison: `isK8s := runtimeCmd == "kubernetes" || runtimeCmd == "k8s"`.

`Runtime.Name()` is thus overloaded: identity *and* executable path. Three options:

- **(a)** Make `Name()` return the binary path. Breaks the identity contract, breaks
  config matching, and is a lie for K8s already.
- **(b)** Add a third branch alongside `isK8s`. Cheapest; extends a pattern already
  acknowledged as a wart.
- **(c)** Promote interactive PTY exec into the `Runtime` interface as a 16th method,
  and let each runtime own its own command construction.

**Recommendation: (b) for this project, with (c) recorded as the right refactor.**
Rationale: the tmux-socket design (§4.4) makes our branch *simpler* than Docker's —
it is `tmux -S <sock> attach`, with no container runtime in the command at all — so
we are adding a small, self-contained case rather than deepening the wart. (c) is a
cross-cutting refactor that should not ride on an alpha-platform bring-up.

### 4.9 Agent networking

Agents need **outbound only** — no `-p`/`--publish` is emitted anywhere in
`pkg/runtime/`, and broker→agent control is exec-based, not network-based. That is
fortunate, because sandboxes have no network egress by default.

**This includes auto-exposed dev-server ports, which need no runtime capability at
all.** `sciontool` PID 1 starts a port-forward manager (`init.go:627`) that scans
`/proc/net/tcp` for loopback listeners and dials **out** to the hub over a reverse
WebSocket tunnel; the hub proxies to the browser by path prefix. Nothing ever dials
into the container. This is the most portable subsystem in Scion and it works here
unchanged — **resolving OQ-8**.

Required reachability from inside a sandbox:

1. **The hub** — `SCION_HUB_ENDPOINT`, for heartbeat, messages, token refresh.
2. **LLM APIs** — `api.anthropic.com` etc.
3. **Git remotes and package registries.**

(1) is **OQ-2**. ptone is checking whether bridge/host-style networking exists
between a sandbox and its Instance, and has confirmed that **hairpinning via egress
is an acceptable fallback** — so this is no longer a blocking unknown. Two cases:

- **Bridge/host networking available** → `SCION_HUB_ENDPOINT=http://localhost:8080`,
  and `applyContainerBridgeOverride` is a no-op. Nothing to build.
- **Hairpin** → `SCION_HUB_ENDPOINT` is the Instance's own public `run.app` URL.
  This is exactly the case `applyContainerBridgeOverride` already handles: when the
  override target is *not* a bridge hostname it is returned **wholesale**, preserving
  scheme and implicit 443 (`hubenv.go:119-126`). So we set
  `broker.container_hub_endpoint` to the `run.app` URL and the existing code path
  carries it. **No new mechanism** — this is the same shape as the Caddy/public-domain
  deployment the override was written for.

**The hairpin's real cost is not latency, it is the auth hop.** Agent traffic
re-enters through public ingress and must therefore satisfy whatever guards it. This
collides with **S2** (§4.11: do not disable the invoker IAM check). Scion already
has the mechanism: `SCION_TRANSPORT_TOKEN` / `_AUDIENCE` / `_TOKEN_EXPIRY` /
`_MODE` (`httpdispatcher.go:830-834`) carry a GCP OIDC ID token precisely for IAP
and Cloud Run invoker checks.

The wrinkle: per the sandbox guide, a sandbox has **no GCP-service access even with
`--allow-egress`**, so it likely cannot mint its own ID token from the metadata
server. The **launcher can**, and already injects the token as a value. But an
injected token expires, which is the same problem `SCION_AUTH_TOKEN` has — solved
there by writing to a file and signalling `kill -USR2 1`
(`handlers.go:1833-1889`). **If we hairpin with invoker IAM enforced, transport
tokens need the same file+signal refresh treatment.** That is a concrete, scoped
work item, and it only exists in the hairpin case — which is a good reason to want
OQ-2 to resolve the other way.

A Unix-socket transport (`SCION_HUB_ENDPOINT=unix:///…`) would sidestep both the
hairpin and the auth hop entirely. `pkg/apiclient/transport.go` has no UDS support
and nothing in the repo does. Given ptone's guidance that hairpin is acceptable,
this is **not** proposed for v1 — recorded as the cleanest answer if the hairpin's
auth handling proves annoying in practice.

### 4.9a ⚠️ TITLE FALSIFIED — IAP *is* available on Instances. Retained for the header-mimicry ruling, which stands

> # ❌ THE PREMISE OF THIS SECTION IS FALSE — `spike-iap`, 2026-08-26
>
> **`iapEnabled: true` on an Instance is LIVE, not inert. Verified.** An
> unauthenticated request gets a 302 to `accounts.google.com` with
> `x-goog-iap-generated-response: true`, and the container never logs it — enforced
> at the edge. An IAP-audienced OIDC token arrives with a real
> `X-Goog-IAP-JWT-Assertion` (ES256, `iss=https://cloud.google.com/iap`) that this
> hub's `IAPAuthenticator` should accept nearly as-is. Full results: §10b and
> `ac0-results.md`.
>
> **This contradicts the relay that Instances have no direct IAP "and it is not
> coming soon."** Either it shipped ahead of the messaging, or that answer spoke for
> the IAP side of a two-sided piece of work. Worth re-asking the Cloud Run team —
> but the design should now follow the measurement, not the relay.
>
> **✅ Surface confirmed — these were Instances, not Services.** Create was
> `…/v2/projects/ptone-experiments/locations/us-east4/`**`instances`**`?instanceId=iap-test-1`,
> PATCH likewise, and `gcloud beta run instances list` showed them running. This was
> checked specifically because the assertion's `aud` reads
> `/projects/…/locations/us-east4/`**`services`**`/iap-test-2`.
>
> > **⚠️ Named gotcha for P6: the audience says `services` for an Instance.** It is
> > IAP's claim-naming convention, not the deployment surface. **This is no longer
> > merely a gotcha — the same convention applied to `x-serverless-authorization` is
> > what breaks the invoker check (below).** For the *assertion* the convention is
> > harmless and we must follow it; for the invoker token it is the defect. Anyone deriving the
> > expected audience from the resource type will construct
> > `…/instances/{name}`, get an opaque audience-validation failure, and look for the
> > bug in the wrong place. Construct it as
> > `/projects/{number}/locations/{region}/services/{name}` **regardless of surface**,
> > and say why in the comment. Note this also differs from the GCE/GKE format
> > (`/projects/{number}/global/backendServices/{id}`).
>
> **❌ `invokerIamDisabled: true` IS REQUIRED — OQ-17 answered NO, and S2 does not
> survive. This reverses the block that stood here for roughly twenty minutes.**
>
> That block said the invoker check stays on and one IAM binding to the IAP service
> agent is sufficient. It was **documentation-derived**, from a page written for Cloud
> Run *Services*, and it is **wrong for Instances**. Measured, with one variable:
>
> | Token `aud` | HTTP |
> |---|---|
> | `/projects/{n}/locations/{r}/`**`services`**`/{name}` | **401** |
> | Instance URL | **200** |
> | `/projects/{n}/locations/{r}/`**`instances`**`/{name}` | **200** |
>
> IAP emits the **`services`** form in `x-serverless-authorization`; the Instance
> invoker check rejects it. The grant is irrelevant — the token fails *verification*
> before IAM is consulted. Confounders eliminated (fresh Instance, 180 s IAM wait,
> full reconcile, probed at 5 m 15 s). A fourth control confirmed the invoker check
> **does** read `x-serverless-authorization` and accepts it when the audience is
> right, so the fault is one string in IAP. Full detail: §10b.1, and
> `defect-iap-instance-audience.md`.
>
> **This question flipped three times in one session** — required, then not required
> (documentation), then required (measurement). The two documentary readings were both
> confidently argued and both wrong for our surface. **The lesson is not "be more
> careful with docs"; it is that a documented behaviour for surface A is a *hypothesis*
> for surface B, and this project has now paid for that twice** (see also
> `sandboxLauncher`, where schema presence was likewise not delivery).
>
> **Consequence for S2, stated plainly: IAP is the sole perimeter.** Defence-in-depth
> is unavailable on Instances today. S2's intent — keep the invoker check on — cannot
> be honoured, so P6 must compensate with *ordering* instead: never publish the URL,
> and never disable the invoker check, until IAP is **confirmed enforcing** (302
> carrying `x-goog-iap-generated-response`). Reconcile is 30–75 s and the interval
> between "invoker off" and "IAP on" is fully open, so this is a deploy **gate**, not
> a PATCH.
>
> **A second gap, same root:** there is **no per-Instance IAP IAM resource**.
> `getIamPolicy` *and* `setIamPolicy` both 404 at
> `iap_web/cloud_run-{region}/services/{instance}` even holding `iap.admin`, while real
> Services return 200. Only **region-level** scope exists — which grants hub access to
> everything later deployed in that region and is **not shippable**. P6 must choose: a
> dedicated project, the auth-proxy Service after all (Services *do* have per-resource
> IAP IAM), or the broad scope documented loudly. **This partially un-does the
> simplification that "IAP is live" appeared to hand us.**
>
> **Three consequences already firm, recorded now so they are not lost:**
>
> - **The footgun did not disappear; it moved.** I4 shows `invokerIamDisabled: true`
>   + `iapEnabled: false` = **HTTP 200, wide open**. If IAP does require invoker off,
>   these two fields are a coupled pair with one lethal combination, reachable by any
>   rollback or partial reconcile — and reconciliation takes **30–75 s**, so the
>   window exists even in the happy path. Deploy tooling (P6) must treat them as one
>   setting, refuse the dangerous pair, and apply them in a safe order.
> - **Dropping the proxy service costs something this section assumes it does not.**
>   The argument below — that agent traffic never meets IAP because the hub checks
>   the agent token first — holds *at the hub layer*, and was written when IAP sat on
>   a **separate service in front**. Agents could hairpin straight to the Instance
>   URL and skip it. With IAP **on the Instance**, it is enforced at the edge before
>   the hub is consulted, and the agent token buys nothing there. **The proxy was
>   also what kept IAP off the agents' path.**
> - **Therefore OQ-2 becomes decisive, not cosmetic.** Option **A** (sandbox reaches
>   the launcher's port internally, never touching the edge) is now the only path on
>   which agent traffic sidesteps IAP entirely. **A2 no longer works as written** —
>   its Instance-URL audience is rejected by IAP; it collapses into C's machinery
>   (IAP-client-ID-audienced OIDC, minted and refreshed launcher-side because
>   sandboxes cannot mint their own), minus the second hop. My earlier framing that
>   A2 made OQ-2 a latency-and-simplicity question **does not survive adopting IAP.**
>
> None of this argues against IAP. It argues that "drop the proxy" and "agents are
> unaffected" cannot both be assumed, and the text below implies both.

**ptone, 2026-08-25:** Cloud Run Instances have **no direct IAP support, and it is
not coming soon.** The interim topology is: set IAP as the auth method, but front the
Instance with a **Cloud Run *service* running IAP as an auth proxy**. His question:
for the sandbox→hub hairpin, can we take a **faster u-turn straight to the Instance
by mimicking the IAP header**, or must traffic go all the way out and back in
through the proxy service?

**Answer: neither. Go direct — but not by mimicking anything.** The premise that a
u-turn *requires* IAP mimicry is what needs dropping.

**1. The header cannot be mimicked, by design, and we should not make it mimicable.**
The hub's `IAPAuthenticator` (`pkg/hub/proxyauth.go:92-141`) reads exactly one
header — `X-Goog-IAP-JWT-Assertion` (`:51`) — and **cryptographically verifies it**:
`jwt.ParseSigned(..., []jose.SignatureAlgorithm{jose.ES256})` (`:101`, algorithm
allow-list, so no `alg:none` confusion), `kid` lookup against Google's JWKS
(`:110-118`, `DefaultIAPJWKSURL` at `:57`), signature check at `:122`, then mandatory
audience, issuer `https://cloud.google.com/iap`, and `exp` with 30s skew
(`:154-189`). `X-Goog-Authenticated-User-Email` is **never read anywhere in the Go
tree**. Tests pin all of this (`proxyauth_test.go:157,193,226,259`).

So mimicry would mean *adding a trust-the-header path*. **Do not.** The moment the
hub accepts an unsigned identity header, anything that can reach the hub's port can
assert any user — and the sandbox is, by construction, a thing that can reach the
hub's port. The IAP front door becomes decorative while still looking authoritative
in config. **The property that makes the u-turn "fast" is exactly the property that
makes it unsafe.**

> ⚠️ There *is* an existing header-trust path — `extractProxyUser`
> (`pkg/hub/auth.go:517-533`) trusting `X-Forwarded-User-*`. It is reachable only
> when **no** `ProxyAuthenticator` is configured *and* the peer is in
> `TrustedProxies` (`auth.go:277`). Configuring IAP disables it. **It will look like
> the easy answer to this problem. It is the thing not to reach for** — on a
> single-node box the sandbox may well satisfy a naive trusted-CIDR test.

**2. The hub does not need IAP for agent traffic at all — the paths are already
independent.** `UnifiedAuthMiddleware` checks the agent token **first**
(`auth.go:149-168`, `X-Scion-Agent-Token` via `extractAgentToken`,
`agenttoken.go:299-315`, validated HS256 at `:157-180`) and **returns at `:158`**.
The `ProxyAuthenticator` is only consulted at **Step 3a** (`auth.go:231-234`), and
only when no bearer token was presented. `AuthConfig.AuthMode` is *never read* inside
that middleware. **An agent presenting its own token never touches the IAP code**,
even with `auth.mode=proxy`. Nothing needs to be built or relaxed for this.

**3. Therefore the real question is purely a networking one, and it is OQ-2.**
The edge is what enforces IAP; the hub is not. Traffic that never leaves the Instance
never meets the edge, so there is no IAP obligation to satisfy and nothing to forge.
The only question left is whether a sandbox can reach the launcher's listening port.

**Recommendation — in preference order:**

| # | Path | Auth | Cost | Verdict |
|---|---|---|---|---|
| **A** | Sandbox → Instance-internal address:8080, never traversing the edge | Existing `X-Scion-Agent-Token` | **Zero new code** | **Best.** Gated only on OQ-2. |
| ~~**B**~~ | ~~Same, over a **unix socket**~~ | — | — | ❌ **DEAD (2026-08-25).** gVisor runs without `--host-uds`; AF_UNIX does not cross the sandbox boundary (§4.4a). Killed by the same answer as §4.4. |
| **A2** | Sandbox → **the Instance's own `run.app` URL** — out to the edge, straight back in, **never touching the IAP proxy service** | OIDC ID token audienced to the **Instance URL**, satisfying Cloud Run **invoker IAM** (not IAP) | One edge round-trip; launcher must mint + refresh the token | **The right fallback.** Strictly better than C. |
| **C** | Full loop out through the **IAP proxy service** and back in | OIDC audienced to the **IAP client ID**, in `Proxy-Authorization`, **plus** the app token | Two hops; agent liveness coupled to the proxy's availability | **Superseded by A2.** No reason to prefer it. |
| **D** | Mimic the IAP header | — | Makes the hub spoofable by anything with network reach | **Rejected.** See above. |

**A2 added 2026-08-25 by ptone, and it is a correction to this section rather than a
new idea.** §4.9 already described precisely this — *"Hairpin → `SCION_HUB_ENDPOINT`
is the Instance's own public `run.app` URL"* — and §4.9a then collapsed "hairpin" and
"through the IAP proxy" into one option, silently making the fallback look worse than
it is. **The Instance has its own `run.app` URL and the IAP proxy is a separate Cloud
Run service in front of it; the two are independently addressable.** That is the
whole reason the proxy exists — IAP is not native to Instances — and it is equally
the reason agent traffic can skip it.

**Why A2 beats C on every axis:** one hop instead of two; no dependency on the proxy
service's availability for agent liveness; audience is the Instance URL rather than
an IAP client ID; and it satisfies **S2** (§4.11) honestly, since invoker IAM stays
enforced. The `auth.transport` machinery (`transport_token.go:98-131`) supplies the
token in both cases, so A2 costs no more code than C.

**One security property to state explicitly, because it is easy to assume otherwise:**
A2 means the hub is reachable at the edge on a path that **bypasses IAP**, gated only
by `run.invoker`. That does not create a hole — a caller reaching the Instance URL
with an invoker token but no IAP assertion and no agent token falls through to
Step 3a (`auth.go:231`) with no proxy user, and must then present real hub
credentials. So IAP is **defence in depth for humans, not the sole perimeter.** But
it does mean **granting `run.invoker` is granting the ability to bypass IAP**, so
that grant should stay scoped to the Instance's own SA and not become a convenience
role.

**This narrows a claim I made an hour ago.** I said OQ-2's stakes had risen because
the fallback now dragged in proxy coupling. With A2 in the picture that is only half
true: the fallback still needs transport-token refresh, but it loses the proxy
dependency entirely. **A is still better than A2; A2 is no longer painful.** OQ-2
matters for latency and simplicity, not for viability.

**C is already built, which is the good news.** This is precisely what `auth.transport`
does for the HA tier: the hub impersonates a dedicated SA to mint Google OIDC ID
tokens (`pkg/hub/transport_token.go:98-131`) and injects
`SCION_TRANSPORT_TOKEN`/`_AUDIENCE`/`_MODE` into the container
(`httpdispatcher.go:830-834`); the client sends OIDC in `Proxy-Authorization` while
keeping the app token in `X-Scion-Agent-Token`
(`pkg/transportauth/transportauth.go:259-269`). So C is a **configuration** of an
existing mechanism, not new design — **except** for the expiry problem already
flagged above: a sandbox cannot mint its own token (no GCP service access under
`--allow-egress`), so the launcher must refresh it via the file+`kill -USR2 1`
pattern (`handlers.go:1833-1889`).

**Note this sharpens OQ-2's stakes.** Previously "hairpin is an acceptable fallback"
made OQ-2 low-consequence. With IAP in front, the hairpin now also drags in a second
refreshing credential and a hard dependency on the proxy service for every agent
API call. **A is now materially better than C, not merely tidier** — so OQ-2 is worth
resolving properly rather than defaulting to the hairpin.

**Also from ptone:** Instances *do* support **ssh for debugging**. My earlier note
that there is no `ssh` verb was scoped to gcloud 575.0.0, where `gcloud alpha run
instances` exposes only `dev`, `logs`, `add-iam-policy-binding`, … — no `ssh`. Treat
that as a version lag, not an absence.

> **✅ CONFIRMED on gcloud 582.0.0, 2026-08-26** (ptone flagged it; I verified):
>
> ```
> gcloud beta run instances ssh INSTANCE [--container=CONTAINER] [--region=REGION]
> "Starts a secure, interactive shell session with a Cloud Run instance."
> ```
>
> Present on **both** `alpha` and `beta`. **It is hidden from the group listing** —
> it does not appear under `COMMANDS` in `gcloud beta run instances --help`, which is
> why my 582 verb inventory (in `deploy-instance-with-sandbox.md`) missed it a second
> time. It is only visible if you ask for the verb directly.
>
> **Design consequence: the §6.1a IAP-tunnel plumbing is retired.** `iap_exec.go` on
> the `cloudrun-instances-runtime` branch remains a correct implementation of an
> approach we no longer need for operator debugging access. This does **not** touch
> §4.9a, which is about *agent* traffic authenticating to the hub — a different
> problem with a different answer (A2, invoker-audienced OIDC). Do not collapse them.
>
> **Not a TTY answer.** `ssh` gives an interactive shell **into the Instance**, i.e.
> into the launcher. It does not cross into a *sandbox*, which is where the §4.4a PTY
> problem lives. It is useful as an operator tool and as evidence the platform can
> carry an interactive session, and it is **not** a substitute for T5/T6.
>
> **Methodological note, twice-burned.** Both this and the "sandbox CLI does not
> exist on Instances" error in `ac0-results.md` are the same mistake: reading a
> *listing* and reporting an *absence*. A command group's help enumerates visible
> commands only; it is not a complete index. **Negative claims about a CLI require
> probing the specific verb**, never scanning the group.

(2) is settled: **inference endpoints are reachable from a sandbox** (ptone,
2026-08-25). The sandbox guide's caveat that `--allow-egress` does not grant *GCP
service* access still stands, so **Vertex AI as an LLM backend remains untested** —
note it as a caveat if an operator selects that backend, but it does not affect the
default Anthropic/OpenAI path.

### 4.10 Metadata interception degrades safely

`SCION_METADATA_MODE=block` is the broker default, which sets
`MetadataInterception` and hence `--cap-add NET_ADMIN` for essentially every
hub-dispatched agent. Sandboxes are unlikely to grant `NET_ADMIN`.

This is fine. The iptables layer is explicitly non-fatal
(`pkg/sciontool/metadata/server.go:367-374`) and the `GCE_METADATA_HOST` /
`GCE_METADATA_ROOT` env vars are the primary mechanism — which is exactly why K8s
works with no `NET_ADMIN` anywhere. We inherit the K8s posture: functional
protection via env vars, with the defence-in-depth layer absent. The residual
exposure is documented at `server.go:126-133`. **No design change needed; the
runtime should simply not request the capability.**

> **Confirmed by measurement, 04:54 — and it is now stronger than "unlikely to grant".**
> `iptables -t nat` is **not available inside a gVisor sandbox at all**: there is no
> `/proc/net/ip_tables_names`, because gVisor's netstack does not implement the `nat` table.
> The capability is not merely ungranted, the table does not exist. Two consequences:
> **(a)** this section's prediction was right and the runtime should not request
> `NET_ADMIN`; **(b)** `GCE_METADATA_HOST` is not the primary mechanism, it is the **only**
> mechanism, so anything that hardcodes `169.254.169.254` and ignores the env var simply
> fails rather than degrading. See **§11.12**, which measured the whole chain and makes the
> binding decision this section's posture implies.

### 4.11 Security: a blocker that must be fixed before any public deploy

`devAuthMiddleware` (`pkg/hub/web.go:1382+`) **auto-logs-in any cookieless request
as an admin dev user, with no token comparison** — the presence of `DevAuthToken` is
the only gate:

```go
if ws.config.DevAuthToken == "" { return next }
// …no user in session → auto-login as dev@localhost
```

Compose that with (i) the hub image's `--host 0.0.0.0` CMD, (ii) a public `run.app`
URL, and (iii) `--no-invoker-iam-check`, which the Instances preview guide
*recommends* — and the result is a **publicly reachable, unauthenticated admin UI**.
There is no firewall in this topology; the `run.app` hostname is the only secret,
and it is semi-guessable.

**Requirements for this design:**

- **S1.** Dev auth must be impossible to enable when bound to a non-loopback
  interface. Fail startup, loudly. (This is item 0.7 in
  `single-node-packaging.md`, now escalated from onboarding polish to a deploy
  blocker.)
- **S2.** The default deploy must **not** pass `--no-invoker-iam-check`.
- **S3.** First-run login uses the bootstrap token (Option 5 in
  `single-node-auth.md`), which needs `establishSession()` extracted from its four
  independent writers. Device grant remains the hostname-independent human path.

**S1 and S2 gate any deployment reachable from the internet.** I am treating them as
in-scope for this design, not as someone else's cleanup.

- **S5 — the metadata emulator becomes a credential-minting endpoint the moment we move
  it off loopback, and it does not authenticate callers.**

  > **DOWNGRADED, 06:30 — the premise was wrong and the risk does not exist on this tier.**
  > This entry assumed §11.12's conclusion that the emulator must move off loopback to the
  > launcher's link-local address so sandboxes could reach it. **Measurement says the
  > emulator runs *inside each sandbox*** (`sciontool init`, one per sandbox, sharing the
  > harness's network namespace) **and never leaves loopback.** Nothing has to move, so
  > nothing is exposed: a loopback listener inside a gVisor sandbox is reachable only by
  > that sandbox.
  >
  > **The residual below is likewise retired for this tier.** "Any sandbox on the Instance
  > can obtain the runtime SA's token" was a consequence of a *shared* launcher-side
  > endpoint. There is no shared endpoint. **Each sandbox talks to its own emulator, which
  > asks the hub, which applies its own authorization** — so the credential path is
  > per-agent and mediated, not ambient. **S5 comes off the multi-tenancy revisit trigger
  > (§11.2, D1); the region-scoped IAP grant stays on it.**
  >
  > **What remains true, and what to keep:** the emulator still does not authenticate its
  > callers, so **the day anything moves it off loopback, everything below applies again in
  > full.** That is not hypothetical — a patch doing exactly that was written and reverted
  > tonight. The `resolveBindAddress` guard and `TestBindAddress_Never0000` are retained
  > with no consumer precisely to catch that day. **Read the rest of this entry as the
  > standing condition on a future change, not as a current exposure.**

  Read the source before
  assuming that is safe: the only gate on the emulator is `requireMetadataFlavor`, which
  checks for the header `Metadata-Flavor: Google`. **That is a convention, not
  authentication** — it exists to defeat browser cross-origin requests, not attackers.
  (`Config.AuthToken`/`TokenFunc` authenticate the emulator **to the hub**; they do not
  authenticate callers **to the emulator**.) So: anything that can route to the endpoint
  and sets one header receives a service-account token.

  **Requirement: bind link-local, never `0.0.0.0`.** The lazy fix has identical
  functionality and a much larger blast radius, which is exactly the shape of change that
  gets made under time pressure and never revisited.

  **Residual, accepted and stated rather than assumed: any sandbox on the Instance can
  obtain the runtime SA's token.** On a single-tenant, single-operator, single-project
  tier this is acceptable at launch. It is **not** acceptable the day this tier hosts two
  tenants, and it therefore joins the region-scoped IAP grant on the **multi-tenancy
  revisit trigger** (§11.2, D1). Both entries on that trigger are now security-material;
  the trigger is no longer a tidiness note.

**S5 does not gate the first deploy** — S1 and S2 do — but it must be in the release notes
under documented limitations (§10), because an operator who assumes sandbox isolation
implies credential isolation will be wrong.

**S4 — found here, owned elsewhere.** Auditing `handlers_runtime_brokers.go` for the
`ExitCode`/`ExitReason` work turned up a broker authorization gap: three handlers
under `/api/v1/runtime-brokers/{id}/…` act on the path `{id}` without verifying the
authenticated caller is that broker (`handleBrokerHeartbeat` :623, `getRuntimeBroker`
:300, `getBrokerProjects` :853). Traffic is authenticated, so it is cross-tenant
rather than anonymous.

The reason it survived is worth carrying: the heartbeat loop contains a guard
commented *"Security check: ensure the agent belongs to this broker"* testing
`agent.RuntimeBrokerID != id` — **path-vs-agent, never caller-vs-path.** A request
addressed to another broker's endpoint carrying that broker's own agents passes it for
every agent. A comment asserting a security property is not evidence of one, and this
is the shape to watch for: the check is real, it is just checking a different pair
than its comment claims.

**Filed as [ptone/scion#1263](https://github.com/ptone/scion/issues/1263) and handed
to `auth-lead` (ptone's routing, 2026-08-26). Not in scope for this design** — recorded
here only so the provenance is not lost, and because P3/P4 touch the same file. Do not
fix it as a side-effect of tier work; it has an owner.

---

## 5. Durability — Tier 0, pure ephemeral

**Decided by ptone (2026-08-25): Tier 0. Durability lands as follow-up work.**

Cloud Run Instances have an ephemeral filesystem and **destructive updates** — a
redeploy replaces the disk. Tier 0 accepts that wholesale: **a redeploy is a factory
reset.** Nothing is replicated, nothing is mounted, cost is zero.

This diverges from GLOSSARY's definition of the tier ("accepts restart/redeploy
downtime and *single-volume durability*"). That is a deliberate, recorded divergence
for this substrate, not an oversight — see the note in §12 (Cross-References).

### 5.1 What is lost on redeploy — the exact inventory

Tier 0's only real engineering obligation is to be **honest and specific** about
this, so it cannot surprise anyone. Verified against the code:

| Lost | Where | Consequence |
|---|---|---|
| Hub SQLite DB | `~/.scion/*.db` | Projects, **users**, secrets, user API tokens, message history |
| Integration sidecar DBs | `~/.scion/scion-chat-app.db`, `~/.scion/scion-a2a-bridge.db` (`handlers_integrations.go:1116,1158`) | Chat/A2A bridge state. **Separate databases** — a future Postgres migration would *not* cover these |
| Broker dispatch state | `~/.scion/runtime-broker-state/<brokerID>/dispatch-attempts/*.json` (`state_store.go:36-53`) | In-flight dispatch records |
| Agent homes | `<agentDir>/scion-agent.json`, `<agentHome>/agent-info.json`, `prompt.md`, `.gitconfig`, `.scion/scion-token` (`provision.go:1286-1326`, `run.go:755`) | Per-agent identity and config |
| Managed-agent state | `managed_state.go:59-67` | — |
| Workspaces | project dirs | Rebuildable from git |
| **Agent worktrees** | `<parentOfProject>/.scion_worktrees/<project>/<agent>` (`workspace_handlers.go:322-336`) | **Sits outside `~/.scion`** — see §5.3 |

**Also, and already true on every restart:** the agent/user signing keys, OIDC keys,
and the session secret are **never written to disk** at all. So a restart — not just
a redeploy — invalidates every session and every issued agent token. Tier 0 does not
make this worse; it does mean "just restart it" is never transparent to users.

### 5.2 What Tier 0 requires us to *build* (nearly nothing, but not nothing)

- **`workspaceWriteBlocked` already permits writes — but for the wrong reason.**
  Because `K_SERVICE` is unset on an Instance (§4.6), `isCloudRunEnv()` returns
  false and the gate never engages. We get the Tier 0 behaviour we want *by
  accident*: the guard designed to stop users writing into storage that silently
  evaporates is simply blind here, in precisely the situation it was written for.
  No code change is needed to ship, but this should be an **explicit, commented
  decision** rather than a silent coincidence — otherwise the first person to make
  `isCloudRunEnv()` smarter will 503 every workspace write on this tier without
  understanding why.
- **Say so in the UI.** A persistent banner on an ephemeral deployment. The failure
  mode we are guarding against is a user doing three days of work in a workspace
  they believed was durable.
- **Do not silence the existing warning.** `warnEphemeralProjectPath`
  (`workspace_storage.go:130-140`) logs *"hub-managed project served from ephemeral
  local path"*. Under Tier 0 that line is **correct**, and it is the single
  highest-signal string to grep for during rollout.

### 5.3 Two findings that constrain the durability follow-up

Recording these now because they determine the shape of the follow-up, and they are
cheap to get wrong later:

1. **`~/.scion` is not a sufficient mount boundary.** Agent worktrees live at
   `<parentOfProject>/.scion_worktrees/`, a *sibling* of the project dir, outside
   `~/.scion`. Mounting only `~/.scion` durably would still lose them. This is an
   argument for reaching durability via a non-`local` workspace backend rather than
   by making the local one durable.
2. **`$HOME` is the only relocation lever.** There is no `SCION_HOME`,
   `SCION_STATE_DIR`, or `SCION_DATA_DIR` anywhere in the repo. Also,
   `volumeMountBase` is hardcoded to `/mnt` (`workspace_storage.go:30`) and neither
   `V1CloudRunVolumeConfig` nor `V1GKESharedVolumeConfig` has a mount-path field —
   so a volume must be mounted at exactly `/mnt/<volume_name>`.

**And one guard already in the tree worth wiring up.** `isMountedVolume`
(`workspace_storage.go:94-110`) compares `syscall.Stat_t.Dev` against the container
root to detect exactly this failure. Its own comment describes the trap:

> *"readiness would report healthy forever while every project tree is written to
> storage that vanishes on reschedule."*

It **fails open** when the device ID is unreadable, which its comment concedes
"silently reinstates the bug this function exists to catch." When durability is
built, wire the `determinable == false` case into readiness. It costs nothing and it
is the cheapest possible guard against a silent-data-loss deployment.

### 5.4 The follow-up ladder (not in scope, recorded for continuity)

| | Control plane | Workspaces | Cost |
|---|---|---|---|
| **Tier 0** ← *this design* | ephemeral | ephemeral | $0 |
| **Tier 1** | replicated to GCS (Litestream-style / checkpoint-on-write) | ephemeral | cents/mo |
| **Tier 2** | Filestore | Filestore | ~$160-200/mo — rejected in `single-node-auth.md` on cost |

Tier 1 is the natural next rung: the control plane is small, write-light, and the
only part whose loss is genuinely *surprising*. The existing `cloudrun-volume`
workspace backend (§3.3) is the seam for durable workspaces later, without redesign.

---

## 6. Alternatives Considered

### 6.1 Agent-per-Cloud-Run-Instance — *not an alternative; a sibling in flight*

**This is not a road-not-taken. It is a second runtime that is actively being
built.** Per ptone (2026-08-25): Cloud Run Instances will remain *"a distinct
supported runtime,"* and it is *"actually fully built in another branch."*

The in-tree surface is the visible tip: `V1CloudRunInstancesConfig`
(`settings_v1.go:863-865`) documents *"the per-agent-instance variant of Cloud Run,
where each agent gets its own Cloud Run service instance"*, with a factory case at
`factory.go:170`, both currently wired to the 193-line stub whose `Run` always
errors. The real implementation lives elsewhere.

So the honest framing is **two runtimes, two topologies, one platform**:

| | `cloudrun-instances` (sibling) | `cloudrun-sandbox` (this design) |
|---|---|---|
| Unit of isolation | one Cloud Run Instance per agent | one sandbox per agent, all inside one Instance |
| Filesystem | not shared — needs remote staging | **shared by construction** |
| Per-agent billing | yes | no — one Instance |
| Control channel | network | `exec` + shared tmux socket |
| Image per agent | yes | no, until Templated Sandboxes (§4.2) |

They are complementary. The sibling buys real isolation and per-agent billing at the
cost of the shared filesystem that §3.2 shows the entire launch path rests on. This
design buys cheapness, speed, and the shared filesystem at the cost of shared fate.
Neither subsumes the other, and this design is the one ptone scoped here.

**Three consequences, all now firm:**

1. **The new runtime must be named `cloudrun-sandbox`.** `cloudrun-instances` is
   taken by a real implementation, not a placeholder. Reusing it would conflate
   opposite topologies.
2. **Do not touch, deprecate, or repurpose the existing stubs.** OQ-5 is resolved as
   *keep*. The stub is the sibling's landing pad.
3. ~~**⚠️ Coordination hazard.**~~ **OQ-10 RESOLVED, 2026-08-25 (ptone):**
   [`ptone/scion@cloudrun-instances-runtime`](https://github.com/ptone/scion/tree/cloudrun-instances-runtime),
   head `220115a`. Audited — see §6.1a. The hazard is **much smaller than predicted**,
   and P1 is unblocked.

#### 6.1a Audit of `cloudrun-instances-runtime` (2026-08-25)

Four findings, in descending order of consequence:

**Method note first, because it nearly cost me the finding.** My initial audit ran
against a **shallow clone** (`origin/main` had exactly one commit locally). With no
common ancestor available, `git merge-base` returned empty, every `$B` in my
commands expanded to nothing, and the resulting diffs silently compared the *working
tree* to the branch. That inverted the sign on every number: I read a large addition
as a large deletion. Corrected below after `git fetch --unshallow`. **Anyone
re-verifying this must confirm they have unshallowed first** — the wrong answer here
is confidently wrong, not obviously wrong.

Verified facts: merge base `c01eeee8`, 8 days old. Branch = **23 commits**, and is
**78 commits behind `main`** (which has moved to `aedf89e`).

**1. The registration work P1 was going to do is already on `main`.**
`cloudrun-instances` exists today as a config type (`V1CloudRunInstancesConfig`,
`settings_v1.go:866`), a factory case (`factory.go:176`), and a constructor
(`NewCloudRunRuntimeFromInstances`). The branch does not add these; it *fills them
in*. **P1 therefore has a merged worked example** — follow its shape rather than
inventing one, and check each of §4.7's nine allowlists against what
`cloudrun-instances` actually needed. Any it skipped are either unnecessary or a
latent bug in the sibling; both are worth knowing.

**2. The branch is a substantial addition, not a refactor: +1,826 / −233** across
`pkg/runtime` and `settings_v1.go`. It adds `iap_exec.go` (439), `logs.go` (243),
`logs_test.go` (177), `cloudrun_doctor.go` (55), and grows `cloudrun_runtime.go` by
~750 lines of real Instances-API implementation (`runapi.InstancesClient`, per-agent
Instance creation, NFS provisioning).

**3. It brings prior art we want for P4, rather than removing it.** Two pieces:

  - `cloudrun.ExecConnector` — an interface deliberately abstracting *how* you exec
    into a Cloud Run Instance, with an IAP implementation behind it and a comment
    anticipating it being swapped. §4.8b should conform to this interface rather
    than growing a parallel one.
  - `logs.go` / `StreamLogs` — Cloud Run log streaming, directly relevant to §4.4's
    `GetLogs` fallback if the tmux socket fails AC-0.

**4. The real coordination cost is the branch's rebase, not P1's conflicts.** P1 adds
a new `case "cloudrun-sandbox"` to the factory switch — mechanically separate from
anything the branch touches, so the original "near-certain conflicts" prediction was
over-cautious *for us*. But the branch's own `factory.go` delta currently reverts
`main`'s newer `WorkspaceStorage` wiring on three cases, because that wiring landed
in the 78 commits it predates. **Whoever rebases must preserve it.** That is a real
conflict, it belongs to the branch owner, and §7.1's ordering exists precisely so it
gets resolved once rather than twice.

### 6.2 Docker-in-Docker inside the Instance

Run a real Docker daemon in the Instance and keep `DockerRuntime` unchanged. Zero
new runtime code; full template/image support; keeps `pty_handlers.go` on its happy
path.

**Rejected because** it requires privileged containers, which Cloud Run does not
grant; it forfeits the isolation sandboxes exist to provide; and it reintroduces
`requireImageRegistryForBroker` and the whole image-distribution problem the
omni-image deletes. It also ignores ptone's explicit direction.

### 6.3 Sandboxes with per-agent images via a registry

Skip the omni-image; pull a per-agent image for each sandbox.

**Rejected because it is not currently possible.** `--rootfs /` inherits the
launcher's filesystem by construction; per-sandbox images are the Templated
Sandboxes feature, which is not yet available. Revisit when it ships (§7).

### 6.4 Keep the hub off Cloud Run entirely (GCE VM + Docker)

The path `single-node-packaging.md` and `single-node-auth.md` assume: a plain VM
with a persistent disk, Docker, and Cloudflare Tunnel ingress.

**Not rejected — it remains the right answer for self-hosting**, and it is strictly
more durable and more capable (real per-container limits, template images, no alpha
dependency). It is listed here because it is the honest baseline this design must
justify itself against. This design wins on: no VM to manage or patch, no tunnel, no
registry, scale-to-cheap, and a one-command deploy. It loses on durability and
per-agent resource control. Both should exist.

---

## 7. Migration / Rollout

Nothing here changes existing behaviour. Every change is additive behind a runtime
type that no existing config selects.

- **No migration for existing deployments.** Docker, Podman, and K8s paths are
  untouched. The one shared-code change is §4.6's discriminator, which only alters
  behaviour on a machine that has the sandbox binary.
- **The schema fix is a bug fix** and should land independently — the enum is
  already stale today.
- **S1/S2 (§4.11) should land first and separately.** They are security fixes
  valuable on their own, and they must be in before anything is deployed publicly.
- **Merge-order hazard:** the `auth-refactor` project (branch `scion/auth-refactor`)
  is landing large `pkg/hub` auth changes not yet on main. §4.11's work overlaps it.
  **Check merge order with that project's owner before starting S1/S3.**
### 7.1 Single integration branch — sequencing, not branching

Asked by ptone 2026-08-25: can this land on one integration branch? **Yes, and it
should.** The risk is ordering, not isolation. Three workstreams converge on the same
small set of files — chiefly `pkg/runtime/factory.go`, the runtime registration
surface, and `handlers_runtime_brokers.go`:

| # | Workstream | Touches |
|---|---|---|
| 1 | ptone/scion#1257 (structured status, `agent-status-lead`) | `ExitCode` on `Runtime.List()` output → **every** runtime implementation, plus hub + ent schema |
| 2 | `cloudrun-instances-runtime` branch @ `220115a` (§6.1a) — 78 commits behind main, needs rebase | factory type switch, `V1CloudRunInstancesConfig` |
| 3 | This design's P1 | factory type switch, config allowlist |

Run in parallel these three collide; run in order they barely touch.

**Recommended merge order:** #1257 phases 1–2 → `cloudrun-instances` branch → this
design's P1 rebased onto both. One integration branch, three sequenced merges.

Landing #1257 first is additionally justified because its wire surface is not yet
settled — a contradiction between its D5 (use `Activity` as the crash-reason carrier)
and D7 (Activity is self-reported, never runtime-produced) was raised on
2026-08-25; `list.go:118-121` blanks `Activity` at exactly the moment a runtime
observes a terminal exit, so the reason cannot reach the hub on that path. Until that
resolves, P3 should not hard-code the reason field's shape.

**Revised 2026-08-25, post-audit (§6.1a).** OQ-10 is resolved and the ordering above
is now weaker than it looked. Two of its three premises did not survive:

- **P1 does not depend on workstream 2.** The registration surface P1 needs is
  already on `main`; the branch fills it in rather than defining it. P1 adds one new
  switch case, which conflicts with nothing on that branch. **P1 can start now,
  ahead of workstream 2** — which matters, because workstream 2 is 78 commits behind
  and its rebase is not on our critical path.
- **The #1257 dependency is real but narrow** — it binds P3, not P1. P3 reports
  status against #1257's wire types; P1 is a stub that errors.

**Revised order: P1 now (independent) → #1257 phases 1–2 → P3 → workstream 2 whenever
its owner rebases.** Still one integration branch, still sequenced, but the sequence
no longer serialises P1 behind two other teams. The original ordering was written when
workstream 2 was an unidentified branch of unknown scope; conservatism was right then
and is now just latency.

- **When Templated Sandboxes ship:** per-agent images return. The runtime gains an
  image-selection path and `ImageExists`/`PullImage` stop being no-ops. Because the
  omni-image is confined to §4.2 and the runtime's own methods, this is a change to
  one file plus the image build, not a redesign.

---

## 8. Open Questions

Numbered for reference. **OQ-1 and OQ-2 are the two that change the design**; the
rest change details.

| # | Question | For | Blocks | Default if unanswered |
|---|---|---|---|---|
| ~~**OQ-1**~~ | ~~Durability tier?~~ **RESOLVED — Tier 0, pure ephemeral. Durability as follow-ups.** (ptone, 2026-08-25) | — | — | §5 |
| ~~**OQ-11**~~ | ~~Omni-image manifest?~~ **RESOLVED — antigravity, claude, codex, opencode, grok** (ptone, 2026-08-25). Base image settled by construction: `scion-base`. See §4.2. | — | — | — |
| **OQ-14** | **Does "we can reach inference endpoints" (Q3) mean public HTTPS only, or does Vertex AI via ADC also work?** The guide says `--allow-egress` grants no GCP service access and no use of the Cloud Run SA. If public-only, `vertex-ai`/`gcloud-adc` are unavailable on this tier — affecting 3 of the 5 shipped harnesses. Asked 2026-08-25. | ptone | P3 auth-mode rejection | Public HTTPS only; reject `vertex-ai`/`gcloud-adc` at launch **SHARPENED 2026-08-26 by OQ-2's negative result: the GCE metadata server (`169.254.169.254`) TIMES OUT from inside a sandbox even with `--allow-egress` on, while launcher link-local IPs answer fine. So ADC-via-metadata looks simply unavailable, independent of egress policy — the question is no longer 'does Vertex work' but 'can the launcher mint and push credentials in'. That is the same shape as the retired P7 design (§11.11), which is why that reasoning was preserved rather than deleted outright.** ⚠️ **Open and unowned; now the most consequential remaining question — it affects 3 of the 5 shipped harnesses.** |
| ~~**OQ-13**~~ | ~~Cloud Run project access for AC-0.~~ **RESOLVED 2026-08-25.** ptone granted Token Creator + SA User. Verified end-to-end: `gcloud auth print-access-token --impersonate-service-account=scion-instance-gym@serverless-team-scion...` succeeds, and `gcloud alpha run instances list --project=ptone-experiments --region=us-central1` returns cleanly (0 items). **AC-0 is unblocked.** Agents may also be started *with* that SA directly (needs a scion binary new enough to have the GCP SA flag on create/start). | — | — | — |
| **OQ-12** | Flag names for **per-sandbox resource limits**. Q4 confirmed support ("details later"), but P2/P3 wire `RunConfig.Resources` straight through and cannot emit an invocation without them. | Cloud Run team | P2/P3 detail, not shape | Omit limits; agents share Instance resources |
| ~~**OQ-2**~~ | **CLOSED 2026-08-26 by measurement (`spike-oq2`).** **A sandbox CAN reach its launcher** — not on loopback (the sandbox has its own netns) and not over AF_UNIX (does not cross the gVisor boundary), but directly on the **launcher's link-local address**, **1.64 ms median** vs 35 ms for the hairpin. Option **A** wins; **A2 and the whole transport-token path are dropped, and P7 is deleted.** The launcher discovers its own link-local IP at runtime and injects it per sandbox as `HUB_HOST` — **no hardcoded address**. Two constraints survive: `--allow-egress` is **mandatory** (nothing is reachable without it, launcher included) and **all-or-nothing**; and the **metadata server is unreachable from the sandbox even with egress**. See §11.10. | — | Closed | **A** — direct in-Instance path |
| ~~**OQ-3**~~ | ~~Can sandboxes reach LLM APIs?~~ **RESOLVED — yes, inference endpoints are reachable.** (ptone) Vertex-AI-as-a-GCP-service remains untested; note it if someone selects that backend. | — | — | — |
| ~~**OQ-4**~~ | ~~Per-sandbox CPU/memory limits?~~ **RESOLVED — yes, supported; undocumented, details to follow.** (ptone) | — | — | See §8.1 |
| ~~**OQ-5**~~ | ~~Deprecate or keep the `cloudrun-instances` stubs?~~ **RESOLVED — keep.** It is a real sibling runtime, fully built in another branch (§6.1). | — | — | — |
| **OQ-15** | **Is `Instance.iapEnabled` honoured, or declared-but-inert?** The v2 discovery doc lists `iapEnabled: boolean` ("Optional. IAP settings on the Instance") directly on `GoogleCloudRunV2Instance` — but §4.9a is built on ptone's relay that Instances have **no direct IAP and it is not coming soon**. Treat schema presence as weak evidence: `sandboxLauncher` was in the schema before we could confirm the platform honoured it. **If honoured, the auth-proxy service and the whole A2/C hairpin analysis collapse into "turn IAP on"** — a large simplification of the tier's most complex section. Also ask about `invokerIamDisabled`, since S2 depends on the invoker check staying on. **→ RESOLVED BY TEST 2026-08-26: `iapEnabled` is LIVE** — enforced at the edge (302 to `accounts.google.com`, container never reached), on genuine **Instances** (create URL confirmed `…/instances?instanceId=…`). §4.9a's premise is false and the section is being rewritten. **One sub-question still open — see OQ-17.** | Us | **P6**, not P4 | — |
| **OQ-17** | ~~Is `invokerIamDisabled: true` required when IAP is on?~~ **ANSWERED BY TEST — YES, IT IS REQUIRED.** ⚠️ **This row previously carried the opposite answer**, derived from [enabling-cloud-run](https://cloud.google.com/iap/docs/enabling-cloud-run), which is written for Cloud Run ***services*** and does not hold on Instances. Measurement (§10b.1) overturns it: IAP's `x-serverless-authorization` carries audience `/projects/{n}/locations/{r}/services/{name}`, and the **Instance** invoker check accepts the Instance **URL** and the **`instances`**-path form (both 200) but **rejects the `services` form → 401**. Granting `roles/run.invoker` to the IAP service agent does **not** help; the token is rejected on *audience*, not on identity. **IAP and the Instance invoker check cannot coexist — `invokerIamDisabled: true` is mandatory under IAP.** This question flipped **three times** (required → not required → required); trust only the measured answer. **Changes S2:** IAP becomes the sole perimeter, and the I4 open-config footgun becomes the *supported* configuration. Filed as `defect-iap-instance-audience.md`. | Resolved by test | — | Set `invokerIamDisabled: true`; IAP is the only gate |
| ~~**OQ-16**~~ | ~~Does `sandbox delete --force` behave differently under concurrency?~~ **ANSWERED BY TEST — NO** (`sn-impl-em2`, 2026-08-26). Five concurrent deletes on a live Instance each bound at ~30s and ran **in parallel, not serialised** — the hang is not contention-related and fan-out is no worse than serial. Sandboxes are unreachable at **<1s** under fan-out, same as serially, so the 10s timeout keeps ~10× margin as the fleet scales. Two orphan scares were chased down and both resolved away from the code: the extra `runsc state` processes were an artifact of the **test's own** `sandbox exec` probing (0/4/7 probes → 0/4/7 processes — tracks probe count, not concurrency), and `sandbox wait` shells out to `runsc wait`, which **exits cleanly** on watcher cancellation (identical orphan counts with the watcher absent / killed-first / killed-simultaneously). **P4a validated, no code changes.** See `validation/delete-timeout-validation-results.md`. | — | — | Resolved |
| ~~**OQ-6**~~ | ~~Does `sandbox exec` allocate a TTY?~~ **RESOLVED BY TEST 2026-08-26 (T11 + T5).** No `--tty` flag exists — **and it is not needed.** A launcher-side PTY propagates via gVisor host-fd passthrough, so the inner process sees a TTY without the CLI allocating one. See §4.4a-rev. | — | — | — |
| ~~**OQ-7**~~ | ~~Is `K_SERVICE` set on an Instance?~~ **RESOLVED — NO.** (ptone) Rewrote §4.6; two predicted problems vanish, two new ones appear (`hub_id` derivation, duplicate log ingestion). | — | — | Capability probe |
| ~~**OQ-10**~~ | ~~Which branch holds the built `cloudrun-instances` runtime?~~ **RESOLVED 2026-08-25 (ptone):** `ptone/scion@cloudrun-instances-runtime`, head `220115a`. Audited in **§6.1a** — 23 commits, +1,826/−233, **78 commits behind main**. P1's own conflict surface is small; the rebase burden belongs to the branch owner. **P1 is unblocked.** | — | — | — |
| ~~**OQ-8**~~ | ~~How do auto-exposed agent ports reach a user?~~ **RESOLVED — reverse WebSocket tunnel from inside the container; no runtime capability needed** (§4.9) | — | — | Works unchanged |
| **OQ-9** | Does `sandbox delete` SIGTERM-then-SIGKILL, and is the grace period configurable? (§4.3) | Cloud Run team | Phase 3 | If SIGKILL-only, agents report `offline` not `stopped` — a known K8s-equivalent defect |

### 8.1 OQ-4 resolved — the biggest risk in the design is retired

I had flagged the absence of per-sandbox limits as the thing that could bite
hardest: Scion's per-agent Resource Spec would have to be documented as ignored, and
**one runaway agent could OOM the Instance and take the hub down with it.**
Control-plane/workload shared fate is an architectural weakness, not a rough edge,
and I was prepared to propose concurrency caps and a memory watchdog as mitigation.

**ptone confirms limits are supported** (undocumented; details to follow). That
removes the mitigation work entirely and upgrades the plan:

- `RunConfig.Resources *api.ResourceSpec` is already populated by the single launch
  site. **Wire it straight through to the `sandbox run` flags** rather than
  dropping it.
- The hub's existing per-agent resource UI keeps working; no "ignored on this
  runtime" caveat in the docs.
- Shared fate is reduced to the Instance-level failures we already accept in a
  single-node tier.

Add to AC-0: **confirm a sandbox is actually killed at its memory limit rather than
the Instance being OOM-killed.** The guarantee only counts if the kernel enforces it
at the right boundary. Until the flag names are documented, treat the exact mapping
from `ResourceSpec` as a P3 detail.

---

## 9. Implementation Phases

Commit-sized, ordered so each lands independently and something is testable early.

| Phase | Content | Depends on |
|---|---|---|
| **P0** | **Security fixes.** S1 (dev auth refuses non-loopback bind) and S2 (drop `--no-invoker-iam-check` from defaults). Independently valuable; check `auth-refactor` merge order. | — |
| **P1** | **Registration plumbing.** Add `cloudrun-sandbox` to the factory, the nine allowlists (§4.7), and the JSON schema enum (also fixing the pre-existing stale enum). Runtime is a stub that errors. Add `SandboxLauncherAvailable()`; gate autodetect on `CLOUD_RUN_INSTANCE` then the binary probe, both *before* the `K_SERVICE` branch (§4.6). Pin `hub_id` in the deploy config. | ~~OQ-10~~ **resolved** — sequence against `cloudrun-instances-runtime` (§6.1a); conflict surface is ~1 new switch case |
| **P2** | **Omni-image.** `image-build/omni/Dockerfile` + build wiring. Deployable to an Instance; hub serves; agent launch fails cleanly. | — |
| **P3** | **`Run`/`Delete`/`List`.** Translate `RunConfig` → `sandbox run`, mounts, env, `sciontool init` argv. Local state store (§4.5). Hub endpoint wiring per §4.9 (`container_hub_endpoint` = `run.app` URL if hairpinning). **First end-to-end agent start.** | P1, P2 |
| **P4** | ~~**tmux socket control plane.** `TMUX_TMPDIR` mount~~ → **`sandbox exec` control plane.** `Attach`/`GetLogs`/`Exec` and the `pty_handlers.go` branch (§4.8b), each carried in via `sandbox exec` (§4.4a-rev). **No mount, no `TMUX_TMPDIR`, no `script` wrapper.** Attach is `sandbox exec <id> --env TERM=xterm-256color -- tmux attach`, launcher-side `pty.StartWithSize`. Resize is a second exec (`tmux resize-window`) driven by SIGWINCH, since the signal does not cross. **Browser terminal works.** | P3 *(AC-0 and Tier A/B are done — no longer a gate)* |
| **P4a** | **Timeout-bounded `Delete` — structured as a removable workaround.** `sandbox delete --force` **never returns** — on any sandbox (T9, C1, C2), including one running only `sleep`. **ptone has filed this with the Cloud Run team**, so the workaround is explicitly temporary and must be built to be deleted (§9.2). Issue `--force`, bound it, **treat the timeout as success** (deletion is effective; the sandbox really is gone), and reap the orphaned `runsc delete` — **but only on the timeout path** (§9.2 point 2). **Do not fall back to non-force** — C3 shows plain `delete` refuses *and kills anyway*, leaving live gofer/sandbox processes behind a CLI that reports "not running". **Split out from P4 deliberately** — it is a teardown-correctness fix on the *normal* Tier 0 lifecycle (every redeploy deletes the whole fleet), and it must not be able to slip as "polish" attached to the terminal feature. ~~**Open:** concurrent-delete behaviour is untested and fan-out is our actual pattern.~~ **✅ LANDED AND VALIDATED 2026-08-26** on `scion/dev-rebase-1294` @ `0a1536b3`, timeout **10s**, kill switch `SCION_CLOUDRUN_DELETE_WORKAROUND=off`. Validated on a live Instance: unreachable **<1s** serially *and* under 5-way fan-out (~10× margin), concurrent deletes run **in parallel** (OQ-16 answered), no false positives on a genuinely hung delete, and the orphan matcher is correct for the only orphan the production path creates. Both orphan scares — `runsc state` and watcher-cancel — resolved away from the code; see the OQ-16 row and `validation/delete-timeout-validation-results.md`. | P3 |
| **P5** | **Tier 0 honesty.** Permit workspace writes under an explicit ephemeral profile, UI banner, keep the ephemeral-path warning (§5.2). Small. | P3 |
| **P6** | **Auth + deploy UX — fully designed in §11** (2026-08-26). One-command deploy with two hard gates (IAP-reconcile wait; an uncredentialled-request assertion that *fails the deploy* if the app answers), region-scoped IAP binding, effective-access read-back, docs including the §5.1 loss inventory. **Two findings that resize this phase:** (a) the hub's IAP verification **already exists and already accepts the Instance-form audience** — P6 is largely configuration, not implementation (§11.4); (b) **the bootstrap token is retired for this tier** — the deploying operator's identity seeds `AdminEmails`, so IAP has already proven who they are (§11.6). | P0-P5 |
| ~~**P7**~~ **— DELETED, does not exist** | **OQ-2 answered by measurement 2026-08-26:** the sandbox reaches the launcher directly on the launcher's link-local address in **1.64 ms median**, so agents never cross the IAP perimeter and no agent-side transport credential is needed. The contingent design (launcher-minted token push) is retired — see §11.10, §11.11. **Two constraints survive it:** `--allow-egress` is **mandatory** (without it the sandbox has no network at all, launcher included) and is **all-or-nothing**; and the **metadata server is unreachable from the sandbox even with egress**, which constrains **OQ-14**. | — |

**Status 2026-08-25T21:20Z:** **P0 and P2 are landed** on `scion/sn-impl-em`
(c2068f2), both through code review and security audit. P0 = `IsLoopbackHost` guard
in `initWebServer` + `NewWebServer`; S2 verified already absent. P2 = omni-image with
the five harnesses + build wiring. Reviewing that diff produced §4.3a (the
`ExecUser()` gap). **P1 is the critical path and is now unblocked (OQ-10 resolved; see §6.1a).**

**Dispatched 2026-08-25:** `sn-impl-em` owns **P0-P3** (brief:
`briefs/sn-impl-em.md`). P4-P7 are not dispatched. Start order given to that EM is
**P0 and P2 first** (no dependencies), **P1 now released (OQ-10 resolved)**, **P3 held on #1257's
frozen wire types**. P0 requires a merge-order check against `scion/auth-refactor`
before it starts. AC-0 is to be run the moment Cloud Run project access lands
(ptone provisioning) and its results written back into §10.

**Dispatched 2026-08-26 (ptone chose "new EM"):** `sn-impl-em2` owns **P4, P4a, P5**
(brief: `briefs/sn-impl-em2.md`), starting from `scion/dev-rebase-1294` @ `8a7852f2`.
**P6 and P7 remain undispatched**; P7 is still conditional on OQ-2.

In parallel and deliberately **not** coupled to that EM: `spike-iap` tests OQ-15
(§10b). Its result changes §4.9a, which is a **P6** concern — it must not be allowed
to perturb P4/P4a/P5, and both briefs say so explicitly.

Durability (Tier 1) is explicitly **out of this sequence** and lands as follow-up
work, per ptone. §5.3 records the two constraints that will shape it.

### 9.2 The delete workaround must be built to be removed

**ptone, 2026-08-26: the `sandbox delete --force` hang is filed with the Cloud Run
team.** So P4a is not a permanent fix — it is a temporary shim around a defect
someone else is actively working on. That changes how it should be written.

The default outcome for a workaround is that it silently outlives the bug by years,
because nothing ever tells anyone it is safe to remove. Six requirements, of which
**#2 is a correctness issue and the rest are hygiene**:

1. **One file, one seam.** `pkg/runtime/cloudrun_sandbox_delete_workaround.go` holds
   `DefaultDeleteTimeout`, `deleteWithTimeout` and `reapOrphanedRunsc`. `Delete()`
   calls a single entry point, so removal is `git rm` plus reverting one call site to
   a plain `exec … delete --force`. As first implemented the workaround was
   interleaved with the real logic and could not have been backed out without picking
   it apart.

2. **⚠️ Reap only on the timeout path — this is a latent hazard, not style.** The
   first implementation called `reapOrphanedRunsc` unconditionally after the `select`.
   That is harmless *today*, precisely because `--force` never returns. **When the
   platform is fixed, a working `delete` returns promptly and an unconditional reaper
   will SIGKILL a healthy in-flight operation matching the same pattern** — the
   workaround starts causing damage at the exact moment it stops being necessary.
   If we did not time out, there is by definition no orphan to reap.

   *This is the general shape worth remembering: a workaround whose trigger condition
   is "the platform is broken" must actually test that condition, not assume it.*

3. **Self-detecting.** On a normal return, log **once** at WARN: `sandbox delete
   --force returned in Xms — upstream defect may be fixed; this workaround is a
   candidate for removal`. This converts "someone remembers to re-check" into "the
   system tells us."

4. **Runtime kill switch.** `SCION_CLOUDRUN_DELETE_WORKAROUND=off` (default on) takes
   the plain path. When the fix ships, validation needs no rebuild — which is the
   difference between removal being a five-minute decision and a small project.

5. **One greppable reference**, `deleteDefectRef` — **but it cannot cite a bug
   number.** ptone confirms the Cloud Run bug is tracked **internally to Google**;
   there is no issue this repository can link to. That is the right outcome anyway:
   an internal bug ID in a public tree is a dead reference for every reader outside
   Google. So the constant points at **our own evidence** instead.

   **Consequence — the evidence has to move into the repo.** A comment pointing at
   `/scion-volumes/scratchpad/...` resolves for our agents and for nobody else.
   `defect-sandbox-delete-hang.md` (revision 2, with the T9/C1–C3 control matrix and
   the captured argv) should be copied into the tree next to the existing
   `.design/project-log/p4a-delete.md`, and *that* in-repo path is what the code
   cites.

6. **Exit criteria in the file header.** What must be *observed* to justify removal:
   `--force` returns within the timeout on a sandbox with a live process, and no
   orphaned `runsc delete` remains.

   **Anchor it on the `runsc` version, which is checkable, rather than on a bug
   status, which is not.** The defect was observed on **`runsc google-958767651`,
   spec 1.2.1** (binaries dated 2026-08-04). Anyone evaluating removal can read the
   version off a live Instance in one command and know whether they are even looking
   at a build that could plausibly contain the fix. With no public bug to watch, this
   is the only self-service signal available — which makes requirement #3's
   self-detecting log the primary trigger, not a nicety.

### 9.1 Prerequisite workstream: structured agent status (not owned by this project)

**Filed as ptone/scion#1257. Owned by `agent-status-lead`.** Per ptone this lands
**before** implementation starts. Requirements below were sent to that owner
2026-08-25; they are recorded here because P3 is written against them.

**Target shape:** replace the parsed prose with structured fields and **parse at the
edge**. Docker/Podman/Apple keep their `docker ps` parsing, but it moves *into* the
runtime that natively speaks Docker rather than living in the hub. Runtimes that
already synthesize (K8s) and runtimes that never had strings (`cloudrun-sandbox`)
populate the fields directly.

**What this design actually needs from that workstream — five points:**

1. **One new wire field, not two.** The issue proposes `Running bool` + `ExitCode
   *int`. We do **not** want `Running bool`. `hubclient.AgentHeartbeat` already
   carries `Phase` (`runtime_brokers.go:191`) over the
   created/provisioning/running/stopped/error enum in `pkg/agent/state/state.go`.
   A bool is strictly less expressive — it cannot distinguish provisioning from
   stopped, nor express error — and it would become a third overlapping answer to
   "is this alive" alongside `Status` and `Phase`. The genuine gap is `ExitCode
   *int`, which the hub already computes and merely derives by regex today.
2. **`Phase=stopped, ExitCode=nil` must mean "code unknown", not "clean exit".**
   The current legacy branch falls through to `PhaseStopped` when the regex misses,
   silently reading a missing code as a successful one. Under Docker that is nearly
   harmless because the corpse stays inspectable; here "unknown" is genuinely
   reachable (point 3) and the mislabel is a wrong answer, not a cosmetic one.
3. **The heartbeat must stay level-triggered.** This is the load-bearing constraint.
   The sandbox CLI has **no `list`, no `inspect`, no `ps`** (verbs are `do`, `run`,
   `exec`, `fork`, `tar`, `wait`, `delete`). There is no primitive to ask the
   platform for current state, so unlike Docker and K8s this is not a pollable
   level-triggered source: the only exit signal is `sandbox wait`, which blocks and
   fires **once**. If the wire moves to a delta/event format, a single dropped
   heartbeat loses the crash code *permanently* — there is no corpse to re-read. The
   broker must latch the code and restate full phase + exit code every beat until
   the record is reaped. See §4.5 and §4.3.
4. **`ExitCode` alone cannot mean "crashed".** 137 arrives from OOM-kill, from user
   stop, and from Cloud Run evicting the Instance. Teardown SIGKILLs every sandbox
   at once, so a naive mapping shows the user N simultaneously crashed agents. A
   companion reason is needed — and it should reuse `state.ActivityCrashed` /
   `ActivityLimitsExceeded`, the two values `Activity.IsTerminal()` already
   recognises (`state.go:191-198`), not a parallel enum.
   **⚠️ Revised 2026-08-25 — I now think this ask was wrong.** It was granted as
   written, and the result validates a runtime-derived field against the
   *self-reported* `Activity` enum, which re-couples the two vocabularies D5 had
   deliberately split. It also leaves no way to say "the platform killed me" — the
   exact case this point was raised to handle. See **§9.1b**.
5. **`containerStatus` must become optional and display-only.** Today a new runtime
   is obliged to *fabricate* Docker prose purely to satisfy the regex. If any
   consumer still requires it non-empty, `cloudrun-sandbox` is back to writing
   fiction. Any human-readable string should be rendered *by the hub from the
   structured fields* — the derivation runs the other way.

#### 9.1a Frozen wire contract — #1257 Phase 1 (verified 2026-08-25)

**PR [ptone/scion#1260](https://github.com/ptone/scion/pull/1260)**, branch
`scion/agent-status`, head **`04b6f06`**. **CI-green, but NOT yet merged** — P3
codes against this; do not assume it is on `main`.

**Updated 2026-08-25:** the vocabulary split (§9.1b) landed in Phase 1 after all,
despite my revised advice to defer it. Verified at `04b6f06`:

```go
// pkg/agent/state/state.go:137
type ExitReason string
const (
    ExitReasonCrashed        ExitReason = "crashed"
    ExitReasonLimitsExceeded ExitReason = "limits_exceeded"
)
func (r ExitReason) IsValid() bool  // "" is valid
```

`isValidExitReason` (`handlers_runtime_brokers.go:925`) now delegates to
`state.ExitReason(reason).IsValid()` rather than `Activity.IsTerminal()`. The
provenance re-coupling is gone, and `ExitReasonPlatformEviction` becomes a one-line
addition that does not touch the self-reported `Activity` enum. Debug logging on
silent drops is in at `:727` and `:749`, guarded by `!= ""` so the common empty
case stays quiet.

**Note the wire fields remain plain `string`,** on both `AgentHeartbeat` and
`api.AgentInfo` — the typed vocabulary is enforced at the *producer* and validated
at ingest, not carried in the wire type. The four existing runtimes assign
`string(state.ExitReasonCrashed)` (`docker.go:229`, `podman.go:341`,
`apple_container.go:249`, `k8s_runtime.go:1937`). **P3 must do the same — use the
constant, never a string literal.**

Verified against the branch, not the summary:

**Three carriers**, all with the same two fields — the third is easy to miss:

```go
ExitCode   *int   `json:"exitCode,omitempty"`   // nil = unknown
ExitReason string `json:"exitReason,omitempty"` // "crashed" | "limits_exceeded"
```

| Struct | File | Role |
|---|---|---|
| `hubclient.AgentHeartbeat` | `pkg/hubclient/runtime_brokers.go:197-198` | broker → hub wire |
| `hubclient.Agent` | `pkg/hubclient/types.go:61-62` | hub → client API response |
| `api.AgentInfo` | `pkg/api/types.go:582-583` | runtime → broker |

Semantics: `nil` = unknown, `0` = clean exit, non-zero = crash. `ExitReason` is
validated by `isValidExitReason` (`handlers_runtime_brokers.go:925`) via
`state.ExitReason.IsValid()` — accepted set is exactly
`{"", "crashed", "limits_exceeded"}`. Point 4 above asked for reuse of the
`Activity` terminal values; §9.1b argued that was the wrong ask, and the ask was
reversed before merge.

~~**Minor:** invalid reasons are silently dropped.~~ **Fixed at `04b6f06`** — still
dropped rather than rejected (correct for compat), but now logged at debug.

#### 9.1b ⚠️ Non-zero exit is hardcoded to `PhaseError` — and that is our *normal* shutdown

The single most consequential interaction between #1257 and this tier
(`handlers_runtime_brokers.go:719-729`):

```go
if agentHB.ExitCode != nil && *agentHB.ExitCode != 0 {
    hbPhase = state.PhaseError
    statusUpdate.Message = fmt.Sprintf("Agent crashed with exit code %d", *agentHB.ExitCode)
}
```

Cloud Run Instance teardown SIGKILLs every sandbox simultaneously, and **Tier 0 is
pure ephemeral — this is how agents normally die here.** So the steady-state
experience is: tear down the Instance, and the whole fleet reads `PhaseError`,
"Agent crashed with exit code 137". `PhaseError` is a real state that code branches
on, not merely a display string.

**This pre-dates #1257** — Docker populates `ExitCode` the same way
(`docker.go:225`), so a user `scion stop` already yields 137 → "crashed". The
difference is frequency: on Docker it is an occasional annoyance affecting one
agent; here it is every agent, every shutdown. #1257 does not cause it, but it
freezes the shape that makes it unfixable from the runtime side.

**Why the runtime cannot work around it.** Our only levers are the two fields.
Report 137 → the fleet errors. Report `nil` → phase is right but we discard an exit
code we actually hold. Neither is honest.

**And the vocabulary cannot express the truth.** `ExitReason` validates against
`state.Activity` — the *self-reported* enum. Both legal values are agent-fault.
There is no value for "the platform killed me", and adding one means extending an
enum that agents populate from `agent-info.json`, where it is meaningless. D5
separated the fields for provenance; validating against `Activity` re-couples the
vocabularies.

**Requested of `as-em` (2026-08-25), in priority order:**

1. **Give `ExitReason` its own vocabulary** — a `state.ExitReason` type with the
   same two values today. Zero behaviour change, but a third value later becomes a
   one-line addition rather than a change to the self-reported enum.
   **⚠️ Revised again 2026-08-25, and this one is a correction to my own urgency.**
   I initially argued this was cheap now and expensive later, and pushed for it to
   ride Phase 1. Then I checked the persistence layer:

   ```go
   // pkg/ent/schema/agent.go, branch scion/agent-status
   field.String("exit_reason").Optional(),
   ```

   A plain optional string column, **not an ent enum** — and the wire is a plain
   JSON string. So deferring to Phase 2 costs **no wire change** and **no
   migration**; only a Go-side refactor from `string` to a named type, which the
   compiler catches exhaustively. It is cheap now *and* cheap later. The second
   half was what justified the urgency, and it was wrong.

   **Revised recommendation: defer to Phase 2.** Do not destabilise an approved,
   CI-green PR for this. One residual, also fine: `isValidExitReason` ships in
   Phase 1 and silently drops unknown values, so a Phase 2 value needs hub-before-
   broker deployment ordering — which is normal, and moot in this tier where hub
   and broker ship in the same omni-image (§9.1, point 1).
2. *(Phase 2)* Let `ExitReason` modulate the phase decision, so an
   infrastructure/teardown reason keeps `PhaseStopped`. This is the real fix and it
   also cleans up the Docker `scion stop` case.
3. *(Phase 2)* Render "Agent crashed…" from the reason rather than assuming it from
   the code.

**P3 fallback if none of this lands — not a blocker.** Report `ExitCode=nil,
ExitReason=""` for teardown deaths: "stopped, code unknown", which #1257's own D2
already defines correctly. Accurate, merely lossy.

**Three things that make the workstream bigger than it looks:**

1. **It is a broker↔hub wire-format change.** `ContainerStatus` travels in
   `hubclient.AgentHeartbeat` (`heartbeat.go:278-286`) between independently
   versioned components, so it needs a compat window. Note this does **not** apply
   within our tier: hub and broker ship in the same omni-image (§4.2), so there is
   no skew between them. `cloudrun-sandbox` can therefore be the first *pure*
   structured client with no legacy path — a clean testbed for the new contract.
2. **A pre-existing defect sits in the same area and should be fixed with it.**
   `ExitCode *int` is already declared at `store.go:291` and already populated at
   `handlers_runtime_brokers.go:724,775` — but there is **no `exit_code` column**
   and `agent_store.go:620-728` silently drops it; the number survives only as
   English inside `Message`. This matters more to us than to anyone else: Tier 0 is
   pure ephemeral, so when the Instance dies there is no `docker ps -a` to
   post-mortem and the database row is the *only* copy. Persistence should stay
   welded to the structured-fields change, not deferred.
3. **The consumer surface is wide — 20+ non-generated sites**, not the ~13 a first
   pass suggested. Includes `phaseFromContainerStatus` (`common.go:948-958`),
   `exitedStatusRe` (`:962`), `ExitCodeFromContainerStatus` (`:968-978`), and the
   hub's legacy derivation (`handlers_runtime_brokers.go:758-792`). Mitigating
   factor: **Phase and Activity are already migrated** — the explicit
   `// Legacy path` split at `:758` proves it — so exit code is the last
   unmigrated consumer. This is finishing a migration, not starting one.

**Provenance split — Phase and Activity do not come from the same place.** Worth
recording because it bounds the workstream and it explains why this design needs no
parallel status path:

| Field | Source | In scope for #1257? |
|---|---|---|
| `Activity` | `agent-info.json` on the host FS (`list.go:93-101`), self-reported by sciontool from inside the container. `pkg/runtime` never produces it. | **No** |
| `Phase` | Merge: agent-info.json's phase, overridden by `terminalRuntimePhase()` (`list.go:307-323`) — which is itself a `ContainerStatus` string-matcher at `:318-321`. | **Only the runtime-derived half** |
| `ExitCode` | Runtime-observed only. No path exists today. | **Yes — the whole thing** |

The consequence for us: the self-reported half arrives through a file in the
bind-mounted agent home, and because `--rootfs /` gives the sandbox the launcher's
filesystem (§3.2), that mechanism works **unchanged** under Sandboxes with zero new
code. `cloudrun-sandbox` only has to supply the runtime-observed half — terminal
phase and exit code — which is exactly the surface being structured.

**Dependency:** P3 (`List`) is where `cloudrun-sandbox` first reports status. If the
workstream has landed, P3 populates structured fields. If it slips, P3 must not be
blocked — it can emit strings as a stopgap, but that stopgap should be written to be
deleted, not to be lived with.

**AC-0 is a prerequisite for P4 and should be run the day alpha access lands** — see
below. If it fails, P4 changes shape (fallback in §4.4) and should be re-scoped
before it is started.

---

## 10. Acceptance Criteria

### 10a. Empirical test plan — AF_UNIX and its replacement

**Raised by ptone, 2026-08-25:** *"do we have an empirical test plan given the
uncertainty of `--host-uds` usage?"*

**Honest answer: we had a checklist, not a plan.** AC-0 below lists the right
questions but carries no pass/fail predicates, no execution discipline, and no
statement of what each outcome changes. For the one assumption that already produced
a false positive in this project, that is not good enough. This section is the plan.

**It also corrects a call I made.** I wrote "no test needed" against the UDS item on
the grounds that the platform team's answer was authoritative. The answer *is*
authoritative and I am not seeking a second opinion. But that reasoning conflates two
different purposes for a test, and only one of them is "find out if the claim is
true."

#### Where the risk actually sits now

Worth stating plainly, because it has moved: **§4.4 is settled and §4.4a is not.**
The dead design has an authoritative answer; the live replacement rests on an
unverified PTY trick, an unconfirmed binary, an untested resize path, and an
unmeasured latency budget. Tier B is therefore the tier that matters. Tier A is cheap
insurance, and I would not let it delay Tier B.

> **Where it sits after both tiers ran (2026-08-26):** all four of those unknowns are
> closed. The PTY trick turned out to be **unnecessary** rather than unverified; the
> binary is therefore irrelevant; resize is verified (`resize-window`, out-of-band,
> still required); latency is 37 ms p95 against a 150 ms budget. **The risk has moved
> again — it is now in teardown, not attach.** T9's hanging `sandbox delete --force`
> is the one open behavioural defect on this path, and it sits on the *normal* Tier 0
> lifecycle rather than an edge case. See **P4a**.
>
> Both tiers vindicated running them. Tier A confirmed a ruling I had called
> "no test needed" and would have shipped on the strength of a corrected email; Tier B
> **overturned** a design I had written in detail and was about to dispatch. The
> cheaper of the two was the one I nearly skipped.

#### Ground rules (these are the part that generalises)

1. **Every test runs against a real Cloud Run Sandbox on a real Cloud Run Instance.**
   Not `unshare`, not local Docker, not a locally installed `runsc`. **The
   locally-installed-runsc case is the specific trap**: a self-installed gVisor may
   well have `--host-uds` enabled, which would produce a confident, reproducible,
   *wrong* answer — the exact shape of the `unshare` mistake, one layer more
   convincing.
2. **Write the pass/fail predicate before running the test.** The `unshare` result was
   recorded as PASS because nobody had written down what PASS was supposed to mean.
3. **Characterize negatives; do not just record them.** "Failed" is not a result.
   Capture the exact errno, message, and whether the failure was immediate or a hang.
4. **Capture raw output** to `ac0-results.md` alongside the verdict, so a later reader
   can re-derive the conclusion instead of trusting it.

#### Tier A — ⬆️ **PROMOTED TO BLOCKING, 2026-08-25** (was "~20 min, not blocking")

**Reason for promotion:** the engineering team corrected the ruling — `--host-uds` **is**
set (§4.4-rev). Tier A is no longer insurance against a hypothetical future; it decides
which design we build. **Run Tier A before Tier B.** If T3 passes, a meaningful part of
Tier B is testing a workaround for a problem that does not exist, and P4's shape
changes.

The original rationale is retained because it is what made the test exist at all: it
was justified as a tripwire for a change we thought was years away, and the change
turned out to be a correction arriving the same afternoon. Two of the three reasons
still apply as written — it documents the failure mode, and it inoculates against a
wrong re-verification (ground rule 1).

**Expected results below are stated as *predictions*, not as pass criteria.** After a
reversal, "pass" means *we learned the true behaviour and characterized it*; a
prediction being wrong is a successful test. This is stated explicitly because the
standing risk has now flipped: the earlier failure was over-reading a PASS from a
substitute mechanism, and the temptation now runs the same direction.

| ID | Test | Prediction / what to capture |
|---|---|---|
| **T1** | Socket created **inside** on a `--write` bind mount; `connect()` from the launcher. (`socat UNIX-LISTEN:<mnt>/s.sock,fork EXEC:/bin/cat` inside; `socat - UNIX-CONNECT:<mnt>/s.sock` outside.) **This is §4.4-orig's exact direction and needs `--host-uds` to permit *create*.** | Unknown — this is the deciding test. Capture `stat -c %F <mnt>/s.sock` from the launcher (socket / regular file / absent), and the exact error on failure. |
| **T2** | Reverse direction: socket created on the **launcher**, `connect()` from inside. Needs *open*. | Run regardless of T1's outcome. If T1 fails and T2 passes, the enabled mode is `open`-only and a *reverse* topology is possible — a different design, worth knowing before it is needed. |
| **T3** | **The literal §4.4-orig**, subdivided — see below. | Do **not** report a single verdict. |

##### T3 is subdivided, because a partial pass is the likely outcome

Setup: `TMUX_TMPDIR` to the bind mount; `tmux new-session -d -s scion` **inside**;
then from the launcher, against `-S <mnt>/tmux-<uid>/default`:

| ID | Operation | Needs | Note |
|---|---|---|---|
| **T3a** | `tmux -S <sock> has-session -t scion` | connect + byte stream | Simplest possible RPC. If this fails, T3b–d are moot. |
| **T3b** | `tmux -S <sock> send-keys -t scion 'echo hi' Enter` | connect + byte stream | Task delivery — the highest-value operation after attach. |
| **T3c** | `tmux -S <sock> capture-pane -p -t scion` | connect + byte stream | Log scraping. Confirms data returns, not just that the call succeeds. |
| **T3d** | `tmux -S <sock> attach -t scion` | **`SCM_RIGHTS` fd passing** | **The one most likely to fail independently.** tmux hands the client's terminal fd to the server over the socket. gVisor may proxy bytes without proxying ancillary data. |
| **T3e** | uid/ownership check | matching uid or a permissive mode | tmux creates `tmux-<uid>/` mode 0700 and refuses sockets it does not own. A uid mismatch between sandbox (1000) and launcher looks like a socket failure and is not one. Record both uids. |

**If T3a–c pass and T3d fails**, that is a good outcome, not a failed test: the three
control operations return to native latency and only interactive attach stays on
§4.4a's `sandbox exec` path. Design becomes a hybrid — and one that is *better than
either* pure option, since attach is the operation least sensitive to per-call
overhead and most sensitive to correctness.

#### Tier B — validate the replacement (**this tier gates P4**)

> **✅ EXECUTED 2026-08-26 by `spike-uds-b`.** T4–T9 and T11 recorded; T10 (30-min idle
> soak) was still running at time of writing. **Verdicts and raw output:
> `ac0-results.md`, Tier B section. Design consequences: §4.4a-rev.** Headline: T5's
> negative control was invalidated — a launcher PTY propagates, so the inner `script`
> wrapper is unnecessary; SIGWINCH still does not; p95 latency 37 ms; and T9 turned up
> a new risk (`sandbox delete --force` hangs and orphans a process), now split out as
> **P4a**. The predicates below are the *pre-registered* ones, kept unedited so the
> results can be read against what was actually predicted rather than against a
> retrofitted expectation.

| ID | Test | Pass predicate |
|---|---|---|
| **T4** | `script` present: `sandbox exec <id> -- /usr/bin/script --version`. **Absolute path required — `PATH` is empty inside a sandbox (§3.2c).** | Exits 0. If absent, add `util-linux` to the omni-image, or find another in-sandbox PTY allocator. |
| **T5** | **Negative control — now the highest-value test in Tier B.** `sandbox exec <id> -- tmux attach -t scion` with a launcher-side PTY. **Run two variants:** **T5a** a PTY allocated programmatically around the CLI (the `creack/pty` shape Scion already uses for Docker); **T5b** ptone's formulation, `script -qfc 'sandbox exec <id> -- tmux attach -t scion' /dev/null`. They should be equivalent; if they differ, the CLI is doing something conditional on `isatty` and that itself is the finding. | Predicted to fail with *"open terminal failed: not a terminal"*. **A pass makes §4.4a's inner wrapper unnecessary and moots much of Tier B** — so do not skip this as obviously broken. |
| **T6** | **The fix:** `sandbox exec <id> -- /usr/bin/script -qfc 'tmux attach -t scion' /dev/null`. | tmux UI renders; keystrokes echo; `C-b d` detaches cleanly leaving the session alive (`has-session` still 0). |
| **T7** | Resize out-of-band: `sandbox exec <id> -- tmux refresh-client -C 120x40`. | `tmux display -p '#{pane_width}'` reports 120. |
| **T8** | Keystroke latency over one persistent exec. | p95 echo round-trip **< 150 ms**. Above that, reconsider Tier C. A number is specified deliberately: "feels live" is how a latency regression ships. |
| **T9** | **Teardown:** `sandbox delete --force` while an exec is attached. | Launcher-side process exits non-zero **promptly**; no orphan, no hang. P4 must not leak a process per killed agent, and this is the likeliest place it would. |
| **T10** | Long-lived stability: exec attached, idle 30 min. | Still responsive. Catches idle-timeout behaviour on the exec channel. |
| **T11** | `sandbox exec -h \| grep -i tty` — undocumented TTY flag? (resolves OQ-6) | Either result is fine; a hit deletes the `script` wrapper. |

#### The stdio↔tty question has two halves, on opposite sides of the boundary

**Raised by ptone, 2026-08-25:** *"Run sandbox from `script` or `expect` and it
connects stdio to a tty… The stdio↔tty thing is inherently straightforward to resolve,
and in theory can be done as a wrapper until we build it."*

Correct on the launcher side, and the distinction is worth stating precisely because
it determines whether a wrapper can be the answer at all.

| Half | Where | Difficulty |
|---|---|---|
| **1. Give the `sandbox` CLI's own stdio a tty** | Launcher | **Trivial, and ptone is right that a wrapper does it.** `script`, `expect`, or in Go `creack/pty` — which `pty_handlers.go` already uses for the Docker path. Not the hard part; arguably already solved. |
| **2. Make the *inner* process see a tty** | Inside the sandbox | ~~**Not fixable by any launcher-side wrapper.** No process on the launcher can create a PTY inside the sandbox's namespaces.~~ **WRONG (T5).** True as stated — the launcher cannot *create* a PTY inside — but it does not have to: `sandbox exec` hands the inner process a reference to the launcher's own PTY fd. The inner process sees a tty without one ever being created inside. My error was assuming "a tty inside" required "a PTY device inside". |

Whether half 2 comes free depends entirely on the CLI's implementation:

- **If `sandbox exec` passes its stdio fds through**, half 1 gives us half 2 for free,
  ptone's wrapper is the whole answer, and `--tty` support is unnecessary.
- **If it terminates stdio into pipes and re-plumbs over a control channel** — which
  is what the `--stdin/--stdout/--stderr` boolean flags hint at — then the inner
  process sees pipes no matter what the launcher does, and the PTY must be allocated
  *inside* (§4.4a's `script -qfc` wrapper).

**This is an empirical question, it is T5, and it is dispatched.** Everything above is
reasoning, and reasoning is what produced the last two wrong answers in this document.

**Corollary worth noticing: T5 and T7 are coupled.** If the fds pass through, then
`TIOCSWINSZ` reaches the inner tty naturally and **the out-of-band
`tmux refresh-client -C` resize hack disappears too** — the same mechanism fixes both.
If they do not, both workarounds are needed. Expect T5 and T7 to agree; if they
disagree, something more interesting is happening.

> **✅ RESULT, 2026-08-26 — the first branch is right, the corollary is wrong, and the
> disagreement is the interesting thing I invited.**
>
> **T5:** `sandbox exec` *does* pass its stdio fds through. ptone's wrapper is the
> whole answer and `--tty` is unnecessary. First bullet confirmed.
>
> **T7:** and yet SIGWINCH does **not** propagate. The out-of-band resize stays.
>
> So T5 and T7 **disagreed**, exactly the case I flagged as "something more
> interesting". The resolution is that I had conflated two things under "the fds pass
> through". What crosses is the **fd reference** — the inner process holds a handle to
> the host PTY, which is why `isatty()` is true. What does not cross is **the terminal
> as an addressable device**: `ttyname()` fails, there is no `/dev/pts/N` inside, and
> nothing inside the sandbox is positioned to receive a `TIOCSWINSZ` or a SIGWINCH
> delivered to the host tty's foreground process group.
>
> **The general form, worth carrying to any future question about this boundary:**
> gVisor's host-fd passthrough transports *fd properties*, not *device identity* and
> not *signal delivery*. Ask which of the three a mechanism depends on. `isatty` needs
> only the first; `ttyname` needs the second; SIGWINCH needs the third. That single
> distinction predicts every Tier B result, including the ones I got wrong.

**One note for the eventual implementation:** in shipping code the launcher half is Go
allocating a pty directly, not shelling out to `script` or `expect`. Those are test
scaffolding and a stopgap, not the design. `expect` in particular is the wrong tool
here — it exists for scripted interaction, whereas all we want is a tty, which is
`script`'s single job.

#### Tier C — only if Tier B fails

| ID | Test | Pass predicate |
|---|---|---|
| **T12** | TCP shim: `socat TCP-LISTEN:<port>,fork UNIX-CONNECT:<tmux.sock>` inside, exposed via `--publish`, launcher connects over TCP. | Interactive attach works. Accepts a listening port and a socat process per agent (§4.4a) — hence last resort. |

#### What each outcome changes

| Outcome | Consequence |
|---|---|
| **T3a–e all pass** | **§4.4-orig is restored as the design.** Revert the three P3 removal commits (tmux mount, `TMUX_TMPDIR`, `TmuxSocket`); §4.4a becomes the documented fallback; most of Tier B is moot; P4 returns to its original shape. |
| **T3a–c pass, T3d fails** | **Hybrid** — control operations over the socket at native latency, attach over `sandbox exec` with the `script` PTY wrapper. Restore the mount and `TMUX_TMPDIR`; keep §4.4a for attach only. Tier B narrows to T4–T9. |
| **T1 fails, T2 passes** | `--host-uds` is `open`-only. §4.4-orig as written is dead; a reverse topology is theoretically available but is a new design — do not adopt it without a fresh proposal. |
| **T1 and T3a both fail** | Original ruling stands after all; §4.4a is the design; Tier B proceeds unchanged. Record the exact failure so the third occurrence of this idea is cheap to dismiss. |
| T5 passes | Drop the `script` wrapper entirely; §4.4a simplifies. |
| T4 fails | Omni-image gains `util-linux`; rebuild required (cheap — the chain rebuilds anyway). |
| T6 fails | **Interactive attach is refused for this tier.** Task delivery and log scraping still work over non-interactive exec (§4.4a). One feature lost, not the capability. Try Tier C first. |
| T8 fails | Tier C, or accept degraded interactivity as a documented tier limitation. |
| T9 fails | P4 gains explicit exec-lifecycle management; treat as a P4 scope increase, not a bug found late. |

### 10b. Empirical test plan — is `Instance.iapEnabled` real? (OQ-15)

**Dispatched as `spike-iap`, 2026-08-26, on ptone's instruction. Brief:
`briefs/spike-iap.md`.** Independent of the P4/P4a/P5 implementation work; its outcome
lands on **P6**, not P4.

> ## ✅ EXECUTED 2026-08-26 — verdict: **LIVE**
>
> | # | Verdict |
> |---|---|
> | **I0** | PASS — accepted and echoed. (`launchStage` normalised to **`GA`** here, not `BETA` as in earlier tests — the Instances surface has moved.) |
> | **I1** | Brand, OAuth client and `iap.googleapis.com` all present — "not configured" is ruled out. |
> | **I2** | **CLOSED, negative — tested 05:50, not inferred.** No `--iap` flag in gcloud 582.0.0, and **no `--no-iap` either**, despite `--public`'s help text referencing it. All three variants are rejected identically to an invented control flag. **REST/PATCH only**; the CLI is a release behind the API, and its help text is a release ahead of itself. See §11.5c. |
> | **I3** | PASS — harness baseline; 403 from invoker IAM, no container log line. |
> | **I4** | PASS — **200, wide open.** Confirms `invokerIamDisabled` is honoured. *This is also the footgun; see §4.9a.* |
> | **I5** | **IAP LIVE** — 302 to `accounts.google.com`, `x-goog-iap-generated-response: true`, **container never reached.** The log-absence discriminator did its job. |
> | **I6** | IAP live; audience analysis — **but the variant that mattered was missing its control.** See OQ-17. |
> | **I8** | **COMPATIBLE** — ES256, `iss=https://cloud.google.com/iap`, standard Google `kid`. Hub's `IAPAuthenticator` should accept it nearly as-is. |
>
> **The "declared-but-unenforced" hypothesis is falsified at the edge** — but read
> §4.9a before concluding there is no footgun. I4 is one.
>
> **Two methodological notes worth carrying forward:**
>
> 1. **The log-absence discriminator was the right call.** I5's evidence is that the
>    container *never logged* the request. A status code alone could not have
>    separated edge-rejection from app-rejection.
> 2. **The surface check earned its keep.** The assertion's `aud` says `services` for
>    an Instance, which looked like the signature of having tested the wrong surface.
>    It was not — the create URL was `…/instances?instanceId=…` — but the ten minutes
>    spent confirming that were cheap against the cost of relaying a wrong result.
>    **The gotcha survives even though the worry did not:** see §4.9a.
>
> **Also: the environment was torn down before the controls were run**, so OQ-17 now
> waits on a multi-region Instance-create outage. Same shape as the T9 delete-defect
> controls. **Hold the environment until someone has reviewed the results** — the
> question that invalidates a run usually arrives after it.

#### 10b.1 The IAP **IAM** surface for an Instance — follow-on findings, 2026-08-26

**Revised twice within the hour. Read the whole subsection; the first two readings were
wrong and are recorded because the way they were wrong is instructive.**

Enforcement being live (I5) says nothing about whether the access *policy* is
manageable. Chased down while standing up the `iap-demo` Instance.

##### The finding: IAP has **no per-Instance IAM resource**

This is the **half-delivered outcome the spike went hunting for, one layer up from where
it was expected.** IAP *enforces* on an Instance at the edge. IAP's *IAM surface* has no
object to hang a per-Instance policy on.

| Path probed with `getIamPolicy` | Result |
|---|---|
| Instance — `iap_web/cloud_run-us-east4/services/iap-demo` | **404 NOT_FOUND** |
| Service — `iap_web/cloud_run-us-central1/services/scion-hub` | **200**, policy with bindings + `etag` |
| Service — `iap_web/cloud_run-us-central1/services/scion-discord` | **200**, empty policy + `etag` |
| Region — `iap_web/cloud_run-us-east4` (no leaf) | **200**, and **accepts bindings** |

Real Cloud Run **Services** have the per-resource object. **Instances do not.** The 404
persists with `roles/iap.admin` held, so it is absence, not authorization.

**Written up as `defect-iap-instance-audience.md`** (same directory), which covers this
gap *and* the audience defect below under one root cause, with four questions for the
Cloud Run team. `setIamPolicy` at the per-Instance path was also verified to 404 —
both halves of the IAM surface are absent, not just the read side.
The headline sentence, which may also cover the invoker question below: **IAP models
Instances as Services in its naming, but the Instance-side machinery does not
reciprocate.**

##### Consequence for P6: the only available scope is too broad

The workaround is a **region-level** binding on `iap_web/cloud_run-{region}`, which does
exist and does accept bindings. Applied for the demo (`domain:google.com` and
`user:ptone@google.com`, read-back confirmed).

**This is not acceptable for production.** A region-level grant admits the holder to
*every* Cloud Run resource in that region, including anything deployed there later by
someone with no knowledge of the hub. The tier cannot ship a security story that reads
"access to the hub is granted to everything in us-east4."

Until per-Instance IAP IAM exists, P6 must choose between: a **dedicated project** for
the hub so that region-level scope is effectively resource-level; the **§4.9a auth-proxy
Service** after all, which *does* have per-resource IAP IAM; or accepting the broad
scope with it documented loudly. **This is a live design decision, not a detail** — it
partially un-does the simplification that the IAP-is-live result appeared to hand us.

##### ⚠️ Project-level `roles/iap.httpsResourceAccessor` inherits, invisibly

A grant at the *project* level admits the holder to every IAP-protected resource in the
project and **does not appear in any resource's own policy**. An operator auditing the
hub finds nothing and concludes nobody has access.

Retrospective consequence: **I6's pass is weaker than it read.** The gym SA held exactly
such a project-level grant, so I6 showed only that *some* grant admitted it. The same
inheritance is why `testIamPermissions` returned `accessViaIAP` on a path that does not
exist — it was evaluating inherited project bindings, not a resource policy. **That
false positive is what sent the first reading of this subsection wrong.** Liveness is
unaffected.

##### The error-code tiers, correctly interpreted

Same path, `getIamPolicy`, three identities:

| Caller | Result | Means |
|---|---|---|
| Owner-ish identity, no IAP perms | **404** | *(originally misread as masking)* |
| Gym SA, `accessViaIAP` only | **403** | insufficient permission — existence **hidden** |
| Gym SA **with `roles/iap.admin`** | **404** | permission sufficient ⇒ **genuinely absent** |

So the tiering is real but the opposite way round from the first reading: **403 hides
existence; 404 from a sufficiently-privileged caller is a true negative.** The lesson is
that a 404 and a 403 from IAP are only interpretable *together with the caller's
permissions* — a single probe from a single identity cannot distinguish absence from
denial, and we asserted a diagnosis from exactly that before running the third probe.

**Also retired:** the hypothesis that gcloud's `--resource-type=cloud-run` builds a path
IAP does not recognise. It does not; the flag was correct throughout. gcloud calls the
IAP API directly with no Cloud Run validation step.

##### ✅ OQ-17 ANSWERED — **NO.** IAP and the invoker check cannot coexist on an Instance

**Root cause isolated to a single variable.** Same SA, same principal, same Instance,
IAP entirely out of the picture (`iapEnabled: false`, `invokerIamDisabled: false`);
**only the token audience varies:**

| Token `aud` | HTTP |
|---|---|
| `/projects/{n}/locations/{region}/**services**/{name}` | **401** |
| `https://{name}-{n}.{region}.run.app` | **200** |
| `/projects/{n}/locations/{region}/**instances**/{name}` | **200** |

The Instance invoker check accepts the **URL** form and the **`instances`** path and
rejects the **`services`** path. IAP's `x-serverless-authorization` emits the `services`
path. That is the mechanism.

**Status-code semantics, measured:** no credential ⇒ **403**; unverifiable credential ⇒
**401**. A `services`-audience token and a garbage token both yield 401, so the token is
read and fails *verification*, not authorization.

**Confounders ruled out:** fresh Instance (not PATCHed from another config), IAM grant
then a 180 s wait, PATCH, full reconcile to `CONDITION_SUCCEEDED`, 30 s buffer, probe at
**5 m 15 s** after the grant. Still 401.

**Two hypotheses this also killed**, both of which had adherents:

- *"Invoker IAM is broken on Instances outright, IAP is a red herring"* (mine). Dead —
  1b and 1c are the successful authenticated invocations nobody had previously
  demonstrated.
- *"`roles/run.invoker` is the wrong grant for an Instance"* (ptone's). Dead —
  `run.instances.invoke` exists and `roles/run.invoker` contains it. Cost one command;
  worth it.

**Ambiguity resolved — it is (A).** A fourth control settled whether the invoker check
*reads* `x-serverless-authorization` at all. Tokens injected directly, IAP off:

| Header carrying the token | `aud` | HTTP |
|---|---|---|
| `x-serverless-authorization` | Instance URL | **200** |
| `Authorization` (control) | Instance URL | **200** |
| `x-serverless-authorization` | **`services`** path | **401** |
| `x-serverless-authorization` | `instances` path | **200** |

The invoker check **does** read `x-serverless-authorization` and honours it when the
audience is right. **The defect is one string in IAP's audience generation.** The
Instance-side machinery is behaving correctly throughout.

*Incidental observation:* an externally-set `x-serverless-authorization` is **not
stripped** at the edge. Not a vulnerability — the token is still signature- and
audience-verified, so forging one buys nothing — but a non-obvious property of the
request path, and it is what made these controls possible.

**Scope the upstream ask narrowly.** The fix belongs in `x-serverless-authorization`
**only**. The `x-goog-iap-jwt-assertion` audience is an app-facing contract that every
IAP-protected application verifies against — ours included (`proxyauth.go` enforces a
mandatory audience) — and the invoker check never reads it. Asking IAP to change both
would be a breaking change to a public contract to fix a problem those apps do not
have.

##### Consequence for S2: IAP becomes the **sole** perimeter, and the footgun is now mandatory

`invokerIamDisabled: true` is **required** under IAP on Instances. Defence-in-depth is
unavailable. The I4 combination (`invokerIamDisabled: true` + IAP not yet enforcing =
**HTTP 200 to an anonymous caller**) therefore stops being an avoidable
misconfiguration and becomes *the supported configuration one reconcile away from
disaster* — and reconcile is **30–75 s**, so the open interval is reachable on every
toggle, not just in a bad final state.

**P6 must treat `iapEnabled` reconcile as a deploy gate**, not a fire-and-forget PATCH:
never expose the URL until IAP is confirmed enforcing (302 + `x-goog-iap-generated-response`),
and never disable the invoker check before that confirmation.

**The fix is narrow and worth saying so upstream:** the invoker check already accepts
`/…/instances/{name}`. IAP emitting that instead of `/…/services/{name}` would resolve
it. Written up as `defect-iap-instance-audience.md`.

**Why test rather than ask again.** §4.9a is built on the relay that Instances have no
direct IAP. The v2 schema nonetheless carries `iapEnabled` and `invokerIamDisabled` on
`GoogleCloudRunV2Instance`. ptone's framing is the sharp one: IAP support needs work on
**two** sides, Cloud Run's and IAP's, and the Cloud Run side may well be done and
shipped while the IAP side is not. That produces a field that is accepted, persisted,
echoed back by `describe`, and **enforces nothing**.

**Three outcomes, and the third is the one the plan is built to detect:**

| Outcome | Design consequence |
|---|---|
| **Live** | §4.9a's auth-proxy service disappears; the hub's existing `IAPAuthenticator` works directly against the Instance. |
| **Inert** | §4.9a stands. Record it so the field is not re-raised in three weeks. |
| **Declared-but-unenforced** | **A security footgun and the most valuable finding available.** An operator sets it, sees it confirmed in `describe`, and believes the Instance is protected when it is open to anything holding `run.invoker`. Reportable to the Cloud Run team like the delete defect. |

**Two methodological traps the plan is shaped around:**

1. **A 403 proves nothing** — invoker IAM already rejects unauthenticated requests
   with IAP nowhere in the picture. Isolate IAP by setting `invokerIamDisabled: true`
   so that any *remaining* rejection is attributable to IAP alone.
2. **A client cannot tell where a request died.** An edge 403 and an app 403 look
   identical to `curl`. **Discriminate on the container's own logs**: the probe
   container logs every request to stdout, so Cloud Logging answers "did this reach
   the container at all?" In the IAP-live case the *absence* of a log line is the
   positive evidence. This is the load-bearing part of the harness.

| # | `iapEnabled` | `invokerIamDisabled` | Probe | Reads as |
|---|---|---|---|---|
| **I0** | true | — | `validateOnly=true` POST | Accepted? **Echoed back?** (`sandboxLauncher`'s echo was our first real evidence.) |
| **I1** | — | — | `gcloud iap oauth-brands list` | Is there an IAP resource to attach to? Distinguishes *not implemented* from *not configured* — different answers. |
| **I2** | — | — | grep `iap` in `create`/`update`/`deploy --help`, alpha and beta | Surface check. **Probe the specific flag; do not scan a listing** — we have made that error twice. |
| **I3** | false | false | unauthenticated GET | **Harness baseline.** Expect rejection *and no log line*. Proves the discriminator works. |
| **I4** | false | true | unauthenticated GET | **Open baseline.** Expect 200 + log line. If not, `invokerIamDisabled` is itself inert — which matters to S2. |
| **I5** | **true** | **true** | unauthenticated GET | **The headline.** Open ⇒ IAP inert *and* the "IAP on / invoker off" composition is a fully open hub. Sign-in bounce ⇒ live. |
| **I6** | true | false | GET with OIDC audienced to the Instance URL | Does `X-Goog-IAP-JWT-Assertion` arrive? Capture **decoded claims**, never the raw token. |
| **I7** | true | * | real IAP browser flow | Only if I5/I6 indicate liveness. |
| **I8** | — | — | validate the assertion against `proxyauth.go`'s expectations | **Do not skip if live.** The header arriving ≠ our hub accepting it: ES256-only, JWKS `kid`, `iss=https://cloud.google.com/iap`, mandatory audience, 30 s skew. Cloud Run IAP's audience format may differ from the GCE/GKE format the hub assumes — a mismatch found now is far cheaper than one found in P6. |

**I5 is why the spike exists.** "IAP on, invoker off" is not a perverse configuration —
it is exactly what an operator sets if they believe IAP replaces invoker IAM as the
perimeter.

### AC-0 — Day-one spike (do this before P4 is planned)

- [x] ~~A sandbox is created with `--rootfs /` and a bind-mounted directory.~~ Done.
- [x] ❌ ~~tmux socket across the sandbox bind mount~~ — **ANSWERED 2026-08-25 and the
      answer is NO.** Cloud Run platform team: *"This won't work. We would need to run
      runsc/gvisor with `--host-uds` enabled which we don't."* §4.4 is dead, §4.4a
      replaces it, P4 is re-scoped. The first spike's `unshare` PASS was measuring a
      different mechanism. ~~**No test needed; do not spend a spike on it.**~~
      **Revised 2026-08-25 — see §10a.** Still not a *blocker*, but "no test" was the
      wrong call: the value of testing a settled negative is a tripwire and a
      characterized failure mode, not a second opinion. Tier A of §10a.
- [ ] **NEW — `sandbox exec` PTY trick (§4.4a).** Does
      `sandbox exec <id> -- script -qfc 'tmux attach -t scion' /dev/null` yield a
      working interactive terminal? **Now the item that gates P4's attach feature.**
      Expect a bare `tmux attach` to fail with *"open terminal failed: not a
      terminal"* — that failure is the confirmation that the PTY must be allocated
      inside, not a bug to debug.
- [ ] **NEW — is `util-linux` (for `script`) in the omni-image?** Near-certain, but
      §4.4a depends on it.
- [ ] **NEW — resize via `tmux refresh-client -C <W>x<H>` over `sandbox exec`**
      (§4.4a). SIGWINCH does not cross pipes, so the existing resize path is inert.
- [ ] **NEW — measure keystroke latency through `sandbox exec`.** §4.4 avoided
      putting the CLI in the interactive path deliberately; §4.4a cannot. One
      persistent exec, not per-keystroke spawns — but confirm it feels live.
- [ ] `curl` from inside a sandbox to the launcher's `localhost:8080` — resolves
      OQ-2.
- [ ] `sandbox exec` with a TTY — resolves OQ-6.
- [ ] **`sandbox run`'s positional binds to the sandbox *name*, not the command**
  (§4.3b). If it binds to the command, `--sandbox-name` must be accepted on `run`,
  or identity comes from parsing output — which changes §4.5's state store keying.
- [ ] **`sandbox wait` yields the child's exit code** (§4.3d) — via its own exit
  status or stdout. If it does not, this runtime cannot populate `ExitCode` for
  SIGKILL/OOM deaths and that must be documented as a tier limitation.
- [ ] **`--allow-egress` reaches public inference endpoints**, and confirm ADC /
  Vertex is genuinely unavailable (§4.3c) rather than merely undocumented.
- [ ] **`--write` actually makes bind mounts writable** and `agent-info.json` is
  written from inside the sandbox. If not, the self-reported status path (§9.1) is
  dead and the runtime must supply Phase/Activity itself.
- [ ] **Writes to an *inherited* (`--rootfs /`, not bind-mounted) path are invisible
  to the launcher** — confirming §3.2a. Write a file inside the sandbox to a path
  that exists only via rootfs inheritance, and check the launcher does *not* see it.
  This check expects to *not* see the write; the point is to prove the mount list
  must be explicit, rather than leaving P3 to discover it by debugging.
- [ ] **Is the read-only rootfs view live, or a launch-time snapshot?** Write a file
  from the launcher *after* the sandbox starts, to an inherited path, and read it
  from inside. If it is a snapshot, token refresh via `<agentHome>/.scion/scion-token`
  stops working after the first rotation and P7 changes shape (§3.2a).
- [x] ~~**Is `--mount` repeatable?**~~ **YES** (re-test 2026-08-25). Two mounts, both
  present with correct content. Help text's `string` type is a rendering artefact.
  **Triggered the §3.2b-r reversal** — see it before implementing mounts.
- [x] ~~**Is `-e`/`--env` repeatable?**~~ **YES.** `FOO=bar` and `BAZ=qux` both set.
- [x] ~~**Which mount key syntax parses?**~~ **Both.** `source=`/`destination=` and
  `src=`/`dst=` are accepted. Use the former; it matches `-h`.
- [x] ~~**Is the sandbox binary injected by `sandboxLauncher: true`?**~~ **YES** —
  `/usr/local/gcp/bin/sandbox`, 55 MB, 2026-08-04 (§3.2z). Sandboxes launched,
  `exec`'d, and deleted on a real Instance.
- [x] ~~**Writes to an inherited path are invisible to the launcher.**~~ **Confirmed**
  — a sandbox write to `/tmp` was absent from the launcher. §3.2a holds; the mount
  list is mandatory.
- [x] ~~**Positional binds to the sandbox name.**~~ **Confirmed** — `run <name>` then
  `exec <name>` addresses it. §4.5's state-store keying stands.
- [ ] **Does an `--env` *value* containing `=` or `,` survive parsing?** (§3.2c).
  Low priority: values we control don't exercise it. Only matters if arbitrary agent
  env is ever forwarded.
- [ ] `sandbox run` allocates a TTY (or tmux tolerates its absence). The Docker
      path passes `run -d -i -t` (`common.go:104`, `docker.go:75`) specifically
      because tmux needs it.
- [ ] **Measure the pre-SIGKILL window of `sandbox delete`** on a sandbox with a
      SIGTERM trap. Undocumented and now load-bearing — it sets `SCION_GRACE_PERIOD`
      (§4.3).
- [ ] **A sandbox exceeding its memory limit is killed at the *sandbox* boundary,
      not by OOM-killing the Instance** (§8.1). The limit only counts if enforced in
      the right place.
- [ ] **`hostname` on an Instance, across a redeploy** — is it stable? Determines
      whether `hub_id` fallback is survivable or must always be pinned (§4.6 row 4).
- [ ] **Check for duplicate Cloud Logging ingestion** — stdout is captured by the
      platform, but with `K_SERVICE` unset Scion also stands up its own client
      (§4.6 row 5).

*Resolved before the spike by ptone: OQ-3 (inference endpoints reachable), OQ-4
(limits supported), OQ-5 (keep the sibling), OQ-7 (`K_SERVICE` unset), OQ-9
(`--force`, no grace).*

### Functional

- [ ] One deploy command against a clean GCP project yields a reachable hub.
- [ ] Login works with no pre-existing DNS, hostname, or certificate setup.

**Build integrity of the deployed image (§11.5e).** These three are new, and they exist
because each one was a defect found by running the image rather than by reading it. Assert
them against the **published artifact**, not a local build — the whole class of defect here
is "the thing we shipped is not the thing we compiled."

- [ ] **The deployed binary reports `AssetsEmbedded = true` and the hub serves the
      frontend.** Negative check that would have caught this: the startup log must **not**
      contain `built without web assets`. A `200` on `/` is not sufficient evidence — the
      API answers there too.
- [ ] **`scion version` in the deployed image reports a non-empty `Commit` and
      `BuildTime`,** and the commit matches the image tag. Guards the `GIT_COMMIT`
      build-arg gap in `lib/targets.sh step_build_args`.
- [ ] **The image's own `CMD`, unmodified, leaves a process in the foreground.** Deploy
      with no `--command`/`--args` override and assert the Instance reaches
      `Running = True` and *stays* there. An Instance that reports
      `Instance completed successfully` has failed this check, however healthy the exit
      code looks.
**Sandbox launch integrity (§11.5g).** These exist because a sandbox that never started
reported success for two hours. Every one of them is a negative check, deliberately.

- [ ] **Every argv[0] handed to the `sandbox` CLI is an absolute path.** Unit-assert
      `strings.HasPrefix(entrypoint[0], "/")` on `buildEntrypoint`'s output, and grep the
      package for `sandbox` invocations whose first payload token is a bare name. A bare
      name does not degrade — the sandbox dies instantly and silently.
- [ ] **A sandbox is not reported `running` until an affirmative probe succeeds.**
      `sandbox exec <id> -- true` must return 0 before the hub shows the agent as running.
      Negative check: `sandbox run --detach` returning 0 must **not**, on its own, be
      sufficient — it returns 0 for sandboxes that are already dead.
- [ ] **A sandbox that dies immediately after launch surfaces as a failed start, not as a
      running agent.** Test by launching one with a deliberately bad argv[0] and asserting
      the hub reports failure. This is the exact shape that hid the defect.

**Sandbox entrypoint integrity (§11.5h).** Three separate fatal defects hid behind each other
here. These checks exist so the next one cannot.

- [ ] **The shipped image contains every binary the entrypoint invokes.** Assert at build or
      startup that `tmux` and `sciontool` are present *in the artifact*, not in a Dockerfile.
      The step-5 root cause was a missing tmux in an image whose build chain excluded the layer
      that installs it, while a grep of the repo said otherwise for months.
- [ ] **Nothing in the entrypoint writes to the rootfs.** The rootfs is not writable, as root,
      and there is no overlay. Negative check: the `rm -rf /home/scion && ln -sfn …` chain is
      gone, and the agent home reaches `/home/scion` by bind mount instead.
- [ ] **PID 1 outlives the tmux session and exits when it ends.** Two directions, both required:
      the sandbox must still be alive well after launch, *and* it must stop on its own when the
      session is killed. A fix that only satisfies the first leaks live sandboxes silently.
- [ ] **A failed entrypoint leaves a readable artifact.** After a sandbox dies, its stdout,
      stderr and exit code must be recoverable from the launcher, and the error returned to the
      operator must include them. Test by shipping a deliberately broken entrypoint and
      asserting the operator-visible error names the cause.

- [ ] Creating a project clones its workspace; the files are visible in the hub UI
      workspace browser (i.e. `workspaceWriteBlocked` does not misfire — §4.6).
- [ ] Starting an agent creates exactly one sandbox; `List` reflects it; the hub UI
      shows it running.
- [ ] The agent's home contains its staged harness config, skills, and
      `agent-info.json` — confirming the bind-mount delivery of §3.2.
- [ ] Attaching from the browser gives a working terminal, with resize.
- [ ] Sending a task via the hub reaches the harness (`tmux send-keys` path).
- [ ] The agent commits and pushes to a git remote.
- [ ] Stopping an agent deletes the sandbox and leaves no orphan state record.
- [ ] Auth token refresh succeeds end-to-end (file write + `kill -USR2 1` via
      `sandbox exec`).

### Non-regression

- [ ] Docker, Podman, and K8s runtimes are byte-for-byte unaffected — the §4.6
      discriminator changes nothing on a machine without the sandbox binary.
- [ ] Existing `settings.yaml` files still validate against the amended schema.
- [ ] `requireImageRegistryForBroker` still gates the runtimes it gated before, and
      does not gate `cloudrun-sandbox`.

### Security (blocking)

- [ ] The hub **refuses to start** with dev auth enabled on a non-loopback bind.
- [ ] A fresh deploy is **not** anonymously accessible — verified by an unauthenticated
      `curl` to the `run.app` URL from outside the project.
- [ ] Agent containers do not receive broker operator credentials
      (`harness/auth.go:50-58` isolation holds under the new runtime).

**IAP-specific — added 2026-08-26 after `spike-iap`. These belong to P6.**

- [x] ~~**The one measurement OQ-17 could not take.**~~ **TAKEN — and the answer is NO**
      (§10b.1). IAP and the invoker check **cannot** coexist on an Instance: IAP emits a
      `services`-path audience that the Instance invoker check rejects. The
      documentation-derived answer, which came from Cloud Run *Services*, was wrong for
      *Instances*. **S2 and the deploy defaults therefore do need rethinking**, exactly
      as this item warned.
- [ ] **`invokerIamDisabled: true` is mandatory under IAP, so IAP is the sole
      perimeter — and reconcile is a deploy gate, not a PATCH.** The deploy must not
      publish the URL, and must not disable the invoker check, until IAP is *confirmed
      enforcing* (302 carrying `x-goog-iap-generated-response`). Ordering matters more
      than final state: the 30–75 s reconcile window is a fully-open interval on every
      toggle. Verify the gate by attempting the unsafe ordering and confirming the
      tooling refuses.
- [ ] **The coupled-pair check.** `invokerIamDisabled: true` + `iapEnabled: false`
      returns **HTTP 200 to an anonymous caller** (measured, I4). Deploy tooling must
      **refuse** that combination outright, not merely avoid emitting it, and must
      apply the two fields in an order that never transits through it. Reconciliation
      takes 30–75 s, so the unsafe window is reachable during a toggle, not only in a
      bad final state.
- [ ] **Audience is constructed as `/projects/{number}/locations/{region}/services/{name}`
      — `services`, even for an Instance.** Verified. A plausible-looking
      `…/instances/{name}` yields an opaque audience-validation failure. Assert the
      constructed string in a test, with a comment saying why it looks wrong.
- [ ] **The IAP access-policy scope is a named, deliberate decision — not whatever
      worked.** There is **no per-Instance IAP IAM resource** (§10b.1): the only scopes
      available are **region-level** (`iap_web/cloud_run-{region}`) and **project-level**,
      both of which grant hub access far beyond the hub. P6 must pick one of: a
      dedicated project for the hub, the §4.9a auth-proxy Service (which *does* have
      per-resource IAP IAM), or the broad scope with a release-note warning. **Whichever
      is chosen must be stated in the deploy docs**, because the failure mode is silent.
- [ ] **Project-level `roles/iap.httpsResourceAccessor` anywhere in the project is a
      hub-access grant, and is invisible in any resource's own policy.** Deploy
      verification must read the *effective* access, not the resource policy — an empty
      resource policy does not mean nobody has access (§10b.1).
- [ ] **`setIamPolicy` replaces the whole policy.** The deploy path must
      `getIamPolicy` → merge → `setIamPolicy` **with the returned `etag`**, never
      write a bare binding. A naive write silently removes every existing grant, which
      on a shared project is an availability incident with no error to notice.
- [ ] **Operator-facing note in the docs:** IAP's `403` and `404` are only
      interpretable alongside the caller's permissions — **403 hides whether the
      resource exists; 404 from a caller holding `iap.admin` means it genuinely does
      not.** A single probe from a single identity cannot tell absence from denial. We
      asserted a wrong diagnosis from exactly that mistake (§10b.1); the docs should
      spare the next person.

### Documented limitations (must appear in release notes, not just code)

- [ ] Per-agent image/template selection is unavailable until Templated Sandboxes
      ship (§4.2).
- [ ] Per-agent resource limits are unavailable if OQ-4 resolves negatively (§8).
- [ ] Whatever is lost on redeploy, per the OQ-1 decision (§5.1).
- [ ] **Agents cannot be network-isolated on this tier.** `--allow-egress` is mandatory
      for a sandbox to reach its launcher *and* it is all-or-nothing — measured, §11.10.
      There is no configuration in which an agent talks to the hub but not the internet.
      An operator who expects "sandboxed" to mean "no internet" must be told otherwise
      **before** they run an untrusted agent, not after.
- [ ] **Any sandbox on the Instance can mint the runtime service account's token**
      (S5, §4.11). Sandbox isolation does not imply credential isolation here.
- [ ] **Tools that hardcode `169.254.169.254` and ignore `GCE_METADATA_HOST` will fail
      outright**, not degrade — gVisor has no `nat` table, so there is no interception
      fallback (§4.10, §11.12). Affects bare `curl` metadata calls and older SDKs.
- [ ] The region-scoped IAP grant admits its holder to **every** Cloud Run resource in
      the region, not just this Instance (§11.2, D1).

---

## 11. P6 — Authentication and one-command deploy

**Written 2026-08-26 after ptone's decision on the access-policy scope.** This section
supersedes the "live design decision" left open at the end of §10b.1.

> **Decision (ptone, 2026-08-26 03:04 UTC): region-level IAP scope.**
> *"go for region-level IAP scope."*
>
> **The reasoning, so it can be re-examined rather than inherited.** §10b.1 concluded
> that a region-level binding was "not acceptable for production" because it admits the
> holder to *every* Cloud Run resource in the region. That judgement was made without
> weighting the **deployment model**, and weighting it changes the answer: this tier
> deploys into **the operator's own GCP project**. Where that project holds one Scion
> Instance, a region-level grant is *operationally identical* to a per-Instance grant —
> the set of resources it admits you to has exactly one member. The breadth only bites
> under multi-tenancy in a shared project, which Tier 0 explicitly does not do (§5).
>
> **⚠️ Revisit trigger — the single condition that invalidates this.** If this tier ever
> hosts **more than one tenant in one project**, region scope is immediately wrong and
> the §4.9a auth-proxy Service must come back, because a Cloud Run *Service* does have
> per-resource IAP IAM. Anyone proposing multi-tenancy must reopen this section first.

### 11.1 The perimeter — three candidate gates, one survivor

| Gate | Status | Why |
|---|---|---|
| **Cloud Run invoker IAM** | ❌ **Must be OFF** | Not a choice. OQ-17: IAP's `x-serverless-authorization` carries a `services`-path audience the Instance invoker check rejects → 401. `invokerIamDisabled: true` is mandatory. |
| **IAP** | ✅ **The sole network perimeter** | Edge-enforced, verified on genuine Instances (§10b). |
| **Hub session auth** | ✅ Unchanged | The application-layer gate, behind the perimeter. Independent of the above. |

**The consequence is a single point of failure, and it must be designed for rather than
noted.** With the invoker check off, `iapEnabled: false` — set by accident, by a
`gcloud` command that omits it, or by a PATCH that drops the field — leaves the Instance
**open to the internet** with nothing but hub session auth in front of it. This is the
I4 open-config footgun, and IAP-being-live did not remove it; it *relocated* it. Under
this design the footgun is now the **supported configuration**, which raises the bar for
guarding it. See the deploy gate in §11.5 and S2-rev in §11.7.

### 11.2 The IAP access policy

```
resource:  projects/{PROJECT_NUMBER}/iap_web/cloud_run-{REGION}
role:      roles/iap.httpsResourceAccessor
member:    the operator (and anyone they choose)
```

There is **no per-Instance path** — `iap_web/cloud_run-{region}/services/{instance}`
returns 404 for both `getIamPolicy` and `setIamPolicy`, with `roles/iap.admin` held
(§10b.1). Do not spend time looking for one; it is absent, not hidden.

**⚠️ The audit hazard, which the deploy tooling must actively counter.** A
`roles/iap.httpsResourceAccessor` grant at the **project** level admits its holder to
every IAP-protected resource in the project and **does not appear in any resource's own
policy** (§10b.1). An operator auditing the hub's policy sees nothing and concludes
nobody has access. Therefore the deploy command must **report effective access, not
merely set it** — it should read back the project-level *and* region-level bindings and
print the union. A tool that only prints what it wrote will actively mislead.

### 11.3 Identity flow, end to end

```
browser ──▶ IAP edge ──▶ Instance ──▶ launcher container ──▶ hub
             │             │
             │             └── invoker check DISABLED (mandatory, §11.1)
             └── Google login; 302 to accounts.google.com if unauthenticated
```

Headers arriving at the launcher:

| Header | Use |
|---|---|
| `x-goog-iap-jwt-assertion` | **The credential.** ES256, `iss=https://cloud.google.com/iap`. Signature-verified by `IAPAuthenticator` in `pkg/hub/proxyauth.go`. |
| `x-goog-authenticated-user-email` / `-id` | Convenience only. **Never trust these alone** — they are unsigned. The assertion is the credential. |
| `x-serverless-authorization` | **Ignore.** Present, and *not* stripped at the edge for external callers. Not a vulnerability (still signature- and audience-verified), but it is not our credential and nothing should read it. |

**The audience — the one value most likely to be "fixed" into a breakage:**

```
/projects/{PROJECT_NUMBER}/locations/{REGION}/services/{INSTANCE_NAME}
```

Note **`services`**, for an object that is an **Instance**. This is IAP's fixed resource
vocabulary across every backend type, not a bug and not a mismatch to correct. A future
reader who changes it to `instances` will produce an audience mismatch on every request,
and the resulting 401 will not obviously point back here. **§11.6 lists the symptom.**

### 11.3a Session expiry and WebSockets — IAP's documented behaviour, and what it costs us

The terminal attach (§1 step 5) is a WebSocket upgrade through the IAP edge, and I listed
it as one of the two steps most likely to fail. **Google documents the behaviour, so it
does not need to be discovered by a failing demo.** Three statements from
`iap/docs/sessions-howto`, each with a consequence:

**1. *"IAP only supports WebSocket for initial requests and doesn't continuously check
authorization."*** The upgrade is supported — **step 5's headline risk is largely
retired**, and the walk should treat a failure there as a bug in our code rather than a
platform limit. But read the second clause as the security statement it is:

> **S6 — an established terminal WebSocket is never re-authorized.** Revoking an
> operator's IAP access does **not** close their open terminals; the connection outlives
> the grant, for as long as it stays open, on a session attached to a live agent shell.
> **The hub must enforce its own session lifetime on long-lived connections** rather than
> inheriting one from IAP, because IAP does not provide one. Not a first-deploy blocker on
> a single-operator tier; **it is a release-note limitation and it is the second S-item
> (with S5) that the multi-tenancy trigger must revisit.**

**2. Expired sessions fail differently for XHR than for navigation.** Non-AJAX requests get
a redirect to the login flow, transparent if the user is still signed in. **AJAX requests
get `401`.** The hub's UI is XHR-driven, so the operator's experience of an expired session
is not a login page — it is **an interface that starts returning 401 with no path to
recovery**. The documented remedy is IAP's own handler, `?gcp-iap-mode=DO_SESSION_REFRESH`,
opened in a hidden window.

> **P6 requirement: the frontend must handle `401` from the IAP edge distinctly from `401`
> from the hub.** They mean different things — *your session expired, refresh it* versus
> *you are not authorized*. Conflating them produces the §11.6 failure mode we already
> rejected: an operator who is logged in being told they lack permission.

**3. For Google-account logins the IAP session tracks the underlying Google session** and
is not governed by a JWT `exp`. So session lifetime is **not ours to configure** on this
tier, which is another reason (1) has to be handled in our code.

**None of this is measured yet** — it is documented behaviour, which is weaker evidence
than the measurements elsewhere in this section. The walk should confirm (1) empirically
and can defer (2) to an explicit expiry test.

### 11.4 What P6 actually has to build — much less than expected

Auditing `scion/dev-rebase-1294` before designing, rather than after: **the hub's IAP
verification already exists and already accepts the Instance's audience.** P6 is
substantially a *configuration* phase, not an implementation one.

| Component | State | P6 work |
|---|---|---|
| `hub.IAPAuthenticator` (`pkg/hub/proxyauth.go`) | Exists. Verifies ES256 via JWKS, checks `iss`, mandatory `aud`, `exp`/`iat` with 30 s skew, strips the `accounts.google.com:` prefix. | **None.** |
| Wiring (`cmd/server_foreground.go:1689`, `:2229`) | Exists, behind `auth.mode=proxy`, `auth.proxy.provider=iap`. Fails closed if the audience is empty. | **None.** |
| `isSupportedIAPAudience` | Accepts `/projects/<n>/locations/<r>/services/<s>` — **the Instance form passes unchanged**, because the `services` vocabulary happens to align. | **None.** Add a test pinning this, so nobody "tidies" it. |
| `iapAudienceToCloudRunURL` | Derives `https://{name}-{number}.{region}.run.app`. The OQ-17 control confirmed Instance URLs use exactly this legacy format (that URL returned 200). | **Verify** on a live Instance; fall back to `SCION_SERVER_BASE_URL` if the format ever differs. |
| Config plumbing | `auth.proxy.iap.audience` already in the schema. | Set it at deploy time. |

**This is the most useful thing in this section:** the expensive-looking half of P6 is
already built and was built for the GKE tier. What remains is the deploy command, the
admin bootstrap, and the guard rails.

### 11.5 The deploy command

One command, per §1. Ordered, because two of the steps are gates rather than steps:

1. **Resolve identity.** `gcloud config get account` → the deploying operator's email.
   Used for both the IAP binding and the admin seed (§11.6).
2. **Resolve project number.** Required for the audience; the *number*, not the ID.
3. **Create the Instance** with, at minimum:
   `sandboxLauncher: true`, `iapEnabled: true`, **`invokerIamDisabled: true`**.
4. **⛔ Gate — wait for IAP reconcile.** 30–75 s observed. Poll until the edge actually
   302s to `accounts.google.com`. **Do not proceed on the create response**; the API
   returns before enforcement is live, and a deploy that reports success while the
   Instance is briefly open is precisely the failure this tier cannot have.
5. **Bind the access policy** (§11.2) for the operator.
6. **Read back and print effective access** — project-level *and* region-level, per the
   audit hazard in §11.2.
7. **⛔ Gate — assert the perimeter.** Fetch the URL with **no credential** and require a
   302 to `accounts.google.com` carrying `x-goog-iap-generated-response: true`. **Fail
   the deploy loudly if the app answers.** This is the guard for §11.1's single point of
   failure and it is cheap; it is the difference between "we configured IAP" and "IAP is
   enforcing."
8. **Print the URL.**

**Idempotency:** re-running must converge, not duplicate. Steps 5–7 are safe to repeat;
step 3 becomes a PATCH when the Instance exists — and the PATCH **must** carry
`iapEnabled` and `invokerIamDisabled` explicitly, since a PATCH that omits them is how
the perimeter gets silently dropped.

#### 11.5a Transport for step 3 — measured, gcloud 582.0.0

`gcloud beta run instances deploy` gives us **two of the three fields and not the third**:

| Field | gcloud flag | Available? |
|---|---|---|
| `sandboxLauncher` | `--[no-]sandbox-launcher` | ✅ in SYNOPSIS |
| `invokerIamDisabled` | `--[no-]invoker-iam-check` | ✅ in SYNOPSIS |
| `iapEnabled` | *(none)* | ❌ **absent** |

`--iap` has no positive form on this verb. It appears in the help text only as prose
under `--public` ("equivalent to `--no-invoker-iam-check` and `--no-iap`"). Probed live:

```
$ gcloud beta run instances deploy … --iap --sandbox-launcher --no-invoker-iam-check
ERROR: (gcloud.beta.run.instances.deploy) unrecognized arguments: --iap (did you mean '--image'?)
```

#### 11.5b The sequence — measured end to end, 2026-08-26 03:46–03:49 UTC

**My first instinct here was wrong and I am replacing it.** I initially ruled out a
gcloud + REST hybrid on the grounds that it opens a window where the Instance serves
with no IAP. I then measured it on a throwaway Instance (`arch-idem-probe`), and the
window does not exist if the steps are ordered correctly. All four results below are
measured, not reasoned:

| # | Test | Result |
|---|---|---|
| 1 | REST `PATCH ?updateMask=iapEnabled,invokerIamDisabled` with both in one body | ✅ **Both applied.** One operation, one generation bump. |
| 2 | `gcloud beta run instances deploy` with a *new* image onto that Instance | ✅ Image changed (`hello` → `nginx:alpine`), generation 3 |
| 3 | Did that gcloud update **drop the perimeter**? | ✅ **No.** `iapEnabled: true` and `invokerIamDisabled: true` both survived — gcloud read-modify-writes, it does not blind-replace. |
| 4 | Unauthenticated fetch of the resulting URL | ✅ **302 to `accounts.google.com`** carrying `x-goog-iap-generated-response: true` — i.e. §11.5 step 7 passes |

**The ordering insight that dissolves the window.** The dangerous state is *invoker check
off while IAP is not yet on* — that, and only that, is an open Instance. So:

1. **Create with the invoker IAM check left ON** (its default). The Instance is born
   closed, protected by invoker IAM, with no IAP yet. This is a safe resting state.
2. **One PATCH sets `iapEnabled: true` and `invokerIamDisabled: true` together.** The
   perimeter hands over from invoker IAM to IAP in a single operation.
3. Gate on reconcile (§11.5 step 4), then assert (step 7).

At no point in that sequence is the Instance open. **The invariant to hold onto is not
"one transport" — it is: never send `invokerIamDisabled: true` in a body that does not
also carry `iapEnabled: true`.** That is the rule; the transport is free.

**So the hybrid is permitted, and preferred.** Use `gcloud … instances deploy` for the
things it marshals well — image, env, volumes, service account, sandbox launcher — and
REST for the two perimeter booleans it cannot express (§11.5a). This is strictly more
ergonomic than hand-rolling a full REST body for every field, and result 3 shows it is
safe on the update path, which is the path an operator hits most often.

**Idempotency, re-derived:** re-running is safe. gcloud's update preserves the perimeter
(result 3), and the PATCH is a no-op when the values already match. The earlier claim
that a partial PATCH "silently drops the perimeter" is **only true of a body that names
`invokerIamDisabled` without `iapEnabled`** — which the invariant above forbids.

#### 11.5c v1 and v2 model this resource differently — the settled transport

Probed 03:51 UTC, and this is what actually decides the transport:

| Field | REST **v2** | gcloud (`apiVersion: run.googleapis.com/**v1**`) |
|---|---|---|
| `iapEnabled` | ✅ settable at **create** and via PATCH | ❌ no `--iap` flag (§11.5a) |
| `invokerIamDisabled` | ✅ settable at create and via PATCH | ✅ `--[no-]invoker-iam-check` |
| `sandboxLauncher` | ❌ **does not exist** | ✅ `--[no-]sandbox-launcher` |

> **The `--iap` cell was an inference until 05:50, when I tested it. It holds — and the
> reason it needed testing is instructive.** `--public`'s own help text reads *"Equivalent
> to setting `--no-invoker-iam-check` and `--no-iap`"*, which strongly implies a hidden
> `--iap`/`--no-iap` pair that simply isn't in the synopsis. If that pair existed, the
> whole v1/v2 hybrid below would collapse into a single command, so it was worth the two
> minutes.
>
> **It does not exist.** Parser probes against `gcloud beta run instances deploy`, each
> failing before any API call:
>
> ```
> --iap          → unrecognized arguments: --iap (did you mean '--image'?)
> --no-iap       → unrecognized arguments: --no-iap
> --enable-iap   → unrecognized arguments: --enable-iap
> --bogus-flag   → unrecognized arguments: --bogus-flag     (control)
> ```
>
> `--iap` is rejected **identically to a flag I invented**, which is the cleanest possible
> negative. Note the second line especially: **`--no-iap` does not exist either.** gcloud's
> help text refers to a flag the command does not implement — an upstream documentation
> bug, and precisely the kind that costs an implementer an hour of hunting. Worth reporting
> upstream alongside the `sandboxLauncher` asymmetry.
>
> **Consequence: the hybrid is not avoidable by waiting for a flag we already have.** It is
> avoidable only when gcloud catches up to the v2 surface, which is a separate release.

`POST` to v2 carrying `sandboxLauncher` returns
`400 Unknown name "sandboxLauncher" at 'instance': Cannot find field`. Worse, a **GET**
of `val-delete-2` — which has it set — **does not return the key at all**. v2 does not
merely refuse to write it; it silently omits it on read.

**Neither surface alone can express this tier's Instance.** v2 cannot set the sandbox
launcher; v1 cannot turn IAP on. The hybrid is therefore *required*, not a preference:

1. **gcloud create** with `--sandbox-launcher`, invoker check left ON → born closed.
2. **REST v2 PATCH** `updateMask=iapEnabled,invokerIamDisabled`, both in one body →
   perimeter transfers atomically from invoker IAM to IAP.
3. **gcloud** for subsequent container/image updates (preserves both perimeter fields —
   §11.5b result 3).

⚠️ **Standing hazard: anything that round-trips an Instance through v2 drops
`sandboxLauncher` without complaint.** A read-modify-write built on v2 will quietly
produce an Instance that cannot launch sandboxes, and nothing will error. This is the
same failure family as the perimeter-drop in §11.5b and deserves the same treatment — an
explicit comment at every v2 call site saying which fields that surface cannot see.

⚠️ **One unresolved observation.** `spike-oq2` reported that `PATCH ?updateMask=containers`
returns success without changing the container command. I did not reproduce that path —
gcloud's own update *does* change the image (result 2), so the API is clearly capable of
it, and the likeliest explanation is mask syntax or body shape for a repeated field
rather than an API limitation. **Do not hand-roll container updates over REST until
someone has established the correct spelling; use gcloud for containers.**

### 11.5d The env-var contract — the interface nobody wrote down

`deploy-instance` configures the hub entirely through four `--set-env-vars` entries. That
makes those four strings the **actual interface** between the deploy command and the
running hub, and until now it existed only as a `fmt.Sprintf` in `diGcloudDeploy`. Traced
end to end, 05:35, so the next reader does not re-trace it:

| Env var | koanf key | Struct field | Verified |
|---|---|---|---|
| `SCION_SERVER_AUTH_MODE=proxy` | `auth.mode` | `DevAuthConfig.Mode` | ✅ |
| `SCION_SERVER_AUTH_PROXY_PROVIDER=iap` | `auth.proxy.provider` | `ProxyAuthConfig.Provider` | ✅ |
| `SCION_SERVER_AUTH_PROXY_IAP_AUDIENCE=…` | `auth.proxy.iap.audience` | `IAPAuthConfig.Audience` | ✅ |
| `SCION_SERVER_HUB_ADMINEMAILS=…` | `hub.adminEmails` | `HubServerConfig.AdminEmails` | ⚠️ works, deprecated |

The mapping is mechanical: `envKeyToConfigKey` (`hub_config.go:976`) strips the prefix,
lowercases, splits on `_`, and rewrites known compound segments via `camelCaseFields`
(`adminemails` → `adminEmails`). Each result matches its `koanf:` tag exactly.

> **⚠️ The one real gap: that path is never tested with an actual environment variable.**
> `Auth.Proxy` and `Proxy.IAP` are **pointer** fields. Every existing test —
> `server_foreground_test.go:164`, `helm_chart_ha_contract_test.go:128` — constructs
> `cfg.Auth.Proxy.IAP` **directly in Go**, and the helm test even allocates the pointer by
> hand when it finds it nil. **Nothing exercises env → koanf → nested nil pointer.** If
> mapstructure does not allocate, the audience is empty and the hub **fails closed at
> startup** (§11.4) — visible rather than silent, but the Instance never serves.
>
> **Required: a round-trip unit test** that sets the four vars as `diGcloudDeploy` formats
> them and asserts the four struct fields. Milliseconds to run; it pins the only unverified
> link between "deploy succeeded" and "operator can log in".

**A worry I had and falsified — recorded because someone else will have it.**
`SCION_SERVER_HUB_ADMINEMAILS` targets a **Layer-1 operational setting**, and the bootstrap
layer order is `defaults → SCION_SEED_* → settings.yaml → SCION_SERVER_*` with
`SCION_SERVER_*` on top. That reads like *override*, not *seed* — implying an operator who
later edits admin emails in the UI is silently reverted on every restart, which would
contradict §11.6's "seed at deploy time" decision. **It does not.**
`OperationalSettings.Snapshot()` documents and implements
`DB rows > bootstrap merge (defaults → SEED → yaml → SERVER)`: **DB sections fully own their
keys**, and env vars "are honored as seed input during the deprecation window". Seed
semantics are correct today.

Two minor items remain, both one-liners: the variable is **deprecated** in favour of
`SCION_SEED_SERVER_HUB_ADMINEMAILS` (it will log a deprecation warning on every startup of
every deployed Instance — noise in exactly the logs an operator reads when debugging), and
`--set-env-vars` is itself comma-delimited, so an `--admin-email` containing a comma
silently becomes a second env var. The second is unreachable today because the flag is
single-valued; **guard it rather than redesign it.**

### 11.5e The web assets are not in the binary — a one-tag defect that blocks §1 outright

**Measured 05:45 on the live `e2e-omni` Instance.** The hub reported it itself, in its own
startup log, which is the only reason we found it before ptone did:

> `This binary was built without web assets. The web UI will not be available.`

§1 says the operator *"opens the resulting `run.app` URL, logs in, creates a project…"*.
Everything behind that URL works. **There is nothing to open.**

**Root cause is a single build tag, and it is inherited, not local.**
`image-build/scion-base/Dockerfile:55` builds both binaries with **`-tags no_embed_web`**.
That tag selects `web/embed_stub.go` over `web/embed.go`, dropping the
`//go:embed all:dist/client` and setting `AssetsEmbedded = false`. `image-build/hub/Dockerfile`
and `image-build/omni/Dockerfile` **add no build of their own** — each is `FROM ${BASE_IMAGE}`,
a `mkdir`, and a `CMD`. So **every image in the chain ships scion-base's binary**, and no
container image this repo can produce is capable of serving the frontend.

This had been invisible for a structural reason worth naming: the tag is correct for four of
the five images. Harness images run agents, not a web server. The defect only exists in the
one image that also happens to be the only one we deploy.

**Two cheaper fixes were considered first. Neither exists.**

*Ship `dist/` into the image and serve from disk.* Rejected — not a trade-off, an
impossibility. `AssetsEmbedded` is a compile-time `bool` and there is no filesystem fallback
path in the server. There is nothing to point at a directory.

*Drop the tag from `scion-base`.* Tempting, one line, and **it does not compile.**
`scion-base/Dockerfile:41` is `COPY web/*.go ./web/` — the Go sources only, never
`web/dist`. Without the tag the `//go:embed` has no directory to embed and the build fails.
The tag is load-bearing there, not incidental. Recording this because "just delete the tag"
is the first thing any reader will reach for.

**Decision: rebuild in the omni layer.** A builder stage in `image-build/omni/Dockerfile`,
`FROM ${BASE_IMAGE}`, that `npm run build`s `web/dist/client`, then `go build`s `scion`
*without* the tag, and `COPY --from`s the result over scion-base's binary in the final stage.
`scion-base` and the harness images are untouched and stay lean — correct, since they serve
no UI. Three preconditions were verified against the build system rather than assumed:

| Precondition | Where | Result |
|---|---|---|
| omni's build context is the repo root, so `COPY web/` resolves | `image-build/scripts/lib/targets.sh:163` | `scion-omni` → `${REPO_ROOT}` ✓ |
| `${BASE_IMAGE}` has both toolchains | `core-base/Dockerfile:69` | `FROM node:20-slim`, Go on `PATH` ✓ |
| editing the Dockerfile is enough to republish | `.github/workflows/publish-omni.yml` | triggers on `pull_request`, `paths: image-build/**` ✓ |

**One defect this change would otherwise introduce.** `lib/targets.sh:206 step_build_args`
emits `GIT_COMMIT` **only for the `scion-base` step**; the `scion-omni` case emits
`BASE_IMAGE` and nothing else. Harmless while omni compiled nothing — the moment it compiles,
the deployed binary carries **empty `Commit` and `BuildTime`** while every other image stamps
correctly. Two lines to fix, and worth fixing rather than tolerating: `scion version` is how
anyone confirms which build is live, and this project has already lost hours to not knowing
which binary was running.

> **Rule this generalises to, for anyone extending the image chain:** a base image's build
> flags are part of its contract with its children. `scion-base` promises a `scion` binary;
> it does not promise a *complete* one. Any image that needs a capability the base compiled
> out must rebuild, and the chain gives no signal that it should.

### 11.5f Agent→hub addressing — the gap §11 left open, and it is load-bearing

**This section exists because §11 says, in its own summary line, *"complete for browser→hub;
deliberately silent on agent→hub pending OQ-2."* OQ-2 came back positive and I closed it
without returning here.** That was the wrong place to stop. Closing "can a sandbox reach the
launcher" is not the same as deciding **what address a sandbox is handed for the hub**, and
only the second one is a design decision.

**What is actually shipped today**, read off the live `sandbox run` command line on
`e2e-omni` rather than from source:

```
--env SCION_HUB_ENDPOINT=https://e2e-omni-721899303052.us-east4.run.app
--env SCION_HUB_URL=https://e2e-omni-721899303052.us-east4.run.app
--env GCE_METADATA_HOST=localhost:18380
--env GCE_METADATA_ROOT=localhost:18380
```

**The sandbox is pointed at the hub's own public, IAP-fronted URL.** The hub is running on
the same Instance, a link-local hop away. Instead, an agent wanting to talk to it must egress
to the internet, arrive at the IAP edge, and present credentials **it does not have** — so
IAP answers `302 accounts.google.com`, exactly as it is supposed to. The perimeter we built
in §11.2–11.3 does not distinguish "hostile internet" from "our own agent on our own
Instance," because at the edge those are the same request.

Note the second defect in the same four lines: `GCE_METADATA_HOST` points at **`localhost`**,
and §4.10/OQ-2 measured that **loopback does not work from inside a gVisor sandbox** — only
the launcher's link-local address does. Two separate addressing bugs, both of the same shape:
**an address that is correct for a co-located process and wrong for a sandboxed one.**

> **The rule this generalises to, and the one I should have written when OQ-2 closed:**
> a sandbox is not on the launcher's network and is not on the public internet's side of our
> own perimeter. **Every address handed into a sandbox needs an explicit decision about which
> of the three namespaces it lives in** — sandbox-local, launcher link-local, or public. We
> have so far defaulted two of them to values inherited from the single-process case, and
> both are wrong.

**Design consequence, stated as a requirement rather than a fix:** agent→hub traffic on this
tier should terminate **inside the perimeter**, on the launcher's link-local address, and
never traverse the IAP edge. Routing our own agents through IAP is not merely slow, it is
unauthenticatable by construction — there is no credential we could give a sandbox that would
be both sufficient for IAP and safe to place inside a sandbox, since **any sandbox can read
anything the emulator will mint (S5)**. An IAP-capable credential inside a sandbox would let
a compromised agent re-enter the hub as the operator.

**Status: hypothesis, not conclusion, and deliberately so.** As of 06:10 this is one of three
live explanations for the §1 step-5 hang (agent sandboxes start, then hang before `tmux`).
The other two are the `localhost` metadata address above, and a harness that ignores
`GCE_METADATA_HOST` and hardcodes `169.254.169.254`. **They are distinguished by one command**
— `sandbox exec <name> -- cat /proc/net/tcp` inside a wedged sandbox, reading the destination
address in `SYN-SENT`. `sandbox exec` keeps working while the entrypoint is stuck, which
gives us a live channel into a wedged sandbox.

**Whichever wins, this section stands**, because the addressing question is wrong today
regardless of which wrong address is the one currently hanging.

> **Postscript, 06:15 — none of the three won.** The step-5 hang was `buildEntrypoint`
> spelling its command as bare `sh`; see §11.5g. The sandboxes were dead before any address
> was ever dialled, and `sandbox exec` did *not* keep working — the "live channel into a
> wedged sandbox" I proposed above does not exist, because there was no wedged sandbox.
> **The addressing gap this section describes is still real and still undecided (task #16).**
> It was simply not what was failing. I have left the wrong prediction in place above rather
> than editing it out: it was a reasonable read that three agents shared, and the record is
> more useful with it than without.

#### 11.5f-D DECISION, 06:30 — one listener, link-local destination, agent-token auth

The gap above is now decided. It is also **more load-bearing than when it was written**: the
metadata emulator runs *inside each sandbox* and mints nothing locally — it calls the hub over
`SCION_HUB_ENDPOINT` (§11.12 correction). So this address gates **agent registration, task
delivery, and GCP credentials for three of five harnesses**. It is the last open design
question on the tier.

| | option | verdict |
|---|---|---|
| A | Separate internal listener, bound link-local only, agent API on it | **Deferred.** Most principled — the agent surface becomes unreachable from the internet *regardless* of IAP configuration, which is genuine defence in depth. Rejected on cost for launch: a second listener, port and lifecycle for a property IAP already provides. **Revisit at multi-tenancy** (§11.2, D1). |
| B | **One listener. `SCION_HUB_ENDPOINT` = the launcher's link-local address; the agent API authenticates by agent token rather than proxy identity.** | **✅ Chosen.** The sandbox's traffic never reaches IAP, so it needs no IAP credential, and the agent token remains the agent's authenticator exactly as on every other runtime. Smallest change that is actually correct. |
| C | Keep the public URL; issue the sandbox an IAP-capable credential | **Rejected, and not on cost.** Any credential inside a sandbox is one a compromised agent holds, and an IAP-capable credential lets it re-enter the hub **as the operator**. Not acceptable as a fallback either. |

**The rule this makes explicit, and the reason §11.5f existed at all:**

> **Every address handed into a sandbox needs an explicit decision about which of the three
> namespaces it lives in — sandbox-local, launcher link-local, or public.** Tonight produced
> one error in each direction: `GCE_METADATA_HOST` was assumed to need promoting from
> sandbox-local to link-local when it did not (§11.12), `SCION_METADATA_BIND_ADDRESS` was
> given a link-local value for a sandbox-local listener, and `SCION_HUB_ENDPOINT` is public
> when it should be link-local. **The same function computing the same address is right or
> wrong depending on which end of the connection it describes** — a *destination* on the
> launcher is correct in a sandbox's environment; a *bind address* on the launcher is not.

**Load-bearing dependency — MEASURED 06:32, and it holds.** Option B assumed the proxy/IAP
auth middleware does **not** gate the agent API. If it did, an internal agent request would be
rejected for lacking IAP headers, and B would have become a change to the auth layering rather
than an implementation detail. Confirmed by `sn-adc-metadata`:

- **`UnifiedAuthMiddleware` (`hub/auth.go:109-380`) checks `X-Scion-Agent-Token` at step 1
  (:149), *before* proxy auth at step 3a (:234).** A valid agent token short-circuits; proxy
  auth is never reached.
- **`POST /api/v1/agent/gcp-token` (`handlers_gcp_identity.go:1023`) requires
  `GetAgentFromContext()`** — agent-token auth only. That is the endpoint on the credential
  path, and it is the strictest of the set.
- **The hub already listens on `0.0.0.0:8080` in hosted mode.** Link-local is reachable with
  **no listener change** — option A's cost was real and its benefit is not needed yet.
- **No collateral.** The two consumers that genuinely need a public URL — invite-link
  construction (`admin_invites.go:174`) and the OIDC issuer fallback (`server.go:1135`) —
  read the hub's **own** `HubEndpoint` config, not the per-agent value computed by
  `resolveHubEndpointForCreate/Start`. Changing the sandbox's env var touches neither.

**So the entire change is one value, on the launcher side, in the sandbox's environment: the
same `DiscoverLinkLocalAddress()` that was reverted last hour, now used as a *destination*
instead of a *bind address*.** That is the §11.5f rule paying for itself within the hour.

**Two residuals to state rather than discover.** (a) Agent→hub traffic inside the perimeter is
**plaintext HTTP carrying a bearer agent token**. It never leaves the Instance, and each
sandbox has its own interface, but the token is per-agent and would be an escalation if one
sandbox could observe another's link. Document it; do not fix it at launch. (b) The port must
be read from the hub's configuration, **not hardcoded to 8080** — a hardcoded port turns a
config change into a silent loss of agent connectivity *and* of ADC.

### 11.5g The sandbox does not resolve argv[0] through PATH — and `run` returns 0 anyway

**The single most expensive defect of the build, and the cheapest to fix.** Two tokens.

**The measurement.** Six sandboxes, one Instance, one variable each — same omni image, same
`--sandbox-launcher`, same flags:

| command | outcome |
|---|---|
| `/bin/sleep 600` | alive at T+6s |
| `/bin/sh -c 'sleep 600'` | alive at T+6s |
| `sh -c 'sleep 600'` | **gone** |
| `sh -c 'exec /bin/sleep 600'` | **gone** |
| `/bin/sh -c 'exec sciontool init -- /bin/sh -c "sleep 600"'` | alive at T+6s |

The last row matters as much as the third: **the entire real entrypoint chain works.** The
fault was never in what we asked the sandbox to do, only in how we spelled the first word.

> **CORRECTED, 07:05 — that last sentence was wrong, and so was the row it rests on.** The
> surviving row ran `sleep 600`, *not* the real entrypoint. The real chain contains two more
> independent fatal defects, both measured on `diag-sbx6` and neither reachable until the
> argv[0] fix landed: **`rm -rf /home/scion` fails Permission denied even as root** (the rootfs
> is not writable and there is no overlay — see B32), and **tmux is not installed in the omni
> image at all** (B31). "The entire real entrypoint chain works" was an inference from a
> simplified proxy, presented as a measurement of the thing itself. See §11.5h.

**Two rules for this runtime, and they belong in the contract, not in a commit message:**

> **R1. Every command handed to `sandbox run` / `sandbox do` must use an absolute path for
> argv[0].** The sandbox launcher resolves argv[0] before the sandbox's environment exists, so
> `--env PATH=…` cannot help it. A bare name is not a slow path or a degraded path; it is
> instant, silent death.
>
> **R1 does not apply to `sandbox exec`.** Measured: `sandbox exec p1 -- tmux -V` returns
> `error finding executable "tmux" in PATH [/usr/local/sbin /usr/local/bin /usr/sbin /usr/bin
> /sbin /bin]` — it searches PATH, and it names the directories it searched. I originally stated
> R1 over all three verbs and raised a task to wrap every `Exec` call in a shell. That task was
> withdrawn. **The rule was measured on `run` and generalised to `exec` without a second
> measurement**, which is the same error, in the same document, that R1 itself was written to
> record.
>
> **R2. `sandbox run --detach` exit code carries no information about whether the sandbox is
> alive.** It returned 0 for sandboxes that no longer existed five seconds later. Anything
> that reports an agent as running on the strength of that exit code is reporting a guess.
> Require an affirmative probe — `sandbox exec <id> -- true` — before claiming `running`.

R2 is the reason R1 survived so long. A defect that fails loudly costs minutes; this one
returned success, so the hub reported healthy agents with a terminal you could attach to, and
the investigation went looking for hangs in the harness, in metadata, in gVisor, in image
size. **Roughly two agent-hours went into hypotheses that a non-zero exit code would have
foreclosed immediately.**

**The design lesson, which generalises past this runtime.** The code already contained the
correct diagnosis. `envFor:412`:

```go
// PATH is empty inside the sandbox (AC-0 retest finding). Set a
// reasonable default so the harness and its children can find binaries.
```

Someone measured this exact behaviour and fixed it — for the process environment, one layer
above where it bites. It is the third instance of the same shape in this component, alongside
`buildEntrypoint`'s `sh -c` wrapper and `isClaude()`'s defensive re-splitting of a joined
command string. **Three correct observations, three patches at the point of use, no root
cause recorded anywhere.**

> **Standing hazard for this component: a workaround lands with either a root cause or an
> open defect ID, never bare.** An undocumented workaround is not neutral — it actively
> misleads the next investigator, because it reads as intent.

**Operational note that made this hard to see:** the sandbox CLI's stderr does not reach
Cloud Run logging, `/var/log/sandbox.log` records only `[start]` / `[end] exit_code=`, and
none of `/var/run/sandbox`, `/run/sandbox`, `/tmp/sandbox`, `/var/lib/sandbox` exist. **There
is no local state an operator can read to find out why a sandbox failed.** Diagnosis required
deploying purpose-built Instances whose entrypoint was the experiment. That is a reportable
gap against the platform, independent of our own defects.

**Also measured, and corrects an earlier claim in this document:** `sandbox wait` **exists**.
It is a hidden command, absent from the `--help` verb list, and works as documented. The verb
list printed by `--help` on this CLI is not the verb set — do not reason from its absences.

### 11.5h Step 5, actually root-caused — three stacked defects, none of them the first one found

Everything below was measured on `diag-sbx6`: a real Instance with `--sandbox-launcher`, running
real `sandbox run` and `sandbox exec` against the shipped image. Nothing here is inferred from a
comment, a Dockerfile, or a timing profile.

**The lesson before the findings.** Step 5 was "root-caused" three times before this — B23
(daemonize), then §11.5g (bare argv[0]) — and each fix was real, landed, and moved the visible
symptom **not at all**. The reason is that the entrypoint chain contains *three independent
fatal defects in series*. Fixing the first exposes the second, which fails identically: sandbox
dead within ~200ms, `run` still exits 0, nothing logged. **A serial fault chain behind a silent
failure is indistinguishable from an unfixed single fault**, and the only thing that breaks the
loop is making the failure speak (§B30) or exercising each link in isolation.

| # | Defect | Measurement |
|---|---|---|
| **D1** | **tmux is not in the omni image** | `command -v tmux` → nothing; `/usr/bin/tmux` → `no such file or directory`; `sh -c 'tmux new-session…'` → `tmux: not found`, exit **127** |
| **D2** | **rootfs is not writable, even as root** | `touch /home/scion/.probe` → `Permission denied`; `rm -rf /home/scion` → `Permission denied` |
| **D3** | **`tmux attach-session` needs a controlling terminal** | `sandbox run --help` offers `--stdin/--stdout/--stderr` and **no `--tty`**; attach without a terminal exits 1 with `open terminal failed` |

**D1 is the one that has always been true.** `image-build/core-base/Dockerfile:85` installs
tmux, but the omni build chain is **thick-prep → scion-base → omni** and core-base is not in it.
The sandbox entrypoint exists to run tmux. **No sandbox on this runtime has ever survived**, and
the cause is a missing package. Everyone — me included — confirmed "tmux is installed" by
grepping the source tree and finding *a* Dockerfile, never checking whether that Dockerfile was
in the chain that built the artifact we were running.

> **A grep of the repository is not evidence about a shipped image.** The build assertion this
> needs — entrypoint-required binaries verified present in the artifact — is worth more than the
> one-line fix.

**D2 contradicts this document's own source comments.** `cloudrun_sandbox_runtime.go:44` and
`:263` claim writes to unmounted paths land in *"a private rootfs overlay the launcher never
sees"* while simultaneously calling the rootfs `READ-ONLY`. There is no writable overlay. The
entrypoint's **first** command is `rm -rf /home/scion`, so the `&&` chain terminates before
anything else runs. A competent agent rebutted this hypothesis by quoting the comment, in good
faith; the comment was simply false (B32).

**The fix for D2 is a measurement, not a workaround.** `--mount` accepts **differing source and
destination**:

```
--mount type=bind,source=/scion/probe/home,destination=/home/scion   ->  MOUNTRW=0, writable
```

So mount `agentHome` **at** `/home/scion` and delete the `rm -rf` + symlink chain outright. The
supervisor's hardcoded `HOME=/home/scion` (`supervisor.go:115`) then needs no env plumbing, and
nothing mutates the rootfs. The `source=X,destination=X` identity at `:399` is a convention that
was mistaken for a constraint, and the workaround built on top of it — deleting a directory out
of the image — was strictly worse than the thing it worked around.

**D3's fix must preserve a property the broken code had.** `attach-session` was not decoration:
it was how PID 1 tracked the session's lifetime, so that when the agent's session ended, PID 1
exited and the sandbox stopped. The first proposed replacement, `tmux wait-for scion-exit`,
blocks forever because nothing ever signals that channel — trading "dies instantly" for "never
dies", which is worse, because leaked live sandboxes fail quietly and cost money. The
requirement:

> **PID 1 must outlive the tmux session and must exit when the session ends.**

A `while tmux has-session -t scion; do sleep 2; done` loop satisfies it, is TTY-free, and is
correct on inspection. 2s shutdown granularity is not worth trading for cleverness at PID 1.

### 11.6 Admin bootstrap (S3) — seed, don't trust-on-first-use

IAP authenticates *a Google identity*; it does not say which identity is the **admin**.
Two options:

- **TOFU** — the first authenticated identity claims admin. **Rejected:** it is a race,
  and in a project where a domain-wide binding was used the winner may not be the
  operator.
- **✅ Seed at deploy time** — the deploying operator's own account (step 1) is written
  to `AdminEmails`. Deterministic, no race, no bootstrap secret to leak or expire.

**This retires the bootstrap-token mechanism for this tier** — Option 5 in
`single-node-auth.md`. Worth stating plainly because it is a simplification the P6 row
did not anticipate: with IAP as the perimeter, the operator has already proven who they
are before the hub sees the request, so a second bootstrap credential adds a step and
protects nothing.

**Keep one escape hatch:** an explicit `--admin-email` override, for the case where the
deploying identity is a CI service account rather than a human.

### 11.7 S2-rev — the security story, restated

S2 originally read "the invoker check stays on, so IAP and IAM are defence in depth."
**OQ-17 killed that and it cannot be recovered.** The honest restatement:

> The Instance is protected by **exactly one** network-layer control: IAP. Cloud Run's
> invoker check is disabled because it is incompatible with IAP on Instances (a platform
> defect, filed as `defect-iap-instance-audience.md`, not a choice). Defence in depth is
> provided *behind* the perimeter by hub session auth and authorization, **not** at the
> network layer. The deploy-time assertion in §11.5 step 7 exists because a single
> control with no fallback must be verified rather than assumed.

**If the platform fixes the audience defect**, the invoker check can be re-enabled and
S2's original form restored. That is the one upstream fix that would materially improve
this tier's security posture, and it is why the defect report scopes its ask narrowly to
`x-serverless-authorization` (leaving the app-facing assertion audience untouched).

### 11.8 Failure modes the operator will actually hit

| Symptom | Cause | Fix |
|---|---|---|
| **401** on every request, `x-serverless-authorization` present | `invokerIamDisabled` not set (OQ-17) | Set it. This is the defect, not a misconfiguration on your part. |
| **403** from IAP, no login prompt | No `httpsResourceAccessor` binding for the caller | §11.2. Remember project-level grants are invisible in the resource policy. |
| **404** setting a per-Instance IAP policy | No such resource exists | Bind at region level; stop looking. |
| App answers **without** a login redirect | `iapEnabled` false or still reconciling | §11.5 step 7 catches this at deploy. If seen later, the perimeter has been dropped — treat as an incident. |
| **401**, audience mismatch in hub logs | Someone changed `services` → `instances` in the audience | §11.3. Revert. |
| Login succeeds, hub says not authorized | `AdminEmails` seeded with a different identity | §11.6; use `--admin-email`. |

### 11.9 P6 acceptance criteria

- [ ] One command, from nothing to a printed URL, no manual console steps.
- [ ] The deploy **fails** if an uncredentialled request reaches the app (step 7).
- [ ] Re-running the command converges and does not drop `iapEnabled` or
      `invokerIamDisabled`.
- [ ] Effective access is printed, including inherited project-level bindings.
- [ ] A test pins `isSupportedIAPAudience` against the Instance-form audience with a
      comment explaining why `services` is correct for an Instance.
- [ ] The operator who ran the deploy is admin on first login, with no bootstrap token.
- [ ] `--admin-email` override works for a CI-service-account deploy. **Currently broken —
      `deploy_instance.go:423` hardcodes `--member=user:`+email, and
      `user:sa@…gserviceaccount.com` is not a valid IAM member, so the flag fails at exactly
      the use case its own help text names.** Fix must detect `.gserviceaccount.com` and emit
      `serviceAccount:`. **Test both forms** — a single-form test is what let this ship.
- [ ] Docs state the region-scope caveat and the multi-tenancy revisit trigger.

**Added 05:08 — the deploy must verify that what it deployed actually serves.**

- [ ] **Post-deploy smoke check.** After the Instance reports ready, `deploy-instance`
      fetches the URL and asserts the **hub answered** — not a connection failure, not a
      Cloud Run error page. Behind IAP the success signal for an unauthenticated probe is a
      **302 to `accounts.google.com` carrying `x-goog-iap-generated-response: true`**
      (verified twice on live Instances, 03:49 and 04:21 — a known-good assertion, not a
      guess). **The deploy fails loudly if this does not hold.**

> **Why this and not the obvious alternative.** `sn-e2e-walk` proposed instead that
> `deploy-instance` pass `--command`/`--args` itself, so it stays correct even when the
> image CMD is wrong. The instinct is right — *do not let a broken artifact next door
> silently break the product* — but the remedy creates **two sources of truth for how omni
> starts**, and they drift. The next flag change updates the image while the deploy command
> keeps passing yesterday's arguments, producing an Instance that **starts** and is subtly
> wrong. That is strictly harder to diagnose than the bug it guards against, because nothing
> looks broken.
>
> **Ruling: the image owns how it starts; `deploy-instance` owns verifying that it did.**
> The smoke check also generalises — wrong port, crash loop, missing binary, bad env all
> surface identically — and for a one-command tier it converts *"the URL does nothing"* into
> *"deploy failed, here is why"*. That distinction is most of the difference between a
> product and a demo.
>
> **This criterion exists because the omni CMD defect shipped and nothing caught it.** The
> image built, pushed, pulled and started; only the port was wrong. Every gate we had was
> green.

### 11.10 OQ-2 — ANSWERED, and it deletes a phase

**Measured by `spike-oq2`, 2026-08-26 ~04:10 UTC.** This was the last open question on the
critical path that could still have *added* work. It did the opposite.

**The sandbox reaches the launcher directly over a link-local address, in 1.64 ms median.**
Agents therefore never traverse the IAP perimeter, no agent-side transport credential is
needed, and **P7 does not exist.** §11 is now complete for both browser→hub and agent→hub.

| Path | Result |
|---|---|
| sandbox → launcher, **launcher link-local `169.254.8.x:<port>`** | ✅ **works, 1.64 ms median** — this is the path |
| sandbox → launcher, loopback | ❌ fails — the sandbox gets **its own netns**; `127.0.0.1` is the sandbox, not the launcher |
| sandbox → launcher, AF_UNIX socket | ❌ fails — does not cross the gVisor boundary |
| sandbox → hub, public `run.app` hairpin | ⚠️ reachable at 35 ms, but needs OIDC — 21× slower and vastly more machinery. Not used. |
| launcher → sandbox (`sandbox exec`, full lifecycle) | ✅ works — unchanged from P4 |

#### Three consequences that outlive the question

**1. The launcher's address is discovered, never hardcoded.** I asked specifically because
depending on an undocumented link-local constant is the kind of thing that fails months
later as a hung agent rather than a config error. It does not need to be a constant: the
**launcher** determines its own link-local IP with the standard `getsockname()`-on-a-
connected-UDP-socket trick and injects it per sandbox as `HUB_HOST`. The sandbox never
guesses. If the platform renumbers, the launcher picks up the new address on next start.

> **Design rule: no literal `169.254.8.1` anywhere in the codebase.** The one residual
> platform risk is the gateway ceasing to route to launcher link-local addresses at all —
> a breaking change we could not have insulated against anyway.

**2. `--allow-egress` is REQUIRED, and it is all-or-nothing.** Without it the sandbox has
no working interfaces at all — it cannot reach the launcher either. This is a hard deploy
constraint on `scion deploy-instance`, not a tuning option.

> **Security consequence, stated plainly because it is a property of the tier and not a
> footnote: there is no configuration in which a sandbox can talk to its launcher but not
> to the internet.** We cannot offer network-isolated agents that still function. Anyone
> who assumes agent egress is containable in this tier is wrong, and the deploy docs must
> say so rather than let them find out.

**3. The GCE metadata server is *not* reachable from the sandbox — even with egress on.**
Launcher link-local IPs answer; `169.254.169.254` times out. This is a **constraint on
OQ-14** (Vertex/ADC from inside a sandbox), which remains open and unowned: ADC-via-metadata
looks unavailable regardless of egress, so agents likely need credentials **minted by the
launcher and pushed in**. Whoever picks up OQ-14 starts from that, not from zero.

### 11.11 P7 — retired before implementation (design preserved for OQ-14)

**§11.11 previously carried a full contingent design for P7** — a launcher-minted,
launcher-pushed, short-lived IAP token file — written while the spike ran so that a bad
answer would not also cost a design cycle. OQ-2 returned the good answer. **P7 is deleted:
there is no agent→hub credential problem, because agents do not cross the perimeter.**

That was the point of writing it contingently, and throwing it away is the success case,
not waste. Two things from it are worth keeping, and only two:

- **The pattern is validated for OQ-14.** "Launcher mints, launcher pushes over the exec
  control plane, sandbox holds only a short-lived token" is now the *leading* shape for
  cloud credentials inside sandboxes, given §11.10's metadata-server finding. Its rejected
  alternative — *give the sandbox metadata-server access so it mints its own* — is now
  rejected twice over: it hands agent-controlled code the runtime SA's full authority
  indefinitely, **and it does not work anyway**.
- **The multi-tenancy revisit trigger** (§11.2, D1) no longer needs the P7 entry about the
  runtime SA holding region-wide `iap.httpsResourceAccessor`. We never grant it. The other
  triggers stand.

Full prior text is in git history and in `implementation-state.md`; it is not reproduced
here, because a contingent design for a phase that does not exist is a trap for the next
reader.


### 11.12 OQ-14 — ANSWERED: ADC works, via a per-sandbox emulator on loopback

> **HEADLINE CORRECTION, 06:30 — the emulator does not run on the launcher, and
> `localhost:18380` was correct all along.** This section previously concluded that the
> emulator runs launcher-side and that sandboxes must reach it over the launcher's link-local
> address. **That was wrong, and it was wrong because the OQ-14 spike stood the emulator up by
> hand rather than exercising the shipped path.** Measured by `sn-adc-metadata` at 06:26:
>
> - `metadata.ConfigFromEnv() → metadata.New() → Start()` is called in **exactly one place**:
>   `cmd/sciontool/commands/init.go:448-499`, inside **`sciontool init`**.
> - `buildEntrypoint` runs `sciontool init` **inside the sandbox**. So the emulator is
>   **per-sandbox**, sharing the harness's own network namespace.
> - The launcher's `server_foreground.go` **does not start one.** The comment at :2398
>   calling 18380 the "host-global metadata-server" is misleading — it is per-container.
> - `metaCfg.FetchGCPToken` delegates to **`hubClient.FetchGCPToken()`**. The emulator mints
>   nothing locally; it asks the hub over `SCION_HUB_ENDPOINT`.
>
> **Three consequences, and the second is the one that matters.**
>
> **1. The link-local change is reverted** (`9877da59`). Sending it would have set
> `GCE_METADATA_HOST` to an address where nothing listens *and* made the in-sandbox emulator
> bind an IP absent from its namespace (`EADDRNOTAVAIL`) — no emulator at all. The
> `BindAddress` / `DiscoverLinkLocalAddress` infrastructure is retained with no consumer,
> because the S5 guard is worth keeping against a future architecture.
>
> **2. ADC on this tier does not depend on launcher reachability at all. It depends entirely
> on the sandbox reaching the *hub*.** That is §11.5f / task #16 — the addressing gap this
> design left open. **It is no longer just an agent-registration question; it now gates
> credentials for three of five harnesses.** OQ-14's "answered" status is therefore
> conditional on #16, and I am recording it as such rather than leaving the section reading
> as closed.
>
> **3. S5 is satisfied by the default** — see the correction in §4.11. A loopback-bound
> emulator inside a sandbox is reachable only by that sandbox.
>
> The narrative below is left standing. It contains two corrections that were valuable, and a
> third — this one — that it did not catch, for a reason worth naming: **every step of it
> reasoned from source and from a hand-built spike, and none of it ran the shipped path.**

**Status: answered 04:54, corrected 06:30.** ADC works from inside a gVisor sandbox;
`vertex-ai` and `gcloud-adc` are not stranded. **No bind change is required.** The narrative
below is kept in the order it was established, because two of the corrections in it are worth
more than the conclusion.

OQ-2 established that the GCE metadata server is unreachable from inside a sandbox even
with `--allow-egress`. Taken alone that reads as "ADC is impossible on this tier", which
would strand `vertex-ai` and `gcloud-adc` — **3 of the 5 shipped harnesses**. Before
escalating it as a platform limitation, note what is already in the tree:

**`pkg/sciontool/metadata/` is a GCE metadata server emulator we already ship.**
`server.go` serves the standard endpoint format (assign/block modes, SA email, project);
`iptables.go` installs a DNAT redirecting `169.254.169.254:80` to it, so tools that
hardcode the metadata IP are intercepted transparently.

OQ-2 supplies the missing half: the launcher is trusted, holds the runtime SA, and is
**reachable from the sandbox in 1.64 ms**.

```
agent (hardcodes 169.254.169.254)
   │  DNAT inside the sandbox
   ▼
launcher link-local:18380  ──►  metadata emulator  ──►  real metadata server / SA
```

**Properties, if it holds:** agents get working ADC with **no harness changes**; the
sandbox holds **no durable credential**; minting stays on the trusted side. It is the same
trust shape as the retired P7 design (§11.11) — which is why that reasoning was kept.

**Correction, 04:29 — I had the mechanisms the wrong way round, and §4.10 already said so.**
This is worth admitting rather than silently fixing: **§4.10 of this same document already
records that `GCE_METADATA_HOST`/`GCE_METADATA_ROOT` are the primary mechanism and the
iptables layer is non-fatal defence-in-depth** — that is precisely why K8s works with no
`NET_ADMIN` anywhere. I re-derived it from the source instead of reading my own §4.10, and
briefed the spike with the priorities inverted as a result. `sn-impl-em3` caught it.
**The lesson is the night's recurring one in a new costume: check the record before
re-deriving from primary sources.**

My first draft made iptables load-bearing and the env var a fallback. `sn-impl-em3` read the
code and showed the reverse is true, which changes what has to be tested:

- **`GCE_METADATA_HOST` is the primary mechanism and the runtime broker already sets it**
  (`localhost:18380`). Pointing it at the launcher instead is the whole change.
- **The iptables rule is `REDIRECT`, not DNAT** — and `REDIRECT` is localhost-only *by
  definition*. It was never going to cross a container boundary whatever gVisor implements.
  It is defence-in-depth for tools that hardcode the IP and ignore the env var.

**So the decisive question is: does `GCE_METADATA_HOST` pointed at the launcher's
link-local address work from inside a sandbox?** The gVisor-`nat` question is no longer a
kill-switch; failing it costs coverage, not the approach.

#### The binding decision, which is a security decision

The emulator binds `127.0.0.1` today, so something must change. **The obvious change —
bind `0.0.0.0` — is the wrong one.** That endpoint mints credentials for the runtime
service account, and — **now confirmed by reading the source** — **does not authenticate its callers at all**. The only gate is `requireMetadataFlavor`, which demands the `Metadata-Flavor: Google` header: a convention check, not authentication. (`Config.AuthToken`/`TokenFunc` authenticate the emulator **to the hub**, not callers to the emulator.) Anything that can reach the endpoint and sets one header gets a token. On `0.0.0.0`
it is reachable by every sandbox on the Instance and by anything else that can route to it.

> **Bind to the launcher's link-local address specifically.** Identical reachability for
> sandboxes, far smaller surface, and it composes with the `HUB_HOST` discovery OQ-2 already
> established — the launcher learns that address at runtime anyway.

**Any sandbox on the Instance can therefore obtain the runtime SA's token.** On a single-tenant, single-operator, single-project tier that is
**acceptable at launch — but only stated, never assumed.** It joins the region-scoped IAP
grant on the **multi-tenancy revisit trigger** (§11.2, D1): the day this tier hosts two
tenants, it is immediately wrong.

#### ANSWERED 04:54 — the full ADC chain works from inside a gVisor sandbox

`spike-oq14` ran the whole path, not a stand-in. All three steps exercised:

| Step | Result |
|---|---|
| Metadata reachable on launcher link-local `169.254.8.1:18380` | ✅ |
| `google-auth` SDK inside the sandbox, `GCE_METADATA_HOST` set | ✅ **real token**, 1024 chars, `expires_in: 1799` |
| That token against `cloudresourcemanager.googleapis.com` | ✅ **real API call succeeded** |
| `iptables -t nat` inside the sandbox | ❌ **not available** — no `/proc/net/ip_tables_names`; gVisor's netstack does not implement the `nat` table |

**So OQ-14 is answered: ADC works, via `GCE_METADATA_HOST` pointed at the launcher.**
`vertex-ai` and `gcloud-adc` are not stranded after all.

#### DECISION: ship the emulator, not the proxy — the convenient answer is the wrong one

The spike proved the path using a **transparent proxy** to the real metadata server. It
works, it is a few lines, and **it must not become the design.** It was a test instrument.

The result itself shows why: the proxy handed the sandbox a **real, unrestricted
service-account token with full authority**. That is *exactly* the alternative rejected
when P7 was still alive — *"give the sandbox metadata-server access so it mints its own:
hands agent-controlled code the runtime SA's full authority indefinitely."* **A transparent
proxy reproduces that blast radius precisely**; it only moves the hop.

The emulator is the entire point: it can **block, scope, and audit**. The outstanding work
is one bind change — link-local instead of `127.0.0.1` — and that change is what makes this
defensible rather than merely functional.

> **Recorded deliberately, because the pull toward the proxy will be strong:** the thing
> that worked first is not the thing to ship. Anyone reading *"proxy worked, three lines of
> Python"* will reach for it.

**Open detail:** the spike's token came back for `721899303052-compute@developer.gserviceaccount.com`
— the **default compute SA**, not the Instance's configured runtime SA. Confirm which
identity the emulator serves; silently serving the default compute SA would be a second,
quieter privilege problem.

#### What the iptables negative result costs us

`REDIRECT` was localhost-only anyway, and gVisor has no `nat` table, so **`GCE_METADATA_HOST`
is the only mechanism — there is no defence in depth.** Consequence to document rather than
discover: **anything that hardcodes `169.254.169.254` and ignores the env var simply fails**
— a bare `curl` in an agent's shell command, older libraries, some tooling. §4.10 already
predicted this posture ("we inherit the K8s posture: functional protection via env vars,
with the defence-in-depth layer absent"); it is now measured. **Which of the five shipped
harnesses honour the env var is an open item.**

---

### 11.5i The attach path, measured — and a retracted claim about it

> **CORRECTION (07:55).** This section originally claimed that browser attach was
> unimplemented for `cloudrun-sandbox` and that the broker used `Runtime.Name()` as an
> executable name. **Both claims were false and are withdrawn**, along with review-queue
> entries B34 and B35 and task #24. The `cloudrun-sandbox` PTY dispatch has existed since the
> P4 commit: `cloudRunSandboxBin = "/usr/local/gcp/bin/sandbox"` (`:61`), dispatch at `:166`,
> `:385`, `:784`, dedicated handlers at `:585` and `:1031`, resize at `:705` and `:1005`.
> `runtimeCmd` is only ever string-compared for this runtime, never executed.
>
> **Cause: I read the file out of a working tree 62 commits stale that does not contain this
> tier at all.** `pkg/runtime/cloudrun_sandbox_runtime.go` does not exist in it; my
> `pty_handlers.go` was 1015 lines against the real 1154. The line numbers I cited (`:165`,
> `:941`) were the docker and k8s paths in a file predating the sandbox work.
>
> **The evidence was in front of me and I explained it away.** Earlier in the same session a
> grep failed with *"No such file or directory"* on the runtime file; I noted it, assumed it
> merely lived on a feature branch, and carried on greping the same tree. The correct
> inference was that **the entire checkout was untrustworthy for this project.** From a stale
> tree I then produced a design-doc section, two review-queue entries, a task, and
> instructions to two agents — one of which cost `sn-impl-em3` a wasted request for
> deployment access. `sn-e2e-walk` caught it.
>
> **Standing rule, now that the workspace is known to be `shared-plain` and cannot simply be
> switched:** read this tier only via `git show <ref>:<path>`, never by greping the working
> tree. A bare grep in `/workspace` is evidence about a 62-commit-old snapshot and nothing else.
> This is the same defect as B30 — diagnosis by inference rather than measurement — committed
> while cataloguing it in others.

**What survives is the measurement, and it is worth having.** The probe below was run against
real infrastructure, and it independently confirms the argv the existing implementation
already builds.

#### `diag-sbx7` — the attach path and the step-5 fix, measured before the fix was written

Step 5 was root-caused by measurement (§11.5h). Rather than wait for the fixed image and
discover the attach path the same way, I probed **the attach path and the proposed step-5 fix
together** on a real sandbox (`diag-sbx7`, on the tmux-bearing image
`dev-54c88e836b8d938d2fcb86fa20b853ae279c9359`) **before the fix was written**.

The probe emulated the *proposed* entrypoint — mount at `destination=/home/scion`, log
redirect to `/home/scion/.scion-entrypoint.log`, poll loop instead of `attach-session`:

| Check | Result | What it settles |
|---|---|---|
| Entrypoint runs | `P1RC=0`, `ALIVE=0` at 6 s | The fixed form works |
| Log path | `.scion-entrypoint.log` visible on the **host** at the mount source | B33 fix confirmed by measurement, not reasoning |
| Attach, no PTY | `open terminal failed: not a terminal`, `NOPTY=1` | D3 reconfirmed in situ |
| Attach, with PTY (`script -qc`) | tmux screen + status line `[scion] 0:agent*`; `WITHPTY=124` (held to timeout) | **Attach works.** 124 is the success signal — it never exited on its own |
| `capture-pane` | `CAP=0` | Scrollback path works |
| Kill session | `KILL=0`, then `ALIVE_AFTER_KILL=1` | **PID 1 exits when the session ends** |

That last row is the one worth keeping. The requirement — *PID 1 must outlive the tmux
session and must exit when the session ends* — is now measured on both halves. The
originally-proposed `tmux wait-for scion-exit` would have satisfied the first half and
broken the second, trading "dies instantly" for "never dies". The poll loop satisfies both.

#### The attach path is already implemented — and the probe confirms it

The broker has had a dedicated `cloudrun-sandbox` PTY path since the P4 commit. It dispatches
on a **string comparison**, never by executing the runtime name:

```go
cloudRunSandboxBin = "/usr/local/gcp/bin/sandbox"        // :61
isCloudRunSandbox := runtimeCmd == "cloudrun-sandbox"    // :166, :385, :784

// startCloudRunSandboxExec, :591-596
args := []string{"exec", s.containerID,
    "--env", "TERM=xterm-256color",
    "--", "/usr/bin/tmux", "attach-session", "-t", "scion"}
s.cmd = exec.CommandContext(s.ctx, cloudRunSandboxBin, args...)
ptmx, err := pty.StartWithSize(s.cmd, &pty.Winsize{Cols: ..., Rows: ...})
```

**That argv is character-for-character the one `diag-sbx7` measured working**, and
`pty.StartWithSize` supplies the PTY that `script -qc` supplied in the probe. `waitForTmuxSession`
has its own sandbox branch (`:168-182`) using the same binary, and resize uses it at `:705`
and `:1005`. So the probe is not a discovery of missing work — it is **independent live
confirmation of code that already existed**, which is the more useful of the two outcomes and
the one this section now records.

The one genuine caveat, unchanged: **`sandbox exec` has no `--tty` flag** (§11.5h measured its
flag set). The PTY must come from the caller. The broker allocates one; the CLI path
(`Attach()` → `runInteractiveCommand`) inherits the operator's. Both are satisfied. Any *third*
caller would have to allocate its own.

#### Acceptance — browser attach (§11.5i)

- [ ] Attaching from the browser to a cloudrun-sandbox agent renders a live tmux screen.
- [ ] `capture-pane` scrollback renders (measured working on `diag-sbx7`).
- [ ] Terminal resize propagates end-to-end (`window-size latest`; resize path at `:705`/`:1005`).
- [ ] Detaching leaves the agent running — the poll-loop PID 1 must not exit on detach, only
      on session end. `diag-sbx7` measured both halves; this confirms it through the broker.
- [ ] Any future non-broker caller of the attach path allocates a PTY, since `sandbox exec`
      cannot.

---

## 12. Cross-References

- `single-node-packaging.md` — onboarding Track 0; `requireImageRegistryForBroker`;
  Strategy C's bind-mount path mismatch (dissolved here, §3.2); item 0.7 dev auth
  (escalated here, §4.11).
- `single-node-auth.md` — device grant; Option 5 bootstrap token and the
  `establishSession()` extraction; the `AdminEmails` demotion bug; the "storage
  settles it" argument (§5.1 revisits it in the Instances context).
- `cloudflare-tunnel.md` / `cf-tunnel-arch-state.md` — sibling ingress design, owned
  by `cf-tunnel-arch`. Not needed here (§2). **Carries the `auth-refactor`
  merge-order warning repeated in §7.**
- `GLOSSARY.md` — tier definitions; §5.1 flags a divergence needing resolution.

---

### 11.5j B12 resolved — the IAP-SA `run.invoker` binding is not load-bearing

**Question (#11/B12):** does an IAP-protected Instance need a `roles/run.invoker` binding for
the IAP service agent, as some IAP-behind-Cloud-Run guidance suggests?

**Answer: no, and it is measured rather than argued.**

| Evidence | Source |
|---|---|
| `deploy-instance` grants exactly one role: `roles/iap.httpsResourceAccessor`, region-level | `cmd/deploy_instance.go:454-463` |
| No `run.invoker` binding exists anywhere in `ptone-experiments` | `gcloud projects get-iam-policy`, filtered for `invoker` — zero results |
| `iap-demo` nonetheless serves and enforces — unauthenticated `GET` → **302 to `accounts.google.com`** | live probe, 07:50 |

The mechanism explains the result: `invokerIamDisabled: true` **turns Cloud Run's invoker IAM
check off entirely**, so no code path consults `run.invoker`. Adding the binding would be inert.
This is not merely "unnecessary for browser access" — with the invoker check disabled there is
no request shape that `run.invoker` could gate, so the conclusion does not depend on how the
endpoint is reached.

**Consequence for the deploy command: it is already minimal on this axis, and should stay so.**
Any future instruction to grant `run.invoker` should be treated as over-granting and rejected
unless `invokerIamDisabled` is being turned back off — at which point the perimeter invariant
(never `invokerIamDisabled: true` without `iapEnabled: true`) is the governing rule anyway.

#### An audit trap in the test project, found while checking this

The same policy read turned up `roles/iap.httpsResourceAccessor` granted to **`domain:google.com`
at the *project* level** in `ptone-experiments`. That is a deliberate choice for the IAP demo,
and it is fine for that purpose. It is recorded here because of what it does to *our* evidence:

> **Every hosted-tier instance we deploy into `ptone-experiments` is reachable by any
> `google.com` account, inherited from the project.** A successful §1 walk in this project
> therefore demonstrates that IAP *enforces* — the 302 is real — but says nothing about whether
> the access policy is *narrow*. Those are different claims and only the first is in evidence here.

This is precisely the failure mode `diPrintEffectiveAccess` was written to prevent
(`cmd/deploy_instance.go:469-472`): *"project-level grants inherit invisibly and do not appear in
resource policies. A tool that only prints what it wrote would actively mislead an operator
auditing access."* The tool prints both levels for exactly this reason. Worth confirming during
the walk that the printed project-level section is non-empty here — this project is a live
instance of the case that function exists to catch, so it is a free test of it.

---

### 11.5k The link-local disambiguation problem is a false problem — measured

**Symptom (found by `sn-e2e-walk` at step 4 of the walk).** Agent start fails:
`cloudrunSandboxHubEndpoint` (`hubenv.go:183`) calls `metadata.DiscoverLinkLocalAddress`,
which errors with *"multiple IPv4 link-local addresses found ([169.254.8.1 169.254.9.1
169.254.169.1]) — cannot auto-select; set SCION_METADATA_BIND_ADDRESS to the correct one"*
(`pkg/sciontool/metadata/server.go:297-305`).

**Why this is a §1 blocker and not a configuration detail.** Cloud Run Instances *always* carry
three link-local addresses, so discovery does not fail intermittently here — **it fails always.**
And `deploy-instance` never sets `SCION_METADATA_BIND_ADDRESS`: the only file in the tree that
references the variable is `server.go` itself. So the operator who runs the one deploy command
gets an Instance on which **no agent can start**, and the error's own advice requires them to
already know which of three addresses is correct. §1 says *"runs one deploy command … starts a
Claude agent"*. That path was broken.

**The value in use was a guess.** `SCION_METADATA_BIND_ADDRESS=169.254.8.1` came from the
`sn-adc-metadata` Instance config and was copied forward verbatim; no one had measured which
address actually routes from a sandbox to the launcher.

#### Measurement

Launcher's own addresses (`hostname -I`):

```
172.20.0.1  169.254.8.1  169.254.9.1  169.254.169.1  fddf:3978:feb1:d745::c001
```

From inside a real sandbox, launched with **the production flag set** — `--rootfs / --write
--allow-egress`, matching `cloudrun_sandbox_runtime.go:697-699` — against a listener on
`0.0.0.0`:

| Target | Result | |
|---|---|---|
| `169.254.8.1:9099` | `HTTP=200` | reachable |
| `169.254.9.1:9099` | `HTTP=200` | reachable |
| `169.254.169.1:9099` | `HTTP=200` | reachable |
| `172.20.0.1:9099` | `HTTP=200` | reachable |
| `169.254.8.1:9098` | `rc=7` refused | **control** — closed port on a real address |
| `172.20.0.1:9098` | `rc=7` refused | **control** |
| `169.254.99.99:9099` | `rc=28` timeout | **control** — bogus link-local |
| `10.99.99.99:9099` | `rc=28` timeout | **control** — bogus private |

**The controls are the point.** A probe in which every target returns 200 establishes nothing;
it cannot distinguish "all addresses work" from "something intercepts everything". The negative
cases were run before the positive ones were believed, and they fail in two distinct ways —
refused for a closed port on a real address, timeout for an address the launcher does not own.
The probe discriminates, so the positive results are load-bearing.

**Conclusion: `DiscoverLinkLocalAddress` refuses to choose in exactly the case where every
choice is correct.** The multi-address error guards against nothing on this platform. It also
explains why the guess survived: any of the three would have worked.

#### Fix

On multiple matches, **select deterministically instead of erroring — sort, take the lowest.**
That yields `169.254.8.1`, the value already deployed and working, so the change is
behaviour-preserving. Retain the zero-match error. Retain `SCION_METADATA_BIND_ADDRESS` as an
override (`sn-e2e-walk`'s `79718fd5`, integrated at `311179b`) — as an escape hatch, not as the
mechanism. §1 is then restored: one command, no operator knowledge, no env var.

**The comment must be honest about what the rule is.** The addresses are measured equivalent for
this purpose, so any deterministic rule is safe and sorting merely makes it reproducible. This is
arbitrary-but-stable selection, and dressing it up as principled would mislead the next reader
into thinking the ordering carries meaning.

**Deliberately not done: hardcoding `172.20.0.1`.** It is the launcher's address on the sandbox
network *and* the sandbox's default gateway, which makes it the semantically correct "launcher as
seen from the sandbox". But it is measured on **one** instance, with no evidence of stability
across instances or platform versions. Recorded as a possible refinement, not adopted — the
whole reason `169.254.8.1` needed re-measuring is that someone previously adopted an unmeasured
address because it looked right.

#### Acceptance — §11.5k

- [ ] A fresh `deploy-instance` with **no** `SCION_METADATA_BIND_ADDRESS` set starts an agent
      successfully. This is the §1 property and the only one that matters.
- [ ] `DiscoverLinkLocalAddress` returns a value on a 3-address Instance rather than erroring.
- [ ] Zero link-local addresses still errors.
- [ ] The env var still overrides when set.

---

### 11.13 §1 WALKED GREEN — the measurement, and precisely what it does not cover

**Date:** 2026-08-26, 08:14–08:27 UTC. **Walked by:** `sn-e2e-walk`. This section supersedes every
earlier prediction in §11 about whether the path works. It works.

#### The measurement

Instance `e2e-walk-r2`, `us-east4`, project `ptone-experiments`, image
`ghcr.io/ptone/scion-omni:dev-311179bad484dcc8fb8a57a3758465d95377e355`.

| §1 clause | Step | Result |
|---|---|---|
| one deploy command | — | Instance created |
| opens the `run.app` URL | 0 | IAP enforcing, 302 → accounts.google.com |
| logs in | 1 | 200, `role=member` |
| creates a project | 3 | 201 |
| starts a Claude agent | 4 | 201; `sandbox exec /bin/true` exit 0 (4b); `tmux has-session -t scion` exit 0 (4c) |
| attaches to its terminal from the browser | 5 | **WebSocket 101, real terminal bytes** — `Welcome to Claude Code v2.1.247` |
| watches it commit to a git remote | 6 | commit `7301c25`, pushed, both commits arrived |

Artifacts: project `e59fb8c5` (`e2e-debug-r3`), agent `1f087bd9`.

#### The discovery re-run — because the green walk used an escape hatch

The walk above reached the hub via `SCION_METADATA_BIND_ADDRESS`, so `DiscoverLinkLocalAddress`
never executed and **the mechanism measured was not the mechanism that ships**. Re-run on
`dev-2fa880a55c4f0585d7236b1df9b8b4b4adf89198` with that variable **unset**:

    [sciontool] INFO: Operating mode: hub-connected (endpoint: http://169.254.8.1:8080)

`169.254.8.1` — non-metadata-adjacent, numerically lowest, as §11.5k predicted. Steps 4/4b/4c
PASS. **`deploy_instance.go` never sets the escape-hatch variable, so this is the production
path.** §11.5f is now closed on measurement rather than on argument.

#### What the green walk does NOT establish

This is the operative part of the section. A green run is evidence for exactly what it exercised.

**1. Step 6 pushed to a bare repo *inside the sandbox*** (`/tmp/e2e-remote.git`), not over the
network. Three separable claims live in "commit to a git remote"; the walk closed two:

| Claim | Status |
|---|---|
| git works in the sandbox | PROVEN |
| the sandbox has egress to a git host | PROVEN — `curl github.com` → 200 |
| a **credentialed** push to a real remote succeeds | **NOT PROVEN** |

The plumbing is *not* missing: `sciontool init` runs as PID 1 in the sandbox and configures
`credential.helper` at `cmd/sciontool/commands/init.go:1688` / `:1831` — GitHub App refresh when
`SCION_GITHUB_APP_ENABLED=true`, otherwise a `GITHUB_TOKEN` helper. It never fired because the
walk agent ran in **no-auth mode with no token**. Residual risk is "an existing credential path
unexercised in this deployment shape," not "no credential path." Closing it requires a real
credential in the test project — a decision deliberately escalated rather than improvised.

**2. Readiness reporting is still unverified — see §11.14.** Step 4 reported
`phase=running immediately`, which is the *symptom* of the open readiness defect, not evidence
against it. It was accidentally accurate here because the entrypoint genuinely worked.

**3. IAP policy breadth is unproven.** Per §11.5j, `domain:google.com` holds
`roles/iap.httpsResourceAccessor` at **project** level in `ptone-experiments`. Steps 0–1 prove IAP
**enforces**; they do not prove the policy is **narrow**. A green walk in this project cannot
distinguish the two.

### 11.14 Why a green §1 walk cannot close the readiness defect

The hub marks an agent `running` on the strength of `sandbox run --detach` returning 0 — and
`diag-sbx3` measured that `run` returns 0 **for a sandbox that is already dead**. The operator
impact is the worst available shape: the UI says running, the terminal attaches (WebSocket 101
succeeds), and then nothing ever appears — a failed *start* presenting as a *hang* in our product.

Step 4 of the green walk reported `phase=running immediately`. That is not counter-evidence. It is
the defect's output, which on that run happened to be true.

**The general rule, since this is the third time tonight it has bitten this project:** a test in
which everything succeeds cannot distinguish "the signal is correct" from "the signal is
unconditional." Only a **negative case** discriminates — the control must fail before the positive
is believed. §11.5k reached the same conclusion about link-local reachability (every address
returned 200, which is why "which one is right?" was a false question), and §11.5i records what
happens when a claim is made without exercising the path at all.

**The discriminating test** (in flight): induce a sandbox that starts and then dies; record the
hub-reported phase at ~t+30s and ~t+2min, whether attach returns 101 then produces nothing, and
the entrypoint log. Interpretation fixed *in advance*, so the result cannot be rationalised after
it is seen:

- phase stays `running` ⇒ defect **confirmed**, still live. *Expected.*
- phase becomes `stopped`/`failed` ⇒ defect is fixed, and the cause must be identified, because
  nobody knowingly fixed it.

**Acceptance:** this defect is not closeable by any number of green walks. It closes only on a
negative case.

---

### 11.15 The readiness defect, root-caused: phase measures the wrong layer

**Status:** root cause CONFIRMED by measurement, 2026-08-26 08:35–08:47 UTC (`sn-e2e-walk`, tasks
#27–#29). Fix NOT implemented — the choice is load-bearing and is escalated in §11.15.4.

#### 11.15.1 What was measured

Three inductions against a healthy agent on `dev-2fa880a`, each with a pre-kill control:

| Induction | `sandbox wait` | hub phase | verdict |
|---|---|---|---|
| natural exit — `tmux kill-session`, PID 1 exits | returns | `stopped` | correct |
| SIGTERM — `kill -15 1` | returns | `stopped` | correct |
| SIGKILL — `kill -9 1` | **hangs** | `running` at T+10min | **leak (narrow)** |
| **agent window only** — `tmux kill-window -t scion:agent`, session left alive | n/a — sandbox genuinely alive | **`running` at T+5s, T+35s, T+2min** | **the real defect** |

In the fourth case `tmux has-session -t scion` returns 0 and `list-windows` shows `1: shell*`.
The sandbox is alive. The phase is *accurate about the sandbox* and *useless about the agent*.

#### 11.15.2 The mechanism

`buildEntrypoint` (`pkg/runtime/cloudrun_sandbox_runtime.go:564`):

    tmux new-session -d -s scion -n agent <agent-cmd> \; … \; new-window -t scion -n shell \;
      select-window -t scion:agent; while tmux has-session -t scion 2>/dev/null; do sleep 2; done

The chain is: phase ← `sandbox wait` ← PID 1 ← poll loop ← `has-session` ← **any window open**.
The deliberate second window (`-n shell`) is a plain shell that persists indefinitely. So the
agent process can die while every link in that chain remains truthfully "alive."

**This is not a bug in `sandbox wait`.** `wait` correctly tracks PID 1; PID 1 correctly tracks the
tmux session. Every component is doing its job. **The gap is conceptual: nothing in the chain ever
observes the agent.** "Session alive" was silently adopted as a proxy for "agent alive," and the
`shell` window breaks that proxy by design.

#### 11.15.3 Why this is the top-severity finding in this tier

Measured operator experience when the agent dies:

- phase: `running`
- WebSocket attach: **101, succeeds**
- first frame: `tmuxwindow=shell`
- screen contents: `-bash: dbus-launch: command not found` then `root@sandbox-…:/workspace#`

**A working terminal into a bare root shell that has nothing to do with their agent.** No error, no
banner, no exit status. The failure is *invisible*: every affordance reports success. This is worse
than a blank screen, which at least reads as broken.

And the trigger is not exotic. **Every agent crash, every harness exit, and every normal
completion** lands here. §11.5h's overnight incident — an agent that appeared healthy for hours —
is now explained: it was never the `sh` defect masking a dead sandbox; it was this.

Note the ordering: the SIGKILL leak (#28) is real but confined to OOM, eviction, and operator
kills. **This one fires on the normal path.**

#### 11.15.4 Options, with trade-offs — DECISION REQUIRED

The load-bearing question is not "how do we report accurately" but **"should the sandbox outlive
its agent?"** Each option answers that differently, so the choice is a product decision.

**Option A — drop the `shell` window.** Session dies with the agent, PID 1 exits, `wait` returns,
phase goes `stopped`. One line.
*Rejected as the primary fix.* It destroys post-mortem access: today an operator can attach after
a crash and inspect the filesystem. Trading diagnosis for accuracy is a bad trade in a tier whose
main complaint has been undiagnosable failures.

**Option B — poll the agent window instead of the session:** `while tmux list-windows -t scion |
grep -q '^…agent'; do sleep 2; done`. Also one line, keeps the shell window.
*Rejected for the same reason in weaker form:* PID 1 still exits when the agent exits, so the
sandbox still disappears. It merely narrows the trigger.

**Option C — broker-side agent probe (RECOMMENDED for now).** Keep the lifecycle exactly as it is;
stop inferring agent state from it. The broker's existing 30 s `List()` sweep additionally checks
for the agent window (or the agent PID) and reports a phase distinct from sandbox liveness.
*Cost:* one extra `exec` per sandbox per sweep. *Benefit:* accurate phase, sandbox survives for
post-mortem, no change to entrypoint semantics, and it is **reversible** — a reporting change, not
a lifecycle change.

**Option D — agent-level heartbeat.** The harness reports liveness to the hub; absence marks it
unhealthy.
*Correct long-term and strictly more powerful* — it is the only option that also catches a
**wedged** agent (process alive, doing nothing), which C cannot. *Rejected as the immediate step:*
it needs a protocol and touches every harness, and we should not design that at the end of a
push. **C does not preclude D; C is a subset of D's reporting surface.**

**Attach behaviour must change — but it is NOT independent of the choice above.**

*(Corrected 11:35. The first draft of this paragraph claimed the attach fix was "separable, small,
and should not wait on the lifecycle decision." That is wrong for two of the four options, and the
error is recorded rather than silently overwritten because it would have sent an implementer down
a path that A or B makes moot.)*

The failure mode — attach landing on the `shell` window and presenting it as the agent — **only
exists if the shell window outlives the agent.** So:

- **Under A or B**, the session ends when the agent window closes, PID 1 exits, and the sandbox is
  gone. Attach then fails because there is nothing to attach *to*. The bare-shell case **cannot
  arise**, and a separate attach fix is unnecessary. What is needed instead is that attach-to-a-
  dead-sandbox produces a comprehensible error rather than the current WebSocket 101-then-silence.
- **Under C or D**, the sandbox deliberately survives for post-mortem, the shell window persists,
  and the attach fix is **required** — attach must target the agent window and say plainly when it
  is absent.

The two branches need *different* work, which is why this cannot be started ahead of the decision.
The only genuinely option-independent statement is the weaker one: **attach must never present a
terminal that looks healthy when the agent is gone** — by error under A/B, by explicit status
under C/D.

#### 11.15.5 Acceptance criteria

1. With the agent window killed and the session alive, hub phase is **not** `running` within one
   heartbeat interval (~30 s). Under A/B, where that state is unreachable by construction, the
   restatement is: the sandbox is gone and the hub reports it gone.
2. Attach never presents a terminal that looks healthy when the agent is gone. The two branches
   satisfy this differently and both must be checked against the branch actually chosen:
   **under A/B** by a comprehensible error instead of WebSocket 101-then-silence; **under C/D** by
   attaching to the agent window and stating plainly that the agent is not running.
3. The sandbox remains inspectable after agent death (post-mortem preserved) — unless A/B is
   consciously chosen, in which case this criterion is explicitly waived.
4. Normal agent completion is distinguishable from a crash in the reported state.
5. Negative-case regression test: the #29 induction (`tmux kill-window -t scion:agent`) is
   automated, since §11.14's rule applies — no green run can protect this behaviour.

#### 11.15.6 Independent support for Option C, from the agent that ran the measurements

`sn-e2e-walk`, on standing down, endorsed C unprompted with an argument from lived experience
rather than from design principle:

> *"Post-mortem access to a dead agent's sandbox is the one thing that would have shortened
> tonight by hours."*

This is worth more than the abstract argument in §11.15.4. The overnight cost of this tier was not
that agents failed — it was that failures were **undiagnosable after the fact**, which is why
tasks #18, #22 and #23 all existed. Options A and B pay for accurate reporting with exactly the
capability that shortage cost us. Recorded here so that whoever implements the fix knows the
objection to A/B is empirical, not aesthetic.

---

### 11.16 §11.15 IS WITHDRAWN — the mechanism exists, and the sandbox simply isn't getting it

**Read this before §11.15.** Everything from §11.15.1 to §11.15.6 rests on a premise that is false,
and the four-option menu it produced is retracted. The measurements in §11.15 are still good; the
architecture conclusion drawn from them is not.

#### 11.16.1 What actually detects agent exit

`pkg/config/embeds/templates/default/home/.tmux.conf:85-90`:

```
# --- Agent Exit Detection ---
# When the agent window's command exits, the pane closes and the window is
# destroyed. This hook detects that the "agent" window is gone and kills the session.
set-hook -g pane-exited "if-shell '! tmux list-windows -t scion -F \"##{window_name}\" 2>/dev/null | grep -q \"^agent$\"' 'kill-session -t scion'"
```

The full intended chain, which §11.15 declared missing:

```
agent exits -> pane-exited fires -> no `agent` window remains -> kill-session
   -> poll loop (cloud run) / attach-session (docker) returns
   -> sciontool init's shutdown path: readHarnessExitCode(), classifyExit(),
      session-end hooks, final hub phase report (PhaseError vs PhaseStopped + exit code)
   -> PID 1 exits -> container/sandbox stops
```

So the answer to *"should the sandbox outlive its agent?"* was never open. **It was decided when
that hook was written: it should not.** There is no product decision here, and asking ptone for one
was an error. This is a **parity bug** — see §11.16.3.

#### 11.16.2 How the wrong conclusion was reached, and why it is worth recording

I read the Go and never opened the tmux config. Having found no exit detection in Go, I concluded
none existed, and then designed a four-option replacement for a mechanism that was already there
and already correct. **The gap was in my search, not in the system.**

ptone caught it by asking a question I could not have answered from where I was looking: *"do we
not have parity with docker where the actual process in the sandbox is the sciontool as a process
manager… in docker this detects the agent process exit and then exits causing the container to be
stopped."*

Two distinct errors, both from the same cause:

1. **The §11.15 architecture finding.** Wrong at the top link of the chain.
2. **A prediction I then volunteered to ptone** — that docker must have the same defect, since the
   `-n shell` window exists in `common.go`, `k8s_runtime.go` and `cloudrun_sandbox_runtime.go`
   alike. Wrong, and confidently stated. The shell window is harmless *when the hook is loaded*.

This is the third instance of one failure mode in this project: **concluding a thing is absent
without looking where it would live.** Previously: the browser-attach claim (stale checkout), and
`credential_helper.go` (shell mangling the `git show` ref). Emptiness is not evidence of absence,
and the correct reflex on a negative result is to prove the search would have found the thing.

#### 11.16.3 Why the sandbox does not get the hook — hypothesis under measurement

| Step | Docker | Cloud Run sandbox |
|---|---|---|
| launcher process UID | host user (~1000) | **root (0)** |
| `SCION_HOST_UID` (`cloudrun_sandbox_runtime.go:486`, `common.go:320`) | ~1000 | **0** |
| `setupHostUser()` -> targetUID | ~1000 | **0**, `rootless=false` |
| `supervisor.go:113` `Username != "" && (UID > 0 \|\| Rootless)` | true -> `HOME=/home/scion` | **false -> HOME never set** |
| tmux reads | `/home/scion/.tmux.conf` -> hook loaded | **`/root/.tmux.conf` -> absent -> no hook** |
| agent window dies | session killed, clean shutdown | **session survives** |

Consistent with the bare `root@sandbox-…:/workspace#` prompt measured in #29.

**A comment encodes the error.** `cloudrun_sandbox_runtime.go:573` states *"sciontool init's
hardcoded HOME=/home/scion (supervisor.go:115)"*. `HOME` there is **conditional, not hardcoded**,
and the sandbox does not satisfy the condition. The sandbox entrypoint was written against a
belief about the supervisor that the supervisor does not honour at UID 0.

**Not yet confirmed.** em3 is measuring on the live specimen (`e2e-walk-r2`, preserved overnight
for exactly this): `echo $HOME`, `id`, `ls -la /home/scion/.tmux.conf /root/.tmux.conf`, `tmux -V`,
`tmux show-hooks -g | grep pane-exited`, `tmux list-windows -t scion`. That separates HOME-wrong
from file-not-staged from tmux-too-old-for-`pane-exited`. **If the hook turns out to be registered
and the session still survived, all three are wrong** and the question reopens.

Given §11.14's rule, the fix is not done when a green run passes — it is done when the **#29
negative induction** kills the session.

#### 11.16.4 The wider question the fix must answer

If `HOME` is wrong inside sandboxes, then **everything else the template home provides is also
being missed there** — not just this hook. Sandboxes have been running with a home directory the
processes inside them may never have been pointed at. Whoever fixes this must report what else the
fix repairs or changes, rather than patching the hook alone. Fixing the symptom here would leave a
class of divergence in place and make the next one harder to find.

#### 11.16.5 The relocate candidate is dead for Cloud Run — but the bug is real and latent

em3 answered the filesystem question from source: on a Cloud Run Instance, `cfg.HomeDir` and the
`/scion` root are **on the same filesystem**. `/scion` is created with `os.MkdirAll` — no volume
mount, no `VOLUME` directive in any Dockerfile, no volume configuration in `deploy_instance.go`.
`cfg.HomeDir` comes from `GetAgentHomePath` → `projectDir/agents/<name>/home`, also on the root
filesystem. So every `os.Rename` succeeds and nothing is skipped.

**The relocate candidate therefore dies as an explanation of the missing hook.** Good: it was the
worse of the two, and killing it leaves exactly one cause.

**But the defect in `relocateToScion` is still there**, waiting for the first deployment that puts
`/scion` on a mounted volume:

```
for each entry:  os.Rename(src, dst)  →  on error: log "skipping", CONTINUE
after loop:      os.RemoveAll(src)    →  UNCONDITIONAL
```

A cross-filesystem move fails `EXDEV` on every entry, each is skipped, and the source is then
deleted. The fallback for "could not move your home directory" is "delete your home directory."
The log line says `skipping`, which reads as *nothing happened* — it is the opposite. Tracked
separately (#32) rather than folded into the hook fix; it is not on the current path and merging
them would hide it.

#### 11.16.6 Fix options for the missing hook

All four falsifiers came back intact — no `HOME` from `envFor`, the harness `GetEnv`
implementations, image `ENV`, the entrypoint shell, or broker `cfg.Env`; no `tmux -f` anywhere; no
`/etc/tmux.conf`. With the supervisor's condition false, `s.cmd.Env` stays nil and the child
inherits PID 1's environment, which has no `HOME` at all. tmux then falls back to `getpwuid(0)` →
`/root/.tmux.conf` → absent → no hook. **`HOME` is the single cause.**

| Option | Blast radius | Fixes | Trade-off |
|---|---|---|---|
| **1. Set `HOME`/`USER`/`LOGNAME` in the sandbox's `envFor()`** | cloudrun-sandbox only | the hook **and** everything else the template home provides | leaves the supervisor gap in place for the next caller who hits UID 0 |
| **2. Relax `supervisor.go:113` to set `HOME` whenever `Username != ""`** | **all runtimes** — docker, podman, k8s, sandbox | the cause, everywhere | changes the environment of root-run children in three shipped runtimes; needs its own review and its own regression pass |
| **3. Stop sending `SCION_HOST_UID=0`; drop to the scion user** | cloudrun-sandbox, deeply | takes the normal privilege-drop path | the bind-mounted home is root-owned host-side and the rootfs is read-only; a UID change here risks re-opening EROFS and permission failures that took most of yesterday to close |
| **4. Pass `tmux -f /home/scion/.tmux.conf` in `buildEntrypoint()`** | one line | **only** the hook | leaves `HOME` wrong for the harness, git, and anything else reading `~`; fixes the symptom and hides the class |

**Recommended: 1 now, 2 as a separate follow-up.** Option 1 is sandbox-local, carries no
cross-runtime risk, and repairs the whole class for sandboxes rather than just the hook. Option 2
is the more correct fix and should happen, but it changes behaviour in three shipped runtimes and
must not ride along inside a defect fix that ptone is waiting on. **Option 4 is explicitly rejected**
even though it is the smallest: it would make the hook work while leaving every other consumer of
`~` pointed at `/root`, and the next symptom would be found from scratch.

**Also required, not optional:** correct the two comments at `cloudrun_sandbox_runtime.go:419` and
`:573` that assert a *"hardcoded HOME=/home/scion"*. They are what made this defect invisible to
code review — the sandbox was written against a guarantee the supervisor does not give.

**Open question the fix must answer, not assume:** with `HOME` set correctly, *what else changes?*
Sandboxes have been running with no `HOME` at all. Anything that silently degraded to a root-owned
default may start behaving differently — harness config discovery, git config, npm, the Claude
config directory. The fix should report what moved, not just that the hook now fires.

### 11.17 MEASURED, inside a live sandbox — there are TWO causes, not one

Access unblocked at 12:45. The 4003 was never ours: gcloud 582 hardcodes
`wss://{region}.ssh.run.app/v4` (`api_lib/run/constants.py:36`) and the working endpoint is
`tunnel.cloudproxy.app/v4`. The hidden flag `--iap-tunnel-url-override=wss://tunnel.cloudproxy.app/v4`
restores it. Confirmed working on `e2e-walk-r2`. **Not** IAM, not provisioning-era, not image, not
health — every candidate we spent the morning on was wrong, and the one clue that mattered came
from another team's note.

Route to a sandbox from the launcher: `/usr/local/gcp/bin/sandbox exec <name> -- <cmd>`. The
`worker` container is the launcher, not the sandbox — the diagnostic relayed from the platform team
was run in the wrong container, which is why it reported "no tmux server" as if that were the
finding.

#### 11.17.1 Cause A — `HOME=/root` inside the sandbox (predicted, now measured)

`/proc/1/environ` in the wedged sandbox has `HOME=/root`, `SCION_HOST_UID=0`, `SCION_HOST_GID=0`.
`.scion-entrypoint.log` states the chain in its own words:

```
[sciontool] INFO: Adjusting scion user to UID=0, GID=0
[sciontool] INFO: Successfully adjusted scion user: UID=0, GID=0
[sciontool] INFO: setupHostUser result: targetUID=0, targetGID=0, rootless=false (now euid=0, egid=0)
```

`targetUID=0` and `rootless=false` make `supervisor.go:113` false, so `HOME` is never rewritten and
root's `/root` is inherited. §11.16's chain is confirmed end to end by measurement. The `usermod`
of `scion` to UID 0 is confirmed too.

Correction to §11.16: the child does **not** inherit an empty `HOME` — it inherits `/root`.
Same outcome, wrong detail.

#### 11.17.2 Cause B — the template home is never applied to a sandbox agent home

This is the one nobody predicted, and it is the reason Cause A is not sufficient.

| | contents |
|---|---|
| template on the launcher, `…/templates/global/default/home` | `.gemini/`, `.gitconfig`, **`.tmux.conf`** (3898 B), `.zshrc` |
| provisioned agent home, `/scion/agents/<name>/home` | `.bashrc`, `.claude/`, `.claude.json`, `.scion/`, `agent-info.json`, `.scion-entrypoint.log`, `agent.log` |

**Not one of the four template files is present** — and this holds for all four agents on the
instance, not just the wedged one. The files that *are* there were written later by other
machinery. So `/home/scion/.tmux.conf` does not exist, `/root/.tmux.conf` does not exist, and
**fixing `HOME` alone would have changed nothing.**

This is not `relocateToScion` (#32): a moved-then-deleted home would be empty, and this one has
plenty of content. These files were never written.

**Docker contrast, measured in my own container:** `HOME=/home/scion`, uid 1002,
`/home/scion/.tmux.conf` present, `pane-exited` hook at line 90. Docker parity is real; the hosted
tier is missing both halves of it.

#### 11.17.3 What this does to the fix

§11.16.6's recommendation stands but is now **necessary and not sufficient**. Two independent
defects, each alone fatal to agent-exit detection:

1. `HOME` — set `HOME`/`USER`/`LOGNAME` for the sandbox (§11.16.6 option 1).
2. **Template home application on the hosted path** — mechanism not yet named. Under investigation.
   Until it is named, no fix is authorised: this is the third time a plausible cause has turned out
   to be the wrong one, and the pattern has been guessing where the file *should* be instead of
   looking.

Acceptance for #31 must therefore verify both: `HOME=/home/scion` **and**
`/home/scion/.tmux.conf` present **and** `tmux show-hooks -g` listing `pane-exited` **and** the
session dying when the agent window is killed. Any three of those four passing is still a failure.

#### 11.17.4 Two side findings, recorded so they are not lost

- **Metadata blocking fails in gVisor and it does not matter.** The log carries
  `metadata block: failed to block metadata IP … iptables: exit status 1`. Measured from inside the
  sandbox: `169.254.169.254` is unreachable anyway (curl exit, HTTP `000`), and the emulator on
  `localhost:18380` answers. So the error is noise rather than an exposure — but it is noise that
  reads exactly like a security failure, and someone will eventually act on it.
- **`deploy-instance` cannot complete under service-account impersonation.** gcloud's impersonation
  WARNING lands in the captured output and is spliced into the parsed URL, so the reconcile gate
  polls `https://ssh-probe-WARNING: This command is using service account imperson…` and times out
  after 3m. The instance is created; the command reports failure. Filed as #33 — it sits directly on
  §1's "one deploy command" for any CI or delegated-identity deploy.

#### 11.17.5 Docker gets the template home from provisioning, not from the image

Measured in a docker agent container, which kills the obvious alternative reading of Cause B:

```
/dev/root /home/scion ext4 rw,relatime,...          # bind mount from the host, not image content
/home/scion: .bashrc .claude .claude.json .config .gemini .gitconfig .local .npm .scion .ssh
             .tmux.conf .zshrc agent-info.json agent.log go prompt.md
.tmux.conf: 3898 bytes                              # same size as the launcher's template copy
```

Both runtimes provision a host-side home and bind it in. Docker's receives the template overlay;
the hosted one does not. Same intended path, one of them skipping a step — so this is a defect in
the hosted path, not a facility docker gets for free from its image.

The shape of the gap narrows it further: the hosted home is **partially** provisioned. `.bashrc`,
`.claude`, `.claude.json`, `.scion` and `agent-info.json` are all written; only the four
template-home files are missing. Whatever writes the harness and agent scaffolding runs correctly.
The step that overlays the template's `home/` directory does not.

**Lead, not a conclusion:** the sandbox's PID 1 environment carries `SCION_TEMPLATE_NAME=default`,
and the launcher stores that template under `…/templates/**global**/default/home`. A broker that
resolves the template in a project scope, misses, and continues silently would produce exactly this
partial home. To be killed or confirmed by reading the resolution path, not by argument.

### 11.18 PROOF ON THE REAL PLATFORM: the fix design is sufficient, end to end

Rather than authorise a fix on the strength of a story, I ran the fix by hand on the live sandbox
and watched the whole chain. This consumed the `e2e-window-kill` specimen — a deliberate trade,
made after everything diagnostic had been extracted from it, and the wedged state is trivially
reproducible.

**Procedure, all on `e2e-walk-r2`:**

1. Copied the launcher's template `.tmux.conf` into the agent home at
   `/scion/agents/<name>/home/.tmux.conf`.
2. Confirmed it appeared inside the sandbox at `/home/scion/.tmux.conf`, md5
   `804741bec…` — **identical to the file in my own docker container**. The bind mount was never
   the problem; only provisioning was.
3. `tmux source-file /home/scion/.tmux.conf` against the already-running tmux server.
4. Created a window named `agent` whose command exits on its own after 5 seconds — the natural-exit
   path, not an external kill. Windows were then `[agent, shell]`, the exact shape that previously
   wedged: killing `agent` leaves `shell`, so absent the hook the session survives.
5. Waited 15 seconds.

**Result — every link fired:**

```
tmux has-session      -> sandbox e2e-rerun-1787732812--e2e-window-kill is not running
agent.log             -> [stopped] Session ended
                         Reported final status to Hub (exitCode=0, crash=false)
                         Child exited with code 0
                         Telemetry pipeline stopped
launcher ps           -> no `sandbox wait`, no runsc processes
hub.db agents row     -> e2e-window-kill  phase=stopped
```

Agent command exits → `pane-exited` → no `agent` window → `kill-session` → the entrypoint's
`while tmux has-session` loop returns → `sciontool init` runs session-end hooks and reports a final
status → PID 1 exits 0 → the sandbox stops → the hub shows `stopped`. **This is docker parity,
demonstrated on Cloud Run.**

So the design is settled and the remaining work is delivery, not discovery: get `.tmux.conf` into
the provisioned home (Cause B) and get `HOME` pointing at it (Cause A). Note that step 3 above
bypassed Cause A by sourcing the file explicitly — in the unattended path tmux reads
`$HOME/.tmux.conf` at server start, so **both** causes must be fixed. The file must exist *and* be
where tmux looks.

#### 11.18.1 Three further defects found in the same run

- **Hub session metrics are rejected.** `Failed to report session metrics to hub: hub returned
  error 400: {"error":{"code":"validation_error","message":"session.id is required"}}` — the
  sandbox reports metrics the hub refuses. Filed as **#35**.
- **`exit_code` stays NULL in the hub.** The client logged `exitCode=0` and the row shows
  `phase=stopped` with `exit_code` unset, so the code is transmitted but not persisted. Part of #35.
- **A stale `running` phase is observable right now.** `e2e-rerun-claude` shows `phase=running`
  while no `runsc` process for it exists on the instance. That is #17's symptom, alive after #17
  was closed — its resolution needs re-checking against this observation before the tier ships.

#### 11.18.2 Cause B also kills §1's last clause: no git identity, no push credentials

Checked because the template home contains `.gitconfig` and I wanted the blast radius before
anyone writes a fix. A sandbox agent has **no `.gitconfig` anywhere** — not in the agent home, not
`/etc/gitconfig`.

| | `~/.gitconfig` |
|---|---|
| launcher template | `[safe] directory = /workspace` only — 31 bytes |
| docker agent (measured, mine) | `[safe]` **plus** `[credential] helper = "!f() { echo password=${GITHUB_TOKEN}; echo username=oauth2; }; f"` **plus** `[user] name/email` |
| Cloud Run sandbox | absent |

So the docker path writes the credential helper and the agent identity *on top of* the template —
a second step the hosted path also skips.

**Consequence:** a hosted agent cannot commit (git refuses without `user.name`/`user.email`) and
cannot push (no credential helper). §1 ends with *"watches it commit to a git remote"*. That clause
is dead on arrival for the hosted tier, for reasons that have nothing to do with tmux.

This also disposes of open question **D1** — whether §1 requires a credentialed network push. It no
longer matters which reading D1 takes: the hosted agent cannot do *either* half, so both readings
fail today and both are fixed by the same missing step.

The two symptoms (no `.tmux.conf`, no `.gitconfig`) may or may not share one mechanism. They must
be reported as one only if they demonstrably are — matching symptoms are not evidence of a shared
cause, and this project has already lost half a day to exactly that inference.

##### 11.18.2a CORRECTION, same hour — I overstated it

Caught within twenty minutes of writing §11.18.2, by re-reading my own review queue (D1). The
credential machinery is **not** missing: `sciontool init` configures `credential.helper` at
`cmd/sciontool/commands/init.go:1688` and `:1831` — a GitHub App token refresh when
`SCION_GITHUB_APP_ENABLED=true`, otherwise a `${GITHUB_TOKEN}` helper. On the specimen I inspected
it never fired **because that agent ran in no-auth mode with no token**, which is why the
`[credential]` stanza is absent. My own docker container has `GITHUB_TOKEN` set, which is why mine
has one. Different inputs, not different code paths.

So the correct, narrower statement:

- **Holds:** the four template-home files are not applied to a sandbox agent home. `.gitconfig` is
  one of them, so even the `[safe] directory = /workspace` stanza is missing. Template application
  is genuinely skipped.
- **Withdrawn:** "a hosted agent cannot commit or push." With a token present, `sciontool init`
  writes the helper, and D1's table already records that git works and the sandbox has egress. What
  is unproven remains exactly what D1 said was unproven — a *credentialed* push — and that is a
  credential-provisioning decision, not this defect.
- **Withdrawn:** the claim that this "disposes of D1". It does not. D1 stands as written.

This is the fourth time today I have inferred a missing mechanism from an absent file. The tell is
identical each time: I find something not present, reach for the most structural explanation, and
skip the step of asking what conditions would produce that absence legitimately. Recording it here
because the correction matters less than the pattern.

### 11.19 Delivery plan to a working instance ptone can try (deadline ~14:50)

ptone is offline until roughly 14:50 and wants to *use* the end-to-end experience on return, not
read about it. Everything below is sequenced against that, with the slack stated so it is obvious
where it is being spent.

**The design question is closed** (§11.18 proved the chain on the real platform). What remains is
delivery. Two code changes, one image, one deploy, one walk.

| Phase | Change | Owner | Blocking on |
|---|---|---|---|
| **P1** | **#33** — `deploy-instance` must capture stdout only and validate what it parses. URL must be a well-formed https URL; project number must match `^[0-9]+$`. Unit test feeding warning-prefixed gcloud output through each parser. | developer via em3 | nothing — dispatched 12:57 |
| **P2** | **#31 Cause B** — apply the template home when provisioning a sandbox agent home | developer via em3 | mechanism must be named first |
| **P3** | **#31 Cause A** — set `HOME`/`USER`/`LOGNAME` in the cloudrun sandbox `envFor()`; correct the false "hardcoded HOME" comments at `cloudrun_sandbox_runtime.go:419` and `:573` | same developer, same commit series | nothing — can be written now |
| **P4** | Build and push an omni image | developer | P1–P3 merged |
| **P5** | Deploy a fresh instance and walk §1 steps 0–6 | me | P4 |

**P1 is on the critical path even though it looks like housekeeping.** Impersonation is the only
identity we have in `ptone-experiments`, and until #33 is fixed every deploy bakes a malformed IAP
audience into the instance. Without P1 there is nothing to hand over regardless of P2 and P3.

**Cutoff:** if Cause B's mechanism is not named by **13:40**, P2 gets a deliberately narrow
stopgap — materialise the template home into the agent home at sandbox provisioning time if it is
absent — shipped *as* a stopgap, labelled in the code and in the task, with the root cause left
open. That is the wrong fix in the right place, and I would rather hand ptone a working instance
plus an honest note than a correct diagnosis and nothing to click.

**What P5 verifies, in order:** deploy command exits clean → `run.app` URL redirects to IAP → login
→ create project → start a Claude agent → attach to its terminal in the browser → agent commits →
**agent exits and the sandbox stops, with the hub showing a terminal phase.** The last clause is
new to this walk and is the whole point of #31.

**Known and deliberately out of scope for the demo:** #32 (latent, needs a volume mount to fire),
#34 (real, security-adjacent, but requires ptone's judgement), #35 (noisy 400 on a clean shutdown,
plus `exit_code` never persisted), D1 (credentialed push), D2 (IAP policy breadth), #15 (daemonize
mechanism, still parked). Each is written up; none blocks a demo.

### 11.20 Cause B's mechanism, named and verified (13:47)

`sn-dev-ready` named it and I verified the structural half myself against
`scion/dev-rebase-1294` rather than taking the report.

```
ProvisionAgent (pkg/agent/provision.go:806-823)
  step 1: copy harness-config home/  -> agentHome      # .bashrc, .claude, .claude.json
  step 2: for _, tpl := range chain  -> copy tpl/home/ # .tmux.conf, .zshrc, .gitconfig, .gemini
```

`GetTemplateChainInProject("default", projectPath)` returns an **empty chain** when
`FindTemplateInProjectPath` fails, because the error is discarded around
`pkg/agent/templates.go:413-416` (`return chain, nil`). An empty chain makes the step-2 loop iterate
zero times. Step 1 still runs — **which is precisely why the observed home has `.bashrc` and
`.claude` but none of the four template files.** The partial home was the fingerprint all along.

The Cloud Run sandbox uses the same route as docker (`AgentManager.Start` → `GetAgent` →
`ProvisionAgent`); there is no sandbox-specific override. So this is not a hosted-path branch that
forgot a step — it is a shared function whose failure is invisible.

**What is *not* established:** why resolution fails on the hosted path. The developer's reading —
hub `resolveTemplate` returns nil for `default`, no `TemplateID` is sent, hydration is skipped, the
broker falls back to a local lookup that fails — is a hypothesis, not a measurement. I declined to
ship a fix to a resolution path on that basis at 14:00. Filed as **#37** with the analysis attached.

**What ships today instead:**

1. **The swallow stops.** The discarded error logs at WARN with the template name and the path
   searched. This matters more than the fix: the defect was invisible because that failure had
   nowhere to surface, and the same log is the evidence #37 needs.
2. **A floor in `ProvisionAgent`.** If the chain is empty, copy the *embedded* default template's
   `home/` in the same overlay position. No lookup, deterministic, every runtime. Framed in code as
   a floor — an agent must never come up without its base home — not as a workaround for one broker,
   so it stays defensible after #37 is fixed.
3. **A regression test:** an empty chain must still produce `.tmux.conf`.

This is the difference between a stopgap and a floor, and it is worth being precise about. A
stopgap would have been "copy the template home in the Cloud Run sandbox runtime" — right symptom,
wrong layer, dead the moment #37 is fixed. The floor sits in the function that already owns home
composition, and it is behaviour we would want even with resolution working.

### 11.21 Ambient platform identity is not a registered credential (#41)

Measured 14:30–14:45, 26-Aug, on a fresh one-command deploy. This is a design gap, not a code
defect, and it is the current blocker on §1 step 4.

**The shape of the problem.** Scion has two different notions of "this project can use GCP":

1. **Ambient platform identity** — the process is running on Cloud Run as a service account, and
   ADC is available from the metadata server. Nobody configured this; the platform did it.
2. **Registered project credential** — a `GCPServiceAccount` row in the hub store, scoped to a
   project, with `Verified = true`.

Every credential decision in the hosted path is made against (2). The hosted deploy only ever
produces (1). Nothing bridges them, so a hosted instance behaves as though it has no GCP access
while sitting on a metadata server that would happily hand it a token.

**Where it bites.** `pkg/hub/handlers_agent_create_helpers.go`:

```
hasRequiredAuthCredentials(...)
  gcpSAAssigned := projectHasVerifiedGCPSA(ctx, agent.ProjectID)   // (2), always false when hosted
  ...
  isAuthTypeSatisfied(..., gcpSAAssigned)
      if f.SkippedWhenGCPServiceAccountAssigned && gcpSAAssigned { continue }   // never taken
      if gcpRuntime { skip env-var check }                                       // never taken
```

The skip logic is right and is covered by tests. It simply never runs. Consequence: `vertex-ai` —
the one auth type that needs no operator-supplied secret on GCP — is judged unsatisfiable, and the
harness falls back to demanding `ANTHROPIC_API_KEY`. An operator who has given us a GCP project and
nothing else cannot start an agent.

**Why the obvious fix is not sufficient.** "Seed the row on hosted first boot" fails, because
creation runs the normal verification probe, and that probe asks *can the hub impersonate this
service account*. The account in question is the one the hub is running as. Self-impersonation
requires an explicit `roles/iam.serviceAccountTokenCreator` binding on itself, which no operator
has any reason to have created. Measured verbatim:

```
verified: false
verificationStatus: "failed"
verificationError: "hub service account cannot impersonate
  721899303052-compute@developer.gserviceaccount.com: ensure
  roles/iam.serviceAccountTokenCreator is granted: 403 iam.serviceAccounts.getAccessToken"
```

**A hosted hub cannot verify its own identity.** The predicate is wrong for this case, not merely
unsatisfied. Impersonation answers "can I mint credentials for a *third party*". For the hub's own
runtime identity the question is void — it already holds those credentials.

**Options.**

- **A — seed and short-circuit.** On hosted first boot, write the project-scoped row for the runtime
  SA and mark it verified *by construction*, bypassing the impersonation probe, on the grounds that
  the platform's assignment is itself the proof. Also seed `GOOGLE_CLOUD_PROJECT` and
  `GOOGLE_CLOUD_REGION`, which `deploy-instance` already knows. Keeps one source of truth: every
  consumer keeps asking (2), and (2) becomes true for the right reason.
- **B — widen the predicate.** Make `projectHasVerifiedGCPSA` also return true under hosted mode on
  GCP. Rejected as the primary: it conflates *the hub* having ADC with *the sandbox* having it.
  Those are different processes with different identities, and the sandbox's access is what the
  gate is actually trying to predict. It would also make the predicate lie for non-GCP-backed
  callers that read it for other reasons.
- **C — do nothing; require an API key.** Honest and cheap, but it abandons the §1 claim. "One
  command against your GCP project" that then demands an Anthropic key is a different product
  promise, and a worse one, given the credential is already sitting on the metadata server.

Load-bearing vs reversible: **A is load-bearing** — a row marked verified without a probe is a
trust assertion, and anything later relying on `Verified` inherits it. That is exactly why it is
ptone's decision and not mine. The narrow version (verified-by-construction only when the email
equals the hub's own runtime SA, only under hosted mode) confines the assertion to the one case
where it is tautological.

**Adjacent, needs its own decision.** `deploy-instance` passes no `--service-account`, so the
instance and every agent on it run as the **default compute service account**, which carries
project Editor. Whatever we decide about (A), shipping agent workloads at default-Editor should be
a deliberate choice. If we adopt A, the blast radius of that default grows, because the seeded
identity becomes the one agents actually authenticate with.

### §11.22 The web server does not consume the Layer-1 settings snapshot (#45)

Scion has two config structs that both claim to answer "who is an admin, and who may log in":
`hub.Server.config` and `hub.WebServer.config`. They are not the same object, and only one of them
is kept current.

```
  SCION_SERVER_*  ──► cfg.Hub.AdminEmails ──┬──► hub.Server.config.AdminEmails
                                            └──► ws.config.AdminEmails   (by-value, at construction)
                                                        ▲
  SCION_SEED_* ──► bootstrapKoanf ──► hub_settings DB ──┼── ApplySnapshot ──► hub.Server.config
                        (admin UI writes here too) ─────┘        (never reaches ws.config)
```

`ApplySnapshot` (`operational_settings.go:872`) is the only thing that propagates a settings change
at runtime, and it writes `s.config` exclusively — `AdminEmails` at `:910`, `UserAccessMode` at
`:941`. `WebServer` holds `config WebServerConfig` **as a value** (`web.go:161`), populated once in
`server_foreground.go:2202-2204` and `:2239` from `cfg.Hub.AdminEmails`. Nothing writes it again.

Every browser-facing decision reads the stale copy: proxy/IAP at `web.go:1536`, `:1607`, `:1623`,
`:1653`; OAuth at `:1896`, `:1907`, `:1944`, `:1958`. Every API-facing decision reads the live one
(`handlers_auth.go:1317`, `:1457`). On a hosted IAP instance — where the browser path is the *only*
login path — the settings DB is therefore inert.

**Load-bearing, and the reason this is not just a deploy-instance bug:** the admin UI's *access*
section writes to the DB. Under this topology it can never affect a login, at any time, including
after a restart (restart re-reads `cfg.Hub` from env/yaml, not the DB). A product that offers an
admin screen for access control, and does not wire it to the access-control gate, is worse than one
that offers no screen at all.

**Options.**

- **A — accessor, not copy.** Replace `WebServerConfig.AdminEmails`/`UserAccessMode` with a getter
  the `WebServer` calls per request, backed by the same storage `ApplySnapshot` writes. Single
  source of truth; no second propagation path to keep in sync. Costs a small interface at the
  `WebServer` boundary and requires the storage to be race-safe (today `ws.config` is read
  unlocked on every request precisely because it is immutable after construction — that invariant
  is what any fix has to replace, and `ApplySnapshot` already mutates `s.config` under whatever
  discipline it has, which should be checked rather than assumed).
- **B — ApplyWebSnapshot.** Add a mutator called from `refreshAndApply` alongside `ApplySnapshot`.
  Smaller diff, but it institutionalises two propagation targets and the next Layer-1 field added
  will silently forget one of them. This is the shape that produced the bug.
- **C — construction-time only.** Build `webAdminEmails` from the Layer-1 snapshot instead of
  `cfg.Hub`. Fixes boot (and therefore #44) and nothing else; live admin-UI edits stay inert.

**A is the correct fix; C is the honest stopgap.** B is rejected: it is the existing design, and the
existing design is what failed.

**Recommended sequencing:** land C's effect immediately via `deploy-instance` (set
`SCION_SERVER_HUB_ADMINEMAILS`, labelled, referencing #45) so §1 step 2 works for an operator today;
take A as its own change with its own review, because it touches the request-path read of every
access decision and deserves more than a drive-by.

**Not yet exercised live:** the `user_access_mode` half. The `AdminEmails` half is confirmed by A/B
on `sn-ready`. The read strongly implies the same divergence applies to the login gate, but I have
not run it, and it should be run before anyone treats it as a security finding rather than a
suspicion.

---

## §11.23 — §1 verified end to end (2026-08-26 17:00)

**The yardstick in §1 is met.** Recorded here so the design doc does not lag the measurement.

Instance `sn-step6`, deployed by a single `deploy-instance` invocation from
`us-central1-docker.pkg.dev/ptone-experiments/scion/scion-omni:dev-3f99cb79`
(digest `sha256:4883ecce…920a8`, confirmed running, not a lookalike tag).

| §1 step | Result |
|---|---|
| operator runs one deploy command | PASS |
| opens the run.app URL and logs in | PASS — as **member**, not admin (#44) |
| creates a project | PASS, git-linked and plain |
| starts a Claude agent | PASS **with explicit `harnessConfig`**; the naive path 502s (#48) |
| attaches to its terminal from the browser | tmux 3.4, session `scion` (2 windows), attachable |
| watches it commit to a git remote | **PASS**, verified on the remote side |

**Step 6 evidence chain** (measured inside the sandbox, because `phase=running` is not proof — #17):

```
clone landed         .git + README
/workspace           9p mount, root:root, WORKSPACE_WRITABLE
git commit           COMMIT_OK -> 7336012 on scion/step6--ws6
egress to github     git fetch --unshallow pulled real branches
git push             PUSH_OK
remote-side check    remote log shows 7336012; remote tree contains step6-probe.txt
```

### What this changes in the design's assumptions

1. **§11.x's user-namespace theory for the `/workspace` denial was the wrong mechanism.** The
   correct one is that `mountsFor()` never remapped the mount *destination*. The
   `nobody:nogroup` observation that drove the namespace theory is fully explained by the 9p
   mount's `dfltuid=4294967294` (= `(uint32)−2`), which is what 9p reports for unmapped ownership.
   Recorded because the wrong theory was plausible, self-consistent, and cost hours.
2. **The template home does reach a hosted sandbox** once #31 Cause B is fixed — first direct
   observation. Earlier the design noted this as an open gap.
3. **Cloud Build is a viable second image path.** It is now exercised and produces an image
   functionally equivalent to the GH Actions one; `image-build/cloudbuild-omni.yaml` is committed.
   The design previously assumed a single build path, which turned out to be a single point of
   failure when GitHub Actions went down for over two hours.

### Caveats the design should carry forward, not round away

- **#44** — the one-command deploy prints *"The deployer is seeded as admin"* and the deployer is
  a member. The deploy's own success message is false.
- **#48** — root-caused 17:20; the earlier reading of this caveat was wrong and is corrected here.
  `harness` is silently accepted and ignored (the field is `harnessConfig`), but the 502 that
  follows is **not** the hub resolving to a config it cannot find. The hub resolves to *nothing*:
  with no name from any source and an empty template chain (#37), `hcName` is `""`, so the
  ID/hash stamping block is skipped entirely and the agent dispatches with no harness-config
  identity at all. The broker then invents `antigravity` from its own embedded settings default,
  and an invented name has no hub ID or hash, so hydration is skipped and the broker searches
  on-disk directories that are empty in hosted mode. **The name in the error message is produced
  downstream of the hub, which is why the hub logs are silent about it.**
  Confirmed by a discriminating experiment: `harnessConfig: "antigravity"` explicitly → 201
  running; the identical name reached by fallback → 500 not found.
  The design consequence: the on-disk harness-config fallback is load-bearing on a workstation and
  inert on a hosted launcher, where every config lives only in hub storage. Any design that assumes
  the broker can resolve a bare name locally is wrong in hosted mode. Reproduces on plain projects,
  so it is upstream of #43 and hits a first-time operator sooner.
- **#49** — the workspace is a `depth: 1` shallow clone, so pushes to any remote other than origin
  are rejected with `shallow update not allowed`.
- The verified push was to a **sandbox-local bare repo**, not a credentialed HTTPS remote.
  `GITHUB_TOKEN` is empty; the credential helper is wired but has nothing to supply. Closing that
  gap requires a token decision from the project owner.
