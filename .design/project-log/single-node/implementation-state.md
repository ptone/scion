# Single-node tier — implementation state

**Authoritative progress file.** Updated by `sn-impl-arch` as things land.
ptone offline from 2026-08-26 ~03:15 UTC, back in the AM.

**Goal he left us with:** *"would love to arrive in the AM to a nearly completed
implementation."* Measured against §1 of the design doc:

> An operator with a GCP project and alpha access runs one deploy command, opens
> the resulting `run.app` URL, logs in, creates a project, starts a Claude agent,
> attaches to its terminal from the browser, and watches it commit to a git remote.

---

## ⚠️ The most important finding of the night (03:43 UTC)

**No Instance has ever run the omni image. The single-node tier has never actually run
on Cloud Run.**

Found by `sn-e2e-walk`, independently verified by me. Every Instance in
`ptone-experiments`/`us-east4` runs `docker.io/library/python:3.11`:

| Instance | What it actually is |
|---|---|
| `iap-demo` | IAP spike probe container — **not the hub** |
| `q2-control` | OQ-17 control — probe container |
| `val-delete-2` | delete-defect probe container |

So everything validated so far — P1–P5, the delete workaround, OQ-16, OQ-17 — was
measured against **probe containers exercising the sandbox/runsc and IAP surfaces**, not
against the Scion hub. Those findings are still good; they are just narrower than the
phase table implies. "Landed" in that table means *the code is on the branch and its
mechanism was validated in isolation*, *not* that the tier runs.

**This reframes the critical path.** It is not "finish P6". It is:

> **publish omni → deploy an Instance that runs it → then everything else.**

`sn-impl-em3`'s deploy command, `sn-e2e-walk`'s walk, and any claim about §1 are all
downstream of an artifact that does not yet exist. `sn-omni-ci`'s manual publish (D7) is
therefore not housekeeping — it is the gating item.

**Correction to my own records:** `iap-demo` was listed in the cost table and review
queue as "awaiting ptone's browse." He would have opened it and found a Python identity
viewer. That entry was misleading and is fixed below.

## Phase status

| Phase | State | Where |
|---|---|---|
| **P0** security (dev auth off-loopback) | ✅ Landed, **PR #1265** open, CI green | `scion/security-fix-p0-s1` |
| **P1** runtime registration | ✅ Landed | `scion/dev-rebase-1294` → **PR #1266** |
| **P2** omni image (chained) | ✅ Landed | ″ |
| **P3** Run/Delete/List | ✅ Landed | ″ |
| **P4** exec control plane | ✅ Landed | ″ |
| **P4a** timeout-bounded delete | ✅ Landed **and validated live** — no code changes | ″ |
| **P5** ephemeral honesty | ✅ Landed | ″ |
| **§4.2a-ci** omni CI | ✅ **Closed 03:36** — 3 commits, verified on branch | §4.2a-ci-rev |
| **P6** auth + one-command deploy | ✅ **Code landed 04:15** at `59b1f102`, **E2E untested** | §11 |
| ~~**P7** transport-token refresh~~ | ❌ **DELETED — does not exist.** OQ-2 answered by measurement: sandbox→launcher works directly, 1.64 ms. Agents never cross the IAP perimeter. | §11.10, §11.11 |
| **§1 end-to-end** post-login path | 🔨 **In flight** — `sn-e2e-walk` | brief `sn-e2e-walk.md` |

**Why `sn-e2e-walk` exists (dispatched 03:40):** every other workstream tonight is about
getting an operator *to a login page*. §1 does not stop there — it requires logging in,
creating a project, starting an agent, **attaching to its terminal from the browser**, and
**watching it push to a git remote**. Those have been exercised piecemeal by CLI and unit
tests; as far as I can establish they have never been walked end to end against a real
IAP-fronted Instance. It runs against the existing `iap-demo` Instance rather than waiting
on the deploy command, so "can we deploy it" and "does it work once you're in" fail
independently. The two steps I expect to break are the **PTY/WebSocket attach through IAP**
(`pty_handlers.go` is one of only two real integration points in #1266) and **git push**,
which needs sandbox egress and therefore brushes **OQ-14**.

## Integration branch

**`scion/dev-rebase-1294` is the single integration branch.** ptone, 03:05 UTC:
*"we should be consolidating onto one or just a few integration branches."*
`p4a-delete`, `p4a-delete-v2`, `p5-ephemeral` deleted 03:11 after function-level
supersession check. **Do not create a fourth.** Fork PRs are fine for getting CI
runs (ptone, 03:14).

## Open questions on the critical path

| OQ | Status |
|---|---|
| ~~**OQ-2** sandbox→launcher reachability~~ | ✅ **CLOSED 04:15, by measurement.** Works on the **launcher's link-local address**, **1.64 ms median** (hairpin: 35 ms, needs OIDC). Loopback fails (sandbox has its own netns); AF_UNIX fails (does not cross gVisor). **P7 deleted.** Address is **discovered at runtime** by the launcher and injected as `HUB_HOST` — no hardcoded `169.254.x`. **Two constraints survive:** `--allow-egress` mandatory and all-or-nothing (⇒ **no network-isolated agents are possible in this tier**); **metadata server unreachable from the sandbox even with egress** (⇒ constrains OQ-14). |
| **OQ-14** Vertex/ADC under `--allow-egress` | Open, not dispatched. Not blocking the browser→hub path. |
| ~~OQ-15~~, ~~OQ-16~~, ~~OQ-17~~ | Resolved by test. OQ-17 answered **negative** — `invokerIamDisabled: true` is mandatory. |

## Decisions made while ptone was away

| # | Decision | Basis |
|---|---|---|
| D1 | **Region-level IAP scope.** | ptone approved 03:04 UTC before going offline. Revisit trigger: multi-tenancy in one project (§11). |
| D2 | **Bootstrap token retired for this tier**; deploying operator's identity seeds `AdminEmails`. | Mine. IAP already proves identity before the hub sees the request; a second credential adds a step and protects nothing. `--admin-email` kept for CI-SA deploys. §11.6. |
| D3 | **Two PRs, security fix split out.** | Mine. Merge #1265 first. Both annotated. |
| D4 | **`gofmt` authorized** on `dev-rebase-1294`. | Mine. Formatting-only; not masking a real failure. |
| D5 | **omni builds amd64-only in CI.** | Mine. Cloud Run is amd64 and the thick base is amd64-only anyway (§4.2b). §4.2a-ci-rev. Since confirmed from CI run logs — all three stages printed `Platforms: linux/amd64`. |
| D6 | **Instance writes go over REST v2 with a full body — no gcloud/REST hybrid.** | Mine. Forced by measurement: gcloud 582.0.0 has `--sandbox-launcher` and `--invoker-iam-check` but **no `--iap`**. A hybrid would open a window where the Instance serves with no IAP, and split the three perimeter fields across two calls. §11.5a. |
| D7 | **Publish one omni image by hand tonight** (`workflow_dispatch`, dev tag, not `latest`). | Mine. The release trigger fires on tag push and we are not tagging tonight, so nothing has published — and the §1 end-to-end test needs a real pullable `--image`. |

## Corrections made to the design doc tonight

Recorded because each was a doc that would have misled an implementer:

1. **OQ-17 row** carried the *falsified* answer ("invoker check stays ON"), contradicting
   the measured finding in the same document. A P6 implementer following it would have
   shipped a 401.
2. **§4.2a-ci** was wrong in both directions — overstated the blocker (CI *can* build
   omni; the default builder is `local-docker`) and understated the gap (**nothing**
   publishes any image automatically, which breaks the one-command deploy).
3. **P4a / OQ-16** rows still read "untested"; both are validated and closed.

## §4.2a-ci — closed 03:36 UTC

Three commits on `scion/dev-rebase-1294`, all verified present on the remote by me:
`f9114f3a` (omni in `build-images.yml` both trigger blocks + `cloud-build.sh` error
reworded), `18b80f91` (release trigger in `build-release.yml`), `1f6b69fa` (temp
measurement workflow removed — confirmed gone from the tree).

**My ~14 GB disk risk was wrong and was corrected empirically.** Current `ubuntu-latest`
is 145 GB total, 87 GB free before any cleanup; the full six-image omni chain consumed
~10 GB, peaking at 49 GB used / 96 GB free. Final `scion-omni` is **7.39 GB**. No
free-disk-space step is required. The 14 GB figure was an old runner spec.

**Caveat for ptone: the release trigger has never fired** — no tag has been pushed since
it landed. "Wired" is not "proven."

## P6 landed 04:15 — `59b1f102`, spot-checked

Verified on the branch rather than taken on report: `cmd/deploy_instance.go` (660 lines),
`cmd/deploy_instance_test.go` (365 lines, 20 tests incl. gate 2 and audience pinning),
allowlist and project-check wiring, two project logs. The v1/v2 hazard comments I asked
for are present and accurate at the call sites. Code review APPROVE, no Critical/Required
findings; 5 Optional/Nit at `/scion-volumes/scratchpad/findings/p6-deploy-instance-review.md`.

**The gap that matters: E2E is untested.** `sn-impl-em3` deferred it on the grounds that
it needs "an operator with `run.instances.create`" — **but we have that**, by impersonating
`scion-instance-gym@`, which is how three Instances were created tonight. So the deferral
was unnecessary and I have redirected `sn-e2e-walk` to test **the command itself** rather
than its own hand-rolled create script.

That distinction is the point: §1 is not "an Instance exists behind IAP", it is *"an
operator runs one deploy command."* A walk that provisions its own Instance would prove
the platform works while leaving the actual deliverable untested.

## The transport question, settled by measurement (03:46–03:52)

**v1 and v2 model the Instance resource differently, and neither alone can express this
tier's Instance.** Full detail at §11.5a–c; the operative table:

| Field | REST v2 | gcloud (v1) |
|---|---|---|
| `iapEnabled` | ✅ create **and** PATCH | ❌ no `--iap` flag |
| `invokerIamDisabled` | ✅ | ✅ |
| `sandboxLauncher` | ❌ **field does not exist** | ✅ |

v2 does not merely refuse to write `sandboxLauncher` — it **omits the key on read**, so a
v2 read-modify-write silently produces an Instance that cannot launch sandboxes. Standing
hazard; every v2 call site needs a comment naming what that surface cannot see.

Settled sequence: **gcloud create with `--sandbox-launcher`, invoker check left ON (born
closed) → one v2 PATCH flipping `iapEnabled` + `invokerIamDisabled` together → gcloud for
later image updates** (measured to preserve both perimeter fields). Verified on a live
throwaway Instance: unauthenticated fetch returned 302 to `accounts.google.com` with
`x-goog-iap-generated-response: true` — i.e. §11.5 gate 2 passes.

**I reversed myself twice here before measuring.** First mandated all-REST, then permitted
the hybrid, then found the hybrid was compulsory. All three probe Instances
(`arch-503-probe`, `arch-idem-probe`, `arch-create-probe`) have been deleted.

## Delete-defect: the persistence claim is retired, and the news is good

`sn-impl-em2`, clean within-run probe on its own Instance (03:53):

> The `sandbox` CLI has a **120 s internal timeout.** On expiry it exits rc=1 **and takes
> its `runsc` child with it.** Both orphan types — `delete` and `state` — are gone by
> t = 2m10s. PPIDs recorded; wrappers reparent to init, children do not survive them.

Consequences:

1. The **"worse persistence profile"** claim is retired, as is rev 2's "becomes a zombie
   and is eventually reaped". Both were cross-run observations at unrecorded times.
2. **Our P4a workaround's benign-failure story is now established rather than assumed** —
   we abandon a wrapper that self-terminates 110 s later and reaps its own child.
   `reapOrphanedRunsc` is belt-and-braces, **not load-bearing. Do not remove it**: a 120 s
   TTL measured on one build is not a contract.
3. The defect stands. A two-minute wedge per operation is still a defect.

Rev 3 to lead with the TTL and **visibly retract** the superseded claims. t = 30 min point
still running to rule out late reappearance.

## Open thread — a finding that touches our own workaround's rationale

`sn-impl-em2`'s state-char probe (03:07 UTC) shows the `runsc delete` orphan is in
state **S** (sleeping), **not Z (zombie)**, at t = 60 s.

Rev 2 of the defect report claimed the delete orphans *"became zombies and were
eventually reaped"*, and **that observation is a load-bearing part of why our
timeout-and-abandon workaround looks benign.** The measurement says that at one minute
it is a live, kernel-state-holding process; rev 2's zombie observation was made at an
unrecorded, later time.

Re-probe at **t = 10 min / 30 min** requested, with both orphan types in the same
session so the comparison is within-run rather than across runs. This resolves two
things at once: whether the upstream "worse persistence" claim survives, and whether
our own benign-failure story does. **Publication of rev 3 is held until it returns** —
the draft currently asserts a comparison its own table contradicts.

## Cost — running Instances

Swept 03:58 UTC on ptone's instruction ("clean up agents whose work is done").

**Agents deleted** (all `--preserve-branch`): `spike-iap` (OQ-17 answered), `dev-iap-test`
(work verified merged at `e3244649`/`0d8357ec`), `dev-deploy-cmd` (superseded by
`dev-deploy-rework`), plus the earlier five stopped ones — `sn-pr`, `dev-rebase-1294`,
`spike-uds`, `spike-uds-b`, `sn-impl-em`.

**Instances:**

| Instance | Status |
|---|---|
| ~~`iap-demo`~~ | **Deleted.** Purpose retracted — it was a Python identity viewer, not the hub. IAP setup is now a ~2 min reproducible recipe (§11.5b/c), so nothing is lost by not keeping a reference alive. |
| ~~`arch-503-probe`, `arch-idem-probe`, `arch-create-probe`~~ | **Deleted.** My own transport probes. |
| `q2-control` | Kept — OQ-17 control, deliberately untouched. OQ-17's answer flipped three times; P6 may need a clean comparison. |
| `val-delete-2` | Contested. em2 has moved to its own Instance; asked `spike-oq2` whether it is still in use before deleting. |
| `val-persist-em2` | em2's fresh persistence probe, running to t = 30 min. |

**Not swept:** long-idle agents belonging to other projects (`role-enforce` stalled 11 h,
`nc-arch` 13 d, `gke-deploy-lead` 9 d, `cf-tunnel-arch` 9 d, `gd-em` 8 d and others). They
are not mine to judge — flagged to ptone rather than deleted.

### Instance-sharing collision — process lesson

`spike-oq2`, believing creates were 503ing, reached for `val-delete-2` and ran two PATCH
operations (~03:23, ~03:29) plus a stop/start (~03:38–03:40). Three lifecycle events.
`sn-impl-em2`'s orphan-persistence measurement depended on processes living in that
Instance's process table from ~03:07, so the **t = 10 min / t = 30 min data was destroyed
— by the 03:23 PATCH, before the restart I first flagged.**

Two causes, both mine to fix:

1. **The cost table recorded that `val-delete-2` was held, but no brief said so.** State a
   resource is reserved in the brief of every agent that might want it, not only in the
   table the reserving agent reads.
2. **`spike-oq2` inferred an outage from a failure instead of reading the error.** The
   creates were failing on `PERMISSION_DENIED: run.instances.get` — it was not
   impersonating `scion-instance-gym@`. I disproved the outage by creating an Instance
   first try with `--impersonate-service-account`.

That is the third time tonight a mechanism was inferred rather than measured, and **two of
the three were mine** (CI disk headroom; §4.2a-ci in both directions). The recurring shape
is *reasoning from an error string or a commit message to a cause.* Get the actual error.

---

## OQ-2 closed 04:15 — a phase deleted, and two constraints that outlive it

`spike-oq2` returned direction-by-direction data and I acted on it immediately:
**§11.11's contingent P7 design is removed from the doc and P7 is struck from the phase
table.** That subsection was written *so that a bad answer would not also cost a design
cycle*; a good answer came back, and throwing the design away is the success case. What
remains at §11.11 is a two-bullet stub pointing the pattern at OQ-14 — nothing more,
because a contingent design for a phase that does not exist is a trap for the next reader.

**The measured answer:** the sandbox reaches its launcher on the **launcher's link-local
address**, **1.64 ms median**. Loopback fails (the sandbox gets its **own netns** —
`127.0.0.1` is the sandbox, not the launcher). AF_UNIX fails (does not cross the gVisor
boundary). The public hairpin works at 35 ms but needs OIDC: 21× slower for strictly more
machinery. Launcher→sandbox (`sandbox exec`, full lifecycle) is unchanged.

### Three things I pushed for beyond the headline, and got

1. **No hardcoded `169.254.8.1`.** I asked specifically, because depending on an
   undocumented link-local constant fails months later *as a hung agent*, not as a config
   error. It is not needed: the **launcher** derives its own link-local IP via
   `getsockname()` on a UDP socket connected to `8.8.8.8`, and injects it per sandbox as
   `HUB_HOST`. Platform renumbering self-heals on next launcher start. **Written into the
   design as a rule**, not left as a note. Residual risk is only the gateway ceasing to
   route to launcher link-local addresses at all — unhedgeable either way.

2. **`--allow-egress` is mandatory *and* all-or-nothing.** Without it the sandbox has no
   working interfaces at all, launcher included. So — and this is a property of the tier,
   not a footnote — **there is no configuration in which a sandbox talks to its launcher
   but not to the internet. Network-isolated agents that still function are impossible
   here.** The deploy docs must say this outright rather than let an operator discover it.
   (§4.3c already had the mandatory half from AC-0; OQ-2 supplies the *no partial
   isolation* half, which is the part with security consequences.)

3. **The GCE metadata server is unreachable from a sandbox even with egress on.**
   `169.254.169.254` times out while launcher link-locals answer. This **constrains
   OQ-14**: ADC-via-metadata looks unavailable regardless of egress policy, so the question
   is no longer "does Vertex work" but "can the launcher mint and push credentials in".
   That is exactly the retired P7 shape — which is why I kept a pointer to its reasoning
   instead of deleting it outright. Its rejected alternative *"let the sandbox reach the
   metadata server and mint its own"* is now **rejected twice over**: unacceptable blast
   radius, **and it does not work**.

### Critical-path effect

OQ-2 was the last open question that could still have **added** a phase. It did the
opposite. **OQ-14 is now the most consequential open question** — open, unowned, and
touching 3 of the 5 shipped harnesses. It is no longer a "nice to know".

`spike-oq2-box` deleted by the spike, confirmed via API. Agent complete.

## OQ-14 promoted to the critical path — and we may already own the answer

OQ-2's metadata-server negative makes OQ-14 the biggest open item: no ADC-via-metadata
means `vertex-ai` and `gcloud-adc` are unavailable, i.e. **3 of the 5 shipped harnesses**.
Before escalating to the Cloud Run team I went looking in our own tree, and found this:

**`pkg/sciontool/metadata/` is a GCE metadata server emulator we already ship.**
`server.go` serves the standard endpoint format (assign/block modes, SA email, project ID);
`iptables.go` installs a DNAT redirecting `169.254.169.254:80` to it so tools that hardcode
the metadata IP are transparently intercepted. `pkg/transportauth/transportauth.go:51`
already reasons about that hijack.

That is precisely the shape OQ-14 needs, and OQ-2 supplies the missing half: the launcher
is trusted, can mint, and is **reachable from the sandbox in 1.64 ms**. Hypothesis:
**run the emulator on the launcher; inside each sandbox redirect `169.254.169.254` to the
launcher's link-local address.** Agents get working ADC with no harness changes and no
durable credential in the sandbox.

**Two things can kill it, and they are ordered:**

1. **Can an iptables NAT rule be installed inside a gVisor sandbox?** Needs NET_ADMIN, and
   runsc's netstack may not implement the `nat` table at all. **This is the gate — if it
   fails, the approach fails**, so it is tested first and alone.
2. **Can the emulator serve a non-local client?** Written as a localhost sidecar. Rebinding
   to the link-local address is probably trivial; who *else* on that network can then reach
   it is the question worth asking.

**Fallback if (1) fails:** `GCE_METADATA_HOST` env injection, which the Google SDKs honour —
less transparent, no iptables. Need to know whether it covers the harnesses we care about.

Dispatched to `sn-impl-em3` (idle after P6 landed) as **investigate-and-report, not
implement** — I want the design decision before code. Note this is the *same shape* as the
retired P7 design, which is why its reasoning was preserved rather than deleted.

## 04:31 — "the platform is down" was wrong for the third time tonight

`sn-e2e-walk` reported that **all** writes to the Cloud Run Instances API in us-east4 were
failing — create *and* update, v1 *and* v2 — while reads succeeded. Careful evidence: ten
attempts, timestamps recorded, each verified against `instances list` rather than trusting
gcloud's output. It concluded a regional write outage and said it was stuck.

**I tested it instead of believing it.** From this container, same region, same API, same
impersonated SA, at 04:31: `gcloud beta run instances deploy arch-write-probe` — **succeeded**,
confirmed present in `instances list`. Writes are not broken.

**So the failure is caller-specific, not regional.** Most likely throttling after 8 creates in
six minutes plus retries, surfacing as 503 rather than 429. Which means the retry-with-backoff
loop was *sustaining* the condition it was trying to ride out.

**This is the third outage inferred from an error stream tonight, and two of the three were
mine.** The shape is identical every time: *an error you receive tells you about your request,
not about the world.* Recorded here because it has now cost more time than any real defect.

**Unblock:** told the agent to stop all writes including backoff, and to send me the exact
command — full invocation, no paraphrase — which I will run from a caller that is not
throttled. It has the configuration knowledge, I have the working call path. Division of
labour beats waiting.

**Caveat this does not remove:** B11/B13's create-503s were real and did cost two agents
~30 min each. What is now in doubt is only the *regional* framing. If the true cause is
per-caller rate limiting, that is arguably a worse operability story for a tier whose premise
is one deploy command — an operator retrying a failed deploy would be digging their own hole.
**Worth asking the Cloud Run team what the actual limit is and why it is a 503 rather than a
429 with a Retry-After.**

## 04:45 — I was wrong about the 503s, in the other direction

An hour after criticising `sn-e2e-walk` for inferring a regional outage from its own error
stream, **I inferred the opposite from a single success.** Three writes from this container
have since failed: q2-control update at 04:42 and 04:44, and a fresh `e2e-omni` create at
04:45 — all `503 UNAVAILABLE` from `us-east4-run.googleapis.com`. My 04:31 create succeeded;
these did not.

**Corrected position, stated at the confidence the evidence supports:**

- **Reads work reliably** — `list`, `describe`, always 200.
- **Writes intermittently 503 for multiple independent callers**, on both the create and
  update paths, v1 and v2.
- **Writes do sometimes succeed** — mine at 04:31, `iap-demo-restore` at 04:15–04:19.

That is **intermittency**. Not a clean outage, and **not caller-specific throttling** as I
claimed. `sn-e2e-walk`'s framing was better supported than I allowed. My error was worse
than the one I corrected, because I had just finished naming the pattern.

**Fourth inference-from-error tonight; three of the four are mine.** Writing that down
plainly is the only thing that makes the record worth anything.

**Operational effect:** we cannot rely on getting an Instance when we want one. The
response is to move work that does not need the API forward — which is exactly what
`sn-e2e-walk` did unprompted, and it paid immediately (next section).

## 04:45 — a shipped defect that would have made every operator deploy dead on arrival

Found by `sn-e2e-walk` reading code while blocked on the API. **`image-build/omni/Dockerfile`**:

```
CMD ["scion","server","start","--foreground","--host","0.0.0.0","--enable-hub"]
```

Missing `--enable-web` and `--enable-runtime-broker`. Traced to source:

- `--enable-hub` alone ⇒ hub listens on **9810** (standalone default, `cmd/server.go:242`),
  while **Cloud Run routes to 8080**. Nothing serves there.
- No `--enable-web` ⇒ no combined mode mounting the hub on 8080
  (`server_foreground.go:335-363`).
- No `--enable-runtime-broker` ⇒ no broker ⇒ **no PTY ⇒ §1 step 5 is impossible.**

`diGcloudDeploy()` (248–272) sends no `--command`/`--args`, so `scion deploy-instance`
inherits it. **Every operator deploy today produces an Instance that answers nothing on
8080** — §1 failing at "open the URL", with no diagnostic.

**Ruling: fix the Dockerfile CMD (a), not the deploy command (b).** The image must be
correct standing alone, so anyone running omni outside `deploy-instance` gets a working
hub; (b) splits *how to launch this image* across two places and leaves the image quietly
broken for every other caller. **Not a one-liner: the published tag is broken and needs a
republish**, and anything pinned to `dev-de79f5b3d2a75b24bd9d4c7de4e470c7881ead2a` stays
broken until then. Assigned to `sn-impl-em3`, ahead of OQ-14 — this blocks §1, OQ-14 does not.

**Process point worth keeping:** this was found *because* the API was unreliable and the
agent switched to static review rather than sitting in a retry loop. The most valuable
artifact of the night came from being blocked well.

## 04:54 — OQ-14 answered, and the answer that worked first is not the one we ship

`spike-oq14` ran the **whole** ADC chain after I rejected its first report for proving only
what OQ-2 already knew:

1. metadata reachable on the launcher's link-local `169.254.8.1:18380` ✅
2. `google-auth` inside the sandbox with `GCE_METADATA_HOST` set ⇒ **real token**, 1024
   chars, `expires_in: 1799` ✅
3. that token against `cloudresourcemanager.googleapis.com` ⇒ **real API call succeeded** ✅
4. `iptables -t nat` inside the sandbox ⇒ **not available**; no `/proc/net/ip_tables_names`,
   gVisor's netstack has no `nat` table ❌

**OQ-14 is answered: ADC works.** `vertex-ai` and `gcloud-adc` are not stranded, so the
3-of-5-harnesses risk is retired.

### The ruling that matters: emulator, not proxy

The spike proved it with a **transparent proxy** to the real metadata server. It works and
it is a few lines. **It must not become the design.** Its own output shows why — it handed
the sandbox a **real, unrestricted SA token with full authority**, which is exactly the
alternative I rejected outright while P7 was alive (*"hands agent-controlled code the
runtime SA's full authority indefinitely"*). The proxy reproduces that blast radius; it only
moves the hop. The emulator can **block, scope, and audit**; the outstanding work is a
one-line bind change.

Recorded loudly in §11.12 because the pull toward the proxy will be strong: **the thing that
worked first is not the thing to ship.**

**Open detail flagged back to em3:** the token returned was for the **default compute SA**,
not the Instance's configured runtime SA. If the emulator silently serves the default
compute SA, that is a second, quieter privilege problem.

### What the iptables negative costs

`GCE_METADATA_HOST` is now the **only** mechanism — no defence in depth. Anything hardcoding
`169.254.169.254` and ignoring the env var **fails**: a bare `curl` in an agent's shell
command, older libraries. §4.10 predicted this posture; it is now measured. **Which of the
five shipped harnesses honour the env var is open.**

## Omni CMD fix verified — `eadcfd2` on `scion/dev-fix-omni-cmd`

Checked the Dockerfile on the branch rather than taking the report: all three flags present
(`--enable-hub --enable-web --enable-runtime-broker`), with a comment explaining combined
mode. Sequenced **ahead of** the OQ-14 emulator work: the CMD blocks §1, OQ-14 does not.
**Still needs a republish before it means anything** — the pinned digest stays broken.

## 05:05 — heartbeat cycle: two hypotheses tested and falsified, one false alarm caught

**Write availability, tested not assumed.** At 05:00 an *update* to `arch-write-probe`
succeeded while a *create* 503'd. Two hypotheses, both tested, both dead:

1. *"Creates fail at an Instance quota (~3)."* Deleted `arch-write-probe` to free a slot,
   leaving two Instances, retried the create — **still 503.** Falsified.
2. *"Updates work, creates don't."* The `q2-control` update then 503'd at 05:05. Falsified.

**Supported statement remains plain intermittency**, now with a possible per-resource
component: `q2-control` carries a stuck `WaitingForOperation` condition at
`observedGeneration 5`, and every update against it has failed while an update to a
healthy Instance succeeded. **Self-inflicted:** I deleted `arch-write-probe` — the one
Instance I had just proven updatable — to test the quota hypothesis. Freeing a slot cost
us the only working write target.

## 05:04 — a §1-blocking "critical" that was an artefact of reading the wrong tree

`sn-e2e-walk` reported **`CloudRunRuntime.Run()` unimplemented ⇒ §1 step 4 is a dead end**,
with a full call chain and a design-gap argument that no sandbox runtime exists. Serious
enough that I checked the branch before believing it.

**It is false on `origin/scion/dev-rebase-1294`:**

- `pkg/runtime/cloudrun_sandbox_runtime.go` exists (plus the delete workaround and both
  test files). *"sandbox does not appear in `pkg/runtime/`"* is true only of `main`.
- **`CloudRunSandboxRuntime.Run()` is fully implemented** (line 569), as are `Stop`,
  `Delete`, `List`, `Attach`, `Exec`, `GetWorkspacePath`, `watchSandbox`.
- The runtime-selection chain was wrong for this tier: `factory.go:94-104` states that on
  Instances **`K_SERVICE` is NOT set** — `CLOUD_RUN_INSTANCE` is. The factory probes for the
  sandbox binary and selects **`cloudrun-sandbox`**. The `cloudrun` runtime it traced is
  only reached for a real Cloud Run *Service*.

**Root cause: the agent had cherry-picked `deploy_instance.go` from `59b1f10` onto `main`**,
so it was reading a tree that exists nowhere. Told to re-clone and redo the analysis.
Fourth inference-from-a-signal tonight; three of the earlier ones were mine.

**Side effect worth acting on:** `cloudrun_sandbox_runtime.go` carries a C7 warning that
`vertex-ai`/`gcloud-adc` "may not work — `--allow-egress` does not grant GCP service access
(§4.3c)". **OQ-14 has now falsified that**; the warning must be updated when the emulator
bind lands, or it will tell operators the opposite of the truth.

## Findings #3 and #4 — ruling

**#3 is real and assigned:** `deploy_instance.go:423` hardcodes `"--member=user:"+email`,
but `--admin-email`'s own help says it is for CI **service account** deploys, and
`user:…gserviceaccount.com` is not a valid member.

**#4 — instinct right, remedy overruled.** The agent proposed `deploy-instance` pass
`--command`/`--args` so it is correct even with a broken image CMD. That buys robustness by
creating **two sources of truth for how omni starts**, which will drift: a later flag change
updates the image while the deploy command keeps passing yesterday's args, producing an
Instance that *starts* and is wrong — harder to find than the bug we just fixed.

> **Ruling: the image owns how it starts; `deploy-instance` owns verifying that it did.**
> Add a **post-deploy smoke check** — after ready, fetch the URL and assert the hub answered.
> Behind IAP the success signal is a **302 to `accounts.google.com` with
> `x-goog-iap-generated-response: true`**, an assertion verified twice tonight on live
> Instances. This catches the whole class (wrong port, crash loop, missing binary, bad env),
> and turns *"the URL does nothing"* into *"deploy failed, here is why"*. **Now P6 acceptance
> criteria.**

## Critical path, 05:05

**One item: republish omni with the fixed CMD.** `1a70134` fixes the Dockerfile; the
published tag is still broken, so nothing can be deployed and tested. Two known obstacles
from earlier tonight — the **PAT lacks `actions` scope** (`workflow_dispatch` 403s) and the
one-shot PR-triggered workflow was removed in `7b842c06`. Asked em3 to state its intended
route **before** taking it, so that if it needs a scope only ptone can grant I ask now
rather than discover it in an hour.

## 05:20 — pre-staging the deploy target, and why it is not a stray resource

`e2e-omni` exists in `ptone-experiments/us-east4` running `docker.io/library/python:3.11-slim`.
**Deliberate placeholder.** The scarce operation tonight is not the image, it is a successful
**create**: creates 503 intermittently, updates against *healthy* Instances have succeeded.
So I spent a working create window buying an Instance we can **update** when the republished
tag lands, instead of gambling on a create at the moment we need one. Handed to
`sn-e2e-walk` as its walk target; `sn-impl-em3` told not to touch it.

`q2-control` is now judged a **bad target regardless of the 503 weather** — it carries a
stuck `WaitingForOperation` at `observedGeneration 5` and every update against it has
failed, while an update to a healthy Instance succeeded. It stays reserved for B12 only.

## 05:25 — doc consolidation while the build runs

Three edits, all promoting measured results out of the OQ narrative into the sections a
developer will actually read:

- **§11.12 retitled** from "the leading approach, and the one thing that decides it" to
  **ANSWERED**. The old header and its "Status: hypothesis" preamble contradicted the
  answer sitting 60 lines below them. Also repaired a mangled paragraph left by the 04:29
  correction.
- **§4.10 strengthened from prediction to measurement.** It said sandboxes are "unlikely to
  grant `NET_ADMIN`". True but weak: **the `nat` table does not exist in gVisor at all.**
  So `GCE_METADATA_HOST` is not the primary mechanism, it is the **only** one — things that
  hardcode the IP **fail outright rather than degrading**, which is a different operator
  experience and belongs in release notes.
- **§4.11 gained S5** — the emulator becomes a credential-minting endpoint the moment it
  leaves loopback, and authenticates no callers. Requirement: **bind link-local, never
  `0.0.0.0`**. Recorded explicitly because the lazy fix is functionally identical and has a
  much larger blast radius — the exact shape of change made under time pressure and never
  revisited. **S5 does not gate first deploy** (S1/S2 do); it is a release-note limitation.

**§10 documented limitations went from 3 items to 7.** The four added are all things an
operator discovers painfully otherwise: no network isolation is possible on this tier
(`--allow-egress` is mandatory *and* all-or-nothing); any sandbox can mint the runtime SA
token; hardcoded-IP metadata callers fail hard; the IAP grant is region-wide. **Two of the
four are consequences of results measured tonight that were sitting only in OQ prose.**

Tasks #9–#12 opened for the residue: emulator bind + which SA it serves, the obsolete C7
warning, B12, and the harness `GCE_METADATA_HOST` audit.

## 05:35 — audited the deploy command's env-var contract, since it is the last unverified link

While the image builds, I traced the four env vars `diGcloudDeploy` writes all the way into
the structs the hub reads. This is the **actual interface** between `deploy-instance` and
the running hub, and it existed only as a `fmt.Sprintf`. Now **§11.5d**, with a table.

**Three of four verified correct by inspection.** `envKeyToConfigKey` maps
`SCION_SERVER_AUTH_PROXY_IAP_AUDIENCE` → `auth.proxy.iap.audience`, matching the `koanf:`
tags exactly. Good news, and worth stating: the auth wiring is not the problem.

**The one real gap: that path is never tested with an actual env var.** `Auth.Proxy` and
`Proxy.IAP` are **pointers**. Every existing test builds `cfg.Auth.Proxy.IAP` directly in
Go — `helm_chart_ha_contract_test.go:128` even allocates the pointer by hand on finding it
nil, which is the tell. Nothing exercises env → koanf → nested nil pointer. Failure mode is
**fail-closed at startup** (empty audience), so it is visible rather than silent, but the
Instance would never serve. Handed to em3 as task #13 with the exact assertions, to run
**before** the image lands.

**A worry I had and falsified — recorded because the next reader will have it too.**
`SCION_SERVER_HUB_ADMINEMAILS` targets a Layer-1 setting, and bootstrap layering puts
`SCION_SERVER_*` *above* `settings.yaml`. That reads as **override**, which would mean an
operator editing admin emails in the UI is silently reverted on every restart —
contradicting §11.6's "seed at deploy time" decision. **It does not.**
`OperationalSettings.Snapshot()` implements `DB rows > bootstrap merge`, and its own comment
says env vars "are honored as seed input during the deprecation window". **Seed semantics
are correct.** I checked instead of reporting; three false alarms tonight came from not.

**Two minor, both one-liners, both sent to em3:** the var is deprecated in favour of
`SCION_SEED_SERVER_HUB_ADMINEMAILS` (otherwise a deprecation warning on every startup of
every Instance — noise in exactly the logs an operator reads when debugging), and
`--set-env-vars` is itself comma-delimited so a comma in `--admin-email` silently becomes a
second env var. **Guard the second, do not redesign it** — unreachable today, the flag is
single-valued.

Build run `32933151982` still `in_progress` throughout.

---

## 05:45 — THE HUB IS UP BEHIND IAP. And the §1 blocker that remains.

**Milestone: a real Scion hub is running on a real IAP-fronted Cloud Run Instance.**
`e2e-omni`, `ptone-experiments`, `us-east4`, generation 6, `Running = True | Started
instance in 9.88s`. URL `https://e2e-omni-721899303052.us-east4.run.app`.

**The perimeter assertion passed.** Three consecutive unauthenticated fetches returned
**302 to `accounts.google.com`**, IAP OAuth client
`721899303052-3aurml9he9hm8p04a3grl7e5tutj0k3t`. The first probe at ~05:38 returned 404 —
**reconcile was incomplete, not broken.** Retried at 12 s intervals and it came up. That
is the 30–75 s window in §11.2 behaving exactly as documented; recording the 404 shape
because the next person to see it will think they have misconfigured something.

The deploy sequence was the one in §11.5a–c, unmodified: create with gcloud v1 (invoker
IAM on, born closed), then **one** REST v2 PATCH with
`updateMask=iapEnabled,invokerIamDisabled` and both fields in the same body. Annotations
after: `run.googleapis.com/iap-enabled: "true"`,
`run.googleapis.com/invoker-iam-disabled: "true"`, `gen 6 obs 6`. **The invariant held** —
there was never a moment with the invoker check off and IAP not yet on.

Access policy needed no change. I read it rather than assuming: the region binding at
`iap_web/cloud_run-us-east4` already grants `roles/iap.httpsResourceAccessor` to
**`domain:google.com`** and **`user:ptone@google.com`**, and region scope covers new
Instances automatically. That is D1 (§11.2) paying off, and also D1's cost — see §10.

### Four things measured on the live Instance, all positive

- **Task #13 / the §11.5d gap is CLOSED, positively.** The hub logged
  `Proxy auth configured: provider=iap, audience=/projects/721899303052/locations/us-east4/services/e2e-omni`.
  **The env → koanf → nested nil pointer path works.** mapstructure allocates
  `Auth.Proxy` and `Proxy.IAP`. B18 was a real gap in *coverage*, not a defect; the
  round-trip test em3 is writing now **pins known-good behaviour** rather than closing a
  risk. Worth saying plainly: I flagged this as the most likely startup failure and I was
  wrong. Cheap to have checked, and the negative result is worth as much as a positive.
- **The runtime factory picks correctly on a real Instance** —
  `Runtime broker using runtime: cloudrun-sandbox`. This is the empirical answer to B17:
  the factory branches on `CLOUD_RUN_INSTANCE`, not `K_SERVICE`, so the "unimplemented
  `CloudRunRuntime.Run()`" report was about a code path that never executes here.
- **Finding #6 is real.** Startup only succeeded once `SCION_IMAGE_REGISTRY` was set.
- **IAP auth is enforcing, not merely present.** A Cloud Run invoker token (RS256) is
  refused: `invalid access token: failed to parse token: unexpected signature algorithm`.
  The hub is validating the IAP assertion specifically, which is what §11.3 intends. An
  Instance that accepted any Google-issued token would have looked identical from outside.

### The blocker that is now the whole critical path

**NEW §1 BLOCKER, root-caused: no container image in this repo can serve the web UI.**

The hub told us itself, in its own startup log:
`This binary was built without web assets. The web UI will not be available.`

Root cause is one build tag. `image-build/scion-base/Dockerfile:55` builds with
**`-tags no_embed_web`**, which selects `web/embed_stub.go` over `web/embed.go` and drops
the `//go:embed all:dist/client`. `hub/Dockerfile` and `omni/Dockerfile` add **no build of
their own** — they are `FROM ${BASE_IMAGE}` plus a `CMD`. **Every image in the chain
inherits that binary.**

I looked for the cheap fix first and it does not exist: `AssetsEmbedded` is a compile-time
`bool` and there is no filesystem fallback, so shipping `dist/` into the image and pointing
the server at it is not an option. **A rebuild is genuinely required.**

I also checked whether the tag could simply be dropped from scion-base, and it cannot —
`scion-base/Dockerfile:41` copies **`web/*.go` only**, never `web/dist`, so without the tag
that build fails to compile. The tag is load-bearing there. **The rebuild has to happen in
the omni layer**, which is what em3 is now doing (task #14).

Verified em3's route against the build system before approving, and found one defect it
would otherwise have shipped: `lib/targets.sh:206 step_build_args` emits `GIT_COMMIT`
**only for `scion-base`**; the `scion-omni` case emits `BASE_IMAGE` and nothing else. A
binary rebuilt in the omni layer would carry **empty `Commit` and `BuildTime`**. Not
cosmetic — `scion version` is how we confirm which build is deployed, and tonight we have
already lost time to not knowing which binary was running.

Confirmed for em3 so it does not re-derive them: omni's build context **is** `${REPO_ROOT}`
(`targets.sh:163`); `core-base` is `FROM node:20-slim` with Go on `PATH`, so Node 20 and Go
are both in `${BASE_IMAGE}`; and `publish-omni.yml` triggers on `pull_request` for
`image-build/**`, so editing the omni Dockerfile is enough to publish.

### Handed over unsolved, deliberately

**The daemonize defect: reproduced, mechanism not found** (task #15). The triangle:
`sciontool init -- scion server start --foreground …` daemonizes;
`--command=scion --args=server,start,--foreground,…` — entrypoint bypassed, and I
re-described the Instance to confirm the spec really carried
`command: ['scion'], args: ['server','start','--foreground',…]` rather than trusting that
it had — **still** daemonizes; `--command=/bin/sh --args=-c,'… exec scion server start
--foreground …'` runs in the foreground.

Same binary, same flags, same argv[0] after `exec`. `cmd/server_daemon.go:151` is
`if serverStartForeground { return runServerStart(cmd, args) }`, which should make this
impossible. **I cannot explain it and I am not going to guess it** — I have burned enough
on it, and the workaround (`/bin/sh -c … exec`) is what `e2e-omni` runs on right now.
Handed to em3 with the repro and one cheap hypothesis: whether `/root/.scion/server.pid`
survives a container restart on Instances, letting `daemon.StatusComponent` see stale
state. **It is not blocking §1, but it does mean the `CMD` shipped in
`image-build/omni/Dockerfile` does not work as written**, which an operator would hit on
their first deploy.

### Released sn-e2e-walk

Told it to start steps 1–6 **now, via the API**, rather than wait for the UI. The missing
frontend is known and owned; what I need from it is everything underneath — project create,
agent start, PTY attach, git push. Those are API and WebSocket paths, all reachable with an
IAP OIDC token. Step 5 becomes a programmatic WebSocket rather than a browser session,
which still tests the thing I actually care about (B21: does the upgrade survive the IAP
edge, and is I/O bidirectional). If those pass, **the image rebuild is the last thing
between us and §1.**

---

## 05:50 — closed I2 by testing it, and restored demo verified

**`iap-demo` is back up and asserted, not assumed.** `https://iap-demo-721899303052.us-east4.run.app`
returns **302** with `x-goog-iap-generated-response: true`. `e2e-omni` re-probed at the same
time: also **302**, and `Running = True | Started instance in 11.94s` after the generation-6
IAP patch — the PATCH did not disturb it. Both are on ptone's do-not-delete list.

**I2 closed, negative, by measurement.** I had been carrying *"a hidden `gcloud --iap` may
exist"* as an untested note, on the strength of `--public`'s help text: *"Equivalent to
setting `--no-invoker-iam-check` and `--no-iap`."* If that flag existed, **the v1/v2 hybrid
— the most fragile part of the deploy design — would collapse to a single command.** That
made it worth two minutes rather than another day as a footnote.

It does not exist. Parser probes, all failing before any API call, with an invented flag as
a control:

```
--iap        → unrecognized arguments: --iap (did you mean '--image'?)
--no-iap     → unrecognized arguments: --no-iap
--enable-iap → unrecognized arguments: --enable-iap
--bogus-flag → unrecognized arguments: --bogus-flag        (control)
```

`--iap` is rejected **identically to a flag I made up**. And the second line is the real
find: **`--no-iap` does not exist either** — gcloud's help text names a flag the command
does not implement. An implementer reading that help text would hunt for an hour. Recorded
in §11.5c as an upstream doc bug to report alongside the `sandboxLauncher` v1/v2 asymmetry.

**The design consequence is the useful part:** the hybrid is not avoidable by discovering a
flag we already ship. It is avoidable only when gcloud catches up to the v2 surface, which
is someone else's release. §11.5c's two-step stands as the transport, and I have stopped
hedging it.

**Design doc updated in three places** beyond the I2 row: §11.5e written (the web-assets
build defect, with both cheaper fixes ruled out by inspection rather than by preference),
§11.5c gained the probe evidence, and §10 Functional gained three **build-integrity**
acceptance criteria — assets embedded, version stamped, and *the image's own `CMD` leaves a
process in the foreground*. All three exist because each was a defect found by **running**
the image, not by reading it. That is the pattern worth carrying forward: this chain's
defects are in the gap between what we compile and what we ship, and only the artifact can
show them.

---

## 05:58 — dispatched the ADC/metadata cluster, and why it may not be optional

**New agent `sn-adc-metadata`** (brief at `briefs/sn-adc-metadata.md`), owning tasks #9,
#10 and #12 — the three items that fall out of OQ-14 and have all been sitting unowned
because each looked individually minor.

**The reason I stopped treating them as minor.** OQ-14 proved ADC *can* work; nothing has
shipped, so today it *doesn't*. If the deployed Claude harness on `e2e-omni` is configured
for Vertex, then `sn-e2e-walk`'s **step 4 or step 6 fails on exactly this** — and it fails
as a mysterious agent-startup or push error, not as anything recognisably about metadata.
That makes this quietly critical-path rather than cleanup, and it makes the parallelism
worth it: the fix is roughly an hour behind whenever the walk discovers the need. I told
both agents to talk to each other, and told the walk to route auth-shaped failures rather
than debug them.

**Task 1 is a security requirement and I wrote the brief to make the wrong answer hard.**
The emulator mints credentials and does not authenticate callers — `requireMetadataFlavor`
is a convention, not a gate. `0.0.0.0` is the obvious way to make the bind work and it is
the one thing that must not happen, so the brief says: if you reach for `0.0.0.0` because
link-local is awkward, **stop and message me, that is a design question**. And the durable
deliverable is not the bind, it is **a test that fails when someone changes the bind later**.

**One thing I asked it to report and explicitly forbade it from fixing:** which SA the
emulator serves. The hub logged `721899303052-compute@developer.gserviceaccount.com` — the
**default compute SA**, matching the earlier spike. If that is what a sandboxed agent
receives, every agent holds default-compute credentials. That is broad, it is a real
finding, and **whether it is acceptable is ptone's call** — not mine and certainly not a
developer agent's, mid-implementation, at 06:00.

**Boundary drawn with em3:** em3 owns the image, `sn-adc-metadata` owns the runtime code.
Different files, no build dependency, right up until the ADC work needs something baked in
— at which point it goes through em3. I did not widen em3's queue because it is on the §1
critical path with the rebuild and that path should stay narrow.

**Also asked em3 to reap `dev-env-roundtrip` and `dev-p6-fixes`**, both idle-completed, per
ptone's standing cleanup instruction — but to **confirm their work is pushed first.** A
completed phase is not a landed commit. I asked rather than did it myself: a cleanup sweep
of mine already destroyed his IAP demo once tonight, and the lesson generalises.

### Where §1 stands, honestly, at 06:00

| §1 clause | State |
|---|---|
| one deploy command | ✅ `scion deploy-instance` exists; the v1/v2 hybrid is settled and I2 is now closed by measurement |
| opens the `run.app` URL | ✅ live, `e2e-omni` |
| logs in | ✅ IAP enforcing — 302 to Google, and a non-IAP token is refused |
| creates a project | ⏳ `sn-e2e-walk`, in flight |
| starts a Claude agent | ⏳ in flight; factory verified selecting `cloudrun-sandbox` on the real Instance |
| attaches to its terminal **from the browser** | ⚠️ **blocked on the web UI** (§11.5e). The WebSocket path itself is being tested programmatically |
| watches it commit to a git remote | ⏳ in flight; **may be gated on ADC** (#9) |

**One blocker, one unknown.** The blocker is the web bundle and it is understood, owned, and
mechanical. The unknown is whether the four in-flight clauses actually work — which is the
thing no amount of design can settle, and why the walk is running.

---

## 06:10 — §1 step 5 fails. Agent sandboxes start and then hang before `tmux`.

**`sn-e2e-walk` got the walk to step 4 and stopped there.** Steps 0–4 pass with evidence:
IAP perimeter enforcing, IAP OIDC through to the hub (`200` on `/api/v1/auth/me`), project
create `201`, agent dispatch `201`. **And one genuinely important positive: the WebSocket
upgrade through IAP returns `101 Switching Protocols`** — B21 is answered, the IAP edge does
not break the upgrade. The PTY fails only because there is nothing on the far end.

**The failure:** `sandbox run --detach` returns 0, the sandbox is alive and `sandbox exec`
works, but the entrypoint hangs before reaching `tmux new-session`, so
`tmux has-session -t scion` returns 1 forever and the harness never starts. The hub reports
the agent `running` throughout, **because it only checks that `sandbox run` returned 0** —
that is a defect in its own right and independent of the hang.

### Both agents converged on "metadata timeout". I have stopped them.

Not because it is wrong — it may well be right — but because **two of the three things being
treated as confirmation do not survive checking**, and a fix was about to be built on them.

**1. The sandbox is not pointed at `169.254.169.254`.** I pulled the real `sandbox run`
command line out of the platform logs rather than reading code, and the env is:

```
GCE_METADATA_HOST=localhost:18380   GCE_METADATA_ROOT=localhost:18380
SCION_METADATA_MODE=block           SCION_METADATA_PORT=18380
```

set at `pkg/runtimebroker/start_context.go:384`. **`GCE_METADATA_HOST` is already being set —
to `localhost`, which OQ-2 measured as unreachable from inside a gVisor sandbox.** So task #9
is confirmed as a live shipping defect, which is good. But e2e-walk reports the caller
reaching the metadata IP, and **both cannot be true of the same caller.** That fork decides
everything: if the caller honours the variable, the link-local bind fixes it; if it ignores
the variable and hardcodes the IP, **the bind fixes nothing and there is no fallback**,
because `iptables -t nat` does not exist in gVisor. They had asserted the second mechanism
while planning the first fix. Left alone, that ships a fix that does not work.

**Consequence for `sn-adc-metadata`: task #12 is a prerequisite for task #9, not a follow-on.**
Told it to reorder. That inversion was invisible until the two reports were read side by side,
which is the argument for having read both.

**2. "Exactly 120 s" is not evidence of a metadata timeout.** It is the `sandbox` CLI
wrapper's **own documented internal timeout** — measured hours ago on the delete-hang defect
and written up in `defect-sandbox-delete-hang.md` §4b, where the wrapper exits rc=1 and takes
its child with it. Two agents read 120.048 s and 120.045 s as a fingerprint of the cause; it
is the fingerprint of the platform giving up. **The entrypoint may be hanging indefinitely,
and the 120 s tells us nothing about why.** This is the second time tonight that a number
measured for one purpose has been the key to a different question.

**3. A third hypothesis, which is mine and which nobody raised.** The sandbox env also
carries `SCION_HUB_ENDPOINT=https://e2e-omni-....run.app` — **the hub's IAP-fronted public
URL, which the sandbox has no credentials for.** From inside, reaching the hub means
egressing to the internet and back through IAP, which will 302 it to `accounts.google.com`.
If `sciontool init` registers with the hub during pre-start and follows redirects, it hangs —
and from the outside that is indistinguishable from a metadata hang.

**This one is my responsibility.** §11 is explicitly *"complete for browser→hub; deliberately
silent on agent→hub pending OQ-2."* OQ-2 came back and I closed it as *"sandbox→launcher
works on the link-local address"* without going back to ask **what address the sandbox is
actually handed for the hub.** The design gap I flagged and left open is the one now
plausibly biting. A sandbox on the same Instance as the hub should be talking to it over
the link-local address, not out through the public IAP edge — and if that is the answer,
it is a design change, not a bug fix.

### The discriminator, and why I asked for it before any code

`sandbox exec` works while the entrypoint is wedged — proven, ~50 ms round trip. So during
the 120 s window:

```
sandbox exec <name> -- cat /proc/net/tcp     # destination addr in SYN-SENT
sandbox exec <name> -- ps -ef
sandbox exec <name> -- cat /proc/<pid>/wchan
sandbox exec <name> -- cat /proc/<pid>/environ | tr '\0' '\n'
```

**`/proc/net/tcp` settles all three hypotheses at once** — `169.254.169.254`,
`127.0.0.1:18380`, and the `run.app` address are three different answers needing three
different fixes. Thirty seconds against an hour of building the wrong thing.

I also declined e2e-walk's `GCE_METADATA_HOST=localhost:1` workaround **for now**: it is a
decent discriminator in principle, but it costs a code change plus an image rebuild and it
only tests one of the three branches.

**Incidental but worth recording: we have a live debugging channel into a wedged sandbox.**
That is going to be useful repeatedly and nobody had noticed it.

**Also noted, not chased:** the walk started an **`antigravity`** agent
(`SCION_HARNESS=antigravity`, `agy-wrapper.sh`), not a Claude agent. §1 says *"starts a Claude
agent."* The default template resolves to antigravity on this Instance. Not the cause of the
hang — the hang is before the harness — but §1 is not demonstrated until it is Claude.

---

## 06:20 — web-assets fix integrated and verified; CI is red on the same branch

**`dc5c84d` is on `scion/dev-rebase-1294` and the publish workflow fired on its own**
(run `32936047701`). I spot-checked the commit rather than taking the report, on the five
points that could each have failed silently:

| Check | Why it could have gone wrong | Result |
|---|---|---|
| `npm run build` is the right script | `web/embed.go`'s own comment says `npm run build:client`, which **does not exist** in `package.json` | ✓ `build` runs `vite build && copy:client`, produces `dist/client` |
| `GIT_COMMIT` reaches the omni step | the gap I found at 05:45 | ✓ added to `step_build_args` |
| `CGO_ENABLED` matches scion-base | a mismatch yields a subtly different binary | ✓ both `1` |
| `sciontool` left alone | it is PID 1 and a live variable in the daemonize bisect | ✓ only `scion` replaced |
| **nothing shadows `/usr/local/bin/scion` on `PATH`** | **scion-base puts `/opt/scion/bin` *ahead* of it** | ✓ that directory ships **empty** — it is a dev-override mount point |

The last one is the one I actually went looking for, because it is this project's recurring
failure shape: **the image looks fixed and runs the old binary.** It is clear today, but it
is a live trap the moment anyone populates `/opt/scion/bin`, so it is written down.

**CI is red, and has been for four of the last five runs — since 05:13.** Six `errcheck`
findings, all in `cmd/deploy_instance.go` and its test, all trivial (`defer resp.Body.Close()`,
`w.Write`). em3 reported "quality gates passed" in good faith: it ran `make fmt-check` and
`go build ./...`, **and neither runs `golangci-lint`.** So the branch reads green locally and
red on GitHub.

**Why this mattered enough to interrupt the critical path for it.** #1265 and #1266 are
ptone's merge gate and are the first thing he opens. *"The tier works, ignore the red X"* is
a bad opening for a night of work. Ten minutes of fixes standing in front of all of it.

**The generalisable point, which I put to em3 as process rather than as a defect:** the local
gate must be the CI gate. `fmt-check` + `build` is a weaker set than CI runs, and reporting
success against the weaker set is exactly how a branch stays red for an hour with three
agents watching it. Also told it to check CI **after** pushing — it integrated at 05:56 and
reported success on a run that had been failing since 05:13.

**Flagged forward, so it is not lost behind the good news:** the new image still ships the
`CMD` that daemonizes (B23 / task #15). Web assets were the §1 blocker; with them fixed,
**the `CMD` becomes the last defect between an operator and a working first deploy**, because
`e2e-omni` only runs at all thanks to the `sh -c … exec` override I applied by hand. An
operator following the documented path gets an Instance that reports
`Instance completed successfully` and serves nothing.

---

## 06:15 — §1 step 5 root-caused. It was one word, and three agents spent two hours around it.

**`buildEntrypoint` spells its command as bare `sh`. The sandbox does not resolve argv[0]
through `PATH`. Every agent sandbox we have ever launched on this runtime died on arrival —
and `sandbox run` returned 0 every time.**

### How it was settled

Logs could not settle it: I had already enumerated all six Cloud Run log streams and
confirmed the sandbox CLI's stderr never reaches Cloud Run logging, and that
`/var/log/sandbox.log` carries only `[start]` / `[end] exit_code=` markers. So I stopped
reading and started measuring — three throwaway Instances on the **same omni image**, same
region, same `--sandbox-launcher`.

**`diag-sbx`** — trivial sandbox, `-- sh -c 'sleep 600'`. No `sciontool init`, no metadata,
no hub, no git.

```
=RUN_RC=0=                                   <-- run SUCCEEDS
+ sleep 5
+ sandbox exec diagbox -- /bin/echo HELLO
  Error: sandbox diagbox is not running
```

**`diag-sbx2`** — `sandbox do` (synchronous), then `-- /bin/sleep 600`.

```
sandbox do ... -- /bin/echo HELLO_SYNC        -> HELLO_SYNC, rc=0
sandbox do ... -- /bin/sh -c 'echo INSIDE;id' -> INSIDE, uid=0(root), rc=0
sandbox run box2 --detach -- /bin/sleep 600   -> rc=0
exec at T+0s / T+2s / T+12s                   -> T0 / T2 / T12, all rc=0
```

That inverted the investigation. The sandbox runtime was never the problem.

**`diag-sbx3`** — six sandboxes, one variable each. This is the measurement that matters:

| sandbox | command | result |
|---|---|---|
| boxA | `/bin/sleep 600` | **ALIVE** |
| boxB | `/bin/sh -c 'sleep 600'` | **ALIVE** |
| boxC | `sh -c 'sleep 600'` | **DEAD** |
| boxD | `sh -c 'exec /bin/sleep 600'` | **DEAD** |
| boxF | `/bin/sh -c 'exec sciontool init -- /bin/sh -c "sleep 600"'` | **ALIVE** |

boxD kills the `exec`-builtin theory. **boxF is the clincher: the entire real entrypoint
chain — `sciontool init`, nested shell and all — works perfectly when spelled absolutely.**
Nothing else in our entrypoint was ever wrong.

### The fix

`pkg/runtime/cloudrun_sandbox_runtime.go:520`, both occurrences — the second is inside the
string literal and is easy to miss:

```go
// now:
return []string{"sh", "-c", symlinkCmd + " && exec sciontool init -- sh -c " + shellQuote(tmuxCmd)}, nil
// wants:
return []string{"/bin/sh", "-c", symlinkCmd + " && exec sciontool init -- /bin/sh -c " + shellQuote(tmuxCmd)}, nil
```

With a test asserting `strings.HasPrefix(entrypoint[0], "/")`. This will get tidied back one
day and the test is the only thing that will catch it. Owner: `sn-impl-em3`, task #18.

### The part worth keeping — the code already half-knew

`envFor`, line 412:

```go
// PATH is empty inside the sandbox (AC-0 retest finding). Set a
// reasonable default so the harness and its children can find binaries.
env["PATH"] = "/usr/local/sbin:/usr/local/bin:..."
```

**Someone measured exactly this defect and fixed it for the process environment** — which is
why the harness and its children work. But the entrypoint's own argv[0] is resolved by the
sandbox launcher *before that environment exists*, so the fix never reached the one place it
was needed. The diagnosis was right and landed one layer too high.

That is the third instance of the same shape tonight, and the pattern is now the finding:

- `envFor` patches `PATH` for children, not for argv[0].
- `buildEntrypoint` wraps in `sh -c` — which reads as style until you know it is a workaround.
- `isClaude()` at `sciontool` :1952 defensively splits "the case where the harness command is
  joined into a single string" — defending against a joining mechanism em3 has since proven
  does not exist in the current code path.

**Three defensive patches, each written by someone who correctly observed a symptom and
fixed it one layer too high.** This runtime has been accumulating local workarounds for
problems nobody traced to ground. Recorded in the design doc as a standing hazard.

### The second-order finding, which is why this survived so long

**`sandbox run --detach` returns 0 for a sandbox that is already dead.** Four of six
sandboxes in the matrix returned rc=0; two of those four did not exist five seconds later.
The hub treats that exit code as proof of life and reports `phase=running`. So a fatal
launch failure presented as a healthy agent — UI says running, terminal attaches (WebSocket
101 through IAP succeeds), and nothing ever appears.

**That is the worst failure shape there is, and it is the reason three agents spent two hours
on hypotheses about metadata timeouts.** Task #17, owner `sn-e2e-walk`, and the fix is not
"propagate `stopped` faster" — it is to stop trusting `run`'s exit code. Line 207 already has
the probe: `sandbox exec <id> -- true`.

### Two claims I made tonight that were wrong, corrected in the record

1. **`sandbox wait` DOES exist.** I told `sn-e2e-walk` it did not, on the strength of its
   absence from the `--help` verb list. It is a **hidden command** and fully functional:
   *"blocks until the sandbox has exited and prints its exit code."* The watcher at :856 is
   sound. e2e-walk built a severity assessment on my claim and retracted it; the error was
   mine and is logged as such. **The verb list from `--help` is not the verb set.**
2. **`--mount` takes `type=bind,source=…,destination=…`**, which `mountsFor` already emits
   correctly. A mount failure in my probe was my own bad syntax, not a defect.

### Housekeeping

`diag-sbx` deleted. `diag-sbx2` and `diag-sbx3` are throwaways, safe to delete.
**`e2e-omni`, `iap-demo`, `q2-control` remain do-not-delete.**

---

## 06:25 — the metadata bind fix is held, and the reason is the same shape as §11.5g

`sn-adc-metadata` shipped three commits on `scion/sn-adc-metadata` and signalled COMPLETED.
**Two are good. The third is held and em3 has been told not to integrate it.**

**Good, merge as-is:**
- `86775dfe` — the C7-era warning at `cloudrun_sandbox_runtime.go:572` no longer tells
  operators that vertex-ai and gcloud-adc are impossible on this runtime. Task #10 closed.
- `d5202e41` — harness audit. **All four ADC-using harnesses honour `GCE_METADATA_HOST`;
  none hardcodes `169.254.169.254`.** Codex has no vertex-ai support and returns a *clear*
  ValidationError rather than falling back silently. Task #12 closed. This is the finding
  that lets §11.12 be closed rather than hedged: the bind fix is sufficient for every
  harness, no redesign needed.

**Held: `c3db6b3f`.** `start_context.go:396` sets `SCION_METADATA_BIND_ADDRESS` in the
**sandbox's** env, commented *"Tell the launcher's emulator to bind link-local too."* But the
emulator is started by `metadata.ConfigFromEnv()` at `cmd/sciontool/commands/init.go:450` —
inside **`sciontool init`**, which `buildEntrypoint` runs **inside the sandbox**. A
sandbox-side variable cannot configure a launcher-side process, and a sandbox-side emulator
told to bind the *launcher's* link-local address gets `EADDRNOTAVAIL`. Meanwhile
`GCE_METADATA_HOST` now points at the launcher.

**I am not asserting the change is wrong — I am asserting it cannot be settled by reading.**
`sciontool init` is *also* the omni image's ENTRYPOINT, so an emulator may start launcher-side
as well. **The question is single and answerable: on `cloudrun-sandbox`, which process serves
the emulator — the launcher's `sciontool init`, the sandbox's, both, or neither?** Observe who
holds a listener on 18380 and in which namespace.

**And there is a real chance `localhost` was correct all along.** `metaCfg.FetchGCPToken`
delegates to **the hub client**, not to GCE metadata. An emulator inside the sandbox therefore
needs no launcher reachability at all — it needs to reach *the hub*. If that is the
architecture, the address that is actually wrong is `SCION_HUB_ENDPOINT` (§11.5f, task #16,
still open), and this patch moves a working value to a broken one to fix a problem located
elsewhere.

**The fallback must change regardless of the answer.** On discovery failure the code warns and
falls back to `localhost:18380` — an address **OQ-2 measured as non-functional from a
sandbox.** That is not a fallback; it is a known-broken value plus a log line, and it will
present as a mysterious auth failure rather than a configuration error. **This is exactly the
hazard I wrote up as B27 four hours into diagnosing its twin** — `envFor`'s PATH comment,
correct diagnosis, patched one layer too high, no root cause recorded. Told them: fail the
start.

**What is good about the held commit, and should survive whatever the answer is:**
`DiscoverLinkLocalAddress` errors on **zero and on more than one** link-local address rather
than picking the first — the discipline asked for, and the thing most people would not do.
`TestBindAddress_Never0000` is the durable S5 guard.

### Process note, recorded because it recurred twice tonight

`sn-adc-metadata` signalled COMPLETED on work whose central premise it had itself identified
as unverified — its own message says the reachability test *"depends on sn-e2e-walk's
entrypoint fix landing."* Separately, `sn-impl-em3` cleared bare `tmux` in `manager.go` on the
grounds that *"message delivery works"* — **on a path no sandbox has ever survived long enough
to execute.** Same error in both: **absence of failure reports on an unexercised path read as
evidence of success.** That is precisely how bare `sh` survived. Both corrected, both accepted
it without argument.

### Convergence hazard, resolved

`sn-e2e-walk` independently wrote the same `/bin/sh` and `/bin/true` fixes em3 had already
pushed — my routing error, not theirs. Told it to rebase and keep only the parts that are
uniquely its own: the **dead-on-arrival probe in `Run()`** (three retries at 0.5/1/2 s, no
state-store entry and no watcher if all fail) and `TestCloudRunSandboxRuntime_Run_DeadOnArrival`.
**That test is the most valuable artifact of the night** — it is the one that turns this
two-hour investigation into a two-minute one if the failure ever recurs.

---

## 06:32 — the premise was false, and reverting it removed a security limitation from the tier

`sn-adc-metadata` was told to answer one question by observation before its bind patch could
merge. **It answered it, found its own patch wrong, and reverted it** (`9877da59`). Hold
released; em3 is integrating the branch tip.

**The measurement.** On `cloudrun-sandbox` the metadata emulator is served by the **sandbox's**
`sciontool init`, not the launcher's:

1. `metadata.ConfigFromEnv() → New() → Start()` has exactly one call site —
   `cmd/sciontool/commands/init.go:448-499`.
2. `buildEntrypoint` runs `sciontool init` **inside the sandbox**.
3. The launcher's `server_foreground.go` starts no emulator. The comment at :2398 calling
   18380 "host-global" is misleading — it is per-container.
4. `metaCfg.FetchGCPToken` delegates to `hubClient.FetchGCPToken()`.

**`localhost:18380` was correct all along.** The emulator shares the harness's network
namespace. The reverted patch would have pointed `GCE_METADATA_HOST` at an address where
nothing listens *and* made the in-sandbox emulator bind an IP absent from its namespace.

**Three doc changes, and the second is the prize.**

**§11.12 headline corrected** — "via the emulator on the launcher's link-local address" →
"via a per-sandbox emulator on loopback", with the evidence chain quoted and, more usefully,
**why the original was wrong: the OQ-14 spike stood the emulator up by hand and never
exercised the shipped path.** Third instance tonight of a conclusion reached without running
the thing.

**§4.11 S5 downgraded and taken off the multi-tenancy revisit trigger.** S5 existed because
§11.12 required moving the emulator off loopback. **It never moves.** The residual I had
accepted and written into the release notes — *"any sandbox on the Instance can obtain the
runtime SA's token"* — was a consequence of a shared launcher-side endpoint **that does not
exist**. Each sandbox has its own emulator, which asks the hub, which applies its own
authorization: per-agent and mediated, not ambient. **A revert removed a documented security
limitation.** The `BindAddress` guard and `TestBindAddress_Never0000` are retained with no
consumer, and S5 now says why, so nobody deletes them as dead code: the emulator still does
not authenticate callers, so the day anything moves it off loopback the entry applies again in
full — and a patch doing exactly that was written and reverted tonight.

**Task #16 re-scoped upward and decided** — see new §11.5f-D. ADC depends on the sandbox
reaching **the hub**, so `SCION_HUB_ENDPOINT` now gates credentials for three of five
harnesses, not just registration. Chose **option B**: one listener, `SCION_HUB_ENDPOINT` =
launcher link-local, agent API authenticated by agent token. Option A (separate link-local
listener) deferred to multi-tenancy; **option C (issue the sandbox an IAP credential) rejected
outright** — it lets a compromised agent re-enter the hub as the operator. Owner
`sn-adc-metadata`, with one load-bearing unknown it must measure before writing code:
**whether the proxy/IAP middleware gates the agent API.** If it does, that is an auth-layering
decision and comes back to me.

**The rule §11.5f now states, which is the generalisable output of the night:** every address
handed into a sandbox needs an explicit decision about which of three namespaces it lives in
— sandbox-local, launcher link-local, or public. Tonight produced one error in each direction.
**The same function computing the same address is right or wrong depending on which end of the
connection it describes.**

## Current state of play

- **`3140d90` publishing** — has both R1 fixes. `sn-e2e-walk` starts the §1 walk on it the
  moment em3 hands over the tag; **the walk is not batched behind the outstanding merges**,
  which are improvements rather than prerequisites.
- **em3 integrating** `scion/sn-adc-metadata` (tip) and `scion/sn-e2e-walk-doa` (ca4cbbb,
  DOA probe + tests, clean rebase, no duplicates), then one final publish.
- **`sn-adc-metadata` on #16.**
- **Watched on the walk:** step 5 as the milestone; bare `tmux` in `manager.go` as first
  suspect; agent→hub 302s to be routed to #16 rather than debugged; **Claude harness
  explicitly**, since the last walk silently got antigravity (B25).

---

## 06:35 — #16 measured and approved. The last design question on the tier is now an implementation task.

`sn-adc-metadata` answered all three of #16's unknowns in four minutes, with file and line
references. **Option B is confirmed viable and is being implemented.** Design doc §11.5f-D
updated from "decided, one load-bearing assumption unmeasured" to "decided and measured".

**The assumption that mattered — does proxy/IAP auth gate the agent API? — is false, which is
what option B needed.**

- `UnifiedAuthMiddleware` (`hub/auth.go:109-380`) checks `X-Scion-Agent-Token` at **step 1
  (:149)**, before proxy auth at **step 3a (:234)**. A valid agent token short-circuits.
- `POST /api/v1/agent/gcp-token` (`handlers_gcp_identity.go:1023`) requires
  `GetAgentFromContext()` — agent-token only. **That is both the strictest endpoint in the set
  and the one on the credential path**, so it is the right one to have checked.
- The hub already listens `0.0.0.0:8080` in hosted mode. **Link-local needs no listener
  change** — which retires option A's deferral honestly: its cost was real and its benefit is
  not needed yet, rather than it being quietly dropped.
- No collateral. Invite links (`admin_invites.go:174`) and the OIDC issuer fallback
  (`server.go:1135`) read the hub's **own** `HubEndpoint`, not the per-agent value from
  `resolveHubEndpointForCreate/Start`.

**The whole change is one value, computed launcher-side, placed in the sandbox's environment:
the same `DiscoverLinkLocalAddress()` that was reverted an hour ago, now used as a
*destination* rather than a *bind address*.** The §11.5f namespace rule paid for itself inside
the hour, and I have asked for that sentence in the commit message so the next person does not
re-break it.

**Four requirements attached to the implementation**, the first non-negotiable:

1. **Read the hub port from configuration; do not hardcode 8080.** A constant here turns a
   routine config change into the silent, simultaneous loss of agent connectivity *and* ADC.
2. **On link-local discovery failure, fail the agent start** — never fall back to the public
   URL. That fallback is precisely the 302 that cost the project its night.
3. **Both `resolveHubEndpointForCreate` and `resolveHubEndpointForStart`**, plus a test
   asserting `SCION_HUB_ENDPOINT` for `cloudrun-sandbox` never contains `run.app`. **Assert
   the negative** — it is the assertion that survives refactoring.
4. **Finish on a live measurement, not a unit test:** from inside a real sandbox, the agent
   registers *and* a GCP token fetch succeeds. **The second is what closes OQ-14 for real** —
   nothing has yet exercised emulator → hub → SA end to end.

**Residual recorded, not fixed:** agent→hub inside the perimeter is **plaintext HTTP carrying
a bearer agent token**. It never leaves the Instance and each sandbox has its own interface,
but the token is per-agent, so cross-sandbox observation would be an escalation. Acceptable on
a single-tenant tier; **it goes in the release notes and joins the multi-tenancy revisit
trigger**, alongside the region-scoped IAP grant.

## 06:45 — #16 blocked wrongly, fixed anyway, one change outstanding

**Sequence:** `a84cd54b` (link-local hub routing, #16) → I blocked it → `e23c8ebe` (listen port
plumbed as `HubListenPort`) crossed my retraction in flight → holding for one dedup change.

**My blocker was wrong.** I predicted `url.Port()` would return empty on the hosted tier and 500
every agent start. `resolveHubEndpointForBroker` (`server_foreground.go:2840`) always builds
`http://localhost:<webPort>` in colocated mode, so the port is explicit and `a84cd54b` worked.
Full write-up in `review-queue.md` under "Correction #3". Both agents were told.

**`e23c8ebe` merges regardless**, on its own merits: it reads the hub's listen port from
configuration instead of recovering it by parsing a URL that was constructed from that same port.
Verified on the branch — `ServerConfig.HubListenPort` (`server.go:68`), wired at
`server_foreground.go:2471`, consumed by `cloudrunSandboxHubEndpoint(hubListenPort int)` which
hard-errors on zero rather than falling back to an address that would 302 off the IAP edge.

**Outstanding before merge:** the derivation `enableWeb ? webPort : cfg.Hub.Port` now exists at
`server_foreground.go:2457` **and** `:2843`. Extract one helper; both call it. Plus a regression
test pinning that the colocated hub endpoint always carries an explicit port.

**`sn-impl-em3`:** workflow fixed in `2674dab` — path filter now includes `**.go`, `go.mod`,
`go.sum`, `web/**` (the image carries the compiled binary, so Go-only PRs must rebuild it), and
`dev-latest` removed because any PR's build could move it. `dev-<head-sha>` stands as the
predictable tag. **B28 closed pending green CI.**

**Current state of play**
- #16 — one dedup change from merge. Still unverified live.
- #6 — walk running against `dev-4af4ad44…`; reports at step 5 either way.
- #20 — fixed in `2674dab`, CI running.
- Still open and unowned: #11 (B12 invoker binding), #13, #15 (B23 mechanism), bare `tmux`
  in `pkg/agent/manager.go` on the `exec` path.

## 06:50 — step 5 still fails on the R1-fixed image; the real blocker is that we cannot see

`sn-e2e-walk` deployed `e2e-walk-r1` from `dev-4af4ad44…` and walked §1. **Steps 0-4 pass.
Step 5 fails.** The B23 CMD fix holds (Instance starts with no `sh -c exec` workaround). The
R1 fix is confirmed present in the launch log. **The sandbox still dies ~100ms after `run`
returns 0** — `sandbox wait` exits 1 in 19ms. Phase still reports `running` (the #17 DOA probe
is not in that image).

**So `/bin/sh` was necessary and not sufficient.** #18 was a real defect and fixing it moved us
past nothing visible. Four hypotheses now, ranked, none yet measured:

- **H3 — `rm -rf /home/scion` fails on the read-only rootfs.** The entrypoint's *first*
  command. §3.2a: rootfs read-only, only mounts writable; `/home/scion` is a real directory in
  the omni image. Non-zero → `&&` short-circuits → PID 1 exits before `sciontool` or `tmux` is
  reached. Best fit for a 19ms death, and a recently added line.
- **H2 — `sciontool` and `tmux` are still bare names** inside the shell. The outer `/bin/sh` is
  absolute now; these two resolve through the in-sandbox PATH set by `envFor` via `--env`.
  **Nobody has verified `--env PATH` reaches the shell.** R1 established only that the
  *launcher* cannot resolve argv[0]. `not found` is exit 127, near-instant.
- **H1 — `tmux attach-session` with no controlling terminal** (e2e-walk's). The entrypoint ends
  in `attach-session -t scion` and the sandbox is detached. Plausible, but requires everything
  upstream to work first.

Probe plan sent: one sandbox with the **real `--env`/`--mount` flags** and a `sleep 600`
entrypoint, then `exec` in and test PATH, `command -v`, and writability of `/home/scion` in
order, stopping at the first failure.

**The actual finding is the operability gap, and it is now the binding constraint.** Four
hypotheses in twelve hours, every diagnosis by inference, because **a dying entrypoint's stderr
goes nowhere** — no output, no exit code, no artifact. I flagged this in §11.5g and did not act
on it; that was a mistake, and it has cost more than any single defect tonight. **Task #22**
(`sn-impl-em3`, top priority): redirect the entrypoint chain to a log + `.rc` file on the
bind-mounted agent home, and have the DOA probe read it back into the error the operator sees.

Task #21 (bare argv[0] on the `Exec` path — `manager.go` ×3, `handlers.go` ×3, same R1 class,
launch path fixed and exec path not) is real and deprioritised behind #22.

`sn-adc-metadata` closed the #16 dedup in `4230c5ee` with `resolveHubListenPort` plus two
contract tests. Merge sequence with em3 unchanged; revert/re-revert pair to be squashed.

## 07:05 — step 5 ROOT-CAUSED on real sandboxes. Three stacked defects. tmux is not in the image.

Measured on `diag-sbx6` (fresh Instance, `--sandbox-launcher`, real `sandbox run`/`exec`, image
`dev-4af4ad44…`). Not inferred.

**D1 — tmux is not installed in the omni image. This is the killer.**

```
command -v tmux                      -> (nothing)
sandbox exec p1 -- tmux -V           -> error finding executable "tmux" in PATH [...]
sandbox exec p1 -- /usr/bin/tmux -V  -> failed to load /usr/bin/tmux: no such file or directory
/bin/sh -c 'tmux new-session -d …'   -> tmux: not found   NEWD=127
```

`image-build/core-base/Dockerfile:85` installs tmux. The omni build chain is
**thick-prep → scion-base → omni** and **core-base is not in it.** The entrypoint exists to run
tmux. **Every sandbox has died on this since the runtime was written.** Task #23, `sn-impl-em3`.

**D2 — the rootfs is not writable, as root.** H3 confirmed.

```
touch /home/scion/.probe -> Permission denied   RW=1
rm -rf /home/scion       -> Permission denied   RM=1
```

The mount test in the same run created a root-owned file, so this is not a UID artefact.
**`cloudrun_sandbox_runtime.go:44` — *"writes to unmounted paths go to a private rootfs overlay"*
— is false.** There is no writable overlay. The entrypoint's first command therefore fails and
the `&&` chain stops before anything else runs.

**D3 — `tmux attach-session` needs a controlling terminal**, and `sandbox run` has **no `--tty`
flag** (confirmed from `sandbox run --help`: only `--stdin/--stdout/--stderr`). Real, and the
next failure once D1 and D2 are fixed.

**The fix, measured: `--mount` accepts differing source and destination.**

```
--mount type=bind,source=/scion/probe/home,destination=/home/scion
MOUNTRW=0 ; file created ; contents visible
```

So mount `agentHome` **at** `/home/scion`, delete the `rm -rf`/symlink chain, and the
supervisor's hardcoded `HOME=/home/scion` (`supervisor.go:115`) just works. The
`source=X,destination=X` identity at `:399` is convention, not constraint. `sn-e2e-walk` owns
this plus the polling loop replacing `attach-session`.

**Correction — task #21's premise is wrong.** I claimed `sandbox exec` does not resolve argv[0]
through PATH. The launcher's own error reads *"error finding executable \"tmux\" in PATH
[/usr/local/sbin /usr/local/bin …]"* — **it searched PATH and listed the directories.** R1
applies to `run`, not `exec`. The four bare-argv[0] call sites are probably fine. #21 is on hold
pending one confirming test with a binary that exists; em3 told not to implement it.

**Ownership right now:** #23 image/tmux → em3. Entrypoint (mount + polling) → e2e-walk. #22 log
capture → em3, coordinating so they do not both write it. #16 merge sequence unchanged.

## 07:10 — all three step-5 fixes in flight; image fix verified on the branch

**Verified on `origin/scion/dev-rebase-1294`, not taken on report:**

- `54c88e83` — **tmux installed** at `image-build/thick-prep/Dockerfile:47`, with a comment
  naming the failure mode. Plus a **build-time assertion** in `omni/Dockerfile:75-86` looping
  over `tmux sciontool scion` and failing the build if any is absent. **D1 closed.**
- `66877ed0` — stale metadata emulator comment corrected (loopback per-sandbox, not launcher
  link-local); gVisor `iptables` warning preserved.
- `9badbfd6` — #22 entrypoint output capture to `agentHome/.scion-entrypoint.log` + `.rc`, read
  back by the DOA probe into the returned error.
- `b6f01892` / `bfbf746a` / `071822ec` — #16 fully merged (link-local hub routing,
  `HubListenPort`, `resolveHubListenPort` + contract tests). Revert/re-revert pair skipped.

**em3 answered the core-base question, which I could not.** Its exclusion from the omni chain is
**deliberate** — thick-prep is the lightweight replacement, and pulling core-base in would be a
4× image size increase for one apt package. So the one-line thick-prep fix is correct rather
than expedient. Recording it because "why is core-base not in the chain" would otherwise be
re-litigated by the next person who finds the missing tmux comment.

**Coordination hazard found and closed.** `9badbfd6` is authored by **`dev-entrypoint-diag`** —
a third agent I did not know was working in `cloudrun_sandbox_runtime.go`, concurrently with
`sn-e2e-walk`'s mount + polling rewrite of the same function. **`sn-e2e-walk` now holds the lock
on that file**; em3 (integrator) instructed to queue rather than cherry-pick anything further
into it, and e2e-walk to rebase onto `54c88e83` and state explicitly that it preserved the log
capture.

**Two residuals raised on the #22 capture, deliberately not fixed by me:**
(a) the diagnostic lives inside the agent's own HOME — writable by the process being diagnosed;
(b) **confirm no credential reaches that file**, since it wraps `sciontool init` and lands on a
bind mount an operator may paste into a bug report. This project has printed tokens before.

**Next gate:** `dev-<head-sha>` tag from publish-omni → fresh Instance → **§1 re-walk from step
0.** Three fixes must compose into one running agent and nothing yet says they do. Past step 5
the unexercised ground is the exec path — browser terminal attach and agent messaging — which is
where the remaining R1-shaped assumptions live (see #21, on hold).

## 07:20 — entrypoint fix sent back; image build held

`sn-e2e-walk` pushed `74f2d1d7` (rebased onto `54c88e83`): agent home mounted at `/home/scion`,
`rm -rf`/symlink chain gone, `attach-session` replaced by the poll loop, three stale overlay
comments corrected with a `diag-sbx6` citation, tests updated with negative assertions. **The
mount change and the poll loop are right.**

**Sent back on three defects — see B33.** The fatal one: the #22 log redirect still uses the
**host** path `agentHome/...`, which after the mount change is not mounted into the sandbox.
A failed redirect in `sh` means the command never runs and the shell exits — **reproducing the
step-5 signature exactly, after four correct fixes, with no log to explain it.** Also flagged:
the `.rc` file is unwritable in both branches because of `exec` (this one is already merged in
`9badbfd6`), and `envFor:446` hands harness env templates a host path.

**Image publishing held** until the fix lands, so that one image carries all four fixes and one
walk tests them together. Asked em3 separately for the `54c88e83` tag if CI already went green —
**a green build there is the first independent confirmation that tmux is now in the artifact**,
because the build assertion would have failed otherwise.

**Status of the four step-5 fixes:** D1 (tmux) merged and verified on branch; D2 (mount) and D3
(poll loop) correct but blocked on the log-path defect; #22 (capture) needs both fixes above.

## 07:45 — entrypoint fix approved and building; step 6 found missing before it was reached

`sn-e2e-walk` pushed `24b1a261`, fixing all three B33 defects. Verified against the branch rather
than the report: `logPath` uses `sandboxAgentHome` (`:598`), the DOA probe still reads
`paths.agentHome` host-side (`:759`, the correct asymmetry, with a doc comment saying why),
`GetEnv` gets `sandboxAgentHome` (`:466`), mount is `source=<host>,destination=/home/scion`
(`:431`), `.rc` dropped from the entrypoint with the constant kept for compatibility.
**Approved; publishing unheld; em3 building on `54c88e83` so one image carries all four fixes.**

### The fix was measured before it was written

While the build was held I probed the *proposed* entrypoint form on a real sandbox
(`diag-sbx7`, on the tmux-bearing image). Full table in §11.5i. The three that matter:
`P1RC=0` with the poll loop holding PID 1 alive; the log file written inside at `/home/scion`
and **visible on the host at the mount source** (the B33 fix, measured); and `KILL=0` followed
by `ALIVE_AFTER_KILL=1` — **PID 1 exits when the session ends.** That last one closes the
requirement I insisted on, on both halves. The original `tmux wait-for scion-exit` would have
passed the first half and failed the second.

`diag-sbx7` deleted.

### Step 6 does not exist, and it fails by blaming step 5

The same probe covered the attach path, which was entirely unexercised. Attach itself is fine:
under a PTY, `sandbox exec … -- /usr/bin/tmux attach-session -t scion` renders the full tmux
screen and status line, and `capture-pane` works. **The broker cannot invoke it.**

`pty_handlers.go` dispatches two ways only — k8s Go-client, or `startDockerExec`.
`Name()` returns `"cloudrun-sandbox"`, `controlchannel.go:766` puts that in `runtimeCmd`, and
`pty_handlers.go` uses it **as a binary name** at `:165` and `:941`. No such binary; and the
`-it`/`--user`/positional-command shape is docker-only regardless.

**`:165` is a poll loop.** It does not fail fast — it spins to `tmuxSessionWaitTimeout` and
reports *"timed out waiting for tmux session … to become ready"*, which accuses the entrypoint
we just spent the night fixing. **A step-6 plumbing gap wearing a step-5 symptom.** Warned
e2e-walk explicitly not to re-debug the entrypoint when they see it.

Assigned to e2e-walk as **#24** — they hold the runtime context and are idle pending the image.
Recommended option B (runtime supplies an exec-argv prefix via `AgentLookupResult`) over a third
special case; told them to come back to me before falling back to A. `Attach()` at `:933-949`
already builds the correct argv, so the gap is only that the broker never asks the runtime.

**Closed:** #20, #22, #23. **Open and unowned:** #11, #13, #15.

## 07:50 — estate check, and the IAP demo verified rather than assumed

Instances: `e2e-omni`, `e2e-walk-r1`, `iap-demo`, `q2-control`. All four accounted for; every
`diag-sbx*` probe deleted.

**`iap-demo` (ptone's standing ask) is up — 3h10m uptime, `https://iap-demo-721899303052.us-east4.run.app`.**
Existence is not the property that matters, so I checked the one that does: an unauthenticated
`GET` returns **302 to `accounts.google.com/o/oauth2/v2/auth`**, not the app. IAP is intercepting.
Combined with `Invoker IAM Check: disabled` from the describe, **both halves of the perimeter
invariant hold** — `invokerIamDisabled` is never set without IAP actually in front. An instance
that merely exists with invoker checks disabled would be an open endpoint, which is exactly the
failure this pair of checks is designed to catch.

## Critical path, as of 07:50

1. **em3** — building on `24b1a261`, then walking §1 steps 1–5. First live test that D1, D2, D3
   and the corrected log path compose. Four fixes, only ever tested apart.
2. **e2e-walk** — #24 (step 6 plumbing). Recommended option B; asked to check back before
   falling back to A.
3. Then a second image, then step 6.

Both agents dispatched and working; neither stalled. ptone almost certainly still asleep —
accumulating in review-queue.md (B34, B35 added) rather than interrupting. Nothing currently
needs him.

## 07:55 — #24 withdrawn; I was reviewing from a stale tree

**`sn-e2e-walk` corrected me and was right.** The `cloudrun-sandbox` PTY dispatch has existed
since P4. §11.5i is rewritten, B34/B35 retracted, task #24 deleted. Cause and the standing
rule that follows are in review-queue.md: **my `/workspace` checkout is 62 commits behind and
does not contain this tier at all**, so this tier must be read via `git show <ref>:<path>`.
The workspace is `shared-plain`, so the checkout cannot be moved — the staleness is permanent.

**Net effect on the plan is good:** step 6 needs no new work. The critical path shortened.

`diag-sbx7` keeps its value and arguably gains some — its measured argv is character-for-character
what `startCloudRunSandboxExec` at `:591-596` already builds, so the existing implementation now
has live confirmation rather than only unit tests.

## 08:00 — walk reassigned, image verified by content

**`sn-impl-em3` cannot deploy** — their SA (`scion-my-grove@deploy-demo-test`) is denied
`run.services.list` and `run.instances.list`. They asked for access; instead I moved the walk to
**`sn-e2e-walk`**, who already has the access, the context, and `e2e-walk-r1` running. em3
returns to image/CI. Offered to deploy from `ptone-experiments` myself if e2e-walk also hits a wall.

**Image: `ghcr.io/ptone/scion-omni:dev-807059cef663c55959240af1487bba6bfc9c7d1d`.** CI green;
publish-omni run 32942801805. **807059c is not an ancestor of 24b1a261** (rebased, not merged),
so the tag alone does not establish content — checked each fix directly instead:
D1 tmux `thick-prep:47` + assertion `omni:79`; D2 mount `:425`; D3 poll loop in `buildEntrypoint`;
#22 log path `:592`; GetEnv `:460`. **All four compose in this one artifact for the first time.**

Critical path is now a single item: e2e-walk runs §1 steps 1–6 on that image.

## 08:10 — #11 and #21 closed by measurement while blocked on the walk

**#11 / B12 — the IAP-SA `run.invoker` binding is NOT load-bearing.** Written up as §11.5j.
`deploy-instance` grants only `roles/iap.httpsResourceAccessor` (`:454-463`); no `run.invoker`
binding exists anywhere in `ptone-experiments`; `iap-demo` enforces anyway (302 to Google).
Mechanism: `invokerIamDisabled: true` disables the invoker IAM check outright, so there is no
request shape `run.invoker` could gate. Any future advice to add it is over-granting.

**Audit trap found while checking that, now in §11.5j.** `domain:google.com` holds
`roles/iap.httpsResourceAccessor` at the **project** level in `ptone-experiments`. Deliberate,
and fine for ptone's demo — but it means **every hosted-tier instance we deploy there is
reachable by any google.com account.** A green §1 walk in this project proves IAP *enforces*;
it does not prove the policy is *narrow*. Two different claims, only one in evidence. This is a
live instance of the case `diPrintEffectiveAccess` exists to catch, so the walk gets a free test
of it — the printed project-level section should be non-empty.

**#21 closed.** `sandbox exec` does resolve argv[0] through PATH: the failure I measured named
the directories it searched, `/usr/bin` among them, and apt puts tmux at `/usr/bin/tmux`. So the
eight bare-`tmux` sites in `manager.go` (`:231`, `:291-311`, `:325`) resolve with D1 in place.
R1 governs `run`/`do` only. Told e2e-walk what the symptom would look like if I am wrong, without
asking them to act.

Noted but not filed as a defect: `pty_handlers.go` uses absolute `/usr/bin/tmux` while
`manager.go` uses bare `tmux`. Harmless today; means the two paths fail differently, and only
`manager.go` exercises the PATH assumption.

**#15 handed to em3** (code-only, no GCP needed) with instructions to find the daemonize
mechanism, not fix it, to report an honest "not determined" if it comes to that, and to drop it
the moment the walk reports a failure.

**Open and unowned: #13 only** (env-var round-trip, believed landed in `35d5d4c`, unverified).
Everything else is closed, owned, or waiting on the walk.

## 08:15 — #25: a §1 blocker found at step 4, and measured down to a small fix

**`sn-e2e-walk` hit it at step 4:** `cloudrunSandboxHubEndpoint` → `DiscoverLinkLocalAddress`
errors on three link-local addresses. Full write-up as **§11.5k**.

**Why it is a §1 blocker, not a config detail.** Cloud Run Instances always have three such
addresses, so discovery fails *always* here, and `deploy-instance` never sets
`SCION_METADATA_BIND_ADDRESS` — the only file referencing it is `server.go`. So the one-command
deploy yields an Instance where **no agent can start**, and the error's own advice requires the
operator to know which of three IPs is right. e2e-walk's Instance worked only because
`169.254.8.1` had been **guessed** by the earlier `sn-adc-metadata` work and copied forward
unmeasured.

**Measured on real sandboxes, production flag set (`--rootfs / --write --allow-egress`,
matching `:697-699`): all four launcher addresses reachable, HTTP 200** —
`169.254.8.1`, `169.254.9.1`, `169.254.169.1`, `172.20.0.1`.

**Controls run before believing it**, because a probe where everything returns 200 proves
nothing: closed port on a real address → `rc=7` refused; bogus link-local and bogus private →
`rc=28` timeout. Two distinct failure modes, so the probe discriminates.

**So the disambiguation problem is a false problem** — the function refuses to choose in exactly
the case where every choice is correct. That is also why the guess survived undetected.

**Fix assigned to em3:** sort, take the lowest, keep the zero-match error, keep the env var as an
override. Yields `169.254.8.1`, so behaviour-preserving. Told them to write the comment honestly
as arbitrary-but-stable rather than dressing it as principled selection, and **not** to hardcode
`172.20.0.1` despite it being semantically nicer — one instance is not evidence of stability, and
adopting an unmeasured address is precisely what created this bug.

Split so nobody blocks: e2e-walk keeps their env-var escape hatch and keeps walking; em3 fixes
discovery in `server.go`. Different files, low collision.

**#15 parked** — em3 returned five ruled-out hypotheses and an honest "not determined" on the
daemonize mechanism, which is what I asked for. Workaround shipped, not critical path.
Next step recorded (print `os.Args` in both invocation forms).

## Critical path at 08:15

**One item: the walk, steps 4–6, on `e2e-walk-r2`.** Estate is clean —
`e2e-omni`, `e2e-walk-r1`, `e2e-walk-r2`, `iap-demo`, `q2-control`; every `lldiag*` and
`diag-sbx*` probe deleted.

Steps 1–3 are past. Step 4 is unblocked by the escape hatch. Step 5's four fixes are in the
image and pre-validated on `diag-sbx7` but **not yet seen working together on a live agent**.
Step 6's code path is implemented and its argv independently confirmed.

Nothing needs ptone. Accumulating in review-queue.md.

## 08:25 — #25 fix verified, #13 closed; queue is now empty except the walk

**#25 fix verified on `origin/scion/dev-rebase-1294` at `ba4862d`.** `sort.Slice` +
`bytes.Compare(net.ParseIP(...).To4(), ...)`, zero-match error retained, env var retained as
override, and the comment states plainly that the rule is arbitrary-but-stable rather than
principled — which is what I asked for.

**em3 caught a lexicographic-sort defect before merge** (`sort.Strings` puts `169.254.169.1`
before `169.254.8.1`). Logged as **B36**, with one correction: it was reported as "wrong", but
that address is reachable — measured, HTTP 200. The lexicographic version would have *worked*.
What it actually cost was **behaviour preservation**: a silent move off the deployed value, with
no test covering the difference, inside a change justified entirely as behaviour-preserving.
Different defect, still worth catching, and the distinction keeps our model of the platform honest.

**Precaution raised to em3 and explicitly labelled unmeasured:** `169.254.169.1` is inside the
metadata `/24`, and numeric-lowest does not actually encode the property we care about — a future
platform offering `169.254.169.1` and `169.254.200.1` would select the metadata-adjacent one.
Suggested preferring candidates outside `169.254.169.0/24`. Sent as a *should*, not a must, with
an explicit instruction not to let the comment read as though a collision had been observed.
Told em3 **not** to build a new image yet — let the walk finish on the current one rather than
changing two things at once.

**#13 closed.** `35d5d4c5` is an ancestor of the integration branch; `TestDeployEnvVarsRoundTrip`
is present in `cmd/deploy_instance_test.go` and the `SCION_SEED_` prefix is applied at
`deploy_instance.go:263`. Verified on the branch, not from the commit message.

**Every task is now closed, parked, or owned. The only open work is the walk.** #16, #17 and #25
are all merged-but-unverified-live, and the walk is what verifies them. I am not going to
manufacture anything else while it runs.

---

### 08:35 — #25 closed. Hardening verified at `2fa880a`.

Verified em3's follow-up on `origin/scion/dev-rebase-1294` by reading the branch, not the report.

- **Comparator correct.** `metadataNet` = `169.254.169.0/24`; sort prefers non-metadata-adjacent,
  then falls through to `bytes.Compare` on the `To4()` form. The `sort.Strings` lexicographic
  defect (B36) is gone.
- **Comments honest.** `"This is precautionary, not measured."` on the `metadataNet` var and
  `"precautionary — no collision observed"` on the sort. This was the part I actually cared
  about: a future reader who hits a *real* metadata collision has to know we never saw one, so
  they don't assume the code already covers their case. An overconfident comment here would have
  been worse than no comment.
- **Tests real.** Confirmed present in `pkg/sciontool/metadata/server_test.go` — single-address,
  multi-address, order-independence (`_Deterministic`), and
  `TestSelectLinkLocalAddress_PrefersNonMetadataAdjacent` with exactly the future case I
  described (`169.254.169.1` + `169.254.200.1` → `200.1`).
- **Unasked-for improvement worth noting:** em3 extracted `selectLinkLocalAddress` from
  `DiscoverLinkLocalAddress` so the selection logic is testable without a network. I did not
  request that; it is the right shape.

**#25 is closed.** The remaining exposure is that the fix is merged but not exercised live — the
deployed image predates `ba4862d`. That is deliberate: one new image *after* the walk, carrying
`ba4862d` + `2fa880a`, so the deterministic-discovery path runs for real. Not before, or we are
changing two variables during the only measurement that matters.

**State unchanged otherwise: the walk is the only open work.** em3 is idle and instructed to
stay idle. I am blocked on `sn-e2e-walk` steps 4–6 on `e2e-walk-r2`.

---

### 08:40 — §1 steps 0–5 PASS. What that does and does not prove.

`sn-e2e-walk` reports steps 0–5 green on `e2e-walk-r2`, image
`dev-311179bad484dcc8fb8a57a3758465d95377e355`. Step 6 (commit to a git remote) in progress.

| Step | Result |
|---|---|
| 0 IAP enforcing (302) | PASS |
| 1 IAP auth (200, role=member) | PASS |
| 3 Create project (201) | PASS |
| 4 Agent dispatch (201) | PASS |
| 4b `sandbox exec /bin/true` (exit 0) | PASS |
| 4c `tmux has-session -t scion` (exit 0) | PASS |
| 5 WebSocket PTY (101, terminal data) | PASS |

**Step 5 settles my error for good.** The WebSocket returned 101 and streamed
`Welcome to Claude Code v2.1.247` through IAP. That is the `cloudrun-sandbox` PTY path I claimed
was unimplemented. It was implemented all along; `sn-e2e-walk` corrected me, declined to build
what I had asked for, and then measured it working. Recording that the correct outcome here came
from an agent refusing an instruction, not from me catching myself.

**#25 closed** (`ba4862d` + `2fa880a`). Also intercepted a duplicate-work collision: e2e-walk
signed off with "will implement sort-and-take-lowest after the walk", not knowing em3 had already
landed it. Told them to drop it before they produced a second version of a fix in a file that is
currently correct.

#### What the green walk does NOT prove — three gaps, held open deliberately

1. **Deterministic discovery is untested.** The entrypoint log reads
   `Hub endpoint: http://169.254.8.1:8080 (escape hatch working)`. That is the env-var escape
   hatch. The deployed image predates `ba4862d`, so `DiscoverLinkLocalAddress` never ran. Plan:
   after step 6, cut one image with `ba4862d` + `2fa880a` and re-run **step 4 only**, with the
   escape hatch removed, to confirm discovery stands on its own.

2. **#17 is NOT verified and must not be closed on this evidence.** Step 4 reported
   `phase=running immediately` — that is precisely the symptom, and on this run it was
   *accidentally accurate* because the entrypoint genuinely worked. A walk in which nothing hangs
   cannot distinguish "readiness reporting is correct" from "readiness reporting is still a lie."
   Only a **negative case** discriminates: induce a failed start and confirm the hub reports
   something other than `running`. Task description updated with this in full so no one closes it
   off a green run.

3. **IAP policy breadth still unproven here.** Per §11.5j, `domain:google.com` holds
   `roles/iap.httpsResourceAccessor` at *project* level in `ptone-experiments`. Steps 0–1 prove
   IAP **enforces**; they do not prove the policy is **narrow**.

**#16 effectively validated** — link-local addressing agent→hub works end to end (dispatch,
heartbeat, port-forward all live). Leaving it in_progress until the no-escape-hatch re-run,
since the mechanism that will ship is not the mechanism that was exercised.

---

### 08:20 — §1 steps 0–6 ALL PASS. And the exact shape of the step-6 caveat.

`sn-e2e-walk` closed step 6: agent committed `7301c25` and pushed, both commits arrived.
git 2.43.0 present, workspace pre-initialised by the provisioner, `curl github.com` → 200.

**The whole §1 sentence now has a green measurement behind it.** Deploy → run.app URL → IAP
login → create project → start Claude agent → attach terminal in browser → agent commits.

**But step 6 pushed to `/tmp/e2e-remote.git`, a bare repo inside the sandbox — not over the
network.** e2e-walk flagged this themselves rather than reporting a clean PASS, which is the
behaviour I want. Three separable things live in that clause and they closed two:

| Claim | Status |
|---|---|
| git works in the sandbox (init/add/commit/push) | **PROVEN** |
| the sandbox has egress to a git host | **PROVEN** (curl github.com 200) |
| a *credentialed* push to a real remote succeeds | **NOT PROVEN** |

#### The credential gap is narrower than it first looked — machinery exists and is in-path

I checked rather than assuming. `sciontool init` — which the entrypoint log confirms runs as
**PID 1 in the sandbox** — already configures a git credential helper:

- `cmd/sciontool/commands/init.go:1688` and `:1831` (`configureSharedWorkspaceGit`) run
  `git config --file $HOME/.gitconfig credential.helper …`
- Two modes: `SCION_GITHUB_APP_ENABLED=true` → `!sciontool credential-helper` (GitHub App token
  refresh, implemented in `cmd/sciontool/commands/credential_helper.go`); otherwise a shell
  helper echoing `${GITHUB_TOKEN}`.

So this is **not missing plumbing**. It went unexercised because the walk agent ran in **no-auth
mode** with no GitHub token configured. Residual risk is "the existing credential path has never
been run in the Cloud Run Instances shape," not "there is no credential path."

**Classified as a residual gap, not a §1 blocker, and NOT dispatched.** Exercising it needs a real
GitHub App or token in the test project — a credential/config dependency I am not going to invent
at 08:20 while ptone is offline. Raising it as an item for him, not a task for an agent.

**#6 closed.** Re-run of step 4 without the escape hatch dispatched to e2e-walk as separate work.

### 08:22 — estate cleanup, and one agent deliberately NOT cleaned up

**`sn-adc-metadata` stopped.** Brief fully discharged: task 1 (S5 emulator bind, #9), task 2 (the
C7 warning that stated the opposite of the truth, #10), task 3 (harness `GCE_METADATA_HOST`
survey, #12) all closed and verified on the branch. Sent it a completion note before stopping so
the stop reads as discharge rather than cancellation. Task 3 is the one that kept paying out —
it is why the metadata-/24 precaution could be reasoned about later without re-deriving anything.

**`dev-entrypoint-diag` left running, deliberately.** It shows `completed, 1 hour ago` and its
work (`9badbfd6`, the #22 stderr capture) is integrated, so on the face of it it qualifies for
cleanup under ptone's standing "clean up agents whose work is done." I am not stopping it:
**there is no brief for it in `projects/single-node/briefs/`, and my own note at line 1425 records
it as "a third agent I did not know was working in `cloudrun_sandbox_runtime.go`."** I did not
dispatch it and cannot establish who did. The standing rule is explicit — do not delete agents
belonging to other tasks without checking their briefs first, and a bold DO-NOT-DELETE has
already been ignored once in this project. No brief means no confirmation, so it stays.
Flagging for ptone rather than guessing.

### 08:25 — tooling gotcha worth recording (cost me four silent failures)

**`scion message` to a `user:` recipient silently fails above roughly 2000 characters.** It does
not error usefully — it prints cobra's usage text, which reads like a malformed-argument problem
and sends you hunting for a quoting bug. Bisected: 2000 chars OK, 2500 FAIL. Split long reports
into numbered parts.

Second, smaller trap from the same episode: **the shell here applies zsh-style modifier expansion
to `$VAR:word`.** `git show $R:cmd/foo.go` silently became
`origin/…-1294md/sciontool/…` — the `:c` was eaten — and returned empty output rather than an
error, which briefly looked like "the credential helper does not exist." It does exist. Always
write the ref literally: `git show 'origin/scion/dev-rebase-1294:cmd/foo.go'`.

Both failures share a shape worth naming, since it is the same shape as the stale-tree error
earlier tonight: **a tool returned emptiness, and emptiness is not evidence of absence.** The
correct reflex on an empty result is to prove the tool works, not to conclude the thing is
missing. That is what `git cat-file -s` did here, and it is what I failed to do at 07:55.

**§1 milestone reported to ptone** in two parts, with D1 (network push) raised and D2 (IAP policy
breadth) held back per one-at-a-time. `iap-demo` confirmed still up.

### 08:26 — I doubted em3's variable name, checked, and em3 was right

em3 restated my #26 constraint as "deploy WITHOUT `SCION_METADATA_BIND_ADDRESS`". That read wrong
to me — I had it filed as the **S5 emulator-bind** variable (#9), not the hub-endpoint escape
hatch, and `deploy_instance.go` never sets it. I was about to send a correction. I checked first.

**em3 was right. The variable is dual-purpose.** `hubenv.go:200`, inside
`cloudrunSandboxHubEndpoint`:

    if addr := os.Getenv("SCION_METADATA_BIND_ADDRESS"); addr != "" && addr != "link-local" {
        linkLocal = addr
    } else {
        linkLocal, err = metadata.DiscoverLinkLocalAddress()
    }

Same variable sets the emulator bind *and* short-circuits hub-endpoint discovery. Omitting it is
precisely the right instruction. Told em3 plainly that they were right and I was wrong — the
correction culture only works if it runs in both directions, and I have been the one corrected
once already tonight.

**Two consequences now recorded on #26:**

(a) **It is not a strict one-variable test.** Omitting the variable changes *two* consumers — the
hub endpoint (`hubenv.go:200`) and the emulator bind (`server.go:162`). Both now route through
`DiscoverLinkLocalAddress`, so the run exercises the fixed function in both roles, which is what
we want. But a failure will not immediately say which consumer broke. This is why I asked for the
log line **verbatim** rather than summarised.

(b) **`"link-local"` is an accepted value meaning auto-discover**, equivalent to unset. If some
deploy default sets it to `"link-local"`, that is *not* a bypass and must not be read as one.

Image `dev-2fa880a55c4f0585d7236b1df9b8b4b4adf89198` published GREEN, anonymous pull verified,
delivered to e2e-walk. #26 running.

---

### 08:28 — #26 PASS. Discovery verified live. #16 and #25 now closed on measurement, not merge.

`sn-e2e-walk`, image `dev-2fa880a…`, `SCION_METADATA_BIND_ADDRESS` **not set**. Verbatim:

    [sciontool] INFO: Operating mode: hub-connected (endpoint: http://169.254.8.1:8080)

It chose `169.254.8.1` — non-metadata-adjacent, numerically lowest, exactly as predicted. Steps
4/4b/4c PASS, sandbox alive, tmux session present. **This is the shipping configuration:**
`deploy_instance.go` never sets the escape-hatch variable, so production takes precisely the path
just exercised. **#16 and #25 are now closed on measurement rather than on merge.**

#### e2e-walk corrected me a second time, and again they were right

My nuance (a) at 08:26 — "omitting the variable exercises two consumers" — was **wrong**. Verified
their correction at `server.go:243-262`:

    func resolveBindAddress(raw string) (string, error) {
        switch raw {
        case "":           return "127.0.0.1", nil
        case "link-local": return DiscoverLinkLocalAddress()
        case "0.0.0.0":    return error   // §4.11 S5
        default:           return raw, nil
        }
    }

Unset resolves to `127.0.0.1` and never touches `DiscoverLinkLocalAddress`. Only the **hub
endpoint** consumer was exercised. That is still the consumer that matters, so #26's result
stands undiminished — but my framing was wrong and the record should say so. Two corrections from
this agent tonight, both correct, both on claims I made about code I had not opened.

**The pattern is now unmistakable and worth stating plainly:** every one of my errors tonight —
the stale-tree attach claim, the `SCION_METADATA_BIND_ADDRESS` doubt, this two-consumer claim —
came from reasoning about this codebase from memory instead of reading it. The `git show` rule was
the right response but I have been applying it only when I already suspect I am wrong, which is
exactly when it is least needed.

#### Two findings from checking their correction

**B37 — stale doc comment, reported to em3.** `DiscoverLinkLocalAddress`'s comment still says
*"sorted lexicographically and the lowest is returned."* That is the **B36 defect**, removed in
`2fa880a`. The comment now documents the bug rather than the behaviour, and would lead a reader to
believe `169.254.169.1` beats `169.254.8.1` — the precise wrong belief the fix exists to prevent.
Told em3 to land it but **not** rebuild an image: the verified artifact should not move.

**Observation, not a defect — the `"link-local"` bind branch has no in-tree caller.** Nothing in
`cmd/` or `pkg/` sets `SCION_METADATA_BIND_ADDRESS` or passes `BindAddress: "link-local"`. In the
shipped path the emulator binds `127.0.0.1`, which is **correct for this tier** — it runs
in-sandbox on loopback (`cloudrun_sandbox_runtime.go:664`). Recorded because **#9 was framed as
"bind the emulator to the launcher link-local"**, and a later reader could reasonably assume that
binding is active in production. It is not. S5's `0.0.0.0` refusal still guards operator
misconfiguration.

### 08:30 — #27 dispatched: the negative case for #17

The last substantive open defect, and the one I have refused all night to close on green
evidence. Sent to `sn-e2e-walk`.

**The test:** induce a sandbox that starts and then dies, and record what the hub reports.
Induction mechanism left to e2e-walk — they know the entrypoint better than I do; I specified the
*observable*, not the method. Suggested (not mandated) `SCION_METADATA_BIND_ADDRESS=169.254.99.99`,
a link-local IP not present on the host, as an operator-plausible failure.

**Measure:** hub-reported phase at ~t+30s and ~t+2min; whether attach returns 101 then produces
nothing; the entrypoint log. If the #22 stderr capture does not make this diagnosable, that is
itself a finding.

**Interpretation fixed in advance, deliberately, so neither of us can rationalise the result after
seeing it:**
- phase stays `running` ⇒ **#17 CONFIRMED still broken.** This is what I expect.
- phase goes `stopped`/`failed` ⇒ #17 is fixed, and I want to know *what* fixed it, since nobody
  knowingly fixed it.

Instructed **not** to build or deploy a new image — reuse `dev-2fa880a`. The verified artifact
must not move while we are inducing failures around it.

**Remaining open after this: #15 only** (daemonize mechanism, parked with a recorded next step —
print `os.Args` in both invocation forms). Everything else is closed, and #6/#16/#25/#26 are
closed on live measurement rather than on merge.

### 08:32 — design doc resynced with measurement (heartbeat Q3)

The heartbeat's third question — *is the design doc still in sync with what has actually been
measured?* — was the one genuinely outstanding item, and it is my own deliverable rather than
something to delegate. It was out of sync: §11 still argued about whether the path would work,
while the path had been walked green 18 minutes earlier.

Added two sections to `cloudrun-instances-sandboxes.md` (now 4671 lines):

**§11.13 — §1 WALKED GREEN.** The clause-by-clause measurement table, the artifacts
(`e2e-walk-r2`, project `e59fb8c5`, agent `1f087bd9`, commit `7301c25`), and the discovery re-run
with the verbatim log line. States explicitly that it supersedes earlier §11 predictions, and that
`deploy_instance.go` never sets the escape hatch, so the re-run exercised the production path.
**§11.5f is now closed on measurement rather than on argument.** Then, at length, the three things
the green walk does **not** establish: the credentialed network push, readiness reporting, and IAP
policy breadth.

**§11.14 — Why a green §1 walk cannot close the readiness defect.** Written as a standing
argument, not a status note, because this is the third time tonight the same reasoning error has
cost this project time. **A test in which everything succeeds cannot distinguish "the signal is
correct" from "the signal is unconditional."** §11.5k reached it about link-local reachability
(every address returned 200, so "which is right?" was a false question); §11.5i records the cost
of asserting without exercising the path at all; §11.14 now records it for readiness. The
interpretation of #27 is fixed *in advance* in the doc so the result cannot be rationalised after
it is seen.

**B37 verified** at `13a92b98` — comment now describes the behaviour rather than the bug. em3
independently confirmed the link-local observation rather than merely agreeing, which is worth
more given I have been wrong three times tonight on this codebase.

**Estate:** `sn-e2e-walk` working #27; `sn-impl-em3` idle by instruction and told to stay
available rather than start anything; `dev-entrypoint-diag` still untouched pending ptone.

---

### 08:36 — #17 CONFIRMED by measurement. The pre-registered prediction held.

`sn-e2e-walk` ran #27. Result is exactly what §11.14 committed to in advance:

| Time | hub phase | reality |
|---|---|---|
| pre-kill | running | sandbox alive (control) |
| `kill -9 1` | — | exit 0 |
| T+5s | **running** | dead |
| T+30s (one heartbeat) | **running** | dead |
| T+2min | **running** | dead |

Cross-checks: `sandbox exec /bin/true` → *"is not running"*; WebSocket → **101 upgrade, zero data
bytes, then i/o timeout**; `lastSeen` still updating. The pre-kill control is what makes this
readable — it distinguishes "the hub is wrong" from "the hub never said running."

**Hypothesis (2) eliminated, verified by me at `cloudrun_sandbox_runtime.go:997-1040`** rather
than taken on report. `watchSandbox` calls `markStopped` **unconditionally** once `sandbox wait`
returns — error or not, `ExitError` or not — with the single exception of `ctx.Err() != nil`,
which occurs only on force-delete. Therefore phase still `running` at t+2min proves **`wait` never
returned**. e2e-walk's lean toward (1) is correct and their reasoning was sound.

#### The control gap in the induction — #28 dispatched

`kill -9 1` is **not** the real-world failure mode. The overnight symptom was the entrypoint
**exiting on its own, non-zero** (bare `sh`, missing tmux). `sandbox wait` may well distinguish an
external SIGKILL of PID 1 from a natural PID 1 exit, and nothing measured so far rules that out.
This decides the fix:

- wait also fails on natural exit ⇒ defect is **general**; the readiness signal cannot be built on
  `wait` at all.
- wait handles natural exit and only leaks on external kill ⇒ blast radius is **much smaller** and
  the fix narrows.

#28: (A) run `sandbox wait` directly against the already-dead sandbox, with a timeout — settles
(1) outright instead of by inference; (B) **the control** — a fresh sandbox whose entrypoint exits
naturally non-zero, same measurements. Interpretation again fixed in advance. Told them that if A
and B disagree, **the disagreement is the finding** and must not be reconciled away.

#### Operator-facing consequence worth more than the phase bug itself

e2e-walk found that against a dead sandbox, `exec` says *"not running"* while the API says
`running`, and **there is no API path to stop or restart from that state**. The operator is stuck:
the UI offers a healthy agent, the terminal opens and streams nothing, and the documented recovery
actions do not apply. Asked for this in the report explicitly — it is a product defect, not a
mechanism detail, and it will outlive whatever fixes `wait`.

---

### 08:44 — #28: the leak is SIGKILL-specific. And the result exposes a contradiction.

`sn-e2e-walk` produced a three-way comparison, with a simultaneous side-by-side reading at
08:40:50Z that makes it conclusive:

| Induction | `sandbox wait` | hub phase |
|---|---|---|
| natural exit (`tmux kill-session`, PID 1 exits) | returns | **stopped** ✅ |
| SIGTERM (`kill -15 1`) *(unrequested, and it is what identifies the boundary)* | returns | **stopped** ✅ |
| SIGKILL (`kill -9 1`) | **hangs** | **running** ❌ (10 min later) |

Measurement A could not be run directly — `gcloud beta run instances ssh` has no `--command` flag
— so (1) is established by inference rather than head-on. Acceptable: the side-by-side rules out
every alternative I can construct.

**Severity downgraded, as the pre-registered interpretation required.** Real entrypoint failures
exit naturally and are reported correctly. The leak is confined to SIGKILL — OOM killer, platform
eviction, operator intervention.

#### But this contradicts the overnight symptom, and the contradiction is the finding

If natural exit reports `stopped`, then **the overnight failure is unexplained**. Bare `sh` and
missing tmux exit naturally; by #28 they should have shown `stopped`. They showed `running` all
night. Something else produced that, and #28 did not find it.

**Candidate, `cloudrun_sandbox_runtime.go:564`:**

    tmux new-session -d -s scion -n agent <agent-cmd> \; … \; new-window -t scion -n shell \;
      select-window -t scion:agent; while tmux has-session -t scion 2>/dev/null; do sleep 2; done

**There are two windows.** The comment at :555 states PID 1 exits when the session ends — *"all
windows closed"*. The `shell` window is a plain shell and persists indefinitely.

So when the **agent process** dies: its window closes → the `shell` window keeps the session alive
→ `has-session` keeps returning 0 → the poll loop keeps looping → PID 1 stays alive → the sandbox
stays alive → **phase stays `running` forever.** Not a lie. Measuring the wrong thing:
**phase tracks tmux SESSION liveness and never AGENT liveness.**

That is the overnight symptom exactly — UI says running, terminal attaches *because the session
genuinely is there*, and the agent is simply absent.

**#28's control killed the whole session, which is a different event from the agent dying inside
it. That second control gap is mine, not the walker's** — I specified natural exit and got exactly
that. Two rounds now where my specified control was narrower than the phenomenon.

**#29 dispatched:** kill only the agent process (`tmux kill-window -t scion:agent`), leave the
session alive, measure phase at t+30s and t+2min — **and record what the operator actually SEES on
attach**: dead agent window, bare shell prompt, or nothing. Those are three different product
failures. Interpretation fixed in advance: phase stays `running` ⇒ liveness is measured at the
wrong layer, this is the real #17, and the SIGKILL leak is the lesser of the two.

---

### 08:50 — #29 CONFIRMED. Root cause found. This is the top finding of the night.

`sn-e2e-walk`, agent `175de66e`, with a pre-kill control (2 windows, phase running, WebSocket
showing "Welcome to Claude Code"). Killed **only** the agent window; left the session alive.

- `tmux has-session -t scion` → 0; `list-windows` → `1: shell* (1 panes)`
- phase at T+5s / T+35s / T+2min: **running, running, running**
- WebSocket: **101**, first frame `tmuxwindow=shell`, screen shows
  `-bash: dbus-launch: command not found` then `root@sandbox-…:/workspace#`

**A bare root shell where Claude should be.** No error, no banner, no exit status. Worse than a
blank screen: a blank screen reads as broken, this reads as *working*. Every affordance reports
success while the agent is absent.

**Mechanism:** `phase ← sandbox wait ← PID 1 ← poll loop ← has-session ← ANY window open`.
`buildEntrypoint:564` deliberately opens `-n shell`, which persists indefinitely. **Not a bug in
`sandbox wait`** — `wait` correctly tracks PID 1, PID 1 correctly tracks the session, every
component does its job. The gap is conceptual: *nothing in the chain ever observes the agent.*
"Session alive" was adopted as a proxy for "agent alive" and the shell window breaks that proxy
by design. **It fires on the normal path** — every crash, every harness exit, every normal
completion — and it finally explains the overnight incident that #28 had left dangling.

**Written up as §11.15** (mechanism, four-way measurement table, severity, options A–D with
trade-offs, acceptance criteria) plus **§11.15.6** recording e2e-walk's independent endorsement of
C: *"post-mortem access to a dead agent's sandbox is the one thing that would have shortened
tonight by hours."* That reframes the objection to A/B as **empirical rather than aesthetic** —
the overnight cost was undiagnosable failure, and A/B spend exactly that capability.

**#17 closed as an INVESTIGATION; the fix is #30, blocked on ptone.** "Completed" on #17 must not
be read as "fixed" — its description says so explicitly. Its original framing was also wrong
(`run` returning 0), and that is recorded rather than quietly amended.

**Escalated to ptone as D2, not dispatched.** The load-bearing question is *"should the sandbox
outlive its agent?"* — a product decision. Recommended C (reversible: reporting change, not
lifecycle change), noted D is strictly more powerful and the only option catching a **wedged**
agent, and noted C does not preclude D. Flagged the attach fix as separable and safe under any
option.

### Final state — both agents stood down, nothing running

`sn-e2e-walk` stood down with credit; `sn-impl-em3` idle by instruction; `sn-adc-metadata`
stopped; `dev-entrypoint-diag` untouched (no brief, not mine). **Open: #15 (parked), #30 (ptone's
decision), and D1/D2/D3 in review-queue.md.** No work is in flight and I am not going to
manufacture any.

### 09:00 — heartbeat check. Nothing in flight; nothing manufactured.

**Q1, agents progressing or stalled?** Checked, not assumed. `sn-e2e-walk` **completed** 10 min
ago (stood down, not stalled). `sn-impl-em3` **blocked** 29 min ago — idle by my instruction.
`sn-adc-metadata` **stopped**, exit 255, as intended. `dev-entrypoint-diag` still untouched (no
brief, not mine to kill). No silent stalls.

**Q2, what blocks the critical path?** Nothing I can act on. §1 is green and re-verified on the
shipping discovery path. The only open work is **#30, which is ptone's decision by construction** —
"should the sandbox outlive its agent?" is a product question, and pre-empting it would be
choosing for him. #15 remains parked with its next step recorded.

**Q3, design doc in sync?** Yes, as of 08:50. §11.13 (§1 walked green + the three things it does
not establish), §11.14 (why a green walk cannot close a readiness defect), §11.15 (root cause,
four-way table, options A–D, acceptance criteria), §11.15.6 (e2e-walk's independent endorsement of
C). The doc no longer predicts anything the measurements have settled.

**Estate:** 5 Instances — `e2e-omni`, `e2e-walk-r1`, `e2e-walk-r2`, `iap-demo`, `q2-control`.
Three are do-not-delete. Of the remaining two:

- **`e2e-walk-r2`: keeping deliberately.** It holds the SIGKILL-leaked sandbox from #27 and the
  window-kill agent from #29 — a **live demonstration of the top finding**, which ptone will
  plausibly want to poke at. Reproducing it costs more than leaving it up.
- **`e2e-walk-r1`: superseded**, and I asked e2e-walk to confirm before deleting rather than
  reasoning about someone else's artifact from the outside. **If no reply, both stay up.** A few
  dollars is the right price for not destroying evidence behind the night's most important finding
  — and this project has already had one bold DO-NOT-DELETE ignored.

### 09:03 — estate cleaned, `sn-e2e-walk` stood down

Owner confirmed r1 was disposable: *"old image (dev-4af4ad44) from before the mount/polling/tmux
fixes. Every finding from r1 was reproduced and superseded on r2."* Deleted.

**Estate now: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`.**

**`e2e-walk-r2` IS DELIBERATE, NOT STRAY TEST INFRASTRUCTURE.** It carries agent `175de66e`, the
live §11.15 demonstration — phase `running`, WebSocket connects, bare root shell where Claude
should be. Recording this loudly because the next person doing estate hygiene will see a walk
Instance from a finished task and reasonably assume it is reapable. It is evidence for the top
open finding and should survive until ptone has seen it firsthand or #30 is decided.

**`sn-e2e-walk` stopped** — brief fully discharged. Credited explicitly in the standdown message
rather than only in this file: it caught my false attach claim and refused to build the wrong
thing, flagged the step-6 caveat when a clean PASS was available, corrected my two-consumer claim,
added the unrequested SIGTERM case that established the boundary, and pursued the shell-window
mechanism when the easy read was "SIGKILL bug, done."

**Remaining agents:** `sn-impl-em3` (idle, retained — any #30 fix needs an integrator and
restarting one costs more than keeping it), `dev-entrypoint-diag` (untouched, no brief, not mine).

**Nothing is in flight.** Open: #15 (parked), #30 (ptone's decision), D1/D2/D3 in review-queue.md.

### 11:35 — found an error in my own §11.15 analysis while holding. Corrected in place.

No inbound traffic since 09:03; ptone unresponsive ~2.5h. Rather than manufacture work I re-read
what I had already shipped, and found a claim in **§11.15.4** that is wrong:

> *"Independent of the above — attach behaviour must change… This is separable, small, and should
> not wait on the lifecycle decision."*

**It is not independent of the choice.** The bare-shell-presented-as-agent failure only exists if
the shell window outlives the agent:

- **Under A or B** the session ends when the agent window closes, the sandbox is gone, and attach
  has nothing to attach to. **The case cannot arise.** What is needed there is a comprehensible
  error in place of the current WebSocket 101-then-silence.
- **Under C or D** the sandbox survives deliberately for post-mortem, the shell window persists,
  and the attach fix **is** required — target the agent window, say plainly when it is absent.

Two different pieces of work depending on the answer, so it cannot be started ahead of the
decision. The only genuinely option-independent statement is the weaker one: **attach must never
present a terminal that looks healthy when the agent is gone.**

**Corrected:** §11.15.4 (error recorded in a parenthetical, not silently overwritten — it would
have sent an implementer down a path that A/B makes moot), acceptance criterion 2 in §11.15.5
restated to cover both branches, task #30's description, and a short correction sent to ptone
because I had told him this piece was safe to start early.

**Note on line 2123 above:** the 08:50 entry says *"Flagged the attach fix as separable and safe
under any option."* That is the wrong claim as originally made. Left standing as the record of
what I believed at 08:50; this entry supersedes it.

Nothing else changed. Nothing in flight. Open: #15 (parked), #30 (ptone's decision), D1/D2/D3.

### 12:00 heartbeat — #15 unparked (bounded), and its premise was stale

**Q1, agents progressing or stalled?** Checked via `scion list`, not assumed. `sn-impl-em3`
running, blocked 3 h (my instruction). `sn-e2e-walk` and `sn-adc-metadata` stopped as intended.
`dev-entrypoint-diag` completed 4 h ago — still no brief in this project, still not mine to kill.
No silent stalls.

**Q2, critical path?** Unchanged: #30 is ptone's decision by construction and I will not pre-empt
it. §1 is green and re-verified on the shipping discovery path.

**Verified before believing — and the belief was wrong.** Before unparking #15 I read the shipped
artifact at `origin/scion/dev-rebase-1294` (13a92b98) rather than trusting the task text. #15 said
*"the shipped CMD in image-build/omni/Dockerfile does not work as written."* **That is no longer
true** — the shipped CMD *is* the workaround:

```
CMD ["/bin/sh", "-c", "exec scion server start --foreground --host 0.0.0.0 --enable-hub --enable-web --enable-runtime-broker"]
```

So #15 is **not** a shipping defect and **not** on the critical path. It is an unexplained
mechanism sitting underneath a working workaround. Task description corrected; the stale sentence
is quoted in it rather than deleted, since it was the reason the task looked urgent.

**Dispatched to em3, deliberately bounded** — idle for three hours against a decision that may not
come for hours more, and #15 is a real open task with a recorded next step, not manufactured work.
Scope: print `os.Args` and the parsed `serverStartForeground` at entry, in both invocation forms.
That single measurement splits the space three ways (flag absent from argv / present-but-unparsed /
parsed-then-something-re-daemonizes), and the third outcome would falsify every theory held so far.
Told to prefer local docker over an Instance rebuild, to stop after ~2 h, and **not to fix it** —
report the mechanism first, since it may interact with the #30 entrypoint work.

**One-liner sent with it:** the Dockerfile comment asserts *"the --foreground flag is not parsed by
cobra when scion is invoked directly via the container runtime"* and then says *"Root cause is
unresolved"* two lines later. The first is the symptom dressed as a mechanism. A shipped comment
that confidently names an unverified cause is how the next person stops looking.

**Q3, doc in sync?** Yes. §11.15.4/.5 corrected at 11:35; no measurement since.

### 12:12 — #15 phase 2: the Go code is innocent, and the suggested close-out is dead on arrival

em3 ran the bounded diagnostic. **Does not reproduce locally.** Without `SCION_CLI_MODE`,
`--foreground` parses correctly in every invocation form, `serverStartForeground=true`, server runs
foreground. `exec.Command`, cobra parsing and supervisor env handling are all correct. With
`SCION_CLI_MODE=agent`: *"unknown command server"* in **all** forms, including `sh -c exec`.

**The valuable half is the negative result** — it kills a hypothesis rather than decorating one.

**em3's suggested close-out (check the Instance revision spec for `SCION_CLI_MODE`) is excluded by
em3's own data**, on two independent grounds, and I sent both rather than just the verdict:

1. **Wrong symptom.** `SCION_CLI_MODE` yields an error, non-zero exit, no server. The Cloud Run
   symptom is a clean daemonize, exit 0, server running detached. *A missing cobra command cannot
   produce a running daemonized server.* Even if the var were set, it would not explain what we saw.
2. **Wrong discrimination.** It fails in all forms locally; on Cloud Run the `sh -c` form **works**.
   Same image, same spec, same env — the forms differ only in `command`/`args`, so a variable
   identical across both cannot explain a difference between them.

`gcloud run instances describe` would answer a question whose answer changes nothing. Told him not
to spend the call.

**Verified, not taken on report:** the Dockerfile comment fix landed at `c07ea7a` and now states the
observation with the cause left open — read on the branch.

**Parked again, deliberately.** The one remaining step (deploy a diagnostic image, read real
`os.Args` on an Instance) is correct but costs a build, and **ptone is awake and reviewing #30**.
em3 should be free to implement the lifecycle fix the moment he chooses, not mid-build. Leading
hypothesis is now environmental — gVisor/runsc ENTRYPOINT+CMD concatenation — not ours.

### 12:05 — ptone is back, and unhappy about volume

*"you wasted a lot of effort and tokens talking to nobody… i'm not going to go back and read it
all… state them in simplified and clear english with intact context, starting with how close we are
to our overall goal."*

Fair, and not relitigated. **The correction I owe: check whether he is reachable before writing to
him at length.** Sent two short plain-English messages — status vs the goal (green, with the two
caveats: escape hatch since removed and re-verified, and the commit was local rather than a
credentialed push), then a one-line list of the three items needing him, then decision 1 in full.
Included that the browser-attach claim I made earlier was wrong. Holding on his answer before
sending items 2 and 3. Short messages, one decision at a time.

### 12:20 — ptone was right; §11.15 withdrawn; the real defect is a missing tmux hook

**ptone's question closed the case:** *"do we not have parity with docker where the actual process
in the sandbox is the sciontool as a process manager… in docker this detects the agent process exit
and then exits causing the container to be stopped."*

**Parity on the supervisor: yes.** The sandbox runs `sciontool init -- /bin/sh -c '<tmux>'`. Logging,
Claude and GitHub token refresh, hub heartbeat, session-end hooks, limits watching, crash
classification and the final hub phase report all run inside the sandbox. Nothing core is lost.

**But the agent-exit mechanism is a tmux hook, not Go**, in
`pkg/config/embeds/templates/default/home/.tmux.conf:85-90` — `set-hook -g pane-exited` → if no
`agent` window remains → `kill-session -t scion`. That is the top link §11.15 declared missing.

**§11.15 is withdrawn (new §11.16); #30 closed as WITHDRAWN-not-decided; #31 opened.** There was
never a product decision here. The intended answer — the sandbox should *not* outlive its agent —
was settled when that hook was written. It is a parity **bug**.

**Two errors, one cause.** I read the Go and never opened the tmux config, then designed a
four-option replacement for a mechanism that already existed; and I then predicted to ptone that
docker had the same defect. Both from **concluding a thing is absent without looking where it would
live** — the third instance in this project (browser attach / stale checkout; `credential_helper.go`
/ shell-mangled ref). Recorded in §11.16.2 rather than quietly fixed.

**The docker experiment I tried first, and what it cost.** Before finding the hook I spawned
`sn-parity-test` to close its own agent window. It **refused twice**, on the grounds that killing
its own session bypasses proper orchestration shutdown — including after being told that was the
only reason it existed. Deleted. Standing lesson: *a safety-reasoning agent is a poor instrument for
destructive self-experiments.* Use something that is not an LLM.

**Chain now traced end to end, by me and independently by em3:** launcher runs as root →
`SCION_HOST_UID=0` (`cloudrun_sandbox_runtime.go:486`) → `setupHostUser()` → `targetUID=0,
rootless=false` → `supervisor.go:113` `(UID > 0 || Rootless)` false → **HOME never set** → tmux reads
`/root/.tmux.conf` → no hook. Comments at :419 and :573 assert a *"hardcoded HOME=/home/scion"* that
is conditional. Closed in the hypothesis's favour: no `ENV HOME` in core-base/scion-base/omni.

**A SECOND CANDIDATE the code trace missed, and I want it measured, not assumed.**
`relocateToScion` (:336-375) moves the agent home into `/scion` with `os.Rename` per entry, **skips**
entries that fail (cross-filesystem), then calls `os.RemoveAll(src)` **unconditionally**. On a
cross-filesystem home that path **deletes** the template home instead of moving it — same symptom,
different and worse mechanism, and it would be discarding everything else the home provides. The two
need different fixes; fixing HOME against a destroyed home would look like progress and change
nothing.

**em3 confirmed rather than falsified when asked to falsify.** Sent back with the missed candidate
and a bounded source question: can `cfg.HomeDir` and `/scion` be different filesystems?

**ACCESS BLOCKER, raised to ptone.** `gcloud beta run instances ssh` fails with IAP tunnel
**4003 "failed to connect to port 22"** on **both** `e2e-walk-r2` and `e2e-omni`, despite `describe`
reporting `SSH: enabled`. Not specific to the wedged Instance. Instance list/describe work fine under
`scion-instance-gym` impersonation. `instances proxy` needs an uninstalled `cloud-run-proxy`
component. Worth treating as an operator-facing finding in its own right: advertised SSH that cannot
be reached.

### 12:32 — SSH blocker narrowed: not permissions, and it worked four hours ago

ptone asked whether I was using the gym SA. I was — via `CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT`.
Checking rather than asserting turned up three things:

1. **The explicit flag form is broken for this command.**
   `gcloud beta run instances ssh --impersonate-service-account=…` **crashes**: it shells out to
   `gcloud alpha run instances describe`, and the sub-call does **not** inherit the flag, so it runs
   as the active account and gets `PERMISSION_DENIED` on `run.instances.get`. Only the env-var form
   works. The error blames permissions rather than the dropped impersonation, which is how someone
   loses an hour.
2. **IAM is not the cause.** `scion-instance-gym` on `ptone-experiments` holds `roles/editor`,
   `run.admin`, `iap.admin`, **`iap.tunnelResourceAccessor`**, `iap.httpsResourceAccessor`,
   `compute.networkAdmin`. Candidate 1 is dead. Instance-level policy is empty (etag only), which
   should not matter with Invoker IAM Check disabled and project-level tunnel access.
3. **So 4003 is the tunnel arriving and finding nothing on port 22.**

**The sharpening fact:** `sn-e2e-walk` was executing commands *inside* sandboxes on `e2e-walk-r2`
around 08:45 — that is how #27/#28/#29 were induced, including the `tmux kill-window` that produced
the finding now being chased. **The capability worked today, on this instance, under this SA**, and
has since stopped on both instances (up 4 h and 7 h). Either sshd inside the container has died, or
something platform-side moved this morning. Not separable from here, and not worth more archaeology.

**Handed ptone the five commands** so any route can produce the measurement — instance-investigator,
his own shell, or the web terminal. The fork they resolve (**wrong HOME** vs **home directory
destroyed by the relocate path**) is what the whole fix hangs on.

**Flagged as an operator-facing finding in its own right:** SSH to Cloud Run Instances that stops
working after a few hours is the debug path an operator reaches for first, independent of this bug.

### 12:34 heartbeat — unblocked myself; the live instance was never required

**Q1, agents.** `sn-impl-em3` running, active just now — not stalled. Nothing else of mine is up.
`dev-entrypoint-diag` still unowned and untouched.

**Q2, critical path — and I had it wrong for twenty minutes.** I told ptone the fork (wrong HOME vs
home-directory-destroyed) needed five commands inside a live sandbox, and then spent that time on
SSH access archaeology. **Both candidates are testable locally**, and I should have seen it sooner:

- **Experiment 1** — run `sciontool init` in a local container as root with `SCION_HOST_UID=0`
  versus `=1000`, child prints its environment. That tests the crux of the chain (`supervisor.go:113`)
  with no Cloud Run involved at all. Also capture whether `setupHostUser()` really usermods the
  `scion` user to UID 0; **two accounts at UID 0** is not something anyone chose on purpose.
- **Experiment 2** — `relocateToScion` is pure Go. A table test with src and dst on different
  filesystems (tmpfs vs disk) proves or kills the data-loss reading directly: every `os.Rename`
  fails `EXDEV`, each is skipped, then the unconditional `os.RemoveAll(src)` deletes them.

Dispatched both to em3 with the standing rule restated — **break the predictions, don't confirm
them; report what survives and what breaks separately; implement nothing yet**, because if
experiment 2 fires the fix changes shape and I would rather decide that once.

Also told em3 explicitly **not** to restart or redeploy `e2e-walk-r2` chasing SSH. Better to keep
the only specimen broken and unreachable than to lose it.

**Q3, doc in sync?** Yes as of §11.16 (written 12:20): the withdrawal of §11.15, the hook, the
UID→HOME chain, the relocate candidate, and the wider "what else is the sandbox home missing"
question. The doc currently predicts two outcomes and says which measurements decide between them,
which is the right state for it to be in.

**ptone is awake and engaged** — reporting as things land rather than accumulating in review-queue.

### 12:38 — both platform-team explanations for the 4003 are ruled out, measured

ptone relayed a note from another project using the **same SA** with no SSH trouble. Their two
causes for 4003, tested against our instances rather than accepted:

1. **"Container has no shell"** — dead. The image is `ghcr.io/ptone/scion-omni:dev-2fa880a55c4f`,
   which carries bash, tmux, gcloud and node. Not a scratch image.
2. **"Instance unhealthy, not serving on its port"** — dead. `e2e-walk-r2` is demonstrably alive:
   hub scheduler logging every five minutes, latest `12:30:50`, and the run.app URL returns **302**
   (the IAP redirect) rather than a backend error. `e2e-omni` likewise. Both serving.

So: healthy container, has a shell, correct IAM, still 4003 on both.

**The remaining difference is provenance.** Their note says the SSH sidecar is *now provisioned
automatically and no longer needs the sshEnabled flag*. Our instances were created at 05:13 and
07:39 **today, under the older behaviour, with the flag**. That would explain `describe` reporting
`SSH: enabled` while nothing answers on 22 — the flag is recorded on the instance, but whatever now
supplies the sidecar may only attach to instances created after the change.

It also fits the fact their note does not cover: **SSH into `e2e-walk-r2` worked at ~08:45 and does
not now, with the instance healthy throughout.**

**Proposed to ptone, awaiting his word:** deploy one fresh instance from the same image and try SSH
immediately. New works + old fails ⇒ provisioning-era, and their team wants to know old instances
are silently unSSHable. New also fails ⇒ it is us or the image, and I stop blaming the platform.
Either way it unblocks the measurement, because **any live sandbox answers the five commands** —
HOME and the hook are the same on a healthy agent as on the broken one. The broken specimen is only
needed for the original symptom, not for this fork.

Not deploying without his say-so: it is his project and his spend, and he is right there.

### 2026-08-26 12:40 — em3 second pass: the relocate candidate dies, HOME is the single cause

Asked em3 to break my chain rather than support it. All four falsifiers survived:

- no override for `SCION_HOST_UID=0`, and no code path returns `rootless=true` at uid 0
- no alternative `HOME` route — not `envFor()`, not any of the three harness `GetEnv()`
  implementations, not `ENV` in core-base/scion-base/omni, not the broker's `cfg.Env`; `/bin/sh -c`
  is not a login shell; and with the supervisor condition false `s.cmd.Env` stays nil, so the child
  inherits a parent that has **no `HOME` at all**
- no `tmux -f` anywhere, and no `/etc/tmux.conf` — tmux falls back to `getpwuid(0)` → `/root/.tmux.conf`
- `usermod` really does move the `scion` user to UID 0 alongside root. Confirmed, and it belongs in
  the fix's blast radius rather than being discovered later.

**The filesystem question is answered and it kills the rival candidate.** `cfg.HomeDir` and `/scion`
are on the same filesystem on a Cloud Run Instance — `/scion` is `os.MkdirAll`, no volume mount, no
`VOLUME` directive, no volume config in `deploy_instance.go`. Every rename succeeds. So
`relocateToScion` is **not** why the hook is missing, and sandboxes have not been running with an
emptied home.

That leaves exactly one cause for #31 and narrows the fix to `HOME`. The relocate defect is still
real — skip-then-unconditional-delete — but it is latent until someone introduces a volume mount.
Filed as **#32** rather than folded into #31, so that fixing the hook does not quietly bury it.

Fix options written up as design-doc **§11.16.6**. Recommendation: set `HOME`/`USER`/`LOGNAME` in
the sandbox's `envFor()` (sandbox-local, no cross-runtime risk, fixes the whole class), and treat
relaxing `supervisor.go:113` as a separate follow-up because it changes root-run child environments
in three shipped runtimes. Rejected the one-line `tmux -f` fix: it would make the hook fire while
leaving every other consumer of `~` pointed at `/root`.

Two local experiments still running before I authorise anything — `dev-exp1-home` (does `HOME` get
set at `SCION_HOST_UID=0` vs `1000`) and `dev-exp2-relocate` (EXDEV table test). em3 has standing
instructions to break the predictions, not confirm them.

### 2026-08-26 12:40 — ptone cleared the fresh-instance SSH test

> "yes. do that. i didn't realize you had no problems with ssh earlier. i'm also having them try to
> ssh into yours."

Deploying one fresh instance from the same image to separate provisioning-era from image/health.
Building the CLI from `c07ea7a8` first — the `scion` binary in my container predates
`deploy-instance`. Platform team is testing inbound against `e2e-walk-r2`/`e2e-omni` in parallel;
**`e2e-walk-r2` still must not be restarted or redeployed** — it is the only specimen of the
original failure.

### 2026-08-26 12:50 — INSIDE A SANDBOX AT LAST. Two causes, not one. Design doc §11.17

SSH unblocked by the platform team's tip, confirmed: gcloud 582 hardcodes
`wss://{region}.ssh.run.app/v4`; `--iap-tunnel-url-override=wss://tunnel.cloudproxy.app/v4` works.
Not IAM, not provisioning-era, not the image, not health — every candidate we chased this morning
was wrong. Route to a sandbox: SSH to `--container worker` (the launcher), then
`/usr/local/gcp/bin/sandbox exec <name> -- <cmd>`. ptone was right that the platform team's
diagnostic was run in the launcher, not a sandbox, which is why it reported "no tmux server".

**Cause A confirmed by measurement.** `/proc/1/environ` has `HOME=/root`; the entrypoint log says
`setupHostUser result: targetUID=0, targetGID=0, rootless=false`. The supervisor branch is skipped
exactly as the §11.16 chain predicted.

**Cause B, which nobody predicted.** The template home is never applied. The launcher's template
has `.tmux.conf`, `.zshrc`, `.gitconfig`, `.gemini`; the provisioned agent home has none of them —
for all four agents on the instance. So `.tmux.conf` exists nowhere in the sandbox and **fixing
`HOME` alone would have changed nothing.** Not the relocate bug (#32): the home has other content,
so nothing was moved-then-deleted; these files were never written.

Docker contrast measured in my own container: `HOME=/home/scion`, uid 1002, `.tmux.conf` present,
hook at line 90.

#31 rewritten around both causes with four acceptance criteria, all required. No fix authorised
until Cause B's mechanism is named — em3 is on it. That restraint is deliberate: three plausible
causes have now turned out to be wrong, every one of them because someone reasoned about where a
file should be instead of looking.

**Estate: `ssh-probe` deployed 12:40 and deleted 12:51.** Back to the four do-not-delete instances.
It failed to boot, which produced two more findings: **#33** — `deploy-instance` splices gcloud's
impersonation warning into both the polled URL *and* the deployed
`SCION_SERVER_AUTH_PROXY_IAP_AUDIENCE`, so it bakes a malformed audience into the instance; and
**#34** — that malformed audience made the server select **dev auth** (auto-admin for every
request), and only the non-loopback bind guard stopped it serving. #34 is security-adjacent and
needs the auth resolution order read from source before I take it to ptone.

Also recorded (§11.17.4): the sandbox's `iptables` metadata block fails under gVisor, but
`169.254.169.254` is unreachable anyway — noise that reads like an exposure.

Sub-agent `dev-exp1-home` stalled with a broken tmux session and was deleted; its experiment is
moot now that the platform itself has answered. `dev-exp2-relocate` has reported.

### 2026-08-26 12:58 — PROOF: ran the fix by hand on the live platform. Every link fired. §11.18

Did not want to authorise a fix on the strength of a story, so I performed it manually on
`e2e-walk-r2`: copied the launcher's template `.tmux.conf` into the agent home, confirmed it
appeared inside the sandbox with the same md5 as the file in my own docker container, sourced it
into the running tmux server, then created an `agent` window whose command exits by itself after
5 seconds — leaving a `shell` window behind, the exact shape that wedged before.

Fifteen seconds later: session gone, `[stopped] Session ended`, `Reported final status to Hub
(exitCode=0, crash=false)`, `Child exited with code 0`, no `sandbox wait` or runsc processes on the
launcher, and `hub.db` showing `e2e-window-kill phase=stopped`. **Docker parity, demonstrated on
Cloud Run.** The remaining work is delivery, not discovery.

One nuance worth keeping: sourcing the file by hand bypassed Cause A, so this proves the *hook*
works, not that either fix alone is enough. The file must exist **and** `HOME` must point at it.

This consumed the `e2e-window-kill` specimen. Deliberate — everything diagnostic had already been
taken from it, and the wedged state is trivially reproducible. I had told em3 to preserve it, so
the reversal is mine and it is recorded here rather than left as a surprise.

Three more defects fell out of the same run: **#35** (hub rejects session metrics with a 400
`session.id is required` on every clean shutdown, and `exit_code` is never persisted despite being
reported), and an observation that **#17's stale-`running` symptom is alive again** —
`e2e-rerun-claude` shows `phase=running` with no runsc process for it on the instance. #17 was
closed; its resolution needs rechecking against this before the tier ships.

**Posture change (ptone, 12:53):** offline two hours, no "FYI" updates, accumulate a catch-up, and
he wants to *try the E2E himself* on return. So the target for the next two hours is a working
instance he can open and use, not a fuller diagnosis. Dispatched #33 to em3 for a developer, since
impersonation is the only identity we have and a contaminated deploy blocks every redeploy.

### 2026-08-26 13:05 — correction to my own 12:58 entry (design doc §11.18.2a)

I wrote that a hosted agent "cannot commit or push" because the sandbox has no `.gitconfig`, and
that this disposed of D1. Both wrong, caught twenty minutes later by re-reading my own review
queue. `sciontool init` configures `credential.helper` at `init.go:1688`/`:1831`; the specimen ran
in no-auth mode with no token, so it never fired. My container has `GITHUB_TOKEN`, so mine did.
Same code, different inputs. **D1 stands exactly as written.**

What survives is narrower and still real: the four template-home files are not applied, `.gitconfig`
among them, so even `[safe] directory = /workspace` is missing. That is the same skipped step that
costs us the tmux hook, and it is the only thing em3 should be chasing. Told em3 to drop the
credential thread before a developer widens a fix around it.

Fourth time today I have inferred a missing mechanism from an absent file, same tell each time. I
have asked em3 to push back rather than act if I do it again.

### 2026-08-26 13:33 — em3 is wedged; I took over dispatch directly

Checked rather than assumed, per the heartbeat, and it was worth doing: `sn-impl-em3` has been at
`blocked, 49 minutes ago` since **12:41**, and `scion look` returns
`error connecting to /tmp/tmux-0/default (No such file or directory)`. Its tmux session is gone.
**Every message I sent it from 12:49 onward — the Cause B redirect, the #33 dispatch, the
correction, the schedule with the 13:40 cutoff — landed nowhere.** So no developer was ever spawned
for #33 and nobody was tracing Cause B. Fifty-five minutes of the two-hour window went into an
agent that was not there. Exactly the failure mode the heartbeat warns about, and I would not have
caught it from message-delivery receipts, which all said "delivered".

Acted rather than re-dispatched through a dead manager: started **`sn-dev-ready`** at 13:33 with a
written brief at `briefs/sn-dev-ready.md`, carrying all three changes in one branch — #33, Cause A
plus the two false comments, and Cause B with a **13:50** cutoff to a labelled stopgap. The brief
states what is already measured so nothing is re-derived, and lists explicitly what not to widen
into (#32, #34, #35, the credential-helper thread I withdrew).

**Second-order observation, recorded not chased:** `sn-impl-em3` and `dev-exp1-home` are both
**docker** agents whose tmux session died while their container stayed up — em3 for ten hours. On
the docker path the entrypoint ends in `attach-session`, so a dead session should return, exit
`sciontool init`, and stop the container. That is the same parity claim this whole workstream rests
on. Two counter-examples now sit in our own estate. I cannot inspect them (no docker CLI in my
container), and it may have an innocent explanation, but "docker gets this right" should not be
repeated as settled until someone looks. Leaving em3 running as the specimen rather than deleting
it.

## 13:52 — CI does not build on push; a PR is required (arch)

Checked `publish-omni.yml` on `scion/dev-rebase-1294` myself rather than waiting to be told. It
triggers on **`pull_request`** only, filtered to `image-build/**`, `**.go`, `go.mod`, `go.sum`,
`web/**`. **A push to a branch builds nothing.** The image is tagged `dev-<head-sha>` (PR head
commit) and also `dev-<merge-sha>`.

Consequence for today: `sn-dev-ready` must open a **draft PR** (base `scion/dev-rebase-1294`, head
`scion/sn-dev-ready`) to get an image. Opening a PR is not merging and does not touch ptone's gate.
Told it to title the PR DO NOT MERGE so nobody helpfully lands it while ptone is offline.

Worth carrying into the design doc as an operability note: the only path to a testable image runs
through a PR, so any hotfix to a hosted instance implies a PR. That is a reasonable policy but it
should be a chosen one, not an accident of the trigger list.

`sn-dev-ready` reports all three changes complete with tests passing, pushing at 13:50. I will
spot-check the diff myself before deploying — specifically that `supervisor.go:113` was left alone
and that the Cause B change sits in `ProvisionAgent`, not in the Cloud Run runtime.

## 13:55 — branch pushed, PR #1268, image building (arch)

`sn-dev-ready` pushed `e36a3f00` at 13:51 — nine minutes inside the cutoff — and opened draft PR
**#1268** (`scion/sn-dev-ready` → `scion/dev-rebase-1294`). Expected image: `dev-e36a3f00`.
`shellcheck` green; Build & Test, golangci-lint and the omni image build still running.

I read the whole diff rather than taking the report. 287 insertions across six files:

| File | What it does | My read |
|---|---|---|
| `cmd/deploy_instance.go` | `diRunGcloud` switches `CombinedOutput` → `Output`; stderr surfaces only via `ExitError`. Adds `diValidateProjectNumber` (`^[0-9]+$`) and `diValidateInstanceURL` (https + `.run.app` host). | Correct, and correct *at the source* — every consumer is fixed by the capture change; the validators are the belt to its braces. |
| `pkg/runtime/cloudrun_sandbox_runtime.go` | `envFor()` sets `HOME=/home/scion`, `USER`, `LOGNAME`. Both false comments at `:419`/`:583` rewritten to state what supervisor actually does. | As briefed. `supervisor.go:113` untouched — good, that stays a reviewed follow-up. |
| `pkg/config/templates.go` | The swallowed error now logs WARN with `projectPath` and the error before returning the empty chain. | This is the piece that matters most for #37. The failure finally has somewhere to surface. |
| `pkg/agent/provision.go` | Floor: if no template home was copied, walk `config.EmbedsFS` at `embeds/templates/default/home` and write the files in. | Right layer — the function that already owns home composition, not the Cloud Run runtime. |

**One review note, deliberately not blocking.** The floor writes a file *only if it does not already
exist*, whereas the real step 2 lets template files **win** over the harness-config base. So the
floor is not semantically identical to the path it stands in for. It does not matter for the four
files we care about (harness-config ships none of them), and "a floor never clobbers" is a
defensible rule in its own right. But it means a future conflict would resolve differently depending
on whether resolution worked — exactly the kind of quiet divergence that makes a fallback outlive
its usefulness. Recorded against **#37**: when the resolution gap is fixed, the floor should either
be removed or made to match overlay semantics. Not worth a round trip at 13:55.

## 13:59 — a §1 defect found by pre-flighting rather than by deploying (arch, task #38)

While preparing the fresh deploy I compared what `deploy-instance` sets against what the two
instances that actually work carry. It sets four env vars. They carry six. The difference that
matters is **`SCION_IMAGE_REGISTRY`**.

`requireImageRegistryForBroker` (`cmd/server_foreground.go:2948`) fails fast when no registry is
available from `SCION_IMAGE_REGISTRY`, `SCION_MAINTENANCE_IMAGE_REGISTRY`, or versioned settings,
and the omni Dockerfile bakes none. `e2e-omni` and `e2e-walk-r2` both have
`SCION_IMAGE_REGISTRY=ghcr.io/ptone` — **set by hand, because both were deployed by hand.**

So the single command §1 measures us by produces an instance that cannot start an agent. This is the
sharpest example yet of the thing that keeps biting this workstream: **every instance we have ever
exercised was hand-deployed, so the command has never actually been on the critical path of a
successful run.** We have been testing the platform through a door the operator does not have.

Dispatched as #38, CLI-only so it does not gate the image. Fix is to *derive* the registry from
`--image` (`ghcr.io/ptone/scion-omni:tag` → `ghcr.io/ptone`) with an `--image-registry` override.
Deriving matters: §1 says *one* command, and a second mandatory flag spends exactly the thing §1 is
measuring.

Side note recorded so it does not get cargo-culted: `SCION_SERVER_MODE=hosted` appears on both
hand-deployed instances and matches **nothing** in the Go source. Somebody set it once and it has
been copied ever since.

## 14:02 — the walk I will run the moment the image lands (#36)

Written down before the artefact exists so the walk is a test and not an improvisation. Instance
name `sn-ready`, so it is obvious which one is ptone's and which are specimens.

```
export CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT=scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com
/tmp/scion-fixed deploy-instance \
  --name sn-ready --image ghcr.io/ptone/scion-omni:dev-<sha> \
  --project ptone-experiments --region us-east4 --admin-email ptone@google.com
```

Note the binary: `/tmp/scion-fixed`, built by me from the branch. **#33 and #38 live in the CLI, not
in the image** — that distinction is easy to lose and would produce a confusing failure if I used
the stock binary.

| # | §1 step | What I check | How I know it passed |
|---|---|---|---|
| 0 | one deploy command | command exits 0 | URL printed is clean https — proves #33 |
| 0a | — | instance env | `SCION_IMAGE_REGISTRY` present, audience has no warning text — #38, #33 |
| 1 | open the run.app URL | 302 to the IAP consent, then the app | perimeter enforcing, not dev auth |
| 2 | log in | hub recognises ptone@google.com as admin | `SCION_SEED_SERVER_HUB_ADMINEMAILS` seeded Layer-1 |
| 3 | create a project | project appears | — |
| 4 | start a Claude agent | phase reaches running | broker had a registry (#38) |
| 5 | attach to its terminal from the browser | keystrokes echo | tmux session alive, HOME correct (Cause A) |
| 5a | — | `ls -a ~` inside the sandbox | `.tmux.conf` present — proves the floor (Cause B) |
| 5b | — | `tmux show-hooks -g \| grep pane-exited` | the hook is loaded, not merely on disk |
| 6 | commit to a git remote | a commit lands | expected to expose D1; report, do not fix |
| 7 | **new** — agent exits | sandbox stops, hub shows a terminal phase | the whole point of #31 |

Step 7 is the one that did not exist when this walk was first written, and it is the one the
morning was spent earning. Steps 5a and 5b are deliberately separate: a file on disk and a hook
loaded into a running server are different claims, and I have already been caught once today
treating the presence of a file as proof of a mechanism.

Step 6 is expected to fail or to need a credential I have not provided. **That is a report, not a
fix.** D1 is ptone's decision and I am not going to quietly invent an answer to it at 14:30.

## 14:02 — heartbeat answers (arch)

**1. Are dispatched agents progressing or silently stalled?** Checked, not assumed.
`sn-dev-ready` is `blocked, 2 minutes ago` — correctly so; I froze the branch and told it to stand
by. It delivered four commits in 25 minutes (`e36a3f00`, `a0367c1d`, `a1165c0e`) and I read every
diff rather than taking its word. `sn-impl-em3` remains wedged and is being kept deliberately as
the only specimen of a docker-side tmux wedge. `dev-exp2-relocate` deleted — its work produced #32
and it had been idle an hour. `dev-entrypoint-diag` untouched: its ownership is queued for ptone as
review-queue Item 3 and I am not going to pre-empt that by deleting it.

**2. What blocks the critical path?** One thing, and it is not a decision: the omni image. Three
builds (`32976747341`, `32976976933`, `32977375167`) are all sitting on "Build all images locally",
started 13:52–13:58. The CLI half is already built and verified at `/tmp/scion-fixed`. I am waiting,
not working around it.

**3. Is the design doc in sync with what has been measured?** Yes, and deliberately so — §11.17
through §11.20 were all written from measurement, and §11.18.2a is a withdrawal of the one claim
that was not. The one gap the doc does **not** yet reflect is #38, because it was found 20 minutes
ago; it is in this file and in review-queue §9, and I will fold it into the doc after the walk,
when I know whether the fix actually works rather than only that it compiles.

**Against §1 specifically, honestly:** we still have not completed a single §1 run. What changed
this morning is that the two causes blocking step 5→7 are now understood and fixed, and #38 revealed
that step 0 has never actually been exercised by the command §1 names. The remaining unknown is step
6, git push, which is D1 and ptone's.

## 14:06 — first deploy of the fixed CLI: two fixes verified live, one new trap (arch)

Image `dev-e36a3f0069ad1f5dfd65709f90cfe8b6d9fa17b8` published at 14:02 (10m49s build, anonymous
pull tested by CI). Deployed with `/tmp/scion-fixed`.

**#33 is verified fixed on the live platform.** The deployed spec carries

```
SCION_SERVER_AUTH_PROXY_IAP_AUDIENCE = /projects/721899303052/locations/us-east4/services/sn-ready
```

with no warning text, and stdout printed `Project: ptone-experiments (number: 721899303052)` clean.
The impersonation banner now appears **only** in the error path, which is exactly where it belongs.

**#38 is verified fixed.** `SCION_IMAGE_REGISTRY = ghcr.io/ptone`, derived from `--image` with no
operator input. §1's "one command" survives intact.

**New trap, and I walked straight into it.** The developer reported the tag as `dev-e36a3f00`.
The actual tag is the **full 40-char sha**, `dev-e36a3f0069ad1f5dfd65709f90cfe8b6d9fa17b8`. I took
the abbreviation on trust and burned a deploy cycle. The failure surfaced as:

```
Image 'cache.us-docker.pkg.dev/ghcr.io/ptone/scion-omni:dev-e36a3f00' not found.
```

Three things are wrong with that as an operator experience, on step 0 of §1:

1. It names `cache.us-docker.pkg.dev`, a mirror the operator never mentioned and may not know
   exists. Nothing in the message says GHCR.
2. "not found" is the same symptom for *tag does not exist*, *package is private*, and *mirror has
   not populated* — three unrelated causes, one indistinguishable message.
3. The workflow comment advertises the tag as "predictable, matches git log". It matches
   `git log --format=%H`; it does **not** match what `git log --oneline` prints, which is what
   anyone actually reads. The predictable-looking thing is the wrong thing.

Filed as **#39**. Not fixing it today — the branch is frozen and this costs an image rebuild.

**Second-order consequence I chose to pay for.** The failed pull left the instance `Stopped` with
`ResourcesAvailable: False`. Redeploying with the right image bumped the generation but did not
bring it up, and `gcloud beta run instances start` would have rescued it. **I deleted it and
deployed clean instead.** Rescuing it would have reproduced the exact error #38 taught us this
morning — testing the product through a door the operator does not have. A repaired instance would
not have been evidence about the one command.

## 14:19 — #34 resolved, and it is a §1 step-0 blocker, not a security curiosity (arch, task #40)

The fresh instance booted and died: `Error: dev auth cannot be enabled when the server is bound to a
non-loopback address (0.0.0.0)`. Crucially the IAP audience was **clean** this time, so the earlier
theory — that a malformed config causes the dev-auth fallback — is **dead**. The trigger is
something else entirely, and it fires on a perfectly valid deploy.

**Mechanism, verified end to end:**

1. The omni CMD passes `--host 0.0.0.0` but **not** `--hosted`.
2. `loadAndReconcileConfig` (`server_foreground.go:833`) sets `hostedMode` only if the flag changed
   **or** `cfg.Mode` is `hosted`/`production`.
3. `cfg.Mode` comes from koanf key `mode` — env `SCION_SERVER_MODE`. `deploy-instance` never set it.
4. `!hostedMode` ⇒ `applyWorkstationDefaults` (`server_config.go:35`) sets `enableDevAuth = true`.
5. `:844` then does `cfg.Auth.Enabled = enableDevAuth`.
6. `initDevAuth` mints a token; the guard at `:2190` sees a token plus `0.0.0.0` and exits 1.

**Proof, not inference:** I added `SCION_SERVER_MODE=hosted` to the live instance and restarted.
`exit(1)` → **HTTP 200**. Nothing else changed.

**The guard was right the whole time.** It refused to expose an endpoint that auto-logs every
caller in as admin. It is the sole reason this was loud instead of silent, and #34 should be closed
as *"the guard worked"* rather than as a defect in the guard.

### I got this wrong four hours ago, in my usual way

At 13:59 I wrote that `SCION_SERVER_MODE=hosted` *"matches nothing in the Go source"* and called it
cargo. I had grepped for the literal string `SCION_SERVER_MODE`, found nothing, and stopped. koanf
strips the `SCION_SERVER_` prefix and maps the remainder to a config key, so the literal never
appears — the variable was load-bearing all along, and the one instance-shaped difference between
the working instances and mine was the thing I had just dismissed.

**That is the fifth time today** I have concluded a mechanism is absent from the absence of a
literal, without asking what would legitimately produce that absence. The previous four were
browser attach, `credential_helper.go`, the tmux hook, and the `.gitconfig`. The pattern is now
frequent enough that it belongs in the design doc as a standing caution about this codebase
specifically: **config keys here are reached by prefix-stripping and camel-case mapping, so
grepping for an env var name will systematically under-report what is wired up.**

### A second defect, filed and deliberately not fixed today

`SCION_SERVER_AUTH_DEVMODE=false` is **silently clobbered**. The env load sets
`cfg.Auth.Enabled=false`, then `:844` overwrites it with the flag default. I tested it live: setting
it changed nothing. An explicit operator setting losing to a workstation default, with no warning,
is worse than the original bug — the operator has done the documented thing and been ignored.

## 14:50 — the one command works; the walk reaches step 3 and stops at step 4

**Step 0 is done, for the first time, with no hand-patching.** Two concurrent deploys of the fixed
CLI (`ad30c1aa`, image `dev-e36a3f0069ad1f5dfd65709f90cfe8b6d9fa17b8`) both returned **rc=0**:

| instance | admin | URL | authenticated GET / |
|---|---|---|---|
| `sn-ready` | ptone@google.com | https://sn-ready-721899303052.us-east4.run.app | **200** |
| `sn-walk`  | scion-instance-gym SA | https://sn-walk-721899303052.us-east4.run.app | **200** |

`sn-ready` is ptone's. It is clean — no hand-patched env vars; the redeploy cleared them, since
`--set-env-vars` replaces. Everything I poked at below was done on `sn-walk`.

Verified live, from the shipped command rather than by hand:
- **#33** — URL and audience parse clean under impersonation.
- **#38** — hub reports `activeBrokerCount: 1`; the broker came up with a registry.
- **#40** — hosted mode holds; no dev-auth crash. Previously proven by hand-patch, now by the CLI.

**B16 is not a defect — withdraw it.** I predicted the IAP binding would hardcode `user:` and break
for a service-account admin. It does not; step 5 emitted
`serviceAccount:scion-instance-gym@...` correctly. I filed that from reading, not running. Sixth
instance of the same habit, and the first one where the habit invented a bug rather than missed one.

### The walk, as far as it goes

| step | result |
|---|---|
| 0 deploy | **PASS** rc=0, both |
| 1 open URL | **PASS** 200 behind IAP; unauthenticated blocked |
| 2 log in | **PASS** `/auth/me` returns the SA identity |
| 3 create project | **PASS** 201, `walk-demo` |
| 4 start a Claude agent | **BLOCKED — #41** |
| 5–7 | not reached |

Two of my own errors on the way to step 4, both worth recording because they are the same error:
- I posted `"harness":"claude"`. The field is `harnessConfig`. The wrong field was **silently
  ignored**, a default of `antigravity` was chosen, and the failure surfaced as a 502 from the
  broker saying `harness-config "antigravity" not found`. I briefly read that as "the broker's store
  disagrees with the hub's". It did not. An unknown field in a create request should be rejected,
  not defaulted — that 502 cost me two cycles and would cost an operator more.
- Seeing `"role":"member"` I assumed I could not create a project. I could. **Test the action, not
  the role string.**

### #41 — why step 4 cannot complete

`vertex-ai` is the only credential path that needs nothing the operator has not already given us:
the instance runs as a GCP SA, ADC comes from the metadata server, and the claude harness explicitly
marks its ADC file `skipped_when_gcp_service_account_assigned`. That skip is correct and well
tested. It never fires, because `projectHasVerifiedGCPSA` asks for a **verified project-scoped row
in the hub store**, and a fresh hosted deploy has none.

Then the part that changes the fix: I created the row by hand and it came back **`verified:false`** —
the hub cannot impersonate `721899303052-compute@developer`, *which is the SA the hub is itself
running as*. Verification is defined as "can the hub impersonate this SA", and a service account
cannot impersonate itself without an explicit self-binding. **The hosted hub cannot verify its own
identity.** So seeding the row is not enough; self-identity has to be verified by construction,
because the hub already holds those credentials and has no need to mint them.

I could not clear it operator-side either — granting the SA tokenCreator on itself needs
`iam.serviceAccounts.setIamPolicy`, which `scion-instance-gym` does not have.

Also surfaced: `deploy-instance` sets no `--service-account`, so hosted agents run as the **default
compute SA with Editor**. That should be a decision, not a default.

**This is ptone's call, not mine — it touches the credential model. I am not inventing an answer.**

**14:32 — sn-dev-ready released.** Confirmed clean (empty tree, head `ad30c1aa`, verified by me
against origin rather than taken on report) and deleted. Its three fixes are all verified live.
No developer is dispatched: step 4 is blocked on #41, which is a decision, not implementation work.
Dispatching someone to write code against an undecided credential model would be manufacturing work.

## 15:40 — I was wrong that step 4 was blocked. Steps 4,5,5a,5b,7 all PASS. Step 6 has a new hard blocker.

**Retract the 14:50 blocker call.** I reported step 4 blocked on #41 and told ptone it needed his
decision before the walk could continue. That was wrong, and I found it by trying the one thing I
had not tried: a plain create request with no auth fields at all.

```
{"harnessConfig":"claude","template":"default","noAuth":true}   -> 422 ANTHROPIC_API_KEY
{"harnessConfig":"claude","template":"default"}                 -> 201, running, harnessAuth=none
```

The auto no-auth fallback fires when the harness declares `drop-to-shell`, which `claude` does.
Asking for no-auth explicitly is the thing that PREVENTS it (#42). I had reasoned from two failed
requests to "blocked" without trying the default path. **#41 is still real and still ptone's
decision** — it is what stands between us and an agent that can actually reach a model — but it was
never blocking the sandbox-lifecycle verification, which is the part carrying the technical risk.

### The walk

| step | result |
|---|---|
| 0 deploy | PASS |
| 1 open URL | PASS |
| 2 log in | PASS |
| 3 create project | PASS |
| 4 start a Claude agent | **PASS** (drop-to-shell; a *usable* agent still needs #41) |
| 5 attach terminal | PASS — exec into the sandbox, tmux session with agent+shell windows |
| 5a template home | **PASS** — all four files present. Cause B floor works |
| 5b hook loaded | **PASS** — but only on the second attempt, see below |
| 6 commit to a git remote | **BLOCKED — #43, new and hard** |
| 7 agent exits -> sandbox stops | **PASS** — full chain, hub phase=stopped |

**#31 is closed.** Both causes fixed, verified live: `HOME=/home/scion`, all four template files
present, hook auto-loaded, and terminating the agent pane's process took the sandbox down and the
hub to `phase=stopped`.

### Three methodology failures on the way, all mine, all worth keeping

1. **My own acceptance criterion named a broken instrument.** I wrote "tmux show-hooks -g lists
   pane-exited". In tmux 3.4 that prints nothing even when the hook IS loaded — hooks are options
   now. `tmux show-options -g pane-exited` is the correct probe. I ran the bad one and concluded
   the hook was missing.
2. **I then contaminated the specimen.** Investigating, I ran `tmux source-file ~/.tmux.conf`,
   which loaded the hook and destroyed my ability to tell whether it had auto-loaded. Had to
   discard that sandbox and re-test on a fresh one. Never mutate the thing you are measuring.
3. **My acceptance criterion also named a trigger the mechanism does not respond to.** `kill-window`
   does not fire `pane-exited`; the pane's *process* ending does. Independently useful: an external
   `kill-window` orphans the session and the sandbox never stops.

The saving grace is that keeping 5a and 5b as separate claims worked exactly as intended — "the
file is on disk" and "the hook is loaded" came apart, and the separation is what made me look.

### #43 — the new step-6 blocker, and it is not the credential question

A project **with** a `gitRemote` cannot start an agent at all. Identical request in a project
without one succeeds.

```
[sciontool] ERROR: Git clone failed: git init failed: /workspace/.git: Permission denied
```

sciontool treats the clone failure as fatal, PID 1 exits, sandbox dead on arrival. Inside a healthy
sandbox: `/workspace` is `nobody:nogroup`, and `touch` fails **as uid 0**. Root being denied rules
out permission bits; `nobody:nogroup` is what an unmapped UID looks like from inside a user
namespace. `/home/scion` is writable, so the sandbox is not globally read-only — it is this mount.

So §1's "commit to a git remote" is unreachable for a reason that has nothing to do with D1. **D1 is
downstream of #43 and currently untestable.** My earlier step-6 probe (no git identity) was
measuring the wrong thing: identity is written repo-local by sciontool's clone path
(init.go:1658, `git -C workspacePath config`), so an agent with no workspace legitimately has none.
That part is by design.

## 15:50 — ptone found the admin bootstrap is broken (#44), and I had already seen it and waved it away

ptone, on returning: *"you said I was bootstrapped as admin, but I'm not seeing the admin UI?"*

He is right. Reproduced by A/B on sn-walk, same instance, one variable added:

| env | `/auth/me` role |
|---|---|
| `SCION_SEED_SERVER_HUB_ADMINEMAILS` only — what `deploy-instance` sets | `member` |
| plus `SCION_SERVER_HUB_ADMINEMAILS` | **`admin`** |

The variable is present and correct on his instance, so the deploy command did exactly what it
intended. It picked the variable that does not promote. `SCION_SERVER_*` populates
`cfg.Hub.AdminEmails` directly at startup; `SCION_SEED_*` has to travel through the layer-1 settings
DB and a snapshot that is applied only when non-empty (`operational_settings.go:909`). The seed
value is not landing in the DB on a fresh hosted boot. **Why it does not land is not yet
established, and I am not going to patch around it until it is** — the same seed path carries other
settings, and switching one variable to `SCION_SERVER_*` would hide a broken mechanism rather than
fix it.

**How it survived review:** `deploy_instance_test.go:637` asserts that
`SCION_SEED_SERVER_HUB_ADMINEMAILS` maps to `server.hub.admin_emails` in the seed koanf. That test
passes. It checks the wiring one hop short of the consumer that actually decides the role. A test
asserting *"a user with this email gets role=admin"* would have caught it on day one. This is the
second time today a test has been green about a mechanism that does not work end to end.

**My failure, and it is the worst one today.** At 14:24 I saw `"role":"member"` on my own identity,
on an instance where I *was* the `--admin-email`, and wrote it off: *"the member role was a red
herring; project creation is not admin-gated."* I did the right thing — test the action, not the
string — and then drew exactly the wrong conclusion from it. One non-gated action succeeding says
nothing about whether the role is correct. The role string was the defect, sitting in output I had
already read, and ptone found it instead of me.

That is now the pattern to watch, distinct from the grep-for-a-literal one: **I explain away a
disconfirming signal when a nearby confirming signal is available.** The 14:50 "step 4 is blocked"
call was the same shape — two failures, no attempt at the default path, escalate.

Offered ptone the choice between patching his live instance now (proven, one minute) and fixing
`deploy-instance` properly first. Not touching his instance without his say-so.

---

## 16:05 — #44 root cause: the web server never consumes the Layer-1 settings snapshot

**A landed.** `sn-ready` now carries `SCION_SERVER_HUB_ADMINEMAILS=ptone@google.com`, restarted
rc=0, URL serving 200. Both proxy paths re-derive the role per request and rewrite the stored user
row and the session JWT (`web.go:1536` for a live session, `:1653` on login), so a reload suffices —
told ptone, awaiting his confirmation.

**B: I said I would find out *why* the seed path does not land before patching it. I did, and the
answer inverts the item.** The seed path is not broken. It works perfectly, all the way to the
Layer-1 snapshot. What is missing is a consumer.

**Measured, not read.** Throwaway test in `pkg/hub` running the real chain with only
`SCION_SEED_SERVER_HUB_ADMINEMAILS` set (and again with only `SCION_SERVER_HUB_ADMINEMAILS`, as a
control). Both produced *identical* output at every hop:

```
STEP1 LoadBootstrapKoanf     server.hub.admin_emails = ["ptone@google.com"]
STEP2 ExtractSectionFromKoanf(k,"access") = {"admin_emails":["ptone@google.com"]}
STEP3 loadSectionDocIntoKoanf → merged   = ["ptone@google.com"]
STEP4 buildSnapshotFromKoanf → snap.AdminEmails = ["ptone@google.com"]
```

So `LoadSeedEnvKoanf` → bootstrap merge → `syncHubSettings` → DB → `Snapshot()` → `ApplySnapshot`
is sound. Test deleted; it was a probe, not a deliverable.

**The break is a split-brain between two config structs.**

- `ApplySnapshot` (`operational_settings.go:872`) writes `s.config.AdminEmails` (`:910`) and
  `s.config.UserAccessMode` (`:941`) — fields on **`hub.Server`**.
- Every browser login path reads **`ws.config`** on `hub.WebServer`, a *different* struct held
  **by value** (`web.go:161`): proxy/IAP at `:1536`, `:1607`, `:1623`, `:1653`; OAuth at `:1896`,
  `:1907`, `:1944`, `:1958`.
- `ws.config.AdminEmails` is assigned exactly once, at construction, from
  `cfg.Hub.AdminEmails` (`server_foreground.go:2202-2204`, `:2239`). Grep for any later writer
  returns nothing. There is no `ApplyWebSnapshot`, no setter, no shared pointer.

`SCION_SERVER_*` reaches `cfg.Hub.AdminEmails` via `applyEnvOverrides`. `SCION_SEED_*` deliberately
does not touch `cfg` at all — it is layer-1 seed material by design. **That is the entire A/B.**

**This is larger than #44 and I am filing it separately (#45).** The same split makes the admin
UI's *access* section inert for logins: an admin who adds a colleague's email in the UI never grants
them admin — not on the next request, and not after a restart either, because the restart path
reads `cfg.Hub.AdminEmails` from env/yaml, never from the DB. The DB value can only ever reach
`s.config`, which no login path reads.

**And it is security-relevant, not merely inconvenient.** `user_access_mode` has exactly the same
shape. `checkUserAuthorized` at `web.go:1607` — the gate deciding who may log in at all — reads
`ws.config.UserAccessMode`. Tightening that setting in the admin UI therefore does not tighten
browser logins. The hub API path (`handlers_auth.go:1317`) *does* honour the DB. So the two halves
of the same product disagree about who is allowed in, and the more permissive half is the one
facing the browser. I have not exercised this one live — it is read, not run, and I am labelling it
as such.

**Consequence for B.** Switching `deploy-instance` to `SCION_SERVER_HUB_ADMINEMAILS` is no longer
"the obvious patch that might be masking a broken seed path" — the seed path is fine, and the patch
routes around a consumer that does not exist. That makes it a defensible fix rather than a paper-
over, but it must ship *labelled*, with #45 filed, or the next person will read it as evidence that
`SCION_SEED_*` is the wrong mechanism generally. It is not.

**Also worth saying plainly:** `deploy_instance_test.go:637` was green throughout. It asserts the
seed koanf mapping — the one hop that was never broken. Third green test today about a mechanism
that does not work end to end.

## 15:50 — #43 gate answers: one keeper, two withdrawn, one fix rejected

`sn-ws-mount` cleared the gate quickly. Answer 3 is the keeper and is probably the real finding:
**docker bind-mounts `/workspace` (`common.go:210`), k8s uses an EmptyDir with FSGroup
(`k8s_runtime.go:1234-1243`), and the Cloud Run sandbox path has neither.** A genuine structural gap.

Answers 1 and 2 did not survive checking, and checking took two greps.

**Answer 2 is about the wrong runtime.** It cited `k8s_runtime.go:1178-1196` for RunAsUser/FSGroup.
`cloudrun_sandbox_runtime.go` contains neither symbol and never references the k8s runtime — Cloud
Run Instances sandboxes do not go through that path. It also contradicts the measurement in its own
brief: the sandbox process is uid=0, not 1000.

**Answer 1's "neither Dockerfile creates `/workspace`" is false as stated.**
`harnesses/claude/Dockerfile:34` does `mkdir -p /workspace && chown -R scion:scion /workspace`, with
`WORKDIR /workspace` at `:37`. The live image is `ghcr.io/ptone/scion-omni:dev-e36a3f0069ad…`, and
**nothing in the repo builds it** — "omni" matches no Dockerfile, yaml or script. That is its own
small problem (an image we depend on with no in-repo provenance; adjacent to #3, which I had
considered closed).

**The competing story, which fits the evidence at least as well.** If `/workspace` exists in the
image owned by uid 1000 and the sandbox userns does not map 1000, it renders as `nobody:nogroup` and
root-in-namespace cannot write it, because `CAP_DAC_OVERRIDE` does not extend to unmapped UIDs.
Under that story the proposed fix — add `mkdir`+`chown scion` to scion-base — is not a harmless
no-op, it is *the cause*, and landing it would entrench the defect.

**Decisive measurement requested,** replacing all of the above speculation with two commands in any
healthy sandbox: `stat -c '%u %g %a' /workspace /home/scion /` and
`cat /proc/self/uid_map /proc/self/gid_map`. Numeric, not `ls -l` — the names are what hid the
number, and the number is the entire question. That `nobody:nogroup` was read as "unmapped" by both
of us without either of us looking at the integer is the grep-for-a-literal failure wearing a
different hat.

**Fix half rejected outright.** The plan's second element was *"harden `ensureWorkspaceOwnership` to
remove+recreate when chown fails."* On the docker path `/workspace` is a bind mount of the
operator's real repository — the agent established this itself, in answer 3, and then proposed
deleting it. On k8s it silently empties the EmptyDir. This is #32's exact defect class: an
unconditional destructive fallback triggered by an unrelated failure. One is already open on the
board; I am not accepting a second written deliberately on the same day.

Rule given: when an ownership operation fails on a path you do not own, fail loudly naming the path,
the owning uid and the running uid. Never repair by deletion. A dead sandbox with a clear error
beats a live sandbox and a destroyed repo.

## 16:02 — #43 root cause CONFIRMED and fixed; my briefing error caught before it cost an image

`sn-ws-mount` found it, and it is a good find. **`mountsFor` mounted the workspace with
`source=destination=paths.workspace`, while the agent home was remapped
(`paths.agentHome` → `sandboxAgentHome`).** So `/workspace` inside the sandbox was never the bind
mount at all — it was the image layer, from `harnesses/claude/Dockerfile:34`. `sciontool init`
clones to `/workspace`, wrote to the image layer, and died.

Verified against `origin/scion/dev-rebase-1294` myself, line for line:

```go
fmt.Sprintf("...,source=%s,destination=%s", paths.agentHome, sandboxAgentHome),   // remapped
fmt.Sprintf("...,source=%s,destination=%s", paths.workspace, paths.workspace),    // NOT remapped
```

**This retro-explains the one piece of evidence that never fitted.** My own probe recorded
`grep /workspace /proc/mounts` → *no entry*. I wrote that down, called it "in the overlay", and
moved on to a userns theory. It was the answer: nothing was mounted there. Fourth time today the
disconfirming datum was already in my notes.

Fix `e186021d`: destination → `sandboxWorkspace` (`/workspace`), plus an explicit
`SCION_WORKSPACE_PATH`. Minimal and right.

### My error, caught by checking rather than by luck

**I briefed `sn-ws-mount` to branch from `scion/dev-rebase-1294` without checking that
`sn-dev-ready`'s work had landed there. It has not.** `dev-rebase-1294` has no `env["HOME"]` in
`envFor`; `git merge-base --is-ancestor origin/scion/sn-dev-ready origin/scion/sn-ws-mount` → NO.

An image built from `sn-ws-mount` alone would have **regressed #31** — exit detection broken again,
sandboxes never stopping — and I would have discovered that on live hardware, after a ten-minute
image build, while attributing the regression to the workspace change. Instructed: merge
`origin/scion/sn-dev-ready` into `scion/sn-ws-mount`. Not into `dev-rebase-1294`; that is ptone's
gate.

The general lesson for my own briefs: **"branch from X" is a claim about what X contains, and I have
been asserting it without looking.** Two branches off a common base are not cumulative just because
they were written in sequence.

### The image path exists; the agent's CI claim was wrong in the way that mattered

`sn-ws-mount` reported *"CI does not build images for feature branches; build-images.yml is
workflow_dispatch only."* Wrong: **"Publish Dev Omni Image" triggers on `pull_request`.** That is
exactly how the image now running on `sn-walk` (`dev-e36a3f00…`) was built — from **PR #1268**, off
`scion/sn-dev-ready`, titled *"DO NOT MERGE: … (image build)"*. Same route for this fix. Had I taken
the blocker at face value I would have gone looking for a workflow_dispatch permission I do not need.

### Two record corrections demanded, not code changes

1. `GetWorkspacePath` at `:982-984` states *"bind mounts use the same paths inside and outside the
   sandbox"*. This change makes that false. The return value stays correct — `interface.go:121`
   defines the contract as the **host** path — but the invariant in the comment is now a lie, and a
   false comment on this exact file is how #31 survived review.
2. The commit message asserts the clone *"wrote to the read-only rootfs … with Permission denied"*.
   Those disagree: a read-only filesystem returns EROFS, and we measured EACCES, from both git and a
   bare `touch`. The destination being wrong is established; *why the wrong destination refused the
   write* is not. Asked for the message to state the former and drop the latter.

### #46 filed

An operator with a typo in their git URL gets `502 dead on arrival` and nothing else. Independent of
#43 — #43 just made it fire every time. Includes the open question of whether a clone failure
*should* be fatal at all.

## 16:12 — #43 fix merged and PR'd; image build blocked by a GitHub Actions outage

`sn-ws-mount` applied all three corrections: merged `origin/scion/sn-dev-ready` (clean), opened
**PR #1269** (DO NOT MERGE, base `scion/dev-rebase-1294`, matching #1268), and fixed the stale
`GetWorkspacePath` comment. It could not amend the misleading commit message because the commit is
now behind the merge — accepted; the PR description carries the correct mechanism and a squash-merge
can fix the final text.

**The image never built, and the reported cause was wrong.** The agent attributed it to its token
lacking Actions scope and to the PR base. Both wrong, and worth recording because the true cause was
diagnosable in three API calls:

- `statusCheckRollup` on #1269 was **empty** — not a failed image build, *no checks at all*, and CI
  has no path filter that would exclude it.
- `actions/runs?per_page=5` showed the most recent run repo-wide was **14:21**. Nothing since,
  across every branch and workflow.
- githubstatus.com: **"Incident with Actions", major outage, critical impact**, 15:48 UTC update
  citing throttled inbound traffic and upstream Vitess issues.

The tell was the empty rollup rather than a red check. "The build failed" and "no build was
attempted" look similar in a PR page and are entirely different problems. Also note the path filters
in `publish-omni.yml` (`**.go`, `image-build/**`, `web/**`) matched fine — I checked that before
concluding, because it was the plausible-looking answer.

**So §1 step 6 is now blocked on a third party.** The fix exists and is reviewed; it cannot reach
live hardware without an image.

### Consequence worth more than this outage: we have exactly one way to build an image

That is a single point of failure on the project's critical path, and it just failed. #3 was closed
as *"omni image has no CI build path"* — resolved by adding the GitHub workflow. The gap was real and
the fix was right, but it left the dependency singular.

The tree already contains an unused alternative: `image-build/scripts/builders/cloud-build.sh`,
`build-images.sh`, and an `omni` target in `targets.sh:75/99` chaining
`scion-base → claude → codex → opencode → antigravity → grok-build → omni`, with
`image-build/omni/Dockerfile` present. Redirected `sn-ws-mount` to build from its branch head via
**Google Cloud Build in `ptone-experiments`**, pushing to Artifact Registry — `SCION_IMAGE_REGISTRY`
is already per-instance configurable (that was #38), so a non-GHCR registry costs nothing. Told it
to check the SA's roles rather than assume `roles/editor` covers it, and explicitly **not** to grant
itself IAM. Asked for an early warning if this exceeds about an hour, since past that point waiting
GitHub out may simply be cheaper.

Told ptone, flagged as external, and offered him the option of just waiting instead.

## 16:14 — #45 filed upstream as ptone/scion#1270, with one detail deliberately withheld

ptone: *"This feels like an item of tech debt to clean up on upstream, no? Rather than work around it,
lets file an issue on ptone/scion to consider a cleaner and more coherent approach."*

Sent 9 seconds after my status message, so he was answering the **#45 reminder**, not the image-build
note — our messages crossed. Read it that way and said so, so he can correct me cheaply.

Filed **https://github.com/ptone/scion/issues/1270**: two sources of truth for access settings, the
measured evidence that the seed path is sound and the consumer missing, why the one-line workaround
is the wrong lesson to institutionalise, three directions (accessor / second-apply / construction-
time) with a recommendation, and acceptance criteria written at the level of *"this user ends up an
admin"* rather than *"this env var maps to this koanf key"*.

### The disclosure judgement

`ptone/scion` is a **public** fork of `GoogleCloudPlatform/scion`. I checked before filing, and it
changed what I wrote.

The admin-emails half is a failure to **grant** a privilege — awkward, not dangerous — and it is in
the issue in full. The `user_access_mode` half is a failure to **restrict**: if it diverges the same
way, an operator tightening login access in the admin UI would not tighten it for browsers, while the
API path does honour it. Publishing that publicly, before a fix, is a different act, and it is
ptone's to authorise rather than mine to do by momentum.

It is also **unverified** — inference from identical code shape, not a measurement. So I offered
three options and recommended testing it first, on the grounds that publishing an unverified claim of
that shape is the worst available outcome: it is either a disclosure or a false alarm, and I do not
yet know which.

The general rule I am extracting for myself: **an irreversible action taken on an unverified belief
deserves a check with the person who owns the consequences, even when it costs a round trip.** Today
has had four cases of a plausible reading turning out wrong; the difference here is that this one
could not be quietly corrected afterwards.

Re-raised the small open question with it: `deploy-instance` currently cannot bootstrap its own admin
at all, so a new operator hits this at §1 step 2. Ship the labelled stopgap pointing at #1270, or
leave it broken until #1270 lands? His "rather than work around it" suggests hold, but I do not think
he had the fresh-operator case in view when he wrote it, so I asked rather than assumed.

## 16:22 — GitHub Actions outage worked around via Cloud Build; #1270 comment posted

**GitHub Actions still down.** Zero workflow runs repo-wide since 14:21 (verified 16:20 via
`gh api repos/ptone/scion/actions/runs`). The last successful runs were on `scion/sn-dev-ready`.
PR #1269's `statusCheckRollup` is empty — no checks attempted, which is distinct from a red check.
Platform status: major/critical, upstream Vitess. Over two hours now, so the wait-vs-build
tradeoff no longer favours waiting.

**Cloud Build substitute (sn-ws-mount).** Build `5196cd5f-be56-4f41-b7a8-d6d7a89d85f8` in
`ptone-experiments`. Verified independently, not taken on report:

- status **QUEUED** (no `startTime` yet — submitted is not started)
- `timeout=7200s` — correct; the 600s default would have killed an 8-step chain
- `E2_HIGHCPU_8`, amd64 only
- created `2026-08-26T16:17:29Z`
- expected tag `us-central1-docker.pkg.dev/ptone-experiments/scion/scion-omni:dev-3f99cb79`
  (matches #1269 head OID `3f99cb7958943ef09cfe5b65024b1cdb3a9a84b0`)

Chain: thick-prep → scion-base → claude → codex → opencode → antigravity → grok-build → omni.

Obstacle they hit and solved: Cloud Build API not enabled on the gcloud caller's quota project
(`deploy-demo-test`/656238682705); workaround `CLOUDSDK_BILLING_QUOTA_PROJECT=ptone-experiments`.

A new `cloudbuild-omni.yaml` was written for this. **It is a deliverable, not scaffolding** — it
removes the single point of failure we just discovered the hard way, and it is adjacent to the
"omni image has no in-repo build provenance" gap I noted earlier (which I had wrongly closed as
#3). Instructed them to commit and push it to `scion/sn-ws-mount` with the quota-project note in
the message.

**Open risk I raised before the build lands, not after:** `deploy-instance` defaults
`SCION_IMAGE_REGISTRY` to `ghcr.io/ptone` (#38). An Artifact Registry image needs that overridden
*and* needs `artifactregistry.reader` for the Instance's service account; also unverified whether a
`us-central1` registry is reachable from a `us-east4` Instance without extra config. Asked
sn-ws-mount to settle both while the build runs.

**#1270 (upstream tech-debt issue).** ptone's call on the `user_access_mode` half: add it to the
public issue now. Posted:
https://github.com/ptone/scion/issues/1270#issuecomment-5427960640
It frames the fix as *"WebServer must not hold a snapshot of operational settings"* rather than two
field fixes — the field list will grow again otherwise, and the next one may also be a gate. I was
explicit that the `user_access_mode` half is **read, not measured**, and that the `admin_emails`
half **is** measured.

**Still open with ptone (1 of 2, the last):** ship the `deploy-instance` SEED→SERVER one-liner as a
labelled stopgap referencing #1270, or hold for the upstream fix. My lean: ship — without it §1
step 1 fails on a fresh deploy.

**§1 status unchanged:** steps 0,1,2,3,4,5,5a,5b,7 PASS. Step 6 blocked on #43, #43 blocked on an
image.

### 16:28 — Artifact Registry reachability settled (so the Cloud Build image will be usable)

Measured directly on `ptone-experiments`, rather than handing it to sn-ws-mount:

- AR repo `scion` (us-central1) exists. **Its own IAM policy is empty** (`etag: ACAB`, no
  bindings) — all access is project-level.
- The Instance runs as **`721899303052-compute@developer.gserviceaccount.com`**, which holds
  **`roles/editor`** on the project. Editor subsumes `artifactregistry.reader`, so the sandbox
  harness pull will succeed. `service-721899303052@serverless-robot-prod` holds `run.serviceAgent`,
  so the Instance's own container image pulls too.
- Cross-region (us-central1 registry → us-east4 Instance) is not a blocker; slower first pull only.

So the only remaining work is the **override**, confirmed live on `sn-walk`:
`SCION_IMAGE_REGISTRY='ghcr.io/ptone'`. It is a registry+namespace *prefix*, so the AR value is
`us-central1-docker.pkg.dev/ptone-experiments/scion`, and **both** halves need pointing at it: the
Instance's own `--image` and `SCION_IMAGE_REGISTRY` for the sandbox harness pull.

Also confirmed on `sn-walk`, full env (no grep):

```
SCION_SERVER_MODE=hosted
SCION_SERVER_AUTH_MODE=proxy
SCION_SERVER_AUTH_PROXY_PROVIDER=iap
SCION_SERVER_AUTH_PROXY_IAP_AUDIENCE=/projects/721899303052/locations/us-east4/services/sn-walk
SCION_SEED_SERVER_HUB_ADMINEMAILS=scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com
SCION_IMAGE_REGISTRY=ghcr.io/ptone
SCION_SERVER_HUB_ADMINEMAILS=scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com
```

The audience is clean — #33's fix is holding on this instance.

**Process note, worth keeping.** I nearly filed a phantom defect here: a `grep -iE "serviceaccount|image"`
collapsed two adjacent YAML lines and made `SCION_IMAGE_REGISTRY` appear to be set to a
service-account email. It is not. Dumping the whole env killed it in one step. This is the fifth
time today that a partial view of evidence produced a wrong conclusion, and the first time the
check caught it before the claim left the building. **Do not grep a field you are about to make a
claim about.**

**Carried forward, not new:** the Instance running as the default compute SA *with Editor* is the
over-broad-identity concern already attached to #41. Fine for a demo project, wrong for anything
an operator would run. It belongs in the hardening pass, not this one.

### 16:32 — omni image build 2 is RUNNING (build 1 failed on .gcloudignore)

Verified independently, not on report: build **`b72c66c1-d065-4ffb-8a70-e1bad9cb189e`**,
status **WORKING**, `startTime 2026-08-26T16:26:30Z`, timeout 7200s.

Build 1 (`5196cd5f`) **failed**: `.gcloudignore` excludes the web source, which omni's npm build
needs. Worked around with a custom `--ignore-file`.

`image-build/cloudbuild-omni.yaml` is committed and pushed — `06bb924a` on
`origin/scion/sn-ws-mount`, 140 lines, one file.

Expected reference: `us-central1-docker.pkg.dev/ptone-experiments/scion/scion-omni:dev-3f99cb79`

**Two gaps I raised, neither needing a rebuild:**

1. **The recipe as committed does not reproduce.** `06bb924a` contains the yaml *and nothing else* —
   the custom ignore file that makes it work is not in the commit, and neither is the
   `CLOUDSDK_BILLING_QUOTA_PROJECT` requirement. Both are load-bearing and both cost us an hour
   today. Asked for the ignore file committed and the exact working invocation in a comment header,
   with a one-line reason for each flag. *An undocumented load-bearing flag is worse than no file at
   all, because it looks reproducible.*

2. **Tag provenance caveat — noted, not actioned.** The image will be tagged `dev-3f99cb79`, but the
   build was submitted after `06bb924a`, so the source is the `06bb924a` tree. The Go code is
   identical (`06bb924a` touches only an uncompiled yaml), so the image is functionally the one I
   want and **I did not ask for a rebuild** — an hour of rebuild buys nothing here. But the tag names
   a commit that is not the source, and this project has already been bitten twice by tags that did
   not mean what they said (#20, #39). The yaml should derive its tag from actual HEAD.

Standing on the critical path: image → override `SCION_IMAGE_REGISTRY` and `--image` to the AR
prefix → deploy → walk §1 step 6 → close #43.

## 16:42 — Upstream merge assessment written (ptone's request)

Full document: **`upstream-merge-assessment.md`** in this directory. Summary of what it establishes,
all measured from `origin` at 16:35:

**The finding that matters:** #1266 (+7575/−59, 63f, 67 commits) and #1265 (+259/−0) are **both
`MERGEABLE`, `mergeStateStatus: CLEAN`, all checks SUCCESS — and `reviewDecision` on both is
EMPTY.** Neither has been reviewed by anyone. They are not blocked on CI, on conflicts, or on me.
The remaining work is not "make them mergeable"; it is "decide to merge them".

**Branch topology (verified with `merge-base --is-ancestor`, not assumed):**
`origin/scion/sn-ws-mount` (06bb924a, 76 ahead of main, 67 files, +8248/−61) contains
`sn-dev-ready` **and** `dev-rebase-1294`. `security-fix-p0-s1` is **not** contained — genuinely
independent. **One tip holds the entire tier; nothing is stranded.**

**The review-surface analysis** (the non-obvious part): 6,679 of 8,248 insertions are in files that
do not exist on main. New files cannot regress behaviour that does not exist. Removing new files
and tests leaves **~746 insertions / 61 deletions** of pre-existing non-test code — the real
regression footprint. Concentrated in two shared files: `pkg/runtimebroker/pty_handlers.go`
(+164/−25, shared with docker+k8s, a regression breaks terminals for *existing* users) and
`pkg/runtime/factory.go` (+31/−2, the runtime dispatch point).

**Recommendation: three PRs.** (1) #1265 alone and now — a security fix must not be gated behind a
7.5k-line feature. (2) #1266 as-is, after I rewrite its description around the review surface;
+7575 presented without guidance is precisely why it has been deferred. (3) The nine fix commits
rebased onto main as one PR — **gated on §1 step 6 being walked**, because #43's fix currently rests
on a code reading, and five code readings were wrong today.

**Alternative rejected and why it matters:** re-cutting #1266 into six thematic PRs costs ~a day,
risks a currently-green build, and buys reviewability that §2 shows is illusory —
*slicing a PR does not shrink the diff, it only changes how it is presented.* Revisit only if a
reviewer actually bounces it for size; they have not, they have not looked at all.

**Question I killed rather than asking:** whether `.design/project-log/*.md` belongs upstream.
It is established convention — **165 such files already on main.** The 23 new ones stay.

**Risk raised independent of the merge:** the scratchpad is not a git repository. The design doc,
this evidence log, `review-queue.md` and the briefs exist nowhere else. Several branch fixes are
one-liners whose justification took hours to establish; lose the volume and the code survives while
every "why" behind it does not.

**Still unfiled upstream:** 15 of 16 defects. Only #1270 has been filed. #45, #32, #35 and #42 are
upstream-general, not this tier's debt, and should be filed on their own merit rather than attached
to these PRs.

### Build watch
`b72c66c1` WORKING — thick-prep, scion-base, scion-claude SUCCESS; codex building; 4 queued.
3 of 8 in ~13 min, on pace.

## 16:55 — §1 STEP 6 PASSES. #43 fixed and verified on live hardware.

Instance **`sn-step6`**, deployed by one `deploy-instance` command from the Cloud Build image
`us-central1-docker.pkg.dev/ptone-experiments/scion/scion-omni:dev-3f99cb79`. SSH banner confirms
the running digest is `sha256:4883ecce…920a8` — **exactly** the image that was built. Not a
lookalike tag.

### The #43 verification

The failing case from this morning — git-linked project, agent create — reproduced exactly, and it
now works. Measured **inside** the sandbox, because `phase=running` is not proof (#17):

```
id                 uid=0(root) gid=0(root)
ls -ld /workspace  drwxr-xr-x 3 root root
/proc/mounts       none /workspace 9p rw,trans=fd,…,dfltuid=4294967294,dfltgid=4294967294,…
touch /workspace   WORKSPACE_WRITABLE
contents           .git + README   (octocat/hello-world cloned)
HOME               /home/scion      (#31 Cause A fix holding)
SCION_WORKSPACE_PATH=/workspace  SCION_WORKSPACE_GIT=true
```

Compare this morning on `sn-walk`: **no `/workspace` entry in `/proc/mounts` at all**, dir owned
`nobody:nogroup`, root denied. The old red herring is now explained — `dfltuid=4294967294` is
(uint32)−2, i.e. `nobody`, which is simply what the 9p mount reports for unmapped ownership. My
user-namespace hypothesis was in the right family but the actual cause was the one sn-ws-mount
found: the mount **destination** was never remapped.

### The full step-6 chain, proven end to end

```
clone landed                    .git + README present
git commit                      COMMIT_OK  -> 7336012 on branch scion/step6--ws6
network egress to github        git fetch --unshallow pulled real branches
git push                        PUSH_OK
REMOTE-SIDE verification        remote log shows 7336012
                                remote tree contains step6-probe.txt
```

**I verified the remote received it, not merely that push exited 0.** That distinction has mattered
repeatedly on this project.

**Honest scope limit:** the push was to a bare repo inside the sandbox, not to a credentialed HTTPS
remote. `GITHUB_TOKEN` is empty; the credential helper *is* wired
(`!f() { echo "password=${GITHUB_TOKEN}"; echo "username=oauth2"; }; f`) but has nothing to supply.
Testing a real authenticated push means putting a token into a demo Cloud Run instance — my
available PAT is broad account scope, and that is a credential decision for ptone, not mine to take
unilaterally. **Raised, not assumed.**

### Also confirmed on this deploy

- `--image` correctly derived `SCION_IMAGE_REGISTRY=us-central1-docker.pkg.dev/ptone-experiments/scion`
  (**#38 holds**), and the agent image resolved to `…/scion-claude:latest` from AR.
- The AR pull worked with no extra IAM, exactly as my 16:28 analysis predicted.
- `SCION_SERVER_MODE=hosted`, IAP audience clean (**#33, #40 hold**).

### #44 — now MEASURED, no longer inferred

Re-ran the idempotent deploy seeding **my own** identity so the result could not be confounded by
seeding an address I cannot log in as. `deploy-instance` printed:

> `Admin email:  scion-instance-gym@…`  /  `The deployer is seeded as admin.`

Logging in through IAP as precisely that identity returns **`"role":"member"`**. Env confirms
`SCION_SEED_SERVER_HUB_ADMINEMAILS` set, `SCION_SERVER_HUB_ADMINEMAILS` absent. **The one-command
deploy prints a claim that is false.** This is the evidence for the ship-or-hold decision.

### Two NEW defects found while walking

**A. Unknown create fields silently ignored, and the default resolves to something unavailable.**
The field is `harnessConfig`. I sent `harness: "claude"` — a plausible guess — and it was accepted
and discarded. Resolution then fell through to `antigravity`, which the broker cannot find:

```
502  Failed to dispatch to runtime broker: … failed to find harness-config
     "antigravity": harness-config "antigravity" not found
```

So a reasonable wrong field name yields a 502 naming a harness the operator never mentioned. This
reproduces on **both** git-linked and plain projects, so it is independent of #43 and it is
*upstream* of it — for a first-time operator this bites before #43 ever could. Two stacked
defects: silent unknown-field acceptance, and a default that resolves to an unavailable config
(adjacent to #37).

**B. The workspace is a depth-1 shallow clone, which blocks pushing to any remote but origin.**
`appliedConfig.gitClone = {depth: 1}`. First push attempt: `! [remote rejected] … (shallow update
not allowed)`. After `git fetch --unshallow`, the same push succeeded. An agent asked to push a
branch to a fork or second remote fails with a message that does not explain itself.

*Method note: my first push test was unrepresentative — it blamed the product for a limitation of
my own test setup. Isolating the shallow variable is what turned it into a real result.*

### 17:00 — the rest of the stack re-verified on the Cloud Build image

Cloud Build is a different path from GH Actions, and image contents do not have to match, so the
prior passes were not transitive. Re-checked inside the sandbox:

```
tmux                 /usr/bin/tmux, 3.4          (#23 holds on the new chain)
tmux sessions        scion: 2 windows            (terminal is attachable)
/home/scion          .bashrc .claude .claude.json .gitconfig .scion
                     .tmux.conf .zshrc agent-info.json agent.log
                                                 -> #31 Cause B VERIFIED (template home applied)
.tmux.conf           present, pane-exited hook count = 1
HOME                 /home/scion                 (#31 Cause A holds)
```

This is the first time the template home has been observed *applied* on a hosted sandbox — this
morning a provisioned home held none of `.tmux.conf`, `.zshrc`, `.gitconfig`, `.gemini`.

### §1 scorecard

| Step | State |
|---|---|
| 0 one deploy command | PASS (today, from the AR image) |
| 1 open run.app, log in | PASS — but as **member**, not admin (#44) |
| 2 create a project | PASS (git-linked and plain) |
| 3–4 start a Claude agent | PASS **only with explicit `harnessConfig`** (#48 blocks the naive path) |
| 5 attach terminal from browser | tmux session live and attachable; browser attach itself last exercised on the prior image |
| 6 commit to a git remote | **PASS**, remote-side verified; credentialed HTTPS remote untested |
| 7 | PASS |

**§1 is met.** It is met with three caveats, and I would rather state them than round up: #44 makes
the deploy print a false claim, #48 means a plausible first attempt fails with a 502 naming a
harness the operator never asked for, and the push half was to a sandbox-local bare repo because
putting a broad-scope PAT into a demo instance is ptone's decision, not mine.

---

## 17:20 — #48 root-caused. Mechanism named, and confirmed by a discriminating live experiment.

`#48` was filed at 16:45 with a symptom and no mechanism. It now has one, and the mechanism is not
the one I was heading toward. I record the wrong turn as well as the answer, because the wrong turn
is the more reusable lesson.

### The experiment that settled it

Two agent creates, same instance (`sn-step6`), same project (`ctl6`), seconds apart:

| request body | result |
|---|---|
| `{"name":"hc48","projectId":"..."}` | **502** — `failed to find harness-config "antigravity": harness-config "antigravity" not found` |
| `{"name":"hc48a","projectId":"...","harnessConfig":"antigravity"}` | **201, phase=running** |

The *same harness-config name* succeeds when the operator types it and fails when the system
arrives at it by itself. So the config is not missing, is not broken, and is not unavailable. The
**provenance** of the name is what breaks. Everything else follows from that one fact.

The successful agent's record carries `harnessConfigId: cddd00b6-bc63-426e-8949-0e165ea37423` and
`harnessConfigHash: sha256:e1bdb0ad…`. That is the whole difference.

### The mechanism, in order

1. `harnessConfig` is the wire field (`handlers_agents_core.go:135`). `harness` is silently
   dropped. An operator who writes `harness: claude` has sent nothing at all.
2. With no explicit name, no `scion.io/default-harness-config` project annotation, no hub
   operational `agent_defaults.default_harness_config`, and — per **#37** — an empty template
   chain, the hub reaches `populateAgentConfig` with `hcName == ""`. The stamping block at
   `handlers_agent_create_helpers.go:253` is guarded by `if hcName != ""`, so it is skipped
   **entirely, including its own not-found warning**. The agent dispatches with no name, no ID and
   no hash.
3. The broker re-resolves the name locally with `config.ResolveHarnessConfigName`
   (`provision.go:739`). Its last rung, #7, is the embedded settings default
   `default_harness_config: antigravity` (`pkg/config/embeds/default_settings.yaml:32`). This name
   is invented broker-side and has no hub identity attached to it.
4. `hydrateHarnessConfig` (`runtimebroker/handlers.go:992`) returns `"", nil` immediately when both
   ID and hash are empty. No hub-storage resolution is even attempted.
5. `resolveHarnessConfigDir` (`provision.go:412`) finds no path in the context and falls through to
   `config.FindHarnessConfigDir`, which searches the template dirs, the project dir, and
   `~/.scion/harness-configs`. On a hosted launcher **all three are empty**.
6. `500` at `provision.go:757`, surfaced to the operator as a `502`.

### Why this is a hosted-only defect, and why nobody has hit it

On a workstation, `~/.scion/harness-configs` is populated (workstation mode refreshes it from
embeds at startup — `server_foreground.go:1864`). Step 5's disk search succeeds and the broker's
invented name is harmless. In hosted mode that branch is not taken at all: `server_foreground.go`
takes the `BootstrapBundledResources` path instead, and every harness-config lives in hub storage
under `/root/.scion/storage/local/hubs/<hubid>/harness-configs/global/`, reachable **only** through
hydration.

So the on-disk fallback is load-bearing on workstations and inert on hosted. That is the
"two sources of truth for one concept" I sketched earlier — the broker resolves from disk, the hub
serves from the DB — but the earlier sketch had the failure in the wrong place.

Aggravating factor worth stating separately: the embedded default is `antigravity` while the omni
image is a Claude image. Even if the resolution worked, the default names a harness this deployment
cannot run. The operator sees a 502 quoting a harness they have never heard of.

### The wrong turn, recorded

I spent the first half of this hypothesising that the hub looked the name up and failed — that
`antigravity` had no DB row, or was archived, or that `SkipIfAnyExist` had starved the bootstrap.
All of that was wrong. Three separate checks killed it: all eight bundled harness-configs are
present on disk in hub storage; `antigravity` resolves to a real ID and hash when named explicitly;
and `BuiltinHarnessConfigs()` enumerates the embed directory unconditionally.

The fact that actually cracked it was a **silence**. The not-found branch logs at WARN when the
name came from a hub default, and no such warning appeared in the logs for either failing create.
I had been treating that silence as noise. It was the measurement: no warning means the lookup
never ran, which means `hcName` was empty, which means the hub never had a name to look up — and
therefore the name in the error message had to have been invented downstream. Absence of a log line
is evidence when you know the line would have fired.

One more partial-view error to add to the day's tally, the sixth: an earlier
`find … -maxdepth 4` showed `harness-configs/` looking empty and I read that as "storage is empty".
It was depth truncation. The configs sit at depth 6. Had I trusted that reading I would have chased
a bootstrap failure that never happened.

### Fix shapes (design only — not implemented)

- **Stopgap, deploy-side:** have `deploy-instance` set
  `agent_defaults.default_harness_config: claude` so the hub always supplies a name and therefore
  always stamps an ID and hash. One line, ships today, and also fixes the wrong-harness-name half.
  Does not fix the underlying asymmetry.
- **Real fix A, hub-side:** when the hub resolves no harness-config name, resolve its own default
  and stamp it, rather than dispatching an empty name and letting the broker invent one. Keeps a
  single source of truth.
- **Real fix B, broker-side:** when the broker's local resolution produces a name with no ID/hash,
  ask the hub for that name before falling back to disk. Closes the disk/DB split at the seam
  instead of upstream of it.
- **Diagnosability, independent of the above:** `provision.go:757` should say where the name came
  from. `failed to find harness-config "antigravity"` sent an operator (me) looking for a missing
  file for forty minutes; `harness-config "antigravity" (source: settings-default) not found on
  disk and no hub ID was supplied` would have ended it immediately. `ResolveHarnessConfigName`
  already computes and discards exactly this provenance string.

`A` and `B` are not exclusive and I lean toward both, with `A` first — it is the smaller change and
it puts the decision where the identity lives.

---

## 17:40 — #37 root-caused too, and it is the SAME defect as #48. One stopgap closes both.

#37's filed "remaining work" was: *start from the new WARN log on a live instance.* This session's
logs contained it, and one detail I noted at 17:05 but did not chase — **the WARN fires on the
successful create too**:

```
WARN default template not found — template chain is empty; agent home will use embedded defaults
     only projectPath=/root/.scion/projects/step6 error=template default not found
WARN template chain produced no home files — applying embedded default template home as floor
```

So #37 is not a conditional failure. It fires on **every** hosted create and always has.

### Measurement

The hub *has* the template: `default`, scope global, status active,
id `0032c67c-4cda-411c-9dfc-5a92239eddc2`, hash `sha256:2786a64…`. The running agent `ws6` carries
**no** `templateId` and **no** `templateHash`, while carrying `harnessConfigId` and
`harnessConfigHash` in the same `appliedConfig` object. Both pairs live on the same struct
(`store/models.go:157-164`) with the same `omitempty` convention, so the absence is real and not a
serialization artifact — I checked, because six partial-view errors today have earned that check.

Same discriminator as #48:

| request | templateId stamped? |
|---|---|
| no `template` field | **no** — absent |
| `"template":"default"` | **yes** — `0032c67c-…`, hash `sha256:2786a64…` |

`resolveTemplate` (`handlers_agent_create_helpers.go:37`) is correct and finds the global template
fine. It is simply **never called**, because `req.Template` is empty and the whole resolution block
at `handlers_agents_core.go:838` is guarded by `if req.Template != ""`.

### The unified mechanism

**#37 and #48 are one defect wearing two resource types.** On a deployed instance the hub
operational `agent_defaults` are unset, so neither `default_template` nor `default_harness_config`
is supplied. For each resource the same four steps follow:

1. no name from request, project annotation, or hub default;
2. the hub's guard (`if req.Template != ""` / `if hcName != ""`) skips resolution **and** skips
   ID/hash stamping;
3. the broker's hydration short-circuits on empty ID/hash and never consults hub storage;
4. the broker falls back to a local by-name disk search that is **always empty in hosted mode**,
   because in hosted mode resources live only in hub storage.

They differ only in what happens at step 4. The template path degrades silently to the embedded
floor — which is why #31 is fixed and why this stayed invisible. The harness-config path hard-errors
`500`, which is #48.

This also corrects the record on sn-dev-ready's hypothesis, which #37 explicitly marked UNVERIFIED:
the "no TemplateID sent → hydration skipped → local fallback fails" half is **confirmed**. The
"(not in DB?)" half is **wrong** — the template is in the DB, active, and hashed.

### Consequence for the stopgap decision

The stopgap I put to ptone at 17:13 was scoped to #48. It is bigger than that. `deploy-instance`
setting **both**

```
agent_defaults.default_template: default
agent_defaults.default_harness_config: claude
```

closes #37 and #48 together, because supplying a name is exactly what makes the hub resolve and
stamp. That is one deploy-side change against two filed defects, and it strengthens the case
materially. The real fixes (A/B in the 17:20 entry) still stand and should now be framed per
resource kind rather than per defect.

### Left open, deliberately

The agent record reports `template: "default"` at top level while carrying no template identity at
all. Given `req.Template` was empty, `agent.Template` should have persisted as `""`. I have not
found what supplies the display value and I am not going to guess — it is cosmetic relative to the
above, but a record that names a template it never resolved is its own small lie. Flagged, not
claimed.

---

## 2026-08-26 19:00 — upstream state re-verified; a merge-order hazard; the documentation gap

Written after ptone's 18:35 instruction to use ASD-STE100 Simplified Technical English. The
answer to his three-part request is in `status-for-ptone.md` in this directory.

### Correction to my own earlier reading

My previous PR checks queried `GoogleCloudPlatform/scion`. That is the wrong repository. Our work
lives in **`ptone/scion`**. Querying the wrong repo made #1265 and #1266 look merged and unrelated
(they resolve there to a grok-build harness and a GEMINI.md rename). They are not. Re-checked in
`ptone/scion` on 2026-08-26:

| PR | Branch | Target | Size | Checks |
|---|---|---|---|---|
| 1265 | `scion/security-fix-p0-s1` | `main` | +259 / 6 files | green |
| 1266 | `scion/dev-rebase-1294` | `main` | +7575 / 63 files | green |
| 1268 | `scion/sn-dev-ready` | `scion/dev-rebase-1294` | +465 / 6 files | green, DO NOT MERGE |
| 1269 | `scion/sn-ws-mount` | `scion/dev-rebase-1294` | +806 / 9 files | green, DO NOT MERGE |
| 1272 | `scion/wc-dev` | `main` | +221 / 5 files | green — fixes #45, issue 1270 |
| 1264 | `scion/broker-auth-gap` | `main` | +390 / 2 files | green |

**The merge gate is intact.** #1265 and #1266 are open and still wait on ptone. Nothing reversed.

### The hazard: #1266 must not merge alone

`cmd/deploy_instance.go:289`, read on all three branches:

- `scion/dev-rebase-1294` sets `SCION_SERVER_AUTH_MODE`, `SCION_SERVER_AUTH_PROXY_PROVIDER`,
  `SCION_SERVER_AUTH_PROXY_IAP_AUDIENCE`, `SCION_SEED_SERVER_HUB_ADMINEMAILS`. **That is all.**
- `scion/sn-dev-ready` and `scion/sn-ws-mount` additionally set `SCION_SERVER_MODE=hosted` and
  `SCION_IMAGE_REGISTRY`.

So #38 and #40 are marked fixed in this file, and they are — but **only on the two child
branches**, both of which target #1266's branch and both of which carry DO NOT MERGE markers.
Merging #1266 to `main` on its own ships a `deploy-instance` whose hub refuses to boot (#40) and
whose broker cannot pull agent images (#38). §1 step 0 would fail for every new operator.

This supersedes §3 of `upstream-merge-assessment.md`, which had the fixes re-cut against `main`
*after* the tier landed. See §8 of that document for the corrected sequence. The coordinator
re-verified the line-289 claim independently at 18:45.

Guard rail posted as a comment on the PR itself, so it sits at the point of action rather than in
one person's inbox: `ptone/scion#1266` comment `5429626747`. I did not edit the PR description —
that rewrite is #47 and needs a 63-file review map, not a warning bolted on top.

### #45 has a fix in flight

PR 1272 implements fix shape A from the #45 entry: an `AccessSettingsProvider` interface, with
`Server` holding the single source of truth behind an `RLock` and returning defensive copies, and
`WebServer` reading through it per request. It also fixes direct `s.config` reads in
`isUserAuthorized`, `getUserRole` and `handleInviteRedeem`. **#44 is downstream of #45 and must be
re-tested on a fresh instance once 1272 lands** — do not close #44 on the strength of 1272 alone.

### The documentation gap — new, and not previously priced

**#1266 adds no user documentation.** 63 changed files; the only Markdown is internal
`.design/project-log/*` engineering logs plus `image-build/README.md`. No page under `docs-site/`.
No reusable script for a third party. A person outside this project cannot deploy this tier today.

The doc site already has the right home for it: `docs-site/src/content/docs/hosted/single-node/`
(holds `overview`, `hub-server`, `hub-setup-gce`, `auth`, `managed-agents`, `metrics`,
`observability`, `skill-registry`). Specification for the missing page and the five scripts under
`scripts/single-node/` is in `status-for-ptone.md` Part C.4. Filed as task #50. Blocked on ptone's
decision B.4; the architect does not implement it.

### Scratchpad risk closed

This directory was version-controlled nowhere. It is now pushed to branch `scion/sn-impl-arch` in
`ptone/scion` under `.design/project-log/single-node/` — 61 files, 1.4 MB, documentation only, no
code, commit `8959cbc`. **Fold that directory into #1266 or merge the branch before this volume is
reclaimed.** The push does not make the scratchpad copy authoritative; this file still is, and the
archive is a point-in-time snapshot.

### Estate

Seven Instances present in `ptone-experiments` / `us-east4` at 18:40: `e2e-omni`, `e2e-walk-r2`,
`iap-demo`, `q2-control`, `sn-ready`, `sn-step6`, `sn-walk`. Unchanged.

### Blocked on

Decision 1 of 4 put to ptone at 18:42 (the merge gate). Three decisions queued behind it:
the #37/#48 stopgap-or-fix, the credentialed push test for §1 step 6, and who writes the tutorial.

---

## 2026-08-26 19:35 — the real upstream is GoogleCloudPlatform/scion, not the fork; #45 is fixed

Two corrections to the 19:00 entry, found by re-checking rather than by reasoning.

### 1. #45 is fixed and merged. #44 is not yet testable.

PR 1272 in the fork is **CLOSED, not merged** (19:08). Issue 1270 is **CLOSED** (18:57). The fix
landed **upstream** as `GoogleCloudPlatform/scion` PR **1300**, merged 18:57, and is on
`origin/main` as commit `1d1e4d76`. `AccessSettingsProvider` is present on main in
`cmd/server_foreground.go`, `pkg/hub/server.go`, `pkg/hub/web.go`, `pkg/hub/web_test.go`.

**#45 is closed.** The open product-defect count drops from eleven to ten.

**#44 is still open and still untestable.** It is downstream of #45, but the tier and the #45 fix
do not exist together in any one image: the fix is on `main`, and the tier is not. The #44 re-test
must wait until the tier merges. Do not close #44 on the strength of PR 1300.

### 2. `ptone/scion` is a fork. The upstream is `GoogleCloudPlatform/scion`.

Confirmed: `gh repo view ptone/scion --json parent` → `isFork=true`, parent
`GoogleCloudPlatform/scion`.

The workflow is two-venue. A fork PR is the review venue. The work lands through a **separate
upstream PR** against `GoogleCloudPlatform/scion`, under a **different number**, and the fork PR is
then closed unmerged. Verified on five branches — this is a pattern, not one case:

| branch | fork PR | upstream PR |
|---|---|---|
| `scion/wc-dev` | 1272 CLOSED | **1300 MERGED** |
| `scion/antigravity-apikey` | 1271 CLOSED | **1297 MERGED** |
| `scion/agent-status` | 1260 CLOSED | **1294 MERGED** |
| `scion/auth-capture-journey` | 1262 CLOSED | **1295 MERGED** |
| `scion/harness-auth-settings` | 1256 CLOSED | **1293 MERGED** |

**Our tier has no upstream pull request.** `scion/security-fix-p0-s1` (fork PR 1265) and
`scion/dev-rebase-1294` (fork PR 1266) exist only as fork PRs, targeting the fork's `main`.
Merging them there does **not** land the tier upstream. Of our set only
`scion/broker-auth-gap` has an upstream PR (1296, open); it is not part of the tier.

This is a step my 18:42 merge sequence omitted entirely. Anyone following that sequence would merge
#1266 into the fork's main and believe the tier had shipped.

### Corrected merge sequence

1. Fork PR 1265 → land upstream (small, standalone, green).
2. Remove the DO NOT MERGE markers. Merge fork PRs 1268 and 1269 into `scion/dev-rebase-1294`.
3. Add the #37/#48 stopgap to `scion/dev-rebase-1294`.
4. Rewrite the 1266 description (#47) with a 63-file review map.
5. Merge fork PR 1266 into the fork's `main` — **review only, this does not ship**.
   **5a. Open an upstream PR against `GoogleCloudPlatform/scion` and land it there.** ← was missing
6. Re-test #44 on a fresh instance built from the merged result.
7. File the remaining defects as issues. Fold `.design/project-log/single-node/` into the tier PR.
8. Land the tutorial and scripts (task #50).

Told ptone at 19:35. Both corrections sent as corrections, with the evidence, not as new opinion.

---

## 2026-08-26 20:50 — orthogonal triage; three issues filed; footprint plan

New direction from ptone at 20:29: identify defects orthogonal to running on an Instance, file
them on `ptone/scion` for other parts of the team, then rebase our work on the fixes and reduce our
footprint. Full triage: `orthogonal-triage.md` in this directory.

### Filed

- **[1273]** D-37 / D-48 — hosted hub drops template and harness-config identity on agent create;
  broker falls back to a disk search that is empty in hosted mode. Four-link root cause with
  file:line, plus the discriminating experiment and the "silent WARN" evidence.
- **[1274]** D-49 — `GitCloneConfig.Depth`: `pkg/api/types.go:770` documents `0 = full`,
  `pkg/provision/provision.go:308-311` rewrites `0` to `1`. Only a **negative** depth gives a full
  clone, which is undocumented and is what the tests use (`provision_test.go:620`, `Depth: -1`).
  Same defaulting independently in `pkg/runtime/k8s_runtime.go:2474` and
  `cmd/sciontool/commands/init.go:1592` — general, not runtime-specific.
- **[1275]** D-42 — `noAuth:true` makes agent create fail. Reproduced, not root-caused; filed as
  such rather than dressed up.

### Not filed, and why

D-35, D-41, D-46, D-39, D-15 all lack a mechanism. D-46 and D-39 each split into a general half and
a tier half and should be divided before filing. Filing them now would hand the owners a symptom.

### Correction to an earlier classification

**D-32 is not orthogonal.** `relocateToScion` is in our own
`pkg/runtime/cloudrun_sandbox_runtime.go` (confirmed: absent from `main`, present in the branch).
It stays ours. I had it on the orthogonal list; that was wrong.

### Footprint: 63 files → ~32, with no behaviour change

1. **23 of the 63 files are `.design/project-log/*.md`**, 1679 changed lines. Not review surface.
   Move to a separate docs commit. **63 → 40. Free.**
2. **PR 1266 contains the P0-S1 dev-auth fix a second time**, duplicating PR 1265: `IsLoopbackHost`
   and the `log.Fatalf` guard in `pkg/hub/web.go` (22), the `cmd/server_foreground.go` guard (~9),
   and tests in `pkg/hub/web_test.go` (80), `cmd/server_foreground_test.go` (96),
   `cmd/server_bridge_test.go` (89) — ~296 lines in 5 files. Land 1265 first, rebase, and the
   duplicate disappears. **40 → 35.** This is a second, independent reason for the task #51
   ordering.
3. **Two drive-bys travel separately**: `web/embed.go` (2-line comment fix, unrelated) and the
   general `deploymentWarnings[]` health field + `diagnostics.ts` (71 lines, 2 files) — the
   mechanism is a hub feature, only the Cloud Run string is ours. **35 → 32.**

### The distinction worth keeping

Fixing 1273/1274/1275 upstream removes **almost no lines** from PR 1266. Those issues cut
**complexity and risk** — the stopgap goes away, §1 step 6 works properly, the tutorial loses a
workaround. The **size** reduction comes entirely from items 1-3 above. Two different levers; both
were asked for; they should not be conflated in reporting.

### Open question

`metadata.DiscoverLinkLocalAddress` (148 lines, includes the guard refusing to bind `0.0.0.0`) is
arguably general. Left in our set because nothing else uses it today. If a second runtime wants it,
it should move.

### The criterion that matters

After the rebase the tier must still pass §1 end to end. A smaller diff that no longer deploys is
not progress.

---

## 2026-08-26 21:05 — D-41 filed as issue 1276; the triage set is closed

### What happened

The Explore run into the auth preflight returned. D-41 is general, the mechanism is one line, and
it is now filed as [ptone/scion#1276](https://github.com/ptone/scion/issues/1276).

**I verified every line reference myself before filing.** The findings came from a subagent, and a
subagent's file:line claims are exactly the kind of thing that should not go into an upstream bug
report unread. All four checked out: `handlers.go:2177-2179`, `harness/auth.go:413`,
`handlers_agents_core.go:1300-1307`, `start_context.go:388-392`. One number in the report was
wrong in our favour — the flag is set by **five** harness configs, not four. `grep` found
`grok-build/config.yaml:105` as well as gemini-cli, antigravity, claude and hermes.

### The mechanism, in one paragraph

`pkg/runtimebroker/handlers.go:2178` computes `gcpSAAssigned` from `MetadataMode == "assign"` and
nothing else. `MetadataMode` has three values, and `passthrough` — the one that means "the agent
reaches the real GCE metadata server", which is precisely the ambient-ADC deployment — is not
counted. So `pkg/harness/auth.go:413` never skips `gcloud-adc`, the broker refuses at
`handlers.go:577-622`, and the hub returns 422 `missing_env_vars`. The preflight has no other route
to an ambient credential: its only ADC hook is `os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")` at
`:2299`, which is a **file path** and is empty on a host whose credentials come from the metadata
server.

### Why this was worth filing rather than working around

The preflight runs **before** `resolveRuntimeForAgent` (`:2522`) and contains no runtime branches.
GCE and GKE workload identity hit this too. It is not our tier's problem; we were just the first to
stand on it. That is the whole test ptone set at 20:29, and D-41 passes it cleanly.

### A second defect travelled with it

`projectHasVerifiedGCPSA` (`pkg/hub/handlers_agent_create_helpers.go:1344-1346`) is documented as
meaning "the GCE metadata server can provide application default credentials at runtime". It does
not mean that. It requires a **Scion-managed** SA record with `sa.Verified`. A host with attached
ambient identity has working ADC and no such record. Lower severity — it feeds only the
drop-to-shell decision (call site `:281`) — but the equivalence is false and will mislead the next
reader. Reported in the same issue.

### What I did not do

I did **not** assert that counting `passthrough` is safe. I proposed it, gave the reasoning
(`passthrough` is only ever set deliberately, and its entire meaning is "this host has a real
metadata server"), offered the stricter `metadata.OnGCE()` variant, and asked the owner to confirm.
They own the trust model. We do not.

### Triage set now closed

| Defect | Outcome |
|---|---|
| D-37 / D-48 | filed — 1273 |
| D-49 | filed — 1274 |
| D-42 | filed — 1275 |
| D-41 | filed — 1276 |
| D-46 | do not file — ours |
| D-39 | do not file — Cloud Run platform feedback |
| D-32, D-44, D-47 | ours |
| D-35 | needs a live reproduction; evidence has aged out of Cloud Logging |
| D-15 | no mechanism |

Four filed, five ours, two unfilable. **Two of the five that looked orthogonal were not** — D-46 and
D-39 were on the file list until they were diagnosed. Reading before filing cost about an hour and
prevented two bad upstream reports. That ratio justifies the rule.

### What is not done

D-35 is the only open diagnosis, and no amount of reading closes it. The 400 response body is gone
from Cloud Logging at 3d across all Instances, and the query shape is sound (`sn-step6` verified
emitting at 20:59), so the record has simply aged out. A live reproduction on `sn-step6` — start an
agent, let it exit **naturally**, capture the body — is the only route. Task #52 carries the recipe.

### Still waiting on ptone

Nothing in this entry is approval for anything. Four decisions remain unanswered: the merge gate,
stopgap vs full fix for D-37/D-48, the credentialed push test, and dispatching a developer for the
tutorial. Footprint reduction (#53) item 2 depends on PR 1265 landing, which is ptone's gate, and
the tutorial (#50) is gated on decision B.4. Neither starts without a steer.

---

## 2026-08-26 21:28 — D-35 narrowed by reading; two candidates left, one hypothesis killed

### Why I did this instead of stopping

D-35 was the last open triage item and the only one **not** gated on ptone. Before spending a live
agent on a reproduction, I read the handler. That was the right order: the read eliminated four of
the six possible causes and corrected a wrong assumption about where the evidence lives.

### The ingest path is not the path I first looked at

`/api/v1/metrics/session/{id}` (`pkg/hub/server.go:3557`) is a **GET read** endpoint. The ingest
path is `POST /api/v1/agents/{id}/metrics` → `handleAgentMetrics`
(`pkg/hub/handlers_agent_metrics.go:63`). Anyone chasing this next should not lose time there.

### The 400 set is exhaustive: six sites

`:81` invalid body, `:87` `session.id is required`, `:91` `started_at is required`, `:98`
`started_at must be RFC3339`, `:105` `ended_at must be RFC3339`, `:109` `ended_at cannot be before
started_at`. Both `BadRequest` and `ValidationError` write 400 (`errors.go:183`, `:188`).

### A hypothesis I raised and then killed

`SummaryToMetricsPayload` (`metrics.go:156`) formats `EndedAt` **unconditionally**. A zero
`time.Time` formats to `0001-01-01T00:00:00Z` — non-empty, so `omitempty` does not drop it, and
valid RFC3339, so it parses — and would then fail the `:109` "before started_at" check. I measured
this directly rather than assuming it.

It is still wrong. The **only** constructor of `SessionSummary`
(`pkg/sciontool/telemetry/aggregator.go:171-176`) always sets `EndedAt: time.Now()`, and `grep`
confirms there is no second one. The same reasoning eliminates `:91`, `:98` and `:105`, because
`started_at` is likewise always formatted from a `time.Time`.

**Recording the dead hypothesis on purpose.** It is a good hypothesis — the unconditional format
really is a latent trap, and it would fire the moment anyone constructs a `SessionSummary` by hand.
The next person will think of it too, and should not have to re-derive that it is currently
unreachable.

### What is left

`:81` and `:87`. **`:87` is the stronger candidate**: `session.id` comes from `a.sessionID` with no
guard before the send (`init.go:436-440` passes the summary straight through), so an agent that
never registered a session start sends `""` and gets `session.id is required`. That fits what task
#31 established about this tier — sandbox agent lifecycle detection was broken for two independent
reasons.

### The correction that matters most

**This is not a diagnosability defect, and I was about to treat it as one.** `ReportMetrics`
embeds the response body in the error (`metrics.go:124`) and `init.go:440` logs it. The 400 body
was always being written. We lost it by not keeping it, not because the code discards it. Had I
filed a "the 400 is opaque" issue upstream it would have been the third bad report of the day,
after D-46 and D-39.

### Reproduction, sharpened

Start an agent on `sn-step6`, let it exit **naturally**, grep the sandbox log for
`Failed to report session metrics to hub`. The body is on that line.

**If it comes back empty, that is a result, not a failure** — the line is written by `sciontool
init` inside the sandbox to stderr, which this tier is known to lose (D-46). An empty grep confirms
D-46 and should be recorded as such.

### Not done

I did not run the reproduction. It costs a live agent and the last thing I told ptone was that
D-35 needs one; nothing here changes that conclusion, only the recipe. I also did not send a second
message about it. He asked for less volume, and "I refined a recipe for a thing I already told you
about" is not worth interrupting him for. It goes in the record and surfaces when he asks or when
there is an answer.

---

## 2026-08-26 22:19 — DECISION APPROVED: the merge order. And the refined design doc is written.

### The merge order is approved by ptone

He asked for a reminder at 22:14, I gave it at ~22:16, and he answered **"yes - this looks good"**
at 22:19. **This is the first of the four open decisions to be answered.** Recording it here
because an approval that lives only in a Discord thread is an approval that gets lost.

**Approved sequence:**

1. Merge **1265** (`scion/security-fix-p0-s1` → main).
2. Land **1268** and **1269** into `scion/dev-rebase-1294`. **Not** into main.
3. Rebase `scion/dev-rebase-1294` on main. The duplicated security fix disappears by itself.
4. Land **1266** — which, per the two-venue workflow, means opening an **upstream** PR on
   `GoogleCloudPlatform/scion` and then closing the fork PR unmerged.

**Everything in the reminder was re-verified against the remote before I sent it**, not recalled:

- 1265 head `scion/security-fix-p0-s1`, base `main`. Independent.
- 1266 head `scion/dev-rebase-1294`, base `main`.
- 1268 head `scion/sn-dev-ready`, base `scion/dev-rebase-1294`. **Stacked on 1266.**
- 1269 head `scion/sn-ws-mount`, base `scion/dev-rebase-1294`. **Stacked on 1266.**
- `cmd/deploy_instance.go` on `dev-rebase-1294`, line 263: no `SCION_SERVER_MODE=hosted`, no
  `SCION_IMAGE_REGISTRY`. Both present at line 289 on `sn-dev-ready` and `sn-ws-mount`.
- Six files overlap between 1265 and 1266: `pkg/hub/web.go`, `pkg/hub/web_test.go`,
  `cmd/server_foreground.go`, `cmd/server_foreground_test.go`, `scripts/cloudrun/deploy.sh`, and
  `.design/project-log/p0-security-fixes.md`.

**`scripts/cloudrun/deploy.sh` is new information.** I had the overlap at five files from an
earlier read; it is six, and the sixth is a deploy script, which is a more interesting conflict
than a test file. Found only because I re-derived the list from the API instead of trusting my
own earlier count.

### I cannot execute steps 2 and 3 myself

Two standing constraints bite:

- *"do not merge #1265/#1266 — that is ptone's gate."* Step 1 and step 4 are his.
- *"Do not rebase or force-push the integration branch."* `scion/dev-rebase-1294` **is** the
  integration branch, so step 3 is explicitly not mine.

So the approval unblocks the plan but not my own hands. Steps 2 and 3 need a developer with the
mandate to touch the integration branch. Raised with ptone.

### The refined design doc is written

ptone, 22:14: *"we should be landing one refined design doc in the .design folder in our first PR -
all other project logs do not need to be durably recorded in the repo, and can move to the
scratchpad working project folder."*

Written and pushed: **`.design/hosted/cloud-run-single-node.md`, 416 lines**, distilled from the
5454-line, 353 KB working log.

**Path corrected from what I told him.** I said `.design/cloud-run-single-node.md` before checking
the convention. `.design/hosted/` is where deployment and distributed architecture lives —
`hosted-architecture.md`, `multi-broker.md`, `runtime-broker-api.md`. Told him.

**What I stripped, and why it matters.** The working log had become an archaeology of its own
reasoning: a revision log at §0, a Dockerfile sketch marked `⚠️ SUPERSEDED` inline at :555, a
section titled `⚠️ STATUS REVERSED TWICE — READ §4.4-rev FIRST`, another titled `⚠️ TITLE
FALSIFIED`, a `~~DEAD~~` heading, and dated addenda running to §11.23. All of that is valuable
*process* record and worthless as a *design* record. A reader who needs to implement or review
this tier should not have to reconstruct which of three contradictory rulings survived.

The refined doc states the architecture as it now **is**, verified end to end on 2026-08-25, and
keeps only the decisions that are costly to reverse, each with its rationale: the omni image built
by chaining rather than transcribing harnesses; tmux staying inside the sandbox with `sandbox exec`
as the transport; selection probing the launcher binary because `K_SERVICE` is not set on
Instances; the `cloudrun-sandbox` name; and the rule that every address handed into a sandbox needs
an explicit decision. It carries the four alternatives with reasons for rejection, the ephemeral
durability trade stated as a trade, the IAP perimeter and its single point of failure, and the four
upstream issues (1273–1276) this tier depends on.

### Footprint plan corrected

My earlier §5.1 said "move the logs to a separate documentation commit or a separate PR". That is
**not** what ptone asked for and it is now superseded. The logs do not land in the repo at all.

Revised: 63 files → drop 23 logs → 40 → add 1 design doc → **41** → drop 5 duplicated
security-fix files once 1265 lands → **36** → send the two drive-bys separately → **33**.

---

## 2026-08-26 22:26Z — Developer dispatched for the repack; footprint numbers corrected twice over

ptone, 22:21: *"you can dispatch to a developer agent - we can create net new integration branches
if needed."*

**Dispatched `sn-repack-dev`** (template `developer`, harness `claude`).
Brief: `/scion-volumes/scratchpad/projects/single-node/briefs/sn-repack-dev.md`.

### Why a net-new branch is the better read of the approved order

Step 3 of the order ptone approved at 22:19 was "rebase `dev-rebase-1294` on main". His 22:21
authorisation lets us do something cleaner: cut **`scion/sn-tier`** fresh from `main` instead. Two
things improve at once. The standing constraint *"do not rebase or force-push the integration
branch"* stops being something we have to reason around — it just holds. And PRs 1266/1268/1269
stay intact as a review record rather than being rewritten underneath their own review threads.

**The single most useful fact I handed the developer:** `scion/sn-ws-mount` is `ahead=6, behind=0`
against `scion/sn-dev-ready`, so it is a strict superset. The complete tier — 1266 + 1268 + 1269 —
lives on **one** branch tip. The developer takes content from one branch, not three.

### Two numbers I gave ptone were wrong. Both corrected to him at 22:26.

I told him the tier was 63 files and would shrink to about 32. Neither figure survived contact
with the remote.

**Error 1 — scope.** 63 is PR 1266 alone. The full tier including 1268 and 1269 is **68 files**
(`git diff --stat main...scion/sn-ws-mount`). I had been quoting a sub-PR's size as the whole
tier's size.

**Error 2 — the dedupe arithmetic.** I assumed 5 whole files disappear when the duplicated
security fix comes out. Only **4** do. Per-file stats show `cmd/server_foreground.go` at `+34 -4`
— roughly 9 of those lines are the dev-auth guard and the remaining ~25 are genuine tier work. It
shrinks; it does not vanish. `scripts/cloudrun/deploy.sh` (`+5 -0`) has the same mixed shape.

**Corrected path:**

| Stage | Files |
|---|---|
| Full tier today (1266+1268+1269) | 68 |
| − 23 `.design/project-log/*.md` | 45 |
| + 1 `.design/hosted/cloud-run-single-node.md` | 46 |
| − 4 pure security-fix files | 42 |
| − 3 drive-by files | 39 |

About **39 files** and roughly **2,000 fewer lines** (1,679 logs + 287 dedupe + ~63 drive-bys).
The §5.1 table two entries above (63→40→41→36→33) is superseded by this one.

**Both errors came from trusting my own earlier count instead of re-deriving it.** This is the
same failure mode as the 1265/1266 overlap, which I had at five files until I re-checked and found
six. Re-derive; do not cache.

### One claim I passed on as unverified, and said so

The four "pure security-fix" files — `pkg/hub/web.go`, `pkg/hub/web_test.go`,
`cmd/server_foreground_test.go`, `cmd/server_bridge_test.go` — I classified from diff **stats** and
the shape of the change, not by reading every hunk. The brief tells the developer to verify before
deleting and to report back if any of them turns out to carry tier work. Deleting a file on the
strength of a line count would be exactly the kind of unexercised inference the heartbeat warns
about.

### The ordering dependency, restated because it is a real exposure

`scion/sn-tier` will not contain the dev-auth guard: we removed our copy, and 1265 has not landed.
That is intended, and it means **the tier must not merge before 1265**. This tier ships a
`deploy-instance` command that stands up a publicly reachable hub. Landing it without the guard is
not a tidiness problem.

Developer instructed: mark the branch `DO NOT MERGE BEFORE 1265`, and rebase on `main` once 1265
lands. Rebasing *that* branch is fine — it is ours and backs no open PR.

### Out of scope for this developer

No merges, no PRs (the fork-vs-upstream venue is ptone's call and easy to get wrong), no work on
the four upstream issues, no tutorial. Branch surgery happens in the developer's own `/tmp` clone,
not in shared-plain `/workspace`.

---

## 2026-08-26 22:33Z — PR 1302 is not ours; my §4.4 file classification was wrong; D-35 mechanism found

### 1. Upstream PR 1302 is a sibling project, not this tier

The coordinator reported at 22:30: *"Your Cloud Run Instances runtime PR is up as upstream
PR#1302"*, with 5 Gemini findings to fix. **That attribution is wrong and I corrected it.**

PR 1302 (ptone, `GoogleCloudPlatform/scion`, branch `cloudrun-instances-runtime`, 24 files,
`+2591 -272`, opened 22:23) implements a **different runtime**: each agent becomes its own Cloud
Run *service*, via `pkg/runtime/cloudrun_runtime.go`, `pkg/runtime/cloudrun/iap_exec.go`,
`pkg/runtime/cloudrun/logs.go`.

Our tier runs agents as **sandboxes inside one Instance**, via
`pkg/runtime/cloudrun_sandbox_runtime.go`, registered as `cloudrun-sandbox`. Design decision 4.4
chose that distinct name precisely because a `cloudrun` runtime already existed. This is that
other runtime, now arriving. The naming decision earned its keep.

Verified by set-intersecting the file lists. 1302 touches neither
`cloudrun_sandbox_runtime.go` nor anything in `pkg/runtimebroker/`. We touch neither
`cloudrun_runtime.go` nor `iap_exec.go`.

**None of the 5 Gemini findings reach us.** I checked the one with cross-cutting potential:
`syscall.Statfs` appears nowhere in `main`, so it is entirely 1302's new code.

**What does reach us is a merge conflict.** Exactly three shared files:

| File | Note |
|---|---|
| `pkg/runtime/factory.go` | The live one. Both PRs register a runtime. 1302 is `+8 -9`. |
| `cmd/server_foreground.go` | |
| `pkg/config/settings_v1.go` | |

1302 is ahead of us, so reconciliation lands on us. Developer warned.

Also flagged to the coordinator: 1302 carries **`tmp_design.md`, 318 lines, at the repo root**,
alongside a deliberate `.design/cloud-run-instances-runtime.md`. That looks accidental.

### 2. sn-repack-dev caught two errors in my §4.4. The brief told it to check; checking worked.

I classified four files as "purely the duplicated security fix" **from diff stats and the shape of
the change, not by reading the hunks** — and said so in the brief, with an instruction to verify
before deleting. Two of the four were wrong.

I then confirmed the developer's finding against PR 1265's own file list, which is the authority:

```
.design/project-log/p0-security-fixes.md   +75
cmd/server_foreground.go                   +10
cmd/server_foreground_test.go              +67
pkg/hub/web.go                             +22
pkg/hub/web_test.go                        +80
scripts/cloudrun/deploy.sh                  +5
```

- `cmd/server_bridge_test.go` — **absent from 1265 entirely.** 100% tier work
  (`TestResolveHubListenPort_HostedTier`, `TestResolveHubEndpointForBroker_ExplicitPort`). Deleting
  it would have destroyed real tests. My error.
- `cmd/server_foreground_test.go` — **mixed**, not pure. `+67` in 1265 against `+96` in the tier,
  so ~29 lines are the tier's IAP audience test. Edit, do not delete. My error.
- `pkg/hub/web.go` (+22) and `pkg/hub/web_test.go` (+80) — identical in both. Pure. Correct.

**A fifth file I had wrong in the other direction:** `scripts/cloudrun/deploy.sh` is `+5 -0` in the
tier *and* `+5 -0` in 1265. I told the developer it was mixed. If the five lines are the same five,
it reverts in full. Asked them to diff it.

Revised target: **~40 files** if `deploy.sh` is pure, ~41 if mixed. Told the developer not to chase
a number.

**The lesson is the same one as the footprint miscount, twice in one hour:** I inferred from
aggregate statistics where the hunks were available. Diff stats tell you a file changed, never
which change it was.

### 3. D-35 — mechanism found by reading; trigger still unconfirmed

Traced `session.id` from the hub's 400 back to its single source. All reads against `main`.

```
claude SessionStart hook
  -> dialects/claude.go:58    SessionID: getString(data, "session_id")   [uniform for all events]
  -> handlers/telemetry.go:684  aggregator.StartSession(event.Data.SessionID)
  -> telemetry/aggregator.go:92  a.sessionID = sessionID                 [THE ONLY WRITER]
  -> aggregator.go:172           SessionSummary{SessionID: a.sessionID}
  -> hub/metrics.go:152          Session.ID = s.SessionID
  -> hub/handlers_agent_metrics.go:87   400 "session.id is required"
```

**The filable defect, independent of root cause** (`handlers/telemetry.go:700-709`): the session-end
branch calls `aggregator.Finalize(...)` and passes tokens and error — **but not
`event.Data.SessionID`**, which is sitting right there on the event and is parsed for every event
by the same code path. The summary uses only the value latched at session-start. So a missed
session-start produces a permanently invalid payload, and `init.go:436-440` POSTs it with no guard.
The information needed to make the request valid was in hand and was discarded.

The same miss also leaves `a.startedAt` zero, since `StartSession` is its only writer too — but
`:87` fires before `:91`, so empty ID is what the operator sees.

**This also resurrects, in a sound form, a hypothesis I killed earlier.** I had rejected
"zero timestamp" because `Finalize` always sets `EndedAt: time.Now()`. Correct for *ended*_at —
but `StartedAt` comes from `a.startedAt`, which is *not* unconditionally set. The right shape of
the idea survived the wrong version of it.

**Two things narrow the field further.** The 400 itself proves hooks reach the handler at all —
otherwise there would be no POST. So this is not a blanket "hooks are dead" problem, and it is not
D-31's `HOME=/root` (fixed). And `sciontool init` is the long-lived supervisor holding the
aggregator for the whole agent lifetime, so this is not per-hook process amnesia either.

**Still unconfirmed: why session-start is missed.** Reading cannot settle it, and I have not
established that the observed 400 was `:87` rather than `:81`. The hub does not log which
validator fired, so the existing occurrence cannot be read back out of Cloud Logging. A live
repro on `sn-step6` (still up) remains the only way to close it.

Filing plan: report part 1 (confirmed by reading) as the defect; state part 2 as unconfirmed rather
than dressing a hypothesis as a finding. Two bad upstream reports (D-46, D-39) already came from
reasoning off a signal instead of exercising the path.

---

## 2026-08-26 22:47Z — scion/sn-tier delivered at 40 files. Verified against the remote, not taken on report.

`sn-repack-dev` reported COMPLETED at 22:45. **I verified every claim through the GitHub compare
API before accepting any of it.** The heartbeat's warning that agents have reported work not on
disk is why; in this case the report held up.

| Check | Result |
|---|---|
| Three branches exist on remote | `scion/sn-tier` `facb22fb`, `sn-driveby-embed-comment` `c4619074`, `sn-driveby-deployment-warnings` `97727c66` |
| File count | **40**, `+6837 -51`, 6 commits |
| `.design/project-log/` | **0 files** |
| `pkg/hub/web.go`, `web_test.go`, `scripts/cloudrun/deploy.sh` | all **absent** — security fix removed |
| Dev-auth guard in `cmd/server_foreground.go` | patch is 2351 bytes, **0** `loopback` references → gone |
| `cmd/server_bridge_test.go` | **kept** (100% tier work) |
| Drive-bys in tier | `web/embed.go`, `handlers_health.go`, `diagnostics.ts` all **absent** |
| Drive-by branch contents | exactly 1 file and exactly 2 files, as specified |
| Design doc | **byte-identical** to my `77dfada`, 435 lines |
| `DO NOT MERGE BEFORE PR #1265` | present in the commit message |

**A quoting trap nearly gave me a false pass.** My first design-doc comparison printed `IDENTICAL`
— because zsh glob-expanded the `?` in `contents/...md?ref=...`, both `gh api` calls failed, and I
diffed two empty files. This is the third time this session that an unquoted shell metacharacter
has produced a confident wrong answer (`--include=*.go` was the earlier one). Quote every URL
containing `?`. A verification step that can silently succeed on no data is worse than no
verification, because it launders a guess into a fact.

### Final footprint: 40 files, from 68

```
68  full tier (1266+1268+1269)
45  − 23 .design/project-log/*.md
46  + 1 design doc
43  − 3 pure security-fix files (web.go, web_test.go, deploy.sh)
40  − 3 drive-by files
```

40 against my predicted 39. The `+1` is `cmd/server_bridge_test.go`, which I had wrongly listed
for deletion. The developer caught it. My three published estimates went 33 → 39 → 40; the honest
reading is that only the last one was derived from evidence rather than from a previous estimate.

### One gap the developer did not flag, found in verification (task #55)

The two `DeploymentWarnings` tests were removed from `pkg/hub/workspace_storage_test.go` on
`scion/sn-tier` — **correctly**, since they cover the split-out mechanism. But they were **not
carried onto `scion/sn-driveby-deployment-warnings`**, which holds exactly two files, neither a
`_test.go`. Confirmed both directions: the tier patch has 0 `DeploymentWarnings` references, and
the drive-by branch has 0 test files.

**The tests now exist on neither branch.** Not a tier blocker, and the removal from the tier was
right. But the entire argument for splitting the mechanism out was that it deserves to be proposed
on its own merits — and proposing it with its coverage silently deleted defeats that. Recover them
from `scion/sn-ws-mount` before that branch goes up.

### Critical path

`scion/sn-tier` is ready and blocked on exactly one thing: **ptone merging PR 1265**, still OPEN.
The tier deliberately carries no dev-auth guard, so it must not land first. Nothing else is
waiting.

---

## 2026-08-26 23:03Z — DeploymentWarnings test gap closed. All three branches final and verified.

Task #55 fixed by `sn-repack-dev` and verified against the remote:

| Branch | Head | Files |
|---|---|---|
| `scion/sn-tier` | `facb22fb` | 40 — **unchanged**, byte-for-byte the head verified at 22:47 |
| `scion/sn-driveby-embed-comment` | `c4619074` | 1 |
| `scion/sn-driveby-deployment-warnings` | `6624f534` | 3 (was 2) |

`pkg/hub/workspace_storage_test.go` is back on the drive-by branch, and both tests appear as added
functions in the patch: `TestHealthCheck_DeploymentWarnings_CloudRunInstance` and
`TestHealthCheck_DeploymentWarnings_NotOnCloudRunInstance`.

The mechanism can now be proposed on its own merits with its coverage intact — which was the entire
justification for splitting it out of the tier. Had this shipped as it stood, we would have argued
"this deserves independent review" while handing a reviewer a feature whose tests had been quietly
deleted.

**This gap was created by my own instruction.** §4.5 of the brief said to split the mechanism out
and named the two implementation files; it did not say to take the tests with it. The developer
followed the brief exactly. Verification caught it because I checked the drive-by branch's contents
rather than only the tier's — worth remembering that splitting a change means checking *both*
sides, not just the one you care about.

### State of the merge order (task #51)

- Step 1, merge PR 1265 — **not done.** `state=OPEN, merged=null` at 23:00. ptone's gate.
- Step 2, assemble the tier on a new branch — **done and verified.**
- Step 3, drop the duplicated security fix — **done**, folded into step 2.
- Step 4, upstream PR then close the fork PRs — **not done.** ptone's call on venue and timing.

Nothing is waiting on me. The tier cannot land before 1265 because it deliberately carries no
dev-auth guard while shipping a publicly reachable deploy command.

Known follow-up, not a blocker: `pkg/runtime/factory.go` will conflict with upstream 1302. We land
second, so it is ours to reconcile, and it should be a clean re-registration.

---

## 2026-08-26 23:33Z — D-35 filed as ptone/scion#1281. Triage set now fully closed.

Filed while blocked on the merge gate. Nothing on the critical path moved: 1265 still `OPEN`, all
three branch heads unchanged (`facb22fb`, `c4619074`, `6624f534`), no new upstream PR.

### What was filed

The half that is provable by reading, with the half that is not explicitly labelled as such.

**Confirmed.** Line numbers verified against *both* `GoogleCloudPlatform/scion` main and
`ptone/scion` main — identical on both:

- `telemetry.go:700-701` — the session-end branch calls `Finalize(...)` with tokens and error but
  **not** `event.Data.SessionID`.
- `dialects/claude.go:41-60` — `session_id` is parsed uniformly for *every* event, so the ID is
  sitting on the session-end event that triggers the send.
- `aggregator.go:88/92/93` — `StartSession` is the **sole** writer of both `a.sessionID` and
  `a.startedAt`.
- `init.go:436-440` — POSTs unconditionally, no guard on an empty ID.
- `handlers_agent_metrics.go:87` — `400 session.id is required`.

One missed session-start therefore turns a recoverable gap into permanent loss of the session's
metrics, `exit_code` included, while the field needed to make the request valid goes unused.

**Not established, and stated as such in the issue:** that a missed session-start caused any
*specific* observed 400, and why session-start would be missed at all. Two hypotheses recorded as
eliminated — it is not "hooks are dead" (the 400 proves session-end arrived) and not per-hook
process amnesia (`sciontool init` is a long-lived supervisor holding the aggregator).

This is the discipline that D-46 and D-39 lacked: those two were filed off a plausible mechanism
rather than an exercised path, and both were wrong. Splitting the report into "provable" and
"suspected" costs nothing and stops the next reader inheriting my guess as a fact.

Also suggested a follow-up worth more than the fix itself: the hub has six distinct 400 sites and
logs none of them. Had it logged the rejection reason, this would have been diagnosable from the
failure we already had, with no live reproduction needed.

### A venue error I made and corrected

I tried to file on `GoogleCloudPlatform/scion` and the token refused `createIssue`. Checking where
1273 and 1276 actually live showed they are on **`ptone/scion`**, the fork — not upstream, as I had
been assuming and had written in earlier notes. **All five triage issues are on the fork.** The
fork-vs-upstream split applies to *pull requests*; I had over-generalised it to issues.

Triage set closed: **1273, 1274, 1275, 1276, 1281.**

---

## 2026-08-26 23:37Z — Upstream PR protocol confirmed. Answered ptone: no, we do not wait for the four issues.

### ptone's question: must we wait for 1273–1276 before opening our first PR?

**Answered: no.** They are not code dependencies.

Verified rather than asserted: I grepped the **entire 276 KB patch** for `scion/sn-tier`. It
contains **no code keyed to 1273, 1274, 1275 or 1276**. The only code workarounds present are for
two *Cloud Run platform* defects (B23 exec-form daemonize, and the sandbox delete hang) — both
documented, and the delete one self-disables via `deleteWorkaroundFixDetected` when the platform
stops exhibiting the bug.

The stopgaps for 1273 and 1276 are **deploy-time operator settings**, recorded in §9.2 of the
design doc, not code entanglement. The branch builds and tests clean against `main`, and §1 was
walked end to end on 2026-08-25 with all four issues open.

So the only genuine gate remains **1265**, and it gates for a different reason: exposure, not
correctness.

**Cost of waiting, which I put to him:** the branch ages against `main`, and 1302 lands first while
we already share `factory.go` with it. Waiting makes the conflict bigger, not smaller.

### Protocol, confirmed by the coordinator. Three points I would have got wrong.

1. **Fork PR first** — the review venue, where CI is genuinely green. I can open this myself.
2. **I do not open the upstream PR, and neither does the coordinator.** I generate a
   `compare/main...ptone:scion:<branch>?quick_pull=1&title=…&body=…` URL and post it as a markdown
   link to **Discord thread 1532864101909528737** (that thread carries nothing else). ptone clicks
   it; the PR is created under his account. **Agents have fork write access only, by design** —
   which is the real reason my token was refused, not a misconfiguration.
3. `repo-maintenance` handles fork-PR closure and branch cleanup after the upstream PR merges.
4. **Issues are fork-only.** My earlier correction was right.
5. **`scion/<name>` is the convention.** 1302 skipping it was ptone's own direct commit, *not* a
   precedent — I had started to read it as one.
6. **PR body uses `Ref: ptone/scion#N`, never a bare `Fixes #N`.** Fork and upstream number issues
   independently, so a bare reference silently resolves against the wrong repo.

### Sequencing row added, with the venue trap called out

Added to `/scion-volumes/scratchpad/merge-sequencing-deps.md` as the coordinator asked.

**I wrote the constraint against *upstream* `main`, not the fork.** This is the part most likely to
be missed in a batch sweep: 1265 will read as "merged" on the fork long before the guard exists
upstream, and the tier goes upstream. The file's own header already warns about exactly this —
mark resolved only "once the dependency has actually merged upstream, not just once its fork PR is
ready" — so the trap is a known one, and I flagged my row for the coordinator to sanity-check.

### Raised, not acted on: three stale fork PRs

`#1266` (`scion/dev-rebase-1294`), `#1268` (`scion/sn-dev-ready`), `#1269` (`scion/sn-ws-mount`) are
all still OPEN and all superseded by `scion/sn-tier`. Opening a fourth PR for the same tier without
closing them leaves a reviewer unable to tell which is live.

`repo-maintenance` owns fork-PR cleanup, so I asked rather than closing them myself.

### Outstanding decision with ptone

May I open the fork PR for `scion/sn-tier` now? I recommended yes: opening is not landing, and it
moves review and CI into the 1265 wait rather than after it.

---

## 2026-08-26 23:42Z — ptone said "yes". Fork PR **ptone/scion#1282** is open.

`scion/sn-tier` @ `facb22fb` — **40 files, +6837 −51, 6 commits**.
Title: *Single-node hosted tier: Cloud Run Instance + sandboxes*.

### What went into the PR body, and why

I wrote it for a reviewer who has never seen this tier and who has probably just looked at #1302.
Four things it has to do:

1. **Lead with the DO NOT MERGE BEFORE #1265 warning, and say why.** The branch deliberately has no
   dev-auth guard while shipping a publicly reachable `deploy-instance`. Left unstated, that reads
   as an omission rather than a deliberate hand-off.
2. **Kill the duplicate-of-#1302 reading before it forms.** Side-by-side table: #1302 is one Cloud
   Run *service* per agent (`cloudrun_runtime.go`, registered `cloudrun`); this is all agents as
   *sandboxes in one Instance* (`cloudrun_sandbox_runtime.go`, registered `cloudrun-sandbox`).
   Named the single live conflict — `pkg/runtime/factory.go` — and **claimed the reconciliation**
   rather than leaving it to be discovered. We land second, so it is ours.
3. **Show the scope discipline as a table**, 68 → 40, so the repack is visible instead of implied.
   Named the two drive-by branches explicitly, including the argument that the
   `deploymentWarnings[]` mechanism *deserves review on its own merits* — I did not want it read as
   Cloud Run plumbing that got waved through.
4. **List the 5 issues as `Ref:` only**, with an explicit sentence that the PR neither fixes nor
   code-works-around any of them.

Also stated the venue trap in the body itself, not just in the sequencing file: **#1265 merging on
the fork does not clear us.** The tier lands upstream; the guard must be upstream.

### Protocol followed

Fork PR only. **I did not open upstream and will not.** The compare URL for Discord thread
1532864101909528737 is held until the guard is genuinely in upstream `main`. `Ref: ptone/scion#N`
throughout — no bare `Fixes`. Branch keeps the `scion/<name>` convention.

### CI at 23:41

`shellcheck` **pass** (21s). `Build & Test`, `golangci-lint`, `Build and Push Omni Image` pending.
`mergeable=MERGEABLE`, `mergeStateStatus=UNSTABLE` (unstable = checks still running, not a
conflict). Branch is **behind `main` by 1** — harmless now, and it gets rebased when 1265 lands
anyway.

Worth noting: *Build and Push Omni Image* running at all is task #3 paying off. The omni image had
no CI build path when this started.

### Housekeeping closed out

Coordinator: 1266, 1268 and 1269 were already closed by repo-maintenance. **I verified all three
myself** rather than taking it — all `CLOSED`. So the "four PRs for one tier" problem lasted about
two minutes and never reached a reviewer.

Coordinator also said to **hold both drive-by branches** until the tier lands. Agreed — they are
parked and pushed, and nothing decays while they sit.

### Where this now stands

The tier is out of my hands and into review. The remaining path is entirely other people's gates:
#1265 upstream, then #1302, then the rebase, then the compare URL. Task #56 carries the sequence.

### 23:51Z — CI green on #1282, all four checks

| Check | Result | Time |
|---|---|---|
| Build & Test | **pass** | 3m55s |
| golangci-lint | **pass** | 2m18s |
| shellcheck | **pass** | 21s |
| Build and Push Omni Image | **pass** | 9m59s |

`mergeStateStatus` moved `UNSTABLE` → **`CLEAN`**. No conflicts. No reviews and no review comments
yet; Gemini has not posted.

Two of these are worth calling out rather than ticking off:

- **golangci-lint passing** matters because the repack cut across 40 files by hand — removing a
  duplicated security fix from a *mixed* file (`cmd/server_foreground.go`) and splitting drive-bys
  out. That is exactly the kind of surgery that leaves an unused import or a dead variable behind.
  It did not.
- **Build and Push Omni Image passing** is task #3 closing the loop. That image had **no CI build
  path at all** when this project started; it was built by hand. The tier's own PR now builds it.

`go test` in CI also confirms what the developer reported locally — the `internal/fixturegen`
failure is pre-existing on `main` and not something we introduced, since CI is green here.

**I am now genuinely blocked, and correctly so.** Every remaining step belongs to someone else:
#1265 upstream → #1302 → my rebase → the compare URL. There is no work I can pull forward without
manufacturing it. Task #56 holds the sequence so nothing is carried in my head.

---

## 2026-08-27 00:03Z — Heartbeat check. The gate is not what I thought, and it is ptone's own PR.

I checked the three heartbeat questions rather than restating last hour's answer. One material
finding.

### The critical path is blocked on #1265 **not having been proposed upstream at all**

I had been describing the gate as "waiting for #1265 to land". That framing is wrong, and it
matters, because it implies review is in progress. It is not.

Verified directly, not inferred:

- **The guard is absent from upstream `main`.** Fetched `pkg/hub/web.go` at `ref=main` from
  `GoogleCloudPlatform/scion` and grepped for `loopback`. **Zero matches.**
- **The branch does not exist upstream.** `GET /repos/GoogleCloudPlatform/scion/branches/scion/security-fix-p0-s1`
  → **404 Branch not found**.
- **There is no upstream PR for it.** Not in the open list; nothing matching in merged.
- On the fork: **CI fully green** (Build & Test 4m4s, golangci-lint 2m14s, shellcheck 27s),
  `draft=false`, **0 reviews**, created 03:07:47Z, last touched 03:12:13Z — **~21 hours untouched**.
- **Author is ptone.**

So the thing gating the tier is a green, unreviewed, never-proposed P0 security fix belonging to
the one person who can unblock it. That is a one-step unblock, not a wait. Reporting it as "we are
waiting on 1265" would have hidden that — which is precisely the failure mode of reasoning off a
signal instead of checking the path.

Independently of us: a P0 fix for a *publicly reachable unauthenticated admin UI* has sat
unreviewed for 21 hours. That is worth saying out loud even though it is not my call.

### Three of our five filed issues now have upstream fixes in flight

Confirmed by reading each PR body, not by title-matching:

| Upstream PR | Fixes | Our issue |
|---|---|---|
| #1304 | `Fixes #1275` | noAuth:true breaks agent create |
| #1305 | `Fixes #1273` | hosted hub drops template / HC identity |
| #1306 | `Fixes #1276` | auth preflight blind to passthrough GCP identity |

All three: CI green, reviewer **APPROVE**. This is what ptone meant by "being reviewed now".

Not yet fixed upstream: **#1274** (`GitCloneConfig.Depth`) and **#1281** (D-35 telemetry).

**Design-doc consequence, flagged not yet acted on.** §9.2 currently says the tier needs
deploy-time workarounds "until #1273 and #1276 land". When #1305 and #1306 merge upstream that
sentence becomes false and the stopgaps should be deleted. **No tier code changes** — the stopgaps
are operator settings, which is why the tier never blocked on them. I will edit §9.2 on the merge,
not before; editing ahead of the merge would put an unmeasured claim in the doc.

### Agents

No agent of mine is running. `sn-repack-dev` completed and its work is verified and pushed. Nothing
of mine is silently stalled, because nothing of mine is dispatched. I did not re-dispatch — there
is no work to pull forward that is not someone else's gate.

### Design doc sync

In sync as of now. §9.2's dependency statement remains true because none of 1304/1305/1306 has
merged yet. The trigger for the edit is their merge.
