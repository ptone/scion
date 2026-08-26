# AC-0 Spike Results — Cloud Run Instances + Sandboxes

> # ⚠️ READ THIS BEFORE ANYTHING BELOW
>
> **This file is append-only and chronological. The first run was wrong about two
> important things, and its corrections live several hundred lines further down.**
> Reading top-to-bottom and stopping early will mislead you. It already has —
> ptone, 2026-08-25.
>
> | Claim in Run 1 | Reality | Corrected at |
> |---|---|---|
> | *"The `sandbox` CLI does not exist on Cloud Run Instances"* | **FALSE.** The binary **is** present at `/usr/local/gcp/bin/sandbox` — but only when the Instance is deployed with **`sandboxLauncher: true`** on the container. Run 1 omitted that field, so it was measuring an Instance that genuinely had no sandbox support. | "AC-0 Re-test" section (**BINARY EXISTS**) |
> | *"tmux socket assumption holds"* — **PASS (proxy)** | **FALSE.** Proxy-tested with `unshare`, which shares the host VFS outright and is therefore near-worthless as evidence about gVisor. Tier A later tested the real thing: **AF_UNIX does not cross the sandbox boundary in either direction.** | "Tier A — AF_UNIX Empirical Test" section |
>
> **Everything derived from those two claims is void**, including Run 1's
> "Recommendations for P4" items 1 and 4, and rows 1, 2, 6, 7 and 8 of its summary
> table (the `BLOCKED` rows were blocked only by the missing `sandboxLauncher` flag).
>
> **The standing lesson, since it caused both errors:** a negative result from a
> misconfigured deployment and a positive result from a substitute isolation
> mechanism are both worth roughly nothing. Test the real thing, configured the real
> way. See §10a's ground rules in the design doc.

---

## Run 1 — initial spike ⚠️ PARTLY SUPERSEDED (see banner)

**Date:** 2026-08-25
**Instance:** `ac0-test-instance` in `us-east4` (us-central1 was capacity-exhausted)
**⚠️ Deployed WITHOUT `sandboxLauncher: true` — this invalidates its central finding.**
**Image:** `docker.io/library/python:3.11-slim` (hello image too minimal — no coreutils)
**Project:** `ptone-experiments`

## ~~Critical Finding: No Sandbox Binary~~ ❌ FALSE — SUPERSEDED

> **This finding is wrong and was retracted the same day.** The Instance was deployed
> **without `sandboxLauncher: true`**, so the platform did not inject the binary. With
> the field set, `/usr/local/gcp/bin/sandbox` is present (55 MB, dated 2026-08-04) and
> sandboxes launch, exec and delete normally. See the "AC-0 Re-test" section below and
> §3.2z of the design doc. **The text below is retained only as a record of the error.**

**~~The `sandbox` CLI does not exist on Cloud Run Instances.~~** There is no
`/usr/local/gcp/bin/sandbox`, no `runsc`, no `sandbox` binary anywhere in the
filesystem. `find / -name "sandbox" -type f` returns zero results.

The kernel command line reveals `SANDBOX_GRPC_LOCALPATH_ENABLED=1` and
`RIPTIDE_LOCALPATH_ENABLED=1`, suggesting sandbox infrastructure exists at the
hypervisor/host level, but no user-facing CLI is exposed inside the container.

**Impact:** Checks 1–3, 6–8 as written in the design doc (section 10) cannot be
executed. Proxy tests using `unshare` and bind mounts were substituted where
possible.

---

## Environment Baseline

```
Kernel:   Linux localhost 6.9.12 #1 SMP Tue May 5 14:23:20 UTC 2026 x86_64
Hostname: localhost
User:     root (uid=0)
CPUs:     6 (visible), cgroup quota 208500/100000 = ~2.085 CPUs
Memory:   4009884 kB (visible), cgroup limit 2147483648 bytes = 2 GiB
Root FS:  overlay (lowerdir=/mnt/riptide/0, upperdir=/tmp/fs/0/upper, workdir=/tmp/fs/0/work)
Disk:     2.0 GB overlay root, 573 MB /dev/root (ssh binaries)
Network:  ipvlan-eth0 169.254.8.1/16, egress works (tested via httpbin.org)
```

**Capabilities:** Nearly full (`000001fff7fcffff`), seccomp filter active (mode 2, 1 filter).

**Kernel cmdline (relevant params):**
- `EMERALD_CONTAINER_USER_NAMESPACE=1`
- `EMERALD_SERVERLESS_RUNNER_CLEAN_SHUTDOWN=1`
- `EMERALD_SSH_PUBLIC_PREVIEW=1`
- `RIPTIDE_LOCALPATH_ENABLED=1`
- `SANDBOX_GRPC_LOCALPATH_ENABLED=1`
- `EMERALD_GUEST_AGENT_MANAGER=169.254.1.1:5557`

**SSH injection:** Platform injects sshd via `/.google_ssh/` overlay mounts. SSH
sessions enter the container namespace via `nsenter -t 1 -m`. sshd listens on
port 2200 internally.

---

## Check Results

### Check 1: tmux socket across sandbox bind mount (MOST IMPORTANT)
**Result:** PASS (proxy test — no sandbox binary available)
**Command (basic tmux):**
```bash
mkdir -p /tmp/tmux-test
TMUX_TMPDIR=/tmp/tmux-test tmux new-session -d -s test-session
tmux -S /tmp/tmux-test/tmux-0/default has-session -t test-session
```
**Output:**
```
tmux new-session exit: 0
test-session: 1 windows (created Tue Aug 25 21:55:30 2026)
Socket path: /tmp/tmux-test/tmux-0/default
has-session exit code: 0
```

**Command (mount namespace proxy — simulates sandbox bind mount):**
```bash
mkdir -p /tmp/tmux-shared
unshare --mount bash -c '
  mkdir -p /tmp/tmux-inner
  mount --bind /tmp/tmux-shared /tmp/tmux-inner
  TMUX_TMPDIR=/tmp/tmux-inner tmux new-session -d -s ns-test
'
# From OUTER namespace:
tmux -S /tmp/tmux-shared/tmux-0/default has-session -t ns-test
```
**Output:**
```
inner tmux created: 0
/tmp/tmux-inner/tmux-0/default  (socket found)
unshare exit: 0
Outer socket path: /tmp/tmux-shared/tmux-0/default
cross-namespace has-session exit: 0
```
**Implication:** tmux socket created inside a child mount namespace with bind-mount
is visible and usable from the parent namespace. **The load-bearing assumption
holds** — a tmux session started inside a sandboxed environment with a shared
bind-mount directory WILL be reachable from the launcher. This was tested with
`unshare --mount` + `mount --bind` as a proxy for the `sandbox run --mount`
pattern. The actual sandbox binary test remains blocked on binary availability.

---

### Check 2: Measure pre-SIGKILL window of sandbox delete
**Result:** CANNOT TEST (no sandbox binary)
**Proxy test — SIGTERM on child process:**
```bash
bash -c 'trap "echo SIGTERM at $(date +%s.%N)" TERM; sleep 300' &
kill -TERM $!
kill -0 $!  # check if still alive
```
**Output:**
```
Child PID: 667
Started at 1787694990.263585905
Sending SIGTERM at 1787694991.257660807
kill exit: 0
Child still alive
```
**Implication:** Standard SIGTERM behavior — process receives signal but trap
keeps it alive. Cannot measure the sandbox-specific SIGTERM→SIGKILL window without
the sandbox binary. The kernel cmdline includes
`EMERALD_SERVERLESS_RUNNER_CLEAN_SHUTDOWN=1` which suggests a grace period
mechanism exists at the platform level.

---

### Check 3: OOM enforcement boundary
**Result:** PARTIAL (no sandbox binary, but cgroup info gathered)
**Command:**
```bash
cat /sys/fs/cgroup/memory/memory.limit_in_bytes
cat /sys/fs/cgroup/memory/memory.oom_control
mkdir -p /sys/fs/cgroup/memory/test-child
echo "100000000" > /sys/fs/cgroup/memory/test-child/memory.limit_in_bytes
```
**Output:**
```
memory.limit_in_bytes: 2147483648  (2 GiB)
memory.usage_in_bytes: 116117504   (~110 MiB)
memory.oom_control:
  oom_kill_disable 0
  under_oom 0
  oom_kill 0

cpu.cfs_quota_us: 208500
cpu.cfs_period_us: 100000

Sub-cgroup created: YES
Sub-cgroup memory limit set to 99999744: YES (kernel rounded from 100000000)
Parent memory limit writable: YES (reduced to 1073741824 = 1 GiB)
```
**Implication:**
- OOM kill is enabled at the container cgroup level (`oom_kill_disable=0`)
- **Sub-cgroups CAN be created** — we can create child cgroups for sandbox processes
- **Parent cgroup limits are WRITABLE** — the container can modify its own limits (security concern: a sandbox process could raise its memory limit)
- `pids.max` is unlimited (`max`)
- This means we can implement per-sandbox resource limits using cgroup hierarchies, but the container-level limits are the ceiling

---

### Check 4: Instance hostname stability
**Result:** PARTIAL
**Command:**
```bash
hostname
cat /proc/sys/kernel/hostname
```
**Output:**
```
localhost
localhost
```
**Implication:** Hostname is `localhost`, not based on instance name. The kernel
cmdline sets `hostname 'emerald'` for the host, but inside the container namespace
it shows as `localhost`. A redeploy test would be needed to confirm stability, but
since the hostname is always `localhost` regardless of instance name, it's
effectively stable but uninformative. Instance identity must come from environment
or metadata, not hostname.

---

### Check 5: Duplicate Cloud Logging
**Result:** PASS — No duplicates, but stdout is NOT logged
**Command:**
```bash
# Stdout test (NOT routed to Cloud Logging):
echo "AC0-STDOUT-TEST-1787695133"

# /var/log test (IS routed to Cloud Logging):
echo "AC0-VARLOG-TEST-1787695133" > /var/log/ac0-test.log
```
**Output:**
Cloud Logging query for stdout marker: `[]` (empty — NOT logged)
Cloud Logging query for /var/log marker:
```json
[{
  "logName": "projects/ptone-experiments/logs/run.googleapis.com%2F%2Fvar%2Flog%2Fac0-test.log",
  "textPayload": "AC0-VARLOG-TEST-1787695133",
  "resource": {
    "type": "cloud_run_instance",
    "labels": {"instance_name": "ac0-test-instance", "location": "us-east4"}
  }
}]
```
**Logging model:** `/var/log` is a FUSE mount (`loggingfs`). Writes to any file
under `/var/log` are automatically forwarded to Cloud Logging with the log name
derived from the file path (e.g., `/var/log/ac0-test.log` →
`run.googleapis.com//var/log/ac0-test.log`). **Stdout/stderr from PID 1 does NOT
route to Cloud Logging.** The platform also logs SSH sessions and HTTP requests
automatically.

**Implication:** Only 1 log entry per write — no duplicate logging problem.
However, the design must account for the fact that sandbox process stdout/stderr
will NOT appear in Cloud Logging unless explicitly redirected to a file under
`/var/log`. This is different from Cloud Run services where stdout→Cloud Logging
is automatic.

---

### Check 6: rootfs write visibility (section 3.2a)
**Result:** CANNOT TEST (no sandbox binary)
**Proxy observation:** The root filesystem is an overlay
(`lowerdir=/mnt/riptide/0,upperdir=/tmp/fs/0/upper`). Writes go to the overlay
upper layer. Without the sandbox binary, we cannot test whether sandbox writes
are isolated from the launcher.

**Implication:** If sandboxes use a nested overlay (likely, given the Riptide
architecture), writes inside the sandbox would go to the sandbox's own upper
layer and NOT be visible from the launcher. This needs confirmation when the
sandbox binary becomes available.

---

### Check 7: rootfs read view — live or snapshot?
**Result:** CANNOT TEST (no sandbox binary)
**Proxy test (mount namespace):**
```bash
# Write from outer, read from mount namespace child
echo "outer-wrote-this" > /tmp/visibility-test.txt
unshare --mount bash -c 'cat /tmp/visibility-test.txt'
# Result: "outer-wrote-this" — visible (same overlay)

# Write from mount namespace child, read from outer
unshare --mount bash -c 'echo "inner-wrote-this" > /tmp/inner-write.txt'
cat /tmp/inner-write.txt
# Result: "inner-wrote-this" — visible (writes go to same overlay upper)
```
**Implication:** In a basic `unshare --mount`, the overlay upper layer is shared
— both inner and outer can see each other's writes. A real sandbox (if it creates
a fresh overlay) would likely provide snapshot-at-creation semantics. Needs
confirmation with actual sandbox binary.

---

### Check 8: sandbox run positional arg semantics
**Result:** CANNOT TEST (no sandbox binary)

---

## Architect's Priority Checks (from sn-impl-arch)

### Check A: Is `--mount` repeatable?
**Result:** CANNOT TEST — no sandbox binary exists on the Instance

### Check B: Is `-e`/`--env` repeatable?
**Result:** CANNOT TEST — no sandbox binary exists on the Instance

### Mount key syntax (`src=`/`dst=` vs `source=`/`destination=`)
**Result:** CANNOT TEST — no sandbox binary exists on the Instance

**Note:** These checks are blocked on the sandbox binary's availability. The
binary is not injected by the Cloud Run Instance runtime. It may require a
specific image, a feature flag, or may not yet be available in the current
Instance GA/preview.

---

## Additional Findings

### Namespace Support
```
unshare --user:  WORKS (can create user namespaces)
unshare --mount: WORKS (can create mount namespaces)
unshare --pid:   WORKS (can create PID namespaces, process sees PID=1)
Combined:        WORKS (mount + PID together)
```
**Implication:** Even without the sandbox binary, we can build our own isolation
using `unshare`. This is a viable fallback if the sandbox CLI is not available or
doesn't meet our needs.

### Network
- Egress: works (tested with httpbin.org, public IP: 34.96.50.4)
- Internal network: ipvlan on 169.254.x.x
- SSH via IAP tunnel to port 2200

### Deployment Notes
- `us-central1` was capacity-exhausted during test; deployed to `us-east4`
- The `hello` container image (`us-docker.pkg.dev/cloudrun/container/hello`) is
  too minimal — no coreutils, no package manager. Only `/bin/bash` and `/server`.
  Not suitable for testing.
- The SSH command (`gcloud alpha run instances ssh`) has a bug: when using
  `--impersonate-service-account` as a flag, an internal `describe` call doesn't
  propagate impersonation, causing a crash. Workaround: set impersonation globally
  via `gcloud config set auth/impersonate_service_account`.
- SSH does not support `--command` flag — sessions are interactive-only. Commands
  must be piped via stdin heredoc.
- Instance start time: ~30 seconds (image pull + provisioning + startup)

---

## Summary Table

| Check | Status | Key Finding |
|-------|--------|-------------|
| 1. tmux socket | ~~**PASS** (proxy)~~ ❌ **REFUTED** | ~~Socket crosses mount namespace via bind mount~~ — `unshare` proxy was invalid; real test says AF_UNIX does **not** cross. See Tier A. |
| 2. SIGTERM window | ~~**BLOCKED**~~ — not blocked; flag was missing | ~~No sandbox binary~~ — binary exists with `sandboxLauncher: true`. **Still unmeasured.** |
| 3. OOM boundary | **PARTIAL** | Cgroups writable; sub-cgroups creatable; OOM enabled |
| 4. Hostname | **PARTIAL** | Always `localhost`; not instance-name-based |
| 5. Cloud Logging | **PASS** | /var/log → Cloud Logging (1 entry, no dupes); stdout NOT logged |
| 6. rootfs writes | **BLOCKED** | No sandbox binary; overlay structure suggests isolation |
| 7. rootfs reads | **BLOCKED** | No sandbox binary; proxy shows shared overlay |
| 8. sandbox args | **BLOCKED** | No sandbox binary |

## Recommendations for P4

1. ❌ **VOID — the premise is false.** ~~**Sandbox binary is the blocker.**~~ The binary
   exists; the Instance simply needed `sandboxLauncher: true`. **Options (a), (b) and
   (c) below were all struck** — in particular, do **not** build isolation on
   `unshare`/bubblewrap. Retained to show what a missing deploy flag nearly cost.
   ~~The design doc assumes `/usr/local/gcp/bin/sandbox`
   exists. It doesn't. Options:~~
   - (a) Request sandbox CLI enablement from Cloud Run team (the `SANDBOX_GRPC_LOCALPATH_ENABLED`
     kernel param suggests the backend exists)
   - (b) Build our own isolation using `unshare` + cgroups (capabilities and
     namespaces are available)
   - (c) Use a custom image that bundles an isolation tool (e.g., bubblewrap/bwrap)

2. **Logging model differs from Cloud Run services.** stdout/stderr does NOT go to
   Cloud Logging. Must redirect to `/var/log/*` files or use structured logging
   via the loggingfs FUSE mount.

3. **Hostname is `localhost`.** Instance identity must come from metadata or environment
   variables, not hostname.

4. ❌ **VOID — this is the false positive.** ~~**tmux socket assumption holds**
   (proxy-tested). The most critical design assumption — that a tmux socket in a
   bind-mounted directory is reachable from the launcher — was confirmed using mount
   namespaces as a proxy for sandboxes.~~ **`unshare` shares the host VFS outright, so
   the socket was trivially the same inode; gVisor proxies the filesystem through a
   gofer, which is exactly where the two diverge.** Tier A tested the real mechanism:
   both directions fail. Note the phrasing above — "the most critical design
   assumption" — confirmed by the weakest available evidence, and recorded as PASS
   because no one had written down what PASS should mean.

5. **Cgroup hierarchy is user-writable.** Both sub-cgroup creation and parent limit
   modification work. This enables per-sandbox resource limits but also means
   sandbox processes could escalate their own limits unless we use user namespace
   UID mapping to prevent it.

6. **Region capacity is variable.** us-central1 was exhausted during test. The
   system should support multi-region deployment.

---

## AC-0 Re-test (sandboxLauncher: true)
### Date: 2026-08-25
### Instance: sbx-test, us-east4, deployed via REST API

### Context

The original AC-0 spike found no sandbox binary because the Instance was deployed
via `gcloud` CLI, which lacks the `--sandbox-launcher` flag for Instances. This
re-test deployed via the REST API with `sandboxLauncher: true` on the container.

### Step 1: Schema Confirmation

The Cloud Run v2 discovery document confirms `sandboxLauncher` exists on
`GoogleCloudRunV2Container`:

```json
"sandboxLauncher": {
  "description": "Optional. Indicates that this container can act as a sandbox supervisor and launch sandboxes.",
  "type": "boolean"
}
```

### Step 2: Instance Deployment

**Deployed successfully via REST API:**
```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://run.googleapis.com/v2/projects/ptone-experiments/locations/us-east4/instances?instanceId=sbx-test" \
  -d '{"launchStage":"ALPHA","containers":[{"image":"docker.io/library/python:3.11-slim","sandboxLauncher":true,"command":["python3"],"args":["-m","http.server","8080"]}]}'
```

Notes:
- The container image must run a long-lived process (e.g., HTTP server) or the
  Instance will terminate with "Instance completed successfully."
- API accepted `sandboxLauncher: true` without error. The first attempt that
  lacked a long-running process exited in ~21s; the second with `python3 -m
  http.server 8080` started in ~4s and stayed running.
- `launchStage` was sent as `ALPHA` but reflected as `BETA` in the response.

### Step 3: Sandbox Binary Verification

**BINARY EXISTS.** `/usr/local/gcp/bin/sandbox` is present when `sandboxLauncher: true`.

```
-rwxr-xr-x 1 root root 55820984 Aug  4 10:23 /usr/local/gcp/bin/sandbox
```

The binary is ~55 MB, dated 2026-08-04. It is the only `sandbox` binary on
the filesystem. PATH does not include `/usr/local/gcp/bin/` — must use full
path or set PATH explicitly.

### Step 4: Arity Checks (HIGHEST VALUE)

#### Check A: Is `--mount` repeatable? — YES

```bash
/usr/local/gcp/bin/sandbox run arity-mount --detach --rootfs / --write --allow-egress \
  --mount type=bind,source=/tmp/mount-a,destination=/check-a \
  --mount type=bind,source=/tmp/mount-b,destination=/check-b \
  -- /bin/sleep 120
```

**Result: BOTH mounts present.** `/check-a/marker.txt` contained "marker-a",
`/check-b/marker.txt` contained "marker-b". Exit code 0 on all exec checks.

Despite `sandbox run --help` showing `--mount string` (not `stringSlice` or
`stringArray`), the flag IS repeatable in practice. This is likely a Cobra
`StringArrayVar` that renders as "string" in help text.

#### Check B: Is `--env` repeatable? — YES

```bash
/usr/local/gcp/bin/sandbox run arity-env --detach --rootfs / --write --allow-egress \
  --env FOO=bar --env BAZ=qux \
  -- /bin/sleep 120
```

**Result: BOTH environment variables present.**
```
BAZ=qux
FOO=bar
HOME=/root
```

Same pattern as `--mount` — help shows `--env string` but both values are set.

#### Check C: Mount key syntax — BOTH WORK

**Full syntax (`source=`/`destination=`):**
```bash
--mount type=bind,source=/tmp/mount-a,destination=/check-full
```
Result: Works. Exit code 0.

**Short syntax (`src=`/`dst=`):**
```bash
--mount type=bind,src=/tmp/mount-a,dst=/check-short
```
Result: Works. Exit code 0.

**Both syntaxes are accepted.** Either can be used interchangeably.

#### Check D: Full Help Output

**`sandbox --help`:**
```
Serverless sandboxing CLI, providing compartmentalized execution for commands.

Usage:
  sandbox [command]

Available Commands:
  completion         Generate the autocompletion script for the specified shell
  delete             Delete a sandbox
  do                 Execute the specified command in a sandbox
  exec               Execute a command in an existing sandbox session
  fork               Fork a running sandbox to a new one.
  help               Help about any command
  run                Start a new sandbox.
  tar                Export a tarfile of the writable overlay (rootfs-upper) of a running sandbox

Flags:
  -h, --help   help for sandbox
```

**`sandbox run --help`:**
```
The run command creates and starts a sandbox. If no command is specified,
an empty sandbox will be started. The command blocks until the container
has started.

Usage:
  sandbox run <sandbox-id> [command-to-execute] [flags]

Flags:
      --allow-egress          Allow egress for this sandbox.
      --detach                Detach the sandbox from the console
  -e, --env string            Environment variables to set in the sandbox
  -h, --help                  help for run
      --import-tar string     The tarball to import rootfs-upper from
      --mount string          Mounts for the sandbox
  -p, --publish string        Ports to expose from the sandbox
      --rootfs string         Run the command using the root of the executing
                              container as the root directory of the sandbox.
                              (default "/")
      --stderr                Wire the stderr pipe (default true)
      --stdin                 Wire the stdin pipe (default true)
      --stdout                Wire the stdout pipe (default true)
      --template-var string   Template variables (format: KEY=VALUE)
  -w, --workdir string        The working directory to execute the command in.
      --write                 Allow writable mounts
```

**`sandbox exec --help`:**
```
Usage:
  sandbox exec <sandbox-id> <command-to-execute> [args...] [flags]

Flags:
  -e, --env string       Environment variables to set in the sandbox
  -h, --help             help for exec
      --stderr/stdin/stdout  (default true)
  -w, --workdir string   The working directory to execute the command in
```

**`sandbox do --help`:**
```
The do command provides support for executing a command in a sandbox without
having to think about sandbox lifecycle management. A new sandbox will be
created and destroyed for each execution, optionally persisting the state
of the filesystem to a persistence directory between executions.

Usage:
  sandbox do [flags] [command-to-execute]

Flags:
      --allow-egress          Allow egress
  -e, --env string            Environment variables
      --export-tar string     Tarball to export rootfs-upper to on exit
      --import-tar string     Tarball to import rootfs-upper from
      --mount string          Mounts for the sandbox
  -p, --publish string        Ports to expose
      --rootfs string         Root directory (default "/")
      --sandbox-name string   ID for the sandbox (random if not specified)
      --sync-tar string       Tarball for keeping filesystem in sync
      --template-var string   Template variables (format: KEY=VALUE)
  -w, --workdir string        Working directory
      --write                 Allow writable mounts
```

**`sandbox fork --help`:**
```
Fork creates a new sandbox using the state and command line of a running
source sandbox.

Usage:
  sandbox fork <source-sandbox-id> <target-sandbox-id> [flags]

Flags:
      --allow-egress     Allow egress
      --detach           Detach new sandbox
  -p, --publish string   Ports to expose
```

**`sandbox tar --help`:**
```
Creates a tarball of the writable overlay (rootfs-upper) of a sandbox
container, containing all changes made in the sandbox.

Usage:
  sandbox tar <sandbox-id> [flags]

Flags:
      --file string   The file to write the tarball to
```

### Step 5: Additional AC-0 Checks

#### Check 1: tmux socket test — SKIPPED
tmux is not available in `python:3.11-slim`. Would need a different base image
with tmux installed.

#### Check 6: rootfs write visibility — WRITES NOT VISIBLE FROM LAUNCHER
```bash
/usr/local/gcp/bin/sandbox run write-test --detach --rootfs / --write --allow-egress \
  -- /bin/bash -c "echo sandbox-wrote > /tmp/rootfs-write.txt; /bin/sleep 60"

# From launcher:
cat /tmp/rootfs-write.txt
# Result: No such file or directory
```

**Sandbox writes to `/tmp` are NOT visible from the launcher.** The `--write`
flag enables writable overlay within the sandbox, but writes go to a private
overlay — not shared back to the host filesystem. This confirms proper
isolation.

#### Check 8: Positional arg semantics — WORKS
```bash
/usr/local/gcp/bin/sandbox run my-named-sandbox --detach --rootfs / --write --allow-egress -- /bin/sleep 60
/usr/local/gcp/bin/sandbox exec my-named-sandbox -- /bin/echo "reachable by name"
# Output: "reachable by name"
# Exit code: 0
```

The first positional arg after `run` is the sandbox-id. The sandbox is
addressable by this name in subsequent `exec` and `delete` commands.

### Key Findings Summary

| Item | Result |
|------|--------|
| `sandboxLauncher` on REST API Instance | **WORKS** — API accepts it, binary injected |
| Sandbox binary location | `/usr/local/gcp/bin/sandbox` (55 MB, Aug 4 2026) |
| `--mount` repeatable? | **YES** — multiple `--mount` flags all take effect |
| `--env` repeatable? | **YES** — multiple `--env` flags all take effect |
| Mount syntax `source=/destination=` | **WORKS** |
| Mount syntax `src=/dst=` | **WORKS** |
| Sandbox named addressing | **WORKS** — `run <name>` then `exec <name>` |
| rootfs write isolation | **CONFIRMED** — sandbox writes NOT visible from launcher |
| `--help` flag types | Show `string` but behave as repeatable arrays |
| PATH includes sandbox? | **NO** — must use `/usr/local/gcp/bin/sandbox` |
| `sandbox do` command | Available — single-shot lifecycle management |
| `sandbox fork` command | Available — fork running sandbox state |
| `sandbox tar` command | Available — export writable overlay as tarball |
| `sandbox delete` | Requires `--force` for running sandboxes |

### Implications for Implementation

1. **Arity is NOT a concern.** Both `--mount` and `--env` accept multiple
   values despite the `string` help-text type. The implementation can pass
   multiple flags directly without comma-joining or workarounds.

2. **Both mount syntaxes work.** Use `source=/destination=` for clarity since
   it matches the `--help` documentation, but `src=/dst=` also works if
   brevity is preferred.

3. **PATH must be set explicitly.** The sandbox starts with an empty PATH.
   All commands inside the sandbox must use absolute paths, OR the launcher
   must set PATH via `--env PATH=/usr/bin:/bin:/usr/local/bin`.

4. **The `sandbox do` command** is available for one-shot operations
   (create-run-destroy lifecycle). Could be useful for build steps or
   transient operations.

5. **The `sandbox fork` command** enables forking a running sandbox's state
   to a new sandbox — useful for snapshot/restore patterns.

6. **The `sandbox tar` command** can export the writable overlay as a
   tarball. Combined with `--import-tar`, this enables persist/restore
   across sandbox lifetimes.

---

## Tier A — AF_UNIX Empirical Test (§10a)

### Date: 2026-08-25
### Agent: `spike-uds`
### Instance: `spike-uds-t2`, `us-east4`, deployed via REST API
### Image: `python:3.11` with `sandboxLauncher: true`, tmux/socat installed at startup
### Context

The Cloud Run engineering team corrected an earlier ruling: `--host-uds=host` **is**
set on Cloud Run Sandboxes. The original design (§4.4-orig: tmux socket on a bind
mount, control from the launcher) may therefore work. ptone requested an empirical
test. This spike runs Tier A of §10a — tests T1, T2, and T3a–e — against a **real
Cloud Run Sandbox on a real Cloud Run Instance**.

### Ground rules observed

1. **Real Cloud Run Sandbox on a real Instance** — not `unshare`, not local Docker,
   not a locally installed `runsc`.
2. **Pass/fail predicates written before each test.**
3. **Negatives characterized** — exact errors, errnos, and failure modes captured.
4. **Raw output captured** for every test.
5. **T3a–e reported separately.**

---

### T1: Socket created INSIDE sandbox, connect from launcher

**Direction:** sandbox → launcher (needs `--host-uds` to permit **create**)

**Predicate (written before running):** PASS if `socat UNIX-LISTEN` inside the
sandbox on a `--write` bind mount creates a socket file visible from the launcher,
AND the launcher can `socat UNIX-CONNECT` to it and exchange data.

**Setup:**
```bash
mkdir -p /tmp/t1-mount
sandbox run t1-test --detach --rootfs / --write --allow-egress \
  --mount type=bind,source=/tmp/t1-mount,destination=/tmp/t1-mount \
  -- /bin/bash -c 'socat UNIX-LISTEN:/tmp/t1-mount/s.sock,fork EXEC:/bin/cat & sleep 120'
```

**Raw results — inner view (from inside sandbox):**
```
total 1
drwxr-xr-x 2 root root  80 Aug 25 23:22 .
drwxrwxrwt 5 root root 100 Aug 25 23:22 ..
-rw-r--r-- 1 root root 290 Aug 25 23:22 inner-view.txt
srwxr-xr-x 2 root root   0 Aug 25 23:22 s.sock          ← socket EXISTS inside
-rw-r--r-- 1 root root   0 Aug 25 23:22 socat-err.log
```
`stat -c "%F %a" s.sock` → `socket 755`
Inner `socat UNIX-CONNECT` → `inner-ping` echoed back. **Socket works inside.**

Also confirmed with Python (`socket.AF_UNIX`, `socket.bind()`, `socket.listen()`):
```
Python socket bound successfully at /tmp/t1b-mount/py.sock
Socket exists: True
stat: socket 755
```

**Raw results — launcher view (from outside sandbox):**
```
total 8
drwxr-xr-x 2 root root 120 Aug 25 23:22 .
drwxrwxrwt 1 root root 120 Aug 25 23:22 ..
-rw-r--r-- 1 root root 290 Aug 25 23:22 inner-view.txt  ← regular file VISIBLE
-rw-r--r-- 1 root root   0 Aug 25 23:22 py-err.log      ← regular file VISIBLE
-rw-r--r-- 1 root root 502 Aug 25 23:22 py-result.txt   ← regular file VISIBLE
-rw-r--r-- 1 root root   0 Aug 25 23:22 socat-err.log   ← regular file VISIBLE
                                                          ← s.sock ABSENT
                                                          ← py.sock ABSENT
```
`stat /tmp/t1-mount/s.sock` → `cannot statx: No such file or directory`

Launcher `socat UNIX-CONNECT`:
```
2026/08/25 23:22:06 socat[306] E connect(, AF=1 "/tmp/t1-mount/s.sock", 22): No such file or directory
```

**T1 VERDICT: ❌ FAIL**

The socket file created inside the sandbox is **completely invisible** from the
launcher. Regular files created on the same bind mount at the same time are visible.
The gVisor gofer does not proxy socket file creation through the bind mount boundary.
Error: `ENOENT` — the file simply does not exist from the launcher's perspective.

---

### T2: Socket created on LAUNCHER, connect from inside sandbox

**Direction:** launcher → sandbox (needs `--host-uds` to permit **open**)

**Predicate (written before running):** PASS if a `socat UNIX-LISTEN` socket created
by the launcher on a bind-mounted path is connectable from inside the sandbox, and
data exchange works.

**Setup:**
```bash
mkdir -p /tmp/t2-mount
socat UNIX-LISTEN:/tmp/t2-mount/launcher.sock,fork EXEC:/bin/cat &
# Then launch sandbox with --mount on that directory
```

**Raw results — launcher view:**
```
stat: socket 755 root:root
srwxr-xr-x 1 root root 0 Aug 25 23:23 launcher.sock    ← socket EXISTS on launcher
```

**Raw results — inner view (from inside sandbox):**
```
total 1
drwxr-xr-x 2 root root  80 Aug 25 23:23 .
drwxrwxrwt 5 root root 100 Aug 25 23:23 ..
-rw-r--r-- 1 root root  19 Aug 25 23:23 inner-results.txt
srwxr-xr-x 1 root root   0 Aug 25 23:23 launcher.sock  ← socket VISIBLE inside (type preserved!)
```
`stat -c "%F %a" launcher.sock` → `socket 755`

**Inner connect attempt:**
```
2026/08/25 23:23:28 socat[8] E connect(, AF=1 "/tmp/t2-mount/launcher.sock", 29): Connection refused
```

**Launcher sanity check (launcher connecting to its own socket):**
```
sanity-check    ← echoed back correctly, socket works
```

**T2 VERDICT: ❌ FAIL**

The socket file IS visible inside the sandbox — the gofer preserves file metadata
(type=socket, permissions). But `connect()` returns **`ECONNREFUSED`** (errno 111),
not `ENOENT` or `EACCES`. The gofer shows the socket's directory entry but does
**not** proxy the `AF_UNIX connect()` operation across the boundary.

**Key distinction from T1:** In T1, the socket created inside was completely
invisible from outside (`ENOENT`). In T2, the socket created outside IS visible
inside (metadata preserved), but the connection is refused (`ECONNREFUSED`). The
gofer handles the two directions differently:
- **Create direction (inside→out):** socket file inode not propagated at all
- **Open direction (outside→in):** socket file visible but connection not proxied

---

### T3a: `tmux has-session` from launcher

**Predicate:** PASS if `tmux -S <sock> has-session -t scion` succeeds from the
launcher against a tmux socket on the bind mount.

**Setup:**
```bash
sandbox run t3-test --detach --rootfs / --write --allow-egress \
  --env TMUX_TMPDIR=/tmp/t3-mount \
  --mount type=bind,source=/tmp/t3-mount,destination=/tmp/t3-mount \
  -- /bin/bash -c 'export TMUX_TMPDIR=/tmp/t3-mount; tmux new-session -d -s scion; sleep 120'
```

**Inner view (socket exists inside sandbox):**
```
drwx------ 2 root root  40 Aug 25 23:24 tmux-0
--- tmux-0 dir ---
srw-rw---- 2 root root   0 Aug 25 23:24 default        ← socket EXISTS inside
socket 660 root:root
```
`tmux new-session` exit code: 0. Session created successfully inside.

**Launcher view:**
```
/tmp/t3-mount/tmux-0:
total 0
drwx------ 2 root root  40 Aug 25 23:24 .
drwxr-xr-x 3 root root 100 Aug 25 23:24 ..
                                                          ← default ABSENT
```

The `tmux-0/` directory IS visible from the launcher. The `default` socket file
inside it is NOT visible. Same pattern as T1.

**T3a VERDICT: ❌ FAIL — socket not visible from launcher, test cannot execute**

---

### T3b: `tmux send-keys` from launcher

**Predicate:** PASS if `tmux -S <sock> send-keys -t scion 'echo hi' Enter` delivers
keystrokes from the launcher.

**T3b VERDICT: ❌ FAIL — dependent on T3a; socket not visible from launcher**

---

### T3c: `tmux capture-pane` from launcher

**Predicate:** PASS if `tmux -S <sock> capture-pane -p -t scion` returns data.

**T3c VERDICT: ❌ FAIL — dependent on T3a; socket not visible from launcher**

---

### T3d: `tmux attach` from launcher (SCM_RIGHTS)

**Predicate:** PASS if `tmux -S <sock> attach -t scion` works with SCM_RIGHTS fd
passing across the boundary.

**T3d VERDICT: ❌ FAIL — dependent on T3a; socket not visible from launcher**

**Note:** T3d was predicted as the most likely to fail independently (§4.4-rev:
SCM_RIGHTS ancillary data might not be proxied). In practice, the failure occurs
at a lower level — the socket file itself is invisible, so the SCM_RIGHTS question
is never reached.

---

### T3e: uid/ownership check

**Predicate:** Record uid inside sandbox and uid of launcher-side process. PASS if
both captured. This test is independent of socket functionality.

**Results:**
```
Launcher:  uid=0(root) gid=0(root)
Sandbox:   uid=0(root) gid=0(root) groups=0(root)
```

**T3e VERDICT: ✅ PASS**

Both sides run as root (uid=0). **No uid mismatch.** tmux creates `tmux-<uid>/`
mode 0700 and refuses sockets it does not own — since both sides are uid=0, this
ownership check would pass if the socket were visible.

In the production scenario (scion user, uid=1000), both sides would also match
because `--rootfs /` shares the same `/etc/passwd` and the sandbox inherits the
launcher's user namespace. The uid-mismatch failure mode described in §4.4-rev
does not apply here.

---

### BONUS: §4.4a path validation (tmux via `sandbox exec`)

While T3a–d all fail for the socket-crossing design, the **replacement design
(§4.4a)** was validated as a bonus. All three control operations work through
`sandbox exec`:

```
$ sandbox exec t3-test -- tmux has-session -t scion
(exit code: 0)

$ sandbox exec t3-test -- tmux send-keys -t scion 'echo hello-via-exec' Enter
(exit code: 0)

$ sandbox exec t3-test -- tmux capture-pane -p -t scion
root@sandbox-t3-test:~# echo hello-via-exec
hello-via-exec
root@sandbox-t3-test:~#
(exit code: 0)
```

**§4.4a is validated:** tmux inside the sandbox, `sandbox exec` as the transport,
works for has-session, send-keys, and capture-pane. This does not test T3d's
equivalent (interactive attach via `sandbox exec` with the `script` PTY trick) —
that is Tier B's T6.

---

### Summary Table

| Test | Direction | Verdict | Error | Notes |
|------|-----------|---------|-------|-------|
| **T1** | create inside → connect from launcher | **❌ FAIL** | `ENOENT` — socket file invisible | Regular files visible; socket files not propagated |
| **T2** | create on launcher → connect from inside | **❌ FAIL** | `ECONNREFUSED` — visible but unconnectable | Socket metadata preserved; connection not proxied |
| **T3a** | tmux has-session from launcher | **❌ FAIL** | Socket not visible | Same mechanism as T1 |
| **T3b** | tmux send-keys from launcher | **❌ FAIL** | Socket not visible | Dependent on T3a |
| **T3c** | tmux capture-pane from launcher | **❌ FAIL** | Socket not visible | Dependent on T3a |
| **T3d** | tmux attach from launcher (SCM_RIGHTS) | **❌ FAIL** | Socket not visible | SCM_RIGHTS question never reached |
| **T3e** | uid/ownership check | **✅ PASS** | uid=0 both sides | No mismatch; not the failure cause |

### Interpretation

**`--host-uds=host` does NOT make AF_UNIX sockets cross the bind mount boundary.**
The engineering team's correction that the flag is set may be accurate (we cannot
verify the actual flag value from inside the sandbox), but its effect does not extend
to sockets on bind-mounted paths. Neither direction works:

- **Create (inside→out):** socket file is not propagated through the gofer at all
- **Open (outside→in):** socket file metadata is visible but `connect()` is refused

This is consistent with `--host-uds=host` potentially enabling access to *host-native*
Unix domain sockets (e.g., `/var/run/docker.sock` style paths on the VM) rather than
sockets on gofer-mediated bind mounts. The gofer proxies regular file I/O on bind
mounts but does not proxy AF_UNIX socket operations.

### Consequence for the design

Per §10a's outcome table:

> **T1 and T3a both fail → Original ruling stands after all; §4.4a is the design;
> Tier B proceeds unchanged.**

- **§4.4-orig is dead.** The tmux-socket-on-bind-mount design does not work.
- **§4.4a is the design.** tmux stays inside the sandbox; `sandbox exec` carries
  each operation in. The bonus test validates has-session, send-keys, and
  capture-pane through this path.
- **Do not restore** the P3 removal commits (tmux mount, `TMUX_TMPDIR`, `TmuxSocket`).
- **Tier B (T4–T11)** proceeds unchanged — it validates the §4.4a replacement
  mechanics (PTY trick, resize, latency).
- The three P3 removal commits were correct.

---

## Tier B — §4.4a Replacement Validation (§10a)

### Date: 2026-08-25
### Agent: `spike-uds-b`
### Instance: `spike-uds-b`, `us-east4`, deployed via REST API
### Image: `python:3.11` with `sandboxLauncher: true`, tmux installed at startup
### Context

Tier A is finished. AF_UNIX sockets do NOT cross the Cloud Run Sandbox boundary
in either direction; the original tmux-socket design is dead. This spike validates
the replacement (§4.4a): tmux stays inside the sandbox and `sandbox exec` carries
each operation in. The three non-interactive control operations (has-session,
send-keys, capture-pane) were already confirmed in Tier A's bonus check. This
tier tests the hard half: interactive PTY, resize, latency, and teardown.

**Image note:** The omni image was not accessible (registry permissions). T4 is
**PROVISIONAL** — `script` was found in `python:3.11` (Debian Bookworm), which
is the same base family as the omni image (scion-base is Debian-derived). But
T4 does not establish `script`'s presence in the actual shipped image.

### Ground rules observed

1. **Real Cloud Run Sandbox on a real Instance** — not `unshare`, not local Docker.
2. **Pass/fail predicates written before each test.**
3. **Negatives characterized** — exact errors and failure modes captured.
4. **Raw output captured.**
5. **Per-test verdicts, never averaged.**

---

### T4: Is `script` (util-linux) present?

**Predicate (written before running):** PASS if `sandbox exec <id> -- /usr/bin/script
--version` exits 0 and prints a version string. Absolute path required — PATH is
empty inside a sandbox (§3.2c).

**Command:**
```bash
sandbox exec tier-b -- /usr/bin/script --version
```

**Output:**
```
script from util-linux 2.41.5
```
Exit code: 0

**T4 VERDICT: ✅ PASS (PROVISIONAL)**

`script` from util-linux 2.41.5 is present at `/usr/bin/script` inside the sandbox.
**Provisional** because the test image is `python:3.11`, not the omni image. Both are
Debian-derived and `util-linux` is an `essential` package, so absence from the omni
image would be surprising — but this test does not establish that.

---

### T11: `sandbox exec -h | grep -i tty`

**Predicate:** If output contains "tty", `sandbox exec` natively supports TTY
allocation, which would eliminate the need for any PTY wrapper.

**Command:**
```bash
sandbox exec --help
```

**Full output:**
```
The exec command allows you to execute a command in a running sandbox. The sandbox
must be running already, or the command will fail.

Usage:
  sandbox exec <sandbox-id> <command-to-execute> [args...] [flags]

Flags:
  -e, --env string       Environment variables to set in the sandbox
  -h, --help             help for exec
      --stderr           Wire the stderr pipe (default true)
      --stdin            Wire the stdin pipe (default true)
      --stdout           Wire the stdout pipe (default true)
  -w, --workdir string   The working directory to execute the command in
```

`grep -i tty` exit code: 1 (no match)

**T11 VERDICT: ✅ PASS (no TTY flag exists)**

`sandbox exec` has no `--tty` or `-t` flag. The only stdio controls are
`--stdin/--stdout/--stderr` (pipe wiring). OQ-6 is resolved: the CLI does not
support native TTY allocation.

---

### T5: Negative control — bare `tmux attach` without PTY (SHARPENED)

**Predicate (written before running):** PASS (as a negative control) if `sandbox
exec <id> -- tmux attach -t scion` FAILS with "open terminal failed: not a
terminal". A SUCCESS here would mean PTY propagates and §4.4a's workaround is
unnecessary.

Per sn-impl-arch's sharpened instruction, three variants were tested:

#### Baseline (no PTY wrapper)

```bash
sandbox exec tier-b -- /usr/bin/tty
# → not a tty (exit 1)

sandbox exec tier-b -- bash -c 'test -t 0; echo "stdin is tty: $?"'
# → stdin is tty: 1  (NOT a tty)

sandbox exec tier-b -- bash -c 'test -t 1; echo "stdout is tty: $?"'
# → stdout is tty: 1  (NOT a tty)

sandbox exec tier-b -- tmux attach -t scion
# → open terminal failed: not a terminal (exit 1)
```

**Baseline result:** Inner process has NO tty. `isatty()` returns false for
stdin and stdout. tmux refuses with "not a terminal".

#### T5b: ptone's exact formulation — `script -qfc 'sandbox exec ...' /dev/null`

```bash
# Without TERM:
script -qfc 'sandbox exec tier-b -- tty' /dev/null
# → not a tty (ttyname fails)

script -qfc 'sandbox exec tier-b -- bash -c "test -t 0; echo stdin_is_tty:$?"' /dev/null
# → stdin_is_tty:0  (IS a tty!)
# → stdout_is_tty:0  (IS a tty!)

script -qfc 'sandbox exec tier-b -- tmux attach -t scion' /dev/null
# → open terminal failed: terminal does not support clear
```

**Partial propagation without TERM:** `isatty()` returns TRUE but `ttyname()`
fails. tmux tries to use the terminal but fails on "clear" capability because
TERM=dumb inside the sandbox.

```bash
# WITH TERM:
TERM=xterm-256color script -qfc 'sandbox exec tier-b --env TERM=xterm-256color -- tty' /dev/null
# → not a tty (ttyname still fails)

TERM=xterm-256color script -qfc 'sandbox exec tier-b --env TERM=xterm-256color -- bash -c "test -t 0; echo stdin_is_tty:$?; test -t 1; echo stdout_is_tty:$?"' /dev/null
# → stdin_is_tty:0  (IS a tty)
# → stdout_is_tty:0  (IS a tty)

TERM=xterm-256color script -qfc 'timeout 3 sandbox exec tier-b --env TERM=xterm-256color -- tmux attach -t scion' /dev/null
# → [tmux UI renders — full escape sequences, status bar visible]
# → exit 0 after timeout
```

**⚠️ MAJOR FINDING: tmux attach WORKS with launcher-side PTY + TERM.**

#### T5a: python `pty.spawn` wrapping `sandbox exec`

```bash
TERM=xterm-256color timeout 5 python3 -c "
import pty, os
os.environ['TERM'] = 'xterm-256color'
pty.spawn(['sandbox', 'exec', 'tier-b', '--env', 'TERM=xterm-256color',
           '--', 'tmux', 'attach', '-t', 'scion'])
"
# → [tmux UI renders — identical to T5b]
```

**T5a and T5b agree.** Both produce a working tmux attach.

#### fd analysis (the mechanism)

```
# Outer script → sandbox exec → inner process:
  fd 0 → host:[716]   (host PTY fd passed through gVisor)
  fd 1 → host:[716]
  fd 2 → host:[716]

# Inner script → inner process:
  fd 0 → /dev/pts/12  (real PTY device inside sandbox)
  fd 1 → /dev/pts/12
  fd 2 → /dev/pts/12

# No PTY → inner process:
  fd 0 → host:[720]   (pipe)
  fd 1 → host:[721]   (pipe)
  fd 2 → host:[722]   (pipe)
```

**Explanation:** `sandbox exec` passes through the launcher-side file descriptors
via gVisor's host fd mechanism. When the launcher has a PTY allocated, the fds
show as `host:[N]` (host-side references) rather than `/dev/pts/N` (sandbox-local
devices). `isatty()` returns true because the underlying host fd IS a TTY, but
`ttyname()` fails because the device path isn't mapped inside the sandbox.

**T5 VERDICT: ❌ NEGATIVE CONTROL INVALIDATED (this is the valuable result)**

The launcher-side PTY **DOES** propagate through `sandbox exec` to the inner
process. `isatty()` returns true. tmux attach WORKS when `TERM=xterm-256color`
is set via `--env`. **The inner `script` wrapper in §4.4a is NOT necessary.**

The only requirement is:
1. Launcher allocates a PTY (which `pty.StartWithSize` already does)
2. `TERM=xterm-256color` is passed via `--env` on `sandbox exec`

**§4.4a simplifies: `sandbox exec <id> --env TERM=xterm-256color -- tmux attach
-t scion`** — no `script` wrapper needed.

---

### T6: The fix — `script -qfc 'tmux attach'`

**Predicate:** PASS if tmux UI renders, keystrokes echo, `C-b d` detaches
cleanly, and the session survives (`has-session` still returns 0).

**Given T5's finding that the inner `script` wrapper is unnecessary, T6 is now
secondary validation. But it was still run to confirm the mechanism works.**

**Results:**
```
# script allocates a real PTY inside the sandbox:
sandbox exec tier-b -- script -qfc 'tty' /dev/null
# → /dev/pts/6  (real device)

# tmux attach via script:
sandbox exec tier-b -- bash -c '
  export TERM=xterm-256color
  script -qfc "tmux attach -t scion" /dev/null </dev/null &
  PID=$!; sleep 1
  tmux list-clients -t scion
'
# → /dev/pts/7: scion [80x24 xterm-256color] (attached,focused)
# → Attached clients: 1

# stdout shows tmux escape sequences (UI renders)
# detach-client exits cleanly
# has-session returns 0 after detach
```

**T6 VERDICT: ✅ PASS**

The `script` PTY trick works. It allocates a real `/dev/pts/N` device inside the
sandbox, and tmux attach functions correctly through it. But per T5, this
wrapper is unnecessary — the simpler direct approach works.

---

### T7: Resize out-of-band

**Predicate:** PASS if after `tmux resize-window -t scion -x 120 -y 40`, the
pane reports width 120.

**Note from sn-impl-arch:** T5 and T7 should agree. If launcher PTY fds pass
through, SIGWINCH should propagate naturally, making the out-of-band resize
unnecessary. T5 and T7 were tested for agreement.

#### Method 1: `resize-window` (out-of-band)

```bash
sandbox exec tier-b -- tmux resize-window -t scion -x 120 -y 40
# exit: 0

sandbox exec tier-b -- tmux display -p '#{pane_width}x#{pane_height}'
# → 120x40
```

**Works.** Direct tmux resize-window command applied immediately.

#### Method 2: SIGWINCH via launcher PTY resize

```python
# Python test: allocate PTY, attach tmux, resize PTY, check pane size
master_fd, slave_fd = pty.openpty()
# Set initial size 80x24
fcntl.ioctl(master_fd, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 80, 0, 0))
# Launch sandbox exec with tmux attach
proc = subprocess.Popen(['sandbox', 'exec', 'tier-b', '--env', 'TERM=xterm-256color',
                          '--', 'tmux', 'attach', '-t', 'scion'],
                         stdin=slave_fd, stdout=slave_fd, stderr=slave_fd)

# Before resize: 80x24
# Resize launcher PTY to 150x45
fcntl.ioctl(master_fd, termios.TIOCSWINSZ, struct.pack('HHHH', 45, 150, 0, 0))
# After resize: 80x24  ← NO CHANGE

# Resize again to 120x40
fcntl.ioctl(master_fd, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 120, 0, 0))
# After resize: 80x24  ← NO CHANGE
```

**SIGWINCH does NOT propagate.** Resizing the launcher-side PTY has no effect on
the tmux pane size inside the sandbox.

**T7 VERDICT: ✅ PASS (via `resize-window`)**

`tmux resize-window` works as the out-of-band resize mechanism. SIGWINCH does NOT
propagate through `sandbox exec`, so the out-of-band path is still required.

**T5/T7 partial agreement:** PTY fd characteristics propagate (isatty returns
true), but PTY signals (SIGWINCH) do not. This is consistent with gVisor passing
through the fd type but not forwarding terminal-related signals across the
sandbox boundary. The resize path in §4.4a (`tmux resize-window` via separate
`sandbox exec`) remains necessary.

---

### T8: Keystroke latency over one persistent exec

**Predicate:** PASS if p95 keystroke echo round-trip < 150 ms.

Three measurements were taken, testing different paths:

#### Part A: sandbox exec per-call overhead (100 iterations)

Each iteration spawns a new `sandbox exec -> echo -> return`.

```
Samples: 100
Min:     86ms
P50:    100ms
P90:    111ms
P95:    114ms
P99:    117ms
Max:    121ms
```

This is the per-call process-spawn cost of `sandbox exec`.

#### Part B: send-keys + capture-pane round-trip (50 iterations)

Each iteration spawns TWO `sandbox exec` calls (one for send-keys, one for
capture-pane).

```
Samples: 50
Min:    245ms
P50:    278ms
P90:    296ms
P95:    301ms
P99:    305ms
Max:    323ms
```

Two exec spawns (~200ms) plus tmux processing and a 50ms sleep between.

#### Part C: Within one persistent exec — tmux-local operations (50 iterations)

All operations run inside a single persistent `sandbox exec` session, measuring
tmux's own send-keys + capture-pane + grep latency with no additional exec
overhead.

```
Samples: 50
Min:     27ms
P50:     32ms
P90:     35ms
P95:     37ms
P99:     ~40ms
Max:     53ms
```

**T8 VERDICT: ✅ PASS — p95 = 37ms (well under 150ms threshold)**

The keystroke echo latency over one persistent exec is **p95 = 37ms**, far below
the 150ms threshold. The per-call sandbox exec overhead (~100ms) is relevant for
control operations (send-keys, capture-pane) but not for the interactive attach
path, where a single persistent exec session is held open.

**Distribution breakdown:**
- **Interactive attach (Part C):** p95 = 37ms — this is what the user feels
- **Control operations (Part B):** p95 = 301ms — this is send-keys + capture-pane
  round-trip via two separate exec calls. Acceptable for Scion's control cadence.
- **Single exec spawn (Part A):** p95 = 114ms — the fixed cost per control call

**The latency argument for revisiting the dead socket design is weak.** 37ms p95
for interactive keystrokes is indistinguishable from local, and the 300ms control
round-trip is well within Scion's polling interval.

---

### T9: Teardown — `sandbox delete --force` while exec attached

**Predicate:** PASS if `sandbox delete --force` while a persistent exec is
attached causes the launcher-side process to exit promptly and non-zero, with
no orphan, no hang.

**Setup:** Fresh sandbox `t9-v4` with tmux, persistent exec attached via python
`pty.openpty()` (the `pty.StartWithSize` shape).

**Results:**
```
Exec attached: client-7: scion [80x24 xterm-256color] (attached,focused)

--- sandbox delete --force issued ---

Launcher exec wrapper: exited in ~1s, exit code 1 (non-zero)

sandbox delete --force: HUNG >90s
  Output: "Found network annotations for session t9-v4, cleaning up netns"
          "E0000 ... Raising signal 15 with default behavior"
          (then stuck)
  Eventually: "destroying container: stopping container: ... waiting sandbox
               stop: sandbox is still running"

Orphan: 1 process
  /usr/local/gcp/bin/runsc --platform=xemu ... delete --force t9-v4
```

**T9 VERDICT: ⚠️ PARTIAL PASS**

- ✅ **Exec exits promptly and non-zero.** The launcher-side exec process exits
  within ~1 second of delete being issued, with exit code 1. No process leak from
  the exec side — this is the part that matters for P4.
- ⚠️ **`sandbox delete --force` hangs.** The delete command itself does not
  complete within 90+ seconds. It sends SIGTERM but then gets stuck waiting for
  the sandbox to stop. The underlying `runsc delete --force` process becomes
  orphaned.
- ⚠️ **One orphan: the `runsc delete --force` process.** This is a `sandbox`
  CLI-level issue, not a Scion-level one. P4 must handle this with a timeout and
  explicit orphan cleanup.

**Consequence for P4:** The runtime's `Delete` method must:
1. Not block on `sandbox delete --force` — run it with a timeout
2. Clean up orphaned `runsc delete` processes if the delete hangs
3. The exec process does exit cleanly, so the exec lifecycle is sound

---

### T10: Idle stability — exec attached, idle 30 min

**Predicate:** Exec attached, idle 30 min, still responsive.

**Setup:** Persistent exec attached via python `pty.openpty()` to `tier-b`
sandbox at 23:52 UTC. Sandbox has been alive since 23:32 UTC (20 min already).

**T10 VERDICT: PENDING — check at 00:22 UTC (30 min after attach)**

---

### Summary Table

| Test | Verdict | Key Finding |
|------|---------|-------------|
| **T4** | **✅ PASS (provisional)** | `script` from util-linux 2.41.5 present at `/usr/bin/script`. Provisional: tested on `python:3.11`, not the omni image. |
| **T5** | **❌ NEGATIVE CONTROL INVALIDATED** | ⚠️ **MAJOR FINDING:** launcher-side PTY propagates through `sandbox exec`. `isatty()` returns true inside. tmux attach WORKS with `--env TERM=xterm-256color`. Inner `script` wrapper is NOT necessary. |
| **T5a** | (variant of T5) | python `pty.spawn` wrapping sandbox exec: tmux attach works. |
| **T5b** | (variant of T5) | ptone's `script -qfc 'sandbox exec ...'`: tmux attach works. Agrees with T5a. |
| **T6** | **✅ PASS** | Inner `script` PTY trick works (allocates `/dev/pts/N`), but is unnecessary per T5. |
| **T7** | **✅ PASS** | `resize-window` works. SIGWINCH does NOT propagate through `sandbox exec`. Out-of-band resize still required. |
| **T8** | **✅ PASS** | p95 = 37ms (interactive), p95 = 114ms (per exec call), p95 = 301ms (control round-trip). All well under threshold. |
| **T9** | **⚠️ PARTIAL PASS** | Exec exits promptly (1s, exit 1). `sandbox delete --force` hangs >90s; orphaned `runsc delete` process. P4 needs timeout + cleanup. |
| **T10** | **PENDING** | Idle stability check at 30 min mark. |
| **T11** | **✅ PASS** | No `--tty` flag on `sandbox exec`. OQ-6 resolved: native TTY allocation not available. |

### Headline Findings

1. **§4.4a SIMPLIFIES DRAMATICALLY.** The inner `script` wrapper is unnecessary.
   The production path is simply:
   ```
   sandbox exec <id> --env TERM=xterm-256color -- tmux attach -t scion
   ```
   with a launcher-side PTY allocated via `pty.StartWithSize` (which Scion already
   does). No `script`, no double-PTY, no extra binary dependency.

2. **The mechanism:** `sandbox exec` passes through launcher-side fd characteristics
   via gVisor's host fd references (`host:[N]`). `isatty()` returns true because
   the underlying host fd is a PTY. `ttyname()` fails (the device path doesn't
   exist inside the sandbox), but tmux doesn't require it.

3. **SIGWINCH does not propagate.** PTY fd characteristics pass through; PTY signals
   do not. The out-of-band resize path (`tmux resize-window` via separate `sandbox
   exec`) remains required.

4. **Latency is excellent.** p95 = 37ms for interactive keystrokes is
   indistinguishable from local. The latency argument for revisiting the dead
   socket design is conclusively weak.

5. **`sandbox delete --force` is unreliable.** It hangs for >90s and orphans a
   `runsc delete` process. P4's `Delete` must be timeout-protected.

### Consequence for §4.4a

**Before Tier B:**
```
sandbox exec <id> -- /usr/bin/script -qfc 'tmux attach -t scion' /dev/null
```
Resize: `sandbox exec <id> -- tmux refresh-client -C <W>x<H>` (requires control client)

**After Tier B:**
```
sandbox exec <id> --env TERM=xterm-256color -- tmux attach -t scion
```
Resize: `sandbox exec <id> -- tmux resize-window -t scion -x <W> -y <H>`

Simpler, fewer dependencies, no `util-linux` requirement for attach (T4 drops
from critical to irrelevant), and a proven resize mechanism.

---

### T10: Idle stability — exec attached, idle 30 min (FINAL RESULT)

**Predicate:** Exec attached, idle 30 min, still responsive.

**Setup:** Persistent exec attached via python `pty.openpty()` to `tier-b`
sandbox at 23:52 UTC. Sandbox was created at 23:32 UTC.

**Timeline:**
- 23:52 UTC: Exec attached (client-909: scion [80x24 xterm-256color])
- 23:55 UTC (3 min): wrapper alive, exec responsive 108ms, client attached
- 00:16 UTC (24 min): wrapper alive, exec responsive 116ms, client attached
- 00:24 UTC (32 min): **FINAL CHECK** — wrapper alive, exec responsive 166ms,
  client attached, keystrokes delivered and echoed

**Final check output:**
```
Wrapper PID: 6825
wrapper alive: YES
No early exit file — exec has survived 30+ min idle

exec latency: 166ms
has-session: 0
client-909: scion [80x24 xterm-256color] (attached,focused)
scion: 1 windows (created Tue Aug 25 23:32:44 2026) (attached)

# After send-keys 'echo T10-AFTER-30MIN-IDLE-SUCCESS':
root@sandbox-tier-b:~# echo T10-AFTER-30MIN-IDLE-SUCCESS
T10-AFTER-30MIN-IDLE-SUCCESS
```

Instance uptime at check: 52 minutes. Sandbox uptime: ~52 minutes.

**T10 VERDICT: ✅ PASS**

No idle timeout on the exec channel. After 32 minutes idle with a persistent
exec attached, the sandbox is fully responsive: exec calls return in 166ms,
tmux session is alive with client attached, keystrokes are delivered and echoed.

---

### T9 Controls — Isolating the `sandbox delete --force` hang

Per sn-impl-arch: T9 as written only tested delete with an exec attached. Three
controls isolate whether the exec is implicated.

**Platform versions:**
```
sandbox CLI: "Serverless sandboxing CLI" (no --version flag)
runsc: version google-958767651, spec 1.2.1
runsc binary: /usr/local/gcp/bin/runsc, 128 MB, Aug 4 2026
sandbox binary: /usr/local/gcp/bin/sandbox, 55 MB, Aug 4 2026
```

**Previous T9 orphans:** The orphaned `runsc delete --force` processes from earlier
T9 runs are now zombies (`<defunct>`), indicating their parent process was
eventually killed/waited. They were NOT running when C1 started. The deleted
sandboxes (t9-v4, t9-final) are properly gone — `sandbox exec` returns
"not running" for t9-v4 and "no control socket found" for t9-final.

#### C1: No exec attached — tmux running, no attach

**Hypothesis:** If the hang requires an attached exec, C1 should complete quickly.

```
sandbox run c1-test --detach --rootfs / --write --allow-egress \
  -- bash -c 'tmux new-session -d -s scion "bash"; sleep 3600'
# has-session: 0 (tmux alive, no client attached)

sandbox delete c1-test --force
```

**Result:**
```
delete exit: 124  (timeout after 120s)
delete time: 120027ms
output:
  Found network annotations for session c1-test, cleaning up netns
  E0000 ... Raising signal 15 with default behavior
orphan: 1 (runsc delete --force c1-test) — later became zombie
```

**C1 VERDICT: HANG — exec is NOT the cause.**

The delete hangs identically with no exec attached. The exec is a red herring.

#### C2: Idle sandbox, nothing running (bare `sleep`)

**Hypothesis:** If the hang requires any long-lived process (tmux, bash), C2
should complete quickly.

```
sandbox run c2-test --detach --rootfs / --write --allow-egress \
  -- sleep 3600
# exec echo: alive

sandbox delete c2-test --force
```

**Result:**
```
delete exit: 124  (timeout after 120s)
delete time: 120028ms
output:
  Found network annotations for session c2-test, cleaning up netns
  E0000 ... Raising signal 15 with default behavior
orphan: 1 (runsc delete --force c2-test) — later became zombie
```

**C2 VERDICT: HANG — the process inside is NOT the cause.**

Even the simplest possible sandbox (`sleep 3600`) triggers the identical hang on
`sandbox delete --force`. The bug is in `sandbox delete --force` itself, not in
what's running inside the sandbox.

#### C3: Plain `sandbox delete` (no `--force`)

**Hypothesis:** Does the non-force path have the same bug, or is it `--force`-specific?

```
sandbox run c3-test --detach --rootfs / --write --allow-egress \
  -- bash -c 'tmux new-session -d -s scion "bash"; sleep 3600'

sandbox delete c3-test   # no --force
```

**Result:**
```
delete exit: 1
delete time: 209ms
output:
  Found network annotations for session c3-test, cleaning up netns
  cannot delete container that is not stopped without --force flag
  Error: cmd.Wait(delete) failed: exit status 128

# After delete:
sandbox exec c3-test: "sandbox c3-test is not running" (exit 1)
```

**C3 VERDICT: REFUSES — but with a side effect.**

`sandbox delete` without `--force` **refuses** as expected ("cannot delete
container that is not stopped without --force flag"), but it appears to **kill
the sandbox anyway**. After the refusal, `sandbox exec c3-test` reports "not
running". This is a confusing but potentially useful behaviour: the error
message says the delete was rejected, but the sandbox is dead.

**However:** the runsc-gofer and runsc-sandbox processes for c3-test are still
present (not zombies, not killed). So the sandbox is in an inconsistent state:
the `sandbox` CLI reports it as "not running", but the underlying gVisor
processes are alive.

#### C3 orphans detail

```
root  7614  runsc-gofer ... c3-test    (still running)
root  7618  runsc-sandbox ... c3-test  (still running, 19.3% CPU)
```

These are not `delete` orphans — they are the original sandbox runtime processes
that were **never properly cleaned up**.

#### Orphan runsc argv (from C1/C2 delete orphans)

```
/usr/local/gcp/bin/runsc --platform=xemu --platform_device_path=/dev/xemu
  --root=/tmp/runsc-root --ignore-cgroups --TESTONLY-unsafe-nonroot
  --overlay2=root:memory --network=none delete --force <sandbox-name>
```

All delete orphans share identical flags. The `--network=none` is notable — this
is the network mode of the **delete** operation, not the sandbox that was deleted
(sandboxes were created with `--allow-egress` which maps to `--network=host`).

#### Summary of T9 Controls

| Control | What runs inside | `--force`? | Result | Time | Orphan? |
|---------|-----------------|------------|--------|------|---------|
| **T9** (original) | tmux + exec attached | yes | **HANG** | >90s | yes (runsc delete) |
| **C1** | tmux, no exec | yes | **HANG** | >120s | yes (runsc delete) |
| **C2** | bare `sleep` | yes | **HANG** | >120s | yes (runsc delete) |
| **C3** | tmux | no | **REFUSE** (209ms) | 209ms | yes (sandbox runtime processes not cleaned up) |

**Root cause:** The hang is in `sandbox delete --force` itself, not in what's
running inside the sandbox or whether an exec is attached. Every `--force`
delete hangs. The underlying `runsc delete --force` subprocess gets stuck and
becomes a zombie.

**The non-force path (C3)** returns quickly but refuses to delete a running
sandbox, then leaves the sandbox in an inconsistent state (CLI reports "not
running" but runtime processes are still alive).

**Implication for P4:** This is a platform bug, not a Scion design issue. P4's
`Delete` method must:
1. Run `sandbox delete --force` with a short timeout (e.g., 10s)
2. If it hangs, kill the `sandbox delete` process
3. Clean up orphaned `runsc` processes explicitly
4. Verify sandbox is actually gone via `sandbox exec` (which correctly reports
   "not running" even when the delete hangs)

---

## IAP Spike — is `Instance.iapEnabled` real, inert, or half-delivered? (OQ-15)

### Date: 2026-08-26
### Agent: `spike-iap`
### Instances: `iap-test-1`, `iap-test-2`, `us-east4`, deployed via REST API + PATCH
### Image: `us-docker.pkg.dev/cloudrun/container/hello` (baseline), `docker.io/library/python:3.11` (probe)
### Context

The Cloud Run v2 discovery document lists `iapEnabled: boolean` directly on
`GoogleCloudRunV2Instance`. The working assumption was that Cloud Run Instances
have no direct IAP support. This spike tests whether the field is **live**,
**inert**, or **declared-but-unenforced**.

### Ground rules observed

1. **Real Cloud Run Instance** — no local substitutes.
2. **Pass/fail predicates written before each test.**
3. **Failures characterized exactly** — status codes, response headers, body content.
4. **Raw output captured.**
5. **Per-test verdicts, never averaged.**

---

### Precondition Tests

#### I0: `validateOnly` with `iapEnabled: true`

**Predicate:** PASS if the API accepts `iapEnabled: true` and the returned resource echoes it back.

**Result: ✅ PASS**

HTTP 200. The response metadata echoes `"iapEnabled": true`. Both `iapEnabled`
and `invokerIamDisabled` are accepted and echoed. `launchStage` normalized from
`ALPHA` to `GA` (note: previous tests normalized to `BETA`; this changed to `GA`).

```
iapEnabled: True
invokerIamDisabled: True
launchStage: GA
ingress: INGRESS_TRAFFIC_ALL
```

**This is the same echo pattern as `sandboxLauncher`** — accepted and reflected in
the response resource. But as the brief warns, echo is weak evidence of enforcement.

---

#### I1: IAP brand / OAuth client

**Predicate:** Does the project have an IAP brand and OAuth client?

**Result: ✅ IAP BRAND EXISTS**

```
name: projects/721899303052/brands/721899303052
applicationTitle: ptone-experiments
supportEmail: ptone@serverlessdf.apollo-df.dev
```

`iap.googleapis.com` is **ENABLED** in the project. An IAP brand exists. The
infrastructure side is present — "not configured" is ruled out as an explanation
for any inertness.

---

#### I2: gcloud CLI flags for IAP

**Predicate:** Does `gcloud beta/alpha run instances deploy/create/update --help`
expose an `--iap` flag?

**Result: ⚠️ PARTIAL — flag referenced but not implemented**

- `--[no-]invoker-iam-check` **IS** a recognized flag on `beta/alpha` for
  `deploy`, `create`, and `update`.
- `--iap` / `--no-iap` / `--iap-enabled` are **NOT** recognized flags (all
  produce "unrecognized arguments" errors).
- **However**, the `--public` flag description references `--no-iap`:
  ```
  --public
      Make the service public by disabling invoker IAM checks and IAP.
      Equivalent to setting --no-invoker-iam-check and --no-iap.
  ```
  This describes a flag that does not exist in the CLI. The help text was written
  assuming `--[no-]iap` would land, but it has not (yet) on gcloud 582.0.0.

**Implication:** `iapEnabled` can only be set via the REST API (v2) or PATCH. The
gcloud CLI is one release behind the API for this field.

---

### Core Test Matrix

#### I3: Baseline — `iapEnabled=false`, `invokerIamDisabled=false`

**Predicate:** Unauthenticated GET rejected by invoker IAM (403). Container does
NOT log the request.

**Instance:** `iap-test-1` with `hello` image, no `iapEnabled`, no
`invokerIamDisabled`.

**Result: ✅ PASS**

```
HTTP/2 403
<h1>Error: Forbidden</h1>
<h2>Your client does not have permission to get URL <code>/</code> from this server.</h2>
```

Standard invoker IAM rejection. Confirms the baseline is functioning.

---

#### I4: Open baseline — `iapEnabled=false`, `invokerIamDisabled=true`

**Predicate:** Unauthenticated GET returns 200. Proves `invokerIamDisabled` is
honoured.

**Instance:** `iap-test-1` PATCHed to add `invokerIamDisabled: true`. Reconciled
successfully (CONDITION_SUCCEEDED).

**Result: ✅ PASS**

```
HTTP/2 200
<title>Congratulations | Cloud Run</title>
```

Unauthenticated request reached the container and got the hello page.
`invokerIamDisabled` is honoured on Instances.

---

#### I5: THE HEADLINE TEST — `iapEnabled=true`, `invokerIamDisabled=true`

**Predicate (written before running):**
- LIVE = redirect to Google sign-in (302 or HTML page), container does NOT log
- INERT = 200 from container (same as I4)
- HALF-DELIVERED = 200 from container, `iapEnabled` echoed in describe, enforces nothing

**Instance:** `iap-test-1` PATCHed to add `iapEnabled: true` (already had
`invokerIamDisabled: true`). Reconciled successfully.

**Result: IAP IS LIVE**

```
HTTP/2 302
set-cookie: GCP_IAP_XSRF_NONCE_9aUtEeMSKj8TMmzozUzcog=1; expires=...; path=/; Secure; HttpOnly
location: https://accounts.google.com/o/oauth2/v2/auth?client_id=721899303052-3aurml9he9hm8p04a3grl7e5tutj0k3t.apps.googleusercontent.com&response_type=code&scope=openid+email&redirect_uri=https://iap.googleapis.com/v1/oauth/clientIds/721899303052-3aurml9he9hm8p04a3grl7e5tutj0k3t.apps.googleusercontent.com:handleRedirect&...
x-goog-iap-generated-response: true
server: Google Frontend
```

Body: `Invalid IAP credentials: empty token`

**The unauthenticated request was intercepted at the edge by IAP and redirected
to Google's OAuth2 sign-in page.** The container was never reached. The
`x-goog-iap-generated-response: true` header explicitly marks this as an
IAP-generated response.

**IAP is LIVE on Cloud Run Instances. This is not inert. This is not
half-delivered. The edge enforces it.**

---

#### I6: Authenticated request — `iapEnabled=true`, `invokerIamDisabled=false`

**Predicate:** Does an invoker-authenticated request arrive carrying
`X-Goog-IAP-JWT-Assertion`?

**Three variants tested:**

| Variant | Audience | `--include-email` | `invokerIamDisabled` | Result |
|---------|----------|--------------------|----------------------|--------|
| A | IAP client ID | yes | false | **Passed IAP, rejected by invoker IAM** (401, no `x-goog-iap-generated-response`) |
| B | Instance URL | yes | false | **Rejected by IAP** (401, `x-goog-iap-generated-response: true`, "Invalid JWT audience") |
| C | IAP client ID | yes | true | **200 — reached container with `X-Goog-IAP-JWT-Assertion`** |

**IAP client ID** (from the redirect URL): `721899303052-3aurml9he9hm8p04a3grl7e5tutj0k3t.apps.googleusercontent.com`

**Key finding from variants A+B:** IAP and invoker IAM want **different audiences**.
A token audienced to the IAP client ID passes IAP but fails invoker IAM; a token
audienced to the Instance URL fails IAP. This means **`invokerIamDisabled: true` is
required when using IAP** — the two auth mechanisms cannot both be satisfied by a
single token.

---

#### I8: IAP JWT Assertion — decoded claims (I7 skipped; liveness confirmed)

**Predicate:** Compare the assertion's claims against the hub's `IAPAuthenticator`
expectations.

**Instance:** `iap-test-2` with probe container (`python:3.11` HTTP server echoing
all request headers as JSON), `iapEnabled: true`, `invokerIamDisabled: true`.

**Request headers received by the container:**

```json
{
  "x-goog-authenticated-user-id": "accounts.google.com:110532853671892060667",
  "x-goog-authenticated-user-email": "accounts.google.com:scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com",
  "x-goog-iap-jwt-assertion": "<JWT>",
  "x-serverless-authorization": "bearer <JWT>"
}
```

**Decoded `X-Goog-IAP-JWT-Assertion`:**

| Field | Value | Hub expectation | Match? |
|-------|-------|-----------------|--------|
| **`alg`** | `ES256` | ES256 only | ✅ |
| **`typ`** | `JWT` | — | ✅ |
| **`kid`** | `gO4i_Q` | Must match Google's JWKS | ✅ (standard Google kid) |
| **`iss`** | `https://cloud.google.com/iap` | Exactly `https://cloud.google.com/iap` | ✅ |
| **`aud`** | `/projects/721899303052/locations/us-east4/services/iap-test-2` | Mandatory, format TBD | ⚠️ See below |
| **`azp`** | `/projects/721899303052/locations/us-east4/services/iap-test-2` | — | — |
| **`email`** | `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com` | — | ✅ |
| **`sub`** | `accounts.google.com:110532853671892060667` | — | ✅ |
| **`exp`** | `1787707273` | Present, 30s skew | ✅ |
| **`iat`** | `1787706673` | — | ✅ |
| **`identity_source`** | `GOOGLE` | — | ✅ |

**⚠️ Audience format critical finding:** The audience is
`/projects/721899303052/locations/us-east4/services/iap-test-2`. Note **`services`**
— even though this is a Cloud Run **Instance**, IAP uses the `services` resource
path in the audience claim. This is the format the hub's `IAPAuthenticator` must
expect. It differs from the GCE/GKE IAP audience format
(`/projects/{number}/global/backendServices/{id}`).

**Additional header: `x-serverless-authorization`** — An RS256 JWT from
`service-721899303052@gcp-sa-iap.iam.gserviceaccount.com`. This is IAP's internal
service-to-service token; the hub does not need to validate it (invoker IAM would
use it, but we have `invokerIamDisabled: true`).

---

### Supplementary Observations

| Observation | Result |
|-------------|--------|
| `describe` echoes `iapEnabled` after real create? | **YES** — echoed in both `validateOnly` and after PATCH/reconcile |
| Can `iapEnabled` be toggled on existing Instance? | **YES** — via REST PATCH with `updateMask=iapEnabled`. Not gcloud-exposed yet. |
| Can `iapEnabled` be toggled OFF? | **YES** — PATCH with `iapEnabled: false` accepted (HTTP 200) |
| Does enabling IAP change `urls`? | **NO** — URL remains `https://{name}-{project_number}.{region}.run.app` |
| Does enabling IAP change `ingress`? | **NO** — remains `INGRESS_TRAFFIC_ALL` |
| Does enabling IAP change `terminalCondition`? | **NO** — same `Running` / `CONDITION_SUCCEEDED` |
| Reconciliation time for iapEnabled toggle | ~30-75 seconds |
| gcloud `--public` description mentions `--no-iap` | **YES** — but `--no-iap` flag does not exist in gcloud 582.0.0 |

---

### Summary Table

| Test | Config | Verdict | Key Finding |
|------|--------|---------|-------------|
| **I0** | `validateOnly` + `iapEnabled: true` | **✅ PASS** | API accepts and echoes back |
| **I1** | IAP brand check | **✅ EXISTS** | Brand, OAuth client, and `iap.googleapis.com` all present |
| **I2** | gcloud CLI `--iap` flag | **⚠️ PARTIAL** | Referenced in `--public` help but flag does not exist; REST API only |
| **I3** | `iapEnabled=false`, `invokerIamDisabled=false` | **✅ PASS** | 403 from invoker IAM (baseline) |
| **I4** | `iapEnabled=false`, `invokerIamDisabled=true` | **✅ PASS** | 200, container reached (proves `invokerIamDisabled` works) |
| **I5** | `iapEnabled=true`, `invokerIamDisabled=true` | **✅ IAP LIVE** | 302 to `accounts.google.com`, `x-goog-iap-generated-response: true` |
| **I6** | `iapEnabled=true`, `invokerIamDisabled=false` | **✅ IAP LIVE** | IAP+invoker need different audiences; `invokerIamDisabled=true` required with IAP |
| **I7** | — | **SKIPPED** | Liveness confirmed in I5; browser flow not needed |
| **I8** | JWT assertion decode | **✅ COMPATIBLE** | ES256, `iss: https://cloud.google.com/iap`, audience `/projects/.../locations/.../services/{name}` |

---

### Headline

**IAP on Cloud Run Instances is LIVE.** It is not inert. It is not half-delivered.
The field `iapEnabled: true` activates IAP enforcement at the Google Frontend edge.
Unauthenticated requests get 302 to Google sign-in. Authenticated requests (OIDC
token with IAP client ID as audience) pass through and arrive at the container with
`X-Goog-IAP-JWT-Assertion`.

### Design Consequences

1. **§4.9a's auth-proxy service can disappear.** IAP works directly on Instances.
   The hub's existing `IAPAuthenticator` works against the Instance with one
   configuration change: the expected audience must be set to
   `/projects/{number}/locations/{region}/services/{name}`.

2. **`invokerIamDisabled: true` IS required when using IAP on Instances**
   (empirically confirmed 2026-08-26T01:48Z). IAP's `x-serverless-authorization`
   token uses a `services`-path audience (`/projects/{n}/locations/{r}/services/{name}`)
   that the Instance invoker check cannot verify. Granting `roles/run.invoker` to
   the IAP SA is irrelevant — the token itself fails audience verification. This
   differs from the documented Services flow. **S2 implication:** the invoker check
   cannot provide defense-in-depth behind IAP on Instances; IAP at the edge is the
   sole perimeter.

3. **The audience format uses `services` even for Instances.** This is either a
   naming convention or a reflection of IAP's internal routing model. The hub must
   construct the audience as `/projects/{number}/locations/{region}/services/{name}`
   regardless of whether the backend is a service or instance.

4. **The `--iap` gcloud flag is not yet exposed** but is referenced in `--public`
   help text. Operators can set `iapEnabled` via REST API today. The gcloud flag is
   likely one release away.

5. **I5 falsifies the "half-delivered" hypothesis.** This was the outcome the spike
   was designed to detect. Setting `iapEnabled: true` + `invokerIamDisabled: true`
   does NOT produce an open endpoint. The edge enforces IAP. There is no security
   footgun here.

---

## IAP Demo Instance — Live Demonstrator (OQ-17 Test)

### Date: 2026-08-26
### Agent: `iap-demo`
### Instance: `iap-demo`, `us-east4`, deployed via REST API
### Image: `docker.io/library/python:3.11` with PyJWT+cryptography for ES256 verification
### Context

Built on the spike-iap findings. This is a **live demo, not a spike** — the instance
is deliberately left running for ptone to browse.

### URL

**https://iap-demo-721899303052.us-east4.run.app**

### Configuration

| Setting | Value | Purpose |
|---------|-------|---------|
| `iapEnabled` | `true` | Edge-enforced IAP |
| `invokerIamDisabled` | `false` (default) | **Invoker check ON — OQ-17 test** |
| IAP SA (`service-721899303052@gcp-sa-iap.iam.gserviceaccount.com`) | `roles/run.invoker` on instance | Allows IAP to satisfy invoker check |
| IAP access policy | **BLOCKED** — gym SA lacks `roles/iap.admin` | ptone needs to run `gcloud iap web add-iam-policy-binding` |

### OQ-17 Status: EMPIRICALLY CONFIRMED — `invokerIamDisabled: true` REQUIRED

**Tested 2026-08-26T01:48Z on `outage-test-e4`** with all documented setup:
- `iapEnabled: true`, `invokerIamDisabled` absent (default false)
- IAP SA granted `roles/run.invoker` at both resource and project level

**Result: 401** — `www-authenticate: Bearer error="invalid_token"`, no
`x-goog-iap-generated-response` (meaning IAP passed, invoker check failed).

**Root cause:** IAP's `x-serverless-authorization` token has audience
`/projects/{n}/locations/{r}/services/{name}` but the Instance invoker check
expects the Instance URL or `instances` path. The audience mismatch means the
token cannot be verified regardless of IAM grants.

**Conclusion:** `invokerIamDisabled: true` is required for IAP on Instances.
This is an IAP-side integration gap, not a configuration error.

### App Features

- **ES256 signature verification** against Google's IAP JWKS (`pyjwt` + `cryptography`)
- Renders: email, user ID (with `accounts.google.com:` prefix stripped), decoded JWT
  header + payload with human-readable timestamps, all IAP-related headers
- **Does not print the raw JWT** — shows truncated token (first/last 12 chars)
- Handles missing assertion gracefully
- Container installs dependencies at startup (`pip install pyjwt cryptography`)

### Instance Create — 503 Outage Data

| Field | Value |
|-------|-------|
| **First attempt timestamp** | 2026-08-26T01:32:13Z |
| **Method** | REST API POST |
| **Region** | us-east4 |
| **HTTP status** | 200 (SUCCESS) |
| **Response time** | 0.649s (immediate, not delayed) |
| **503 observed?** | **NO** — the outage had cleared by 01:32Z |
| **Instance running** | 01:32:42Z (25.19s after create) |

Only one create attempt was made; it succeeded immediately. No 503s were observed.
The outage reported since 00:54 UTC appears to have recovered by 01:32Z at latest.

### IAP Per-Resource IAM: Does Not Exist for Instances (§10b.1)

**Platform finding.** IAP enforces on Instances (edge-level 302 redirect), but the
IAP IAM resource at the per-service path is never created for Instances.

| Resource | `getIamPolicy` result |
|---|---|
| Instance `iap-demo` (`cloud_run-us-east4/services/iap-demo`) | **404 NOT_FOUND** |
| Service `scion-hub` (`cloud_run-us-central1/services/scion-hub`) | **200** — policy + etag |
| Service `scion-discord` (`cloud_run-us-central1/services/scion-discord`) | **200** — empty policy + etag |
| Region `cloud_run-us-east4` (no service) | **200** — empty policy + etag |

Per-resource access policies cannot be set on Instances (`setIamPolicy` also 404s).
Access must be granted at the **region level** (`cloud_run-{region}`) or
**project level**. This is the half-delivered outcome, one layer up from where
spike-iap expected: enforcement works, the IAM surface doesn't know Instances exist.

**Workaround applied:** Policy set at `cloud_run-us-east4` (region level):
- `domain:google.com` → `roles/iap.httpsResourceAccessor`
- `user:ptone@google.com` → `roles/iap.httpsResourceAccessor`
- Etag: `BwZZ6W5K2bk=`

**Tiered error masking (§10b.1 operator trap):**
- Without `iap.admin` → 403 PERMISSION_DENIED (hides whether resource exists)
- With `iap.admin` → 404 NOT_FOUND (resource genuinely absent)
- `testIamPermissions` → succeeds (evaluates inherited project-level bindings, not per-resource policy)

This makes the problem appear to be permissions when it is actually a missing resource.
The gcloud `--resource-type=cloud-run` path construction is correct; the resource
path `iap_web/cloud_run-{region}/services/{name}` is IAP's convention (uses `services`
even for Instances). Zero Cloud Run Services named `iap-demo` exist in the project.

### IAP Access Policy Resolution

`gcloud iap web add-iam-policy-binding --resource-type=cloud-run` exists and the
resource path is `projects/{number}/iap_web/cloud_run-{region}/services/{name}`.
Note the format: `cloud_run` (underscore) then `-{region}` (hyphen).

The gym SA required `roles/iap.admin` (granted by ptone as project owner) to
discover the 404. `roles/editor` includes `iap.web.updateSettings` but NOT
`iap.web.setIamPolicy` or `iap.web.getIamPolicy`.

### OQ-17 Answer: NO — IAP and Instance Invoker Check Cannot Coexist

**Measured 2026-08-26.** spike-iap's controlled experiment, varying only the token audience:

| Audience | HTTP | Notes |
|---|---|---|
| `/projects/…/locations/us-east4/services/{name}` | **401** | This is what IAP emits |
| Instance URL (`https://iap-demo-….run.app`) | **200** | |
| `/projects/…/locations/us-east4/instances/{name}` | **200** | |

**Root cause:** IAP's token-minting code uses `/services/{name}` for its
`x-serverless-authorization` audience claim regardless of whether the backend is a
Service or an Instance. The Instance invoker check correctly distinguishes resource
types and rejects the `services` path audience.

**Permission is NOT the issue.** `run.instances.invoke` exists as a testable
permission on Instances (BETA stage), `roles/run.invoker` contains it, and the IAP
SA had the binding. The rejection is on audience, not permission.

**Negative finding:** `run.routes.invoke` (the permission Services use for invoker
check) is explicitly rejected as invalid on Instances:
`Permission run.routes.invoke is not valid for this resource`.

**Security implication (S2):** `invokerIamDisabled: true` is mandatory on all
IAP-enabled Instances today. IAP remains the perimeter (302 redirect with
`x-goog-iap-generated-response: true` confirmed). The security posture is IAP-only,
not IAP+invoker. Neither component is individually broken — they disagree on the
audience convention.

**Final instance configuration:**
```
iapEnabled: true
invokerIamDisabled: true   ← PATCH applied 02:01:43Z, reconciled by 02:03:29Z
```

**Instance-testable permissions (for the record):**
```
run.instances.delete        BETA
run.instances.get           BETA
run.instances.getIamPolicy  BETA
run.instances.invoke        BETA
run.instances.setIamPolicy  BETA
run.instances.sshRoot       BETA
run.instances.start         BETA
run.instances.stop          BETA
run.instances.update        BETA
```

### Teardown

**DO NOT DELETE.** This instance is a live demo. When ptone is done:
```
gcloud beta run instances delete iap-demo --region=us-east4 --project=ptone-experiments
```

---

## OQ-2: Sandbox → Launcher Connectivity (spike-oq2)

**Date**: 2026-08-26 04:09–04:14 UTC  
**Instance**: `spike-oq2-box` (us-east4, python:3.11, 4Gi/2CPU, sandboxLauncher=true)  
**Agent**: spike-oq2

### Summary

**The sandbox does NOT share the launcher's network namespace.** It gets its own
netns with a private `172.20.0.0/x` subnet (with `--allow-egress`) or loopback-only
(without). However, the launcher's link-local addresses (`169.254.x.x`) are
**routable from the egress-enabled sandbox**, meaning sandbox→launcher communication
works via the launcher's container IPs — no hairpin or IAP needed.

**P7 (transport-token refresh) does not need to exist.** The sandbox can reach the
launcher locally at sub-2ms latency via link-local IPs when `--allow-egress` is set.

### Launcher Network Environment

```
Outbound IP:     169.254.8.1
Local IPs:       127.0.0.1, 169.254.8.1, 169.254.9.1, 169.254.169.1
Listening ports: 8080 (OPEN), 9999 (test, OPEN), 2200 (CLOSED), 22 (CLOSED)

Routes:
  ipvlan-eth0  dest=0.0.0.0     gw=169.254.1.254  mask=0.0.0.0       (default)
  ipvlan-eth0  dest=169.254.0.0 gw=169.254.1.254  mask=255.255.0.0
  ipvlan-eth0  dest=169.254.1.0 gw=0.0.0.0        mask=255.255.255.0
  ipvlan-eth1  dest=169.254.9.0 gw=0.0.0.0        mask=255.255.255.0
  ipvlan-eth1  dest=169.254.169.0 gw=169.254.169.126 mask=255.255.255.128
```

### Sandbox Network Environment

**With `--allow-egress`:**
```
Outbound IP: 172.20.0.33
Routes:
  eth0  dest=0.0.0.0      gw=172.20.0.1    (default)
  eth0  dest=34.143.77.2   gw=172.20.0.1    (specific host route — likely NAT gateway)
  eth0  dest=172.20.0.1    gw=0.0.0.0       (gateway direct)
  eth0  dest=172.20.0.33   gw=0.0.0.0       (self)
Loopback ports 8080, 9999: CLOSED (separate netns confirmed)
/proc/net/fib_trie: ENOENT (gVisor does not expose fib_trie)
```

**Without `--allow-egress`:**
```
Only loopback interface present. No eth0, no routes.
/proc/net/route: header only (no routes)
/proc/net/dev: lo only
Network completely isolated (ENETUNREACH on all non-loopback addresses)
```

### Direction-by-Direction Results

#### sandbox → launcher (the OQ-2 question)

| # | Path | `--allow-egress` | Result | Mechanism |
|---|------|:-:|--------|-----------|
| 1 | `127.0.0.1:9999` (loopback) | ✅ | **FAIL** — `Connection refused` | Sandbox has its own netns; loopback is private. Nothing listens on sandbox's 127.0.0.1:9999. |
| 1 | `127.0.0.1:9999` (loopback) | ❌ | **FAIL** — `Connection refused` | Same; own loopback, nothing listening. |
| 2 | `169.254.8.1:9999` (launcher IP) | ✅ | **SUCCESS** — HTTP 200, body `OQ2-LAUNCHER-OK` | Sandbox eth0 has default route to 172.20.0.1 which routes 169.254.x.x to the launcher's ipvlan interfaces. |
| 2 | `169.254.8.1:9999` (launcher IP) | ❌ | **FAIL** — `Network is unreachable` | No network interface beyond loopback; no route to anywhere. |
| 3a | `169.254.8.1:9999` | ✅ | **SUCCESS** | Same as PATH 2 — this IS the launcher's primary IP. |
| 3b | `169.254.9.1:9999` | ✅ | **SUCCESS** | Launcher's ipvlan-eth1 address; also routable from sandbox. |
| 3c | `169.254.169.1:9999` | ✅ | **SUCCESS** | Launcher's third local IP; also routable. |
| 3d | `169.254.1.1:9999` | ✅ | **FAIL** — timeout | Not a launcher IP; 169.254.1.x is the gateway subnet. |
| 3e | Metadata server (169.254.169.254) | ✅ | **FAIL** — timeout | Metadata server is NOT reachable from sandbox even with egress. |
| 3 | All 169.254.x.x | ❌ | **FAIL** — `Network is unreachable` | No network at all. |
| 4 | Unix socket (`/host-tmp/oq2-hub.sock`) | ✅ | **FAIL** — socket visible but `Connection refused` | Socket file is visible via bind mount, but AF_UNIX connections across sandbox boundary are refused by gVisor. Consistent with spike-uds §4.4. |
| 4 | Unix socket | ❌ | **FAIL** — socket visible but `Connection refused` | Same mechanism failure regardless of egress flag. |
| 5 | Public `run.app` URL (hairpin) | ✅ | **REACHABLE** — HTTP 403 (Forbidden) | Sandbox can reach the public internet (DNS resolves, TCP connects, TLS handshakes), but gets 403 because invoker IAM is enabled and the request carries no OIDC token. |
| 5 | Public `run.app` URL (hairpin) | ❌ | **FAIL** — `Temporary failure in name resolution` | No network; can't even resolve DNS. |

#### launcher → sandbox

| Mechanism | Result | Notes |
|-----------|--------|-------|
| `sandbox exec <name> -- <cmd>` | **WORKS** | The launcher can execute arbitrary commands inside any sandbox it created. This is a control-plane channel (exec, not network). Confirmed by running all tests above via sandbox exec. |
| `sandbox run ... --detach` | **WORKS** | Launcher creates and manages sandbox lifecycle. |
| `sandbox delete` | **WORKS** | Launcher can terminate sandboxes. |

### Latency Measurements (100 sequential requests, sandbox → launcher)

Measured from egress-enabled sandbox to launcher HTTP server on port 9999.

| Target | Median | P95 | Min | Max |
|--------|--------|-----|-----|-----|
| `169.254.8.1:9999` (launcher primary IP) | **1.64 ms** | 6.47 ms | 0.71 ms | 13.42 ms |
| `169.254.9.1:9999` (launcher eth1 IP) | **1.56 ms** | 4.18 ms | 0.65 ms | 7.88 ms |
| `169.254.169.1:9999` (launcher third IP) | **1.39 ms** | 6.23 ms | 0.62 ms | 39.03 ms |
| Public `run.app` URL (hairpin, 403) | **35.15 ms** | 56.35 ms | 27.21 ms | 62.85 ms |

### Key Findings

1. **The sandbox gets its own network namespace** — it is NOT in the launcher's
   netns. This means `127.0.0.1` inside the sandbox is the sandbox's own loopback,
   not the launcher's.

2. **With `--allow-egress`, the sandbox can reach the launcher via link-local IPs.**
   The sandbox gets an eth0 on a 172.20.0.0/x network with a default route through
   172.20.0.1. That gateway routes traffic to the launcher's 169.254.x.x addresses.
   All three launcher IPs (169.254.8.1, 169.254.9.1, 169.254.169.1) are reachable.

3. **Without `--allow-egress`, the sandbox has no network at all** — only a loopback
   interface. All paths fail with ENETUNREACH. This is "network none" mode.

4. **AF_UNIX sockets do NOT cross the sandbox boundary** even with bind mounts.
   The socket file is visible, but `connect()` returns ECONNREFUSED. Consistent
   with spike-uds findings (gVisor blocks AF_UNIX across container boundaries).

5. **Hairpin works but is ~25x slower** (35ms median vs 1.4ms) and requires auth.
   The sandbox can reach the public URL, but gets 403 because invoker IAM is
   enabled. To use hairpin, agents would need OIDC tokens — the full P7 subsystem.

6. **The local path bypasses IAM entirely.** Traffic from sandbox to launcher via
   169.254.x.x never leaves the Instance. No OIDC token, no IAP credential, no
   Cloud Run invoker check. This is the design-relevant finding.

### Design Implications

- **P7 does not need to exist.** Agents reach the hub at `http://169.254.8.1:<port>`
  with no authentication. The link-local path is 25x faster than the hairpin.
- **P6 transport-auth: not needed for the local path.** Since traffic never leaves
  the Instance, there is no perimeter to authenticate against. Agent ↔ hub
  communication is a private, unauthenticated, sub-2ms local channel.
- **`--allow-egress` is a hard requirement** if agents need to reach the hub this
  way. This also means agents get full internet access, which is the current
  default but noted as a design constraint.
- **`169.254.8.1` is the recommended target** — it is the launcher's primary
  outbound IP and consistently the first link-local address.

### Operability Note

Instance creates via `gcloud beta run instances deploy` and the v1/v2 REST APIs
in us-east4 were intermittently returning 503 ("The service is currently
unavailable") during this spike (approx 03:30–04:05 UTC on 2026-08-26). Both
`POST` (create) and `POST --async` were affected. `GET` (describe/list) worked
fine. sn-impl-arch confirmed hitting the same 503. Retry was the mitigation.
This is a real operability concern for a tier whose deployment model is "one
command."

### Addendum: Three design-critical observations (requested by sn-impl-arch)

#### 1. `--allow-egress` is all-or-nothing: no network-isolated agents that still function

There is **no configuration in which a sandbox can talk to its launcher but not to
the internet.** The `--allow-egress` flag is a binary switch:

- **ON**: sandbox gets an eth0 with a full default route. It can reach the
  launcher's link-local IPs AND the public internet (DNS, HTTPS, arbitrary TCP).
- **OFF**: sandbox has only a loopback interface. It cannot reach the launcher,
  cannot resolve DNS, cannot open any socket to any non-loopback address. Every
  non-local connection returns `ENETUNREACH`.

This is a genuine security property of the tier, not a footnote. If an agent needs
to communicate with the hub — and it does, by definition — it must have
`--allow-egress`, which also gives it unrestricted outbound network access.
**We cannot offer network-isolated agents that still function.** Any network policy
(egress filtering, allowlisting) would need to happen at a layer outside the
sandbox runtime, if such a layer exists.

#### 2. `169.254.8.1` is undocumented — can it be discovered at runtime?

**Yes, but by the launcher, not by the sandbox.** The sandbox cannot discover the
launcher's IP on its own — its route table shows only 172.20.0.x addresses and
gVisor does not expose `/proc/net/fib_trie`.

The **launcher** discovers its own link-local IP trivially:

```python
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.connect(("8.8.8.8", 80))
launcher_ip = s.getsockname()[0]  # Returns "169.254.8.1"
s.close()
```

The launcher then passes this to the sandbox at creation time via `--env`:

```bash
sandbox run agent-1 --detach --rootfs / --write --allow-egress \
  --env HUB_HOST=$LAUNCHER_IP --env HUB_PORT=8080 \
  -- <agent command>
```

The agent reads `HUB_HOST` from its environment. This is the correct pattern:
the launcher owns the address, discovers it once at startup, and injects it into
every sandbox it creates. No hardcoded constant needed anywhere in the agent code.

**Residual risk**: the 169.254.x.x assignment is undocumented GCE/Cloud Run
behavior. If the address changes across Instance restarts or platform updates,
the launcher will discover the new address automatically (the `getsockname()` call
returns whatever the platform assigned). The agent never hardcodes it. The only
failure mode would be if the sandbox's gateway (172.20.0.1) stopped routing to the
launcher's link-local addresses entirely — that would be a platform-level breaking
change, not a configuration drift.

#### 3. Egress-off constrains OQ-14 (Vertex AI / ADC access)

The egress-off results effectively answer **OQ-14** in the negative direction.
Without `--allow-egress`:

- No network interfaces beyond loopback
- DNS resolution fails (`Temporary failure in name resolution`)
- All outbound connections return `ENETUNREACH`

This means a sandbox without egress **cannot reach Vertex AI, cannot use
Application Default Credentials, cannot call any Google API.** If OQ-14 asks
whether agents can use Vertex/ADC from within a sandbox, the answer depends
entirely on `--allow-egress`:

- **Egress ON**: agents CAN reach Vertex AI and use ADC (they have full internet
  access and the metadata server is on the launcher's network, though notably
  169.254.169.254 timed out from the sandbox — ADC via metadata may require a
  different path such as a mounted service account key or workload identity
  federation).
- **Egress OFF**: agents CANNOT reach anything. No Vertex, no ADC, no network.

**Note**: even with egress ON, the GCE metadata server (169.254.169.254) was NOT
reachable from the sandbox (timeout). This means ADC's default metadata-based
credential path may not work even with egress enabled. The launcher's link-local
IPs (169.254.8.1, 169.254.9.1, 169.254.169.1) were reachable, but 169.254.169.254
was not. This is a separate finding that may further constrain OQ-14: agents may
need explicit credentials (service account keys, workload identity tokens minted
by the launcher) rather than relying on the metadata server.

OQ-14 is currently open and unowned. This spike has constrained the answer space.

### Cleanup

Instance `spike-oq2-box` deleted at ~04:17 UTC 2026-08-26.

```bash
# (already executed)
gcloud beta run instances delete spike-oq2-box --region=us-east4 --project=ptone-experiments --impersonate-service-account=scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com
```
