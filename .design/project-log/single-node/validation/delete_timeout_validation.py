#!/usr/bin/env python3
"""
Delete Timeout Validation — P4a on-Instance empirical test.

Runs as the container entrypoint on a Cloud Run Instance with sandboxLauncher.
Output goes to stdout (→ Cloud Logging). Use stdbuf -oL or flush=True.

Predicates are explicit PASS/FAIL per the architect's requirement.
"""

import subprocess
import time
import os
import sys
import concurrent.futures

SANDBOX_BIN = "/usr/local/gcp/bin/sandbox"
RUNSC_BIN = "/usr/local/gcp/bin/runsc"

def log(msg):
    ts = time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime())
    print(f"[{ts}] {msg}", flush=True)

def run(cmd, timeout=None):
    """Run a command, return (returncode, stdout, stderr)."""
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        return r.returncode, r.stdout.strip(), r.stderr.strip()
    except subprocess.TimeoutExpired:
        return -1, "", "TIMEOUT"

def sandbox_run(name, cmd_args):
    """Create a detached sandbox."""
    rc, out, err = run([SANDBOX_BIN, "run", name, "--detach", "--rootfs", "/",
                        "--write", "--"] + cmd_args)
    return rc == 0

def sandbox_exec_probe(name):
    """Returns True if the sandbox is still reachable."""
    rc, out, err = run([SANDBOX_BIN, "exec", name, "--", "/bin/echo", "alive"], timeout=5)
    return rc == 0 and "alive" in out

def sandbox_delete_force(name, timeout=None):
    """Issue delete --force. Returns (completed, elapsed_ms)."""
    start = time.monotonic()
    rc, out, err = run([SANDBOX_BIN, "delete", "--force", name], timeout=timeout)
    elapsed_ms = int((time.monotonic() - start) * 1000)
    completed = (rc != -1)  # -1 means our timeout expired
    return completed, elapsed_ms

def scan_proc_for_sandbox(name):
    """Scan /proc for processes related to a sandbox name. Returns list of (pid, cmdline)."""
    matches = []
    try:
        for entry in os.listdir("/proc"):
            if not entry.isdigit():
                continue
            try:
                with open(f"/proc/{entry}/cmdline", "rb") as f:
                    cmdline_bytes = f.read()
                cmdline = cmdline_bytes.replace(b"\x00", b" ").decode("utf-8", errors="replace").strip()
                if name in cmdline:
                    matches.append((int(entry), cmdline))
            except (FileNotFoundError, PermissionError):
                continue
    except FileNotFoundError:
        pass
    return matches


# ===================================================================
log("=== DELETE TIMEOUT VALIDATION START ===")
log(f"Python: {sys.version}")

# Check sandbox binary
if not os.path.isfile(SANDBOX_BIN):
    log(f"FATAL: sandbox binary not found at {SANDBOX_BIN}")
    sys.exit(1)
log(f"sandbox binary: {SANDBOX_BIN} (exists)")

# runsc version
rc, out, err = run([RUNSC_BIN, "--version"])
if rc == 0:
    log(f"runsc version: {out}")
else:
    log(f"runsc --version failed (rc={rc}): {err}")
    # Try to get version from the binary itself
    rc2, out2, err2 = run([RUNSC_BIN, "do", "--", "/bin/true"])
    log(f"runsc do test: rc={rc2}, err={err2[:200] if err2 else ''}")

log("")

# ===================================================================
# TEST 1: Effectiveness-vs-time curve
# ===================================================================
log("=== TEST 1: Effectiveness-vs-time curve ===")
log("Question: At what time after issuing delete --force does the sandbox become unreachable?")
log("Predicate: If sandbox is unreachable at t<=10s, DefaultDeleteTimeout=10s is justified.")
log("")

sid1 = f"val-eff-{int(time.time())}"
log(f"Creating sandbox {sid1} with /usr/bin/sleep 3600...")
if not sandbox_run(sid1, ["/usr/bin/sleep", "3600"]):
    # Try /bin/sleep as fallback
    if not sandbox_run(sid1, ["/bin/sleep", "3600"]):
        log(f"FATAL: Could not create sandbox {sid1}")
        sys.exit(1)
log(f"Sandbox {sid1} created.")

# Verify it's reachable before we start
if not sandbox_exec_probe(sid1):
    log(f"WARN: Sandbox {sid1} not immediately reachable via exec, waiting 2s...")
    time.sleep(2)
    if not sandbox_exec_probe(sid1):
        log(f"FATAL: Sandbox {sid1} never became reachable")
        sys.exit(1)
log(f"Sandbox {sid1} confirmed reachable.")

# Issue delete --force in background
log(f"Issuing delete --force on {sid1} (backgrounded with 120s timeout)...")
delete_proc = subprocess.Popen(
    [SANDBOX_BIN, "delete", "--force", sid1],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
delete_start = time.monotonic()

# Poll at increasing intervals
probe_times = [1, 2, 3, 5, 7, 10, 15, 20, 30, 60]
unreachable_at = None
last_reachable_at = 0

for t_target in probe_times:
    # Wait until the target time
    elapsed = time.monotonic() - delete_start
    wait = t_target - elapsed
    if wait > 0:
        time.sleep(wait)

    actual_elapsed = time.monotonic() - delete_start
    reachable = sandbox_exec_probe(sid1)

    if reachable:
        last_reachable_at = t_target
        log(f"  t={t_target:>2}s (actual {actual_elapsed:.1f}s): SANDBOX STILL REACHABLE")
    else:
        unreachable_at = t_target
        log(f"  t={t_target:>2}s (actual {actual_elapsed:.1f}s): SANDBOX UNREACHABLE")
        break

# Kill the backgrounded delete
try:
    delete_proc.kill()
    delete_proc.wait(timeout=5)
except:
    pass

log("")
if unreachable_at is not None:
    log(f"RESULT: Sandbox became unreachable between t={last_reachable_at}s and t={unreachable_at}s")
    if unreachable_at <= 10:
        log(f"PASS: DefaultDeleteTimeout=10s is justified (unreachable at <={unreachable_at}s)")
    else:
        log(f"FAIL: DefaultDeleteTimeout=10s is TOO AGGRESSIVE (sandbox still live at 10s)")
        log(f"  Recommend increasing to at least {unreachable_at + 5}s")
else:
    log(f"FAIL: Sandbox still reachable at t={probe_times[-1]}s — delete may not be effective")

log("")

# ===================================================================
# TEST 2: Reaper live test
# ===================================================================
log("=== TEST 2: Reaper live test ===")
log("Question: Can we find and identify a real orphaned runsc delete process?")
log("Predicate: After issuing delete --force and letting it hang, /proc scan finds a runsc delete process.")
log("")

sid2 = f"val-reap-{int(time.time())}"
log(f"Creating sandbox {sid2}...")
if not sandbox_run(sid2, ["/usr/bin/sleep", "3600"]):
    sandbox_run(sid2, ["/bin/sleep", "3600"])
time.sleep(2)  # let it stabilize

log(f"Issuing delete --force on {sid2} (backgrounded)...")
delete_proc2 = subprocess.Popen(
    [SANDBOX_BIN, "delete", "--force", sid2],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)

log("Waiting 15s for delete to enter hanging state...")
time.sleep(15)

# Scan /proc
log("Scanning /proc for orphaned runsc processes...")
all_procs = scan_proc_for_sandbox(sid2)
runsc_delete_procs = [
    (pid, cmd) for pid, cmd in all_procs
    if "runsc" in cmd and "delete" in cmd
]

if runsc_delete_procs:
    log(f"PASS: Found {len(runsc_delete_procs)} orphaned runsc delete process(es):")
    for pid, cmd in runsc_delete_procs:
        log(f"  PID {pid}: {cmd[:200]}")
else:
    log(f"INFO: No orphaned runsc delete processes found for {sid2}")
    if all_procs:
        log(f"  Other processes referencing {sid2}:")
        for pid, cmd in all_procs:
            log(f"  PID {pid}: {cmd[:200]}")
    else:
        log(f"  No processes at all reference {sid2}")

# Kill the backgrounded delete
try:
    delete_proc2.kill()
    delete_proc2.wait(timeout=5)
except:
    pass

log("")

# ===================================================================
# TEST 3: Post-reap state
# ===================================================================
log("=== TEST 3: Post-reap state ===")
log("Question: After killing the delete process, are there surviving runsc-gofer/runsc-sandbox processes?")
log("Predicate: No surviving processes for the sandbox ID after reaping.")
log("")

log(f"Checking for surviving processes for {sid2}...")
time.sleep(2)  # brief pause after killing delete

surviving = scan_proc_for_sandbox(sid2)
# Filter out our own python process
surviving = [(pid, cmd) for pid, cmd in surviving if "python" not in cmd and "validation" not in cmd]

if not surviving:
    log(f"PASS: No surviving processes for {sid2}")
else:
    gofer = [(p, c) for p, c in surviving if "gofer" in c]
    sandbox_procs = [(p, c) for p, c in surviving if "sandbox" in c and "gofer" not in c]
    runsc = [(p, c) for p, c in surviving if "runsc" in c]

    if gofer or sandbox_procs:
        log(f"FAIL: Surviving processes found (cf. defect-sandbox-delete-hang.md section 4):")
    else:
        log(f"INFO: Processes found but may be benign:")

    for pid, cmd in surviving:
        log(f"  PID {pid}: {cmd[:200]}")

log("")

# ===================================================================
# TEST 4: Concurrent deletes (OQ-16)
# ===================================================================
log("=== TEST 4: Concurrent deletes (OQ-16) ===")
log("Question: Does the timeout hold per-sandbox under contention with 5 concurrent deletes?")
log("Predicate: All deletes complete (or timeout) within 2x DefaultDeleteTimeout aggregate.")
log("")

NUM_CONCURRENT = 5
sids = []

log(f"Creating {NUM_CONCURRENT} sandboxes...")
for i in range(NUM_CONCURRENT):
    sid = f"val-conc-{i}-{int(time.time())}"
    sids.append(sid)
    ok = sandbox_run(sid, ["/usr/bin/sleep", "3600"]) or sandbox_run(sid, ["/bin/sleep", "3600"])
    log(f"  {sid}: {'created' if ok else 'FAILED TO CREATE'}")

time.sleep(3)  # let them stabilize

log(f"Issuing {NUM_CONCURRENT} concurrent delete --force commands...")
start_all = time.monotonic()

def delete_one(sid):
    """Delete a single sandbox with a 30s timeout."""
    start = time.monotonic()
    completed, elapsed_ms = sandbox_delete_force(sid, timeout=30)
    return sid, completed, elapsed_ms

with concurrent.futures.ThreadPoolExecutor(max_workers=NUM_CONCURRENT) as pool:
    futures = {pool.submit(delete_one, sid): sid for sid in sids}
    results = []
    for future in concurrent.futures.as_completed(futures):
        sid, completed, elapsed_ms = future.result()
        status = "completed" if completed else "timed out"
        log(f"  {sid}: {status} in {elapsed_ms}ms")
        results.append((sid, completed, elapsed_ms))

total_elapsed_ms = int((time.monotonic() - start_all) * 1000)
log(f"All {NUM_CONCURRENT} deletes finished in {total_elapsed_ms}ms aggregate")

completed_count = sum(1 for _, c, _ in results if c)
timed_out_count = sum(1 for _, c, _ in results if not c)
max_elapsed = max(e for _, _, e in results)

if timed_out_count == NUM_CONCURRENT:
    log(f"INFO: All {NUM_CONCURRENT} deletes timed out (expected with known-bad runsc)")
    if max_elapsed < 35000:  # 30s timeout + 5s margin
        log(f"PASS: Timeout bounded each delete (max {max_elapsed}ms)")
    else:
        log(f"FAIL: At least one delete exceeded timeout significantly (max {max_elapsed}ms)")
elif completed_count == NUM_CONCURRENT:
    log(f"INFO: All {NUM_CONCURRENT} deletes completed normally")
    log(f"WARN: This is unexpected on the known-bad runsc build — possible false positive")
else:
    log(f"INFO: Mixed results: {completed_count} completed, {timed_out_count} timed out")

# Check for orphaned processes
log("")
log("Checking for orphaned processes after concurrent delete:")
for sid in sids:
    orphans = scan_proc_for_sandbox(sid)
    orphans = [(p, c) for p, c in orphans if "python" not in c]
    if orphans:
        log(f"  {sid}: {len(orphans)} process(es) remaining")
        for pid, cmd in orphans:
            log(f"    PID {pid}: {cmd[:150]}")
    else:
        log(f"  {sid}: clean")

log("")

# ===================================================================
# TEST 5: sync.Once WARN verification
# ===================================================================
log("=== TEST 5: sync.Once WARN verification ===")
log("Question: Does delete --force hang (timeout) on this runsc build, confirming the known-bad behavior?")
log("Predicate: On known-bad runsc, delete --force should NOT return normally within 10s.")
log("")

sid5 = f"val-warn-{int(time.time())}"
log(f"Creating sandbox {sid5}...")
sandbox_run(sid5, ["/usr/bin/sleep", "3600"]) or sandbox_run(sid5, ["/bin/sleep", "3600"])
time.sleep(2)

log(f"Issuing delete --force with 15s timeout...")
completed, elapsed_ms = sandbox_delete_force(sid5, timeout=15)

if not completed:
    log(f"PASS: delete --force timed out at {elapsed_ms}ms (expected: known-bad build hangs)")
    log(f"  sync.Once WARN 'upstream defect may be fixed' should NOT fire on this build")
elif elapsed_ms > 10000:
    log(f"INFO: delete --force completed in {elapsed_ms}ms (>10s, close to timeout)")
    log(f"  This may trigger the sync.Once WARN — borderline behavior")
else:
    log(f"WARN: delete --force completed in {elapsed_ms}ms (<10s)")
    log(f"  This would trigger the sync.Once WARN 'upstream defect may be fixed'")
    log(f"  Investigate: is this runsc build actually fixed?")

log("")

# ===================================================================
# Summary
# ===================================================================
log("=== VALIDATION COMPLETE ===")
log("")
log("IMPORTANT: Do not tear down this Instance before the architect has reviewed these results.")
log("To re-run: redeploy with the same image and command.")

# Keep the container alive so the Instance persists
log("")
log("Keeping container alive (sleep loop). Check Cloud Logging for results.")
while True:
    time.sleep(3600)
