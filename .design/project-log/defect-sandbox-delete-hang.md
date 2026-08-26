# Defect: `sandbox delete --force` always hangs and orphans a `runsc` process

**Filed by:** Scion single-node design workstream
**Date:** 2026-08-26 (**revision 2** — supersedes the version circulated earlier today)
**Component:** Cloud Run Sandboxes — `sandbox` CLI (`/usr/local/gcp/bin/sandbox`), platform-injected via `sandboxLauncher: true`
**Severity (to us):** High — teardown is our *normal* lifecycle, not an edge case
**Upstream status:** **Filed with the Cloud Run team, tracked internally** (ptone,
2026-08-26). No public issue exists, so this document is the citable record — it is
what our workaround code references, and it should travel with the code rather than
living only in a scratchpad.

> **Identifying the affected build.** Because there is no public bug to watch, the
> checkable anchor is the runtime version: **`runsc google-958767651`, spec 1.2.1**
> (binaries dated 2026-08-04). Anyone assessing whether a given Instance still has
> the defect should read that off the live host rather than infer it from a date.

> ### What changed in revision 2
>
> Revision 1 reported the hang as occurring *while an interactive `sandbox exec` was
> attached*, and flagged that we had not run the controls to know whether the exec was
> implicated. **We have now run them, and the exec is a red herring.**
>
> `sandbox delete --force` hangs on **every** sandbox we tried, including one running
> nothing but `sleep 3600`. The claim is therefore simpler and stronger than revision 1
> stated: **`--force` delete does not complete, full stop.** Please discard revision 1's
> framing — in particular §5's list of unknowns, which is now resolved rather than
> outstanding.
>
> Revision 2 also adds: version strings, the exact orphan argv, the fate of the
> orphans, and a separate defect in the **non**-`--force` path (§4).

---

## 1. Summary

`sandbox delete --force` does not return. It reaches network-namespace cleanup, raises
SIGTERM, then blocks. We observed 120 s and stopped waiting; exit was by our timeout,
not by the command completing. The underlying `runsc … delete --force` subprocess is
left behind.

This is **independent of what runs inside the sandbox** and **independent of whether an
exec is attached** — see the control matrix in §3.

Separately, plain `sandbox delete` (no `--force`) **refuses** — correctly and in 209 ms
— but appears to kill the sandbox anyway while leaving its gVisor processes running.
That is a second, distinct defect (§4).

## 2. Environment

| | |
|---|---|
| Platform | Cloud Run **Instances** (beta), deployed via Cloud Run v2 REST with `containers[0].sandboxLauncher: true` |
| Region / project | `us-east4` / `ptone-experiments` |
| Container image | `docker.io/library/python:3.11`, `tmux` installed at startup |
| `sandbox` binary | `/usr/local/gcp/bin/sandbox`, 55 MB, **dated Aug 4 2026**. Self-describes as "Serverless sandboxing CLI"; **no `--version` flag** |
| `runsc` | `/usr/local/gcp/bin/runsc`, 128 MB, dated Aug 4 2026. **version `google-958767651`, spec 1.2.1** |
| Sandbox invocation | `sandbox run <id> --detach --rootfs / --write --allow-egress -- <cmd>` |

## 3. The control matrix — this is the core of the report

| Case | Inside the sandbox | `--force` | Result | Time | Orphan |
|---|---|---|---|---|---|
| **T9** | tmux + interactive exec attached | yes | **HANG** | >90 s | `runsc delete` |
| **C1** | tmux, **no exec attached** | yes | **HANG** | **120 027 ms** (our timeout) | `runsc delete` |
| **C2** | **bare `sleep 3600`** | yes | **HANG** | **120 028 ms** (our timeout) | `runsc delete` |
| **C3** | tmux | **no** | **REFUSES**, then kills anyway | 209 ms | gofer + sandbox procs (§4) |

C1 removes the exec. C2 removes everything — no tmux, no shell, a single `sleep`. Both
hang identically, to within a millisecond of each other, which is our timeout firing
rather than any property of the workload.

### Output, identical across T9 / C1 / C2

```
Found network annotations for session <id>, cleaning up netns
E0000 ... Raising signal 15 with default behavior
    <blocks indefinitely>
```

and in the T9 case, eventually:

```
destroying container: stopping container: ... waiting sandbox stop: sandbox is still running
```

### The orphan

Identical argv in every `--force` case:

```
/usr/local/gcp/bin/runsc --platform=xemu --platform_device_path=/dev/xemu \
  --root=/tmp/runsc-root --ignore-cgroups --TESTONLY-unsafe-nonroot \
  --overlay2=root:memory --network=none delete --force <sandbox-id>
```

Two observations we'd flag to whoever picks this up:

1. **`--network=none` on the delete** — this is the delete operation's own network
   mode, not the sandbox's. The sandboxes were created `--allow-egress`, which maps to
   `--network=host`. We don't know whether that mismatch is related to the hang, but it
   is the one thing in the argv that looks surprising given the preceding log line is
   about netns cleanup.
2. **`--TESTONLY-unsafe-nonroot`** is present in the production argv. Presumably
   deliberate; noting it because the name invites a double-take.

### The good news, and it matters for our workaround

- **Orphans do not persist indefinitely.** By the time we ran the controls, earlier
  `runsc delete --force` orphans had become zombies (`<defunct>`) — their parent was
  eventually reaped. They were not consuming CPU.
- **The delete is effective despite not returning.** The sandboxes really are gone:
  `sandbox exec` on them reports "not running" / "no control socket found".

So the `--force` hang appears to be **a reporting/termination failure rather than a
state leak**. That is a much better failure mode than we assumed in revision 1, and it
is why our workaround (§5) is viable.

## 4. Second, distinct defect: plain `delete` refuses but kills

```
$ sandbox delete c3-test          # no --force
Found network annotations for session c3-test, cleaning up netns
cannot delete container that is not stopped without --force flag
Error: cmd.Wait(delete) failed: exit status 128
                                                    # 209 ms, exit 1
```

The refusal is correct and fast. But afterwards:

- `sandbox exec c3-test` → **"sandbox c3-test is not running"**
- and yet `runsc-gofer` and `runsc-sandbox` for `c3-test` are **still running** — one
  of them at 19.3 % CPU. These are the original sandbox runtime processes, not delete
  orphans.

So a command that reported failure has left the sandbox dead to the CLI and alive to
the host. **A caller who correctly handles the error and retries will be operating on a
sandbox the CLI has already disowned.** We think this is worse than the hang, because
the hang at least announces itself.

## 5. Why this matters to us, and what we're doing about it

We are designing a single-node Scion tier where a Cloud Run Instance hosts the control
plane and each agent runs in its own sandbox. The tier is **pure-ephemeral: redeploy is
the normal lifecycle**, and a redeploy tears down *every* sandbox at once.

- A ≥120 s non-returning call per sandbox is not a teardown path at fleet scale.
- We cannot distinguish "slow" from "wedged" from the CLI's behaviour, so we cannot
  report accurate agent lifecycle state during shutdown.
- The non-`--force` path can't be used as a polite first attempt, because of §4.

**Our workaround**, now that we know deletion is effective: issue `--force`, bound it
with a timeout, treat the timeout as success, and reap the orphan rather than waiting
on it. We'd rather not carry this, and we're picking the timeout value blind.

## 6. What would help us most

1. Is this known, and is `--force` expected to be bounded? By what?
2. §4 — is "refuse but kill" intended? If not, it's the more dangerous of the two.
3. Is treating a `--force` timeout as success actually safe, or are there cases where
   the sandbox survives the hang?
4. Is there a supported way to delete many sandboxes at once? That's our real access
   pattern and doing it serially at 120 s each is untenable.

## 7. Provenance and limits

Produced during Tier B of an empirical test plan run against a real Cloud Run Sandbox
on a real Cloud Run Instance — deliberately not `unshare`, not local Docker, and not a
self-installed `runsc`, all rejected as substitutes after an earlier
substitute-mechanism result in this project produced a confident wrong answer.

**Limits worth stating:** single Instance, single image (`python:3.11`), single day,
four cases. We did not test whether the hang eventually resolves beyond 120 s, and we
did not test concurrent deletes — which, given fan-out is our actual pattern, is the
most obvious gap remaining. The Instance has been deleted, so re-probing means
re-provisioning.

Raw output and full Tier B results: `ac0-results.md` (tests **T9**, **C1–C3**). Design
consequences tracked as phase **P4a** in `cloudrun-instances-sandboxes.md`.
