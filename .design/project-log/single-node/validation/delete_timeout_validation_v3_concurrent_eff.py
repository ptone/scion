#!/usr/bin/env python3
"""
Supplemental Test: Concurrent Effectiveness-vs-Time Curve

Measures when each of 5 concurrently-deleted sandboxes becomes unreachable.
This fills the gap identified by the architect: Test 1 was serial, Test 4
measured timeout but not effectiveness. Fan-out is our actual teardown pattern.

Risk under test: if effectiveness degrades under contention (5 gVisor teardowns
competing), unreachability might move from <1s to >10s, making
DefaultDeleteTimeout=10s unsafe.

NOTE: reachability is judged by `sandbox exec`. Per defect-sandbox-delete-hang.md
section 4, the CLI can disown a sandbox whose processes are still running -- so
"unreachable via exec" means the sandbox control plane has torn down, even if
low-level processes persist briefly. Test 3's /proc scan corroborates this:
the runsc delete process survives while the sandbox itself is unreachable.
"""

import subprocess
import time
import os
import sys
import threading
import http.server

# ===================================================================
# Health check server (startup probe) -- skip if already bound
# ===================================================================
class HealthHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")
    def log_message(self, *args):
        pass

try:
    health_server = http.server.HTTPServer(("", 8080), HealthHandler)
    threading.Thread(target=health_server.serve_forever, daemon=True).start()
except OSError:
    pass  # port already bound by bootstrap

SANDBOX_BIN = "/usr/local/gcp/bin/sandbox"
RUNSC_BIN = "/usr/local/gcp/bin/runsc"

def log(msg):
    ts = time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime())
    print(f"[{ts}] {msg}", flush=True)

def run(cmd, timeout=None):
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        return r.returncode, r.stdout.strip(), r.stderr.strip()
    except subprocess.TimeoutExpired:
        return -1, "", "TIMEOUT"

def sandbox_run(name, cmd_args):
    rc, out, err = run([SANDBOX_BIN, "run", name, "--detach", "--rootfs", "/",
                        "--write", "--"] + cmd_args)
    return rc == 0

def sandbox_exec_probe(name):
    rc, out, err = run([SANDBOX_BIN, "exec", name, "--", "/bin/echo", "alive"], timeout=5)
    return rc == 0 and "alive" in out

def scan_proc_for_sandbox(name):
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
log("=== SUPPLEMENTAL: CONCURRENT EFFECTIVENESS-VS-TIME CURVE ===")
log(f"Python: {sys.version}")

# Check sandbox binary
if not os.path.isfile(SANDBOX_BIN):
    log(f"FATAL: sandbox binary not found at {SANDBOX_BIN}")
    while True:
        time.sleep(3600)

# runsc version
rc, out, err = run([RUNSC_BIN, "--version"])
if rc == 0:
    log(f"runsc version: {out}")

log("")
log("Question: Under fan-out (5 concurrent delete --force), at what time does each")
log("sandbox become unreachable? Does contention degrade effectiveness beyond 10s?")
log("")
log("Predicate: All 5 sandboxes unreachable at t<=10s under concurrent delete.")
log("")
log("Instrument: sandbox exec -- /bin/echo alive (same as Test 1).")
log("Per defect-sandbox-delete-hang.md section 4, sandbox exec disowns a sandbox")
log("whose underlying processes are still running. Test 3 corroborates: the runsc")
log("delete process survives while the sandbox is unreachable via exec.")
log("")

NUM_CONCURRENT = 5
probe_times = [1, 2, 5, 10, 15, 20, 30]

# ===================================================================
# Phase 1: Create 5 sandboxes
# ===================================================================
log(f"Creating {NUM_CONCURRENT} sandboxes...")
sids = []
for i in range(NUM_CONCURRENT):
    sid = f"val-ceff-{i}-{int(time.time())}"
    sids.append(sid)
    ok = sandbox_run(sid, ["/usr/bin/sleep", "3600"]) or sandbox_run(sid, ["/bin/sleep", "3600"])
    log(f"  {sid}: {'created' if ok else 'FAILED TO CREATE'}")
    if not ok:
        log("FATAL: could not create sandbox")
        while True:
            time.sleep(3600)

# Verify all reachable
log("Verifying all sandboxes reachable...")
time.sleep(2)
for sid in sids:
    if not sandbox_exec_probe(sid):
        log(f"  WARN: {sid} not reachable, waiting 3s...")
        time.sleep(3)
        if not sandbox_exec_probe(sid):
            log(f"  FATAL: {sid} never became reachable")
            while True:
                time.sleep(3600)
    log(f"  {sid}: reachable")

log("")

# ===================================================================
# Phase 2: Issue 5 concurrent delete --force
# ===================================================================
log(f"Issuing {NUM_CONCURRENT} concurrent delete --force (backgrounded)...")
delete_procs = []
for sid in sids:
    p = subprocess.Popen(
        [SANDBOX_BIN, "delete", "--force", sid],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE
    )
    delete_procs.append(p)

delete_start = time.monotonic()
log(f"All {NUM_CONCURRENT} deletes issued at t=0.")
log("")

# ===================================================================
# Phase 3: Poll reachability at each probe time
# ===================================================================
# Track per-sandbox: first time it was unreachable, last time it was reachable
unreachable_at = {}  # sid -> probe time when first unreachable
last_reachable_at = {}  # sid -> last probe time when still reachable

for sid in sids:
    unreachable_at[sid] = None
    last_reachable_at[sid] = 0

for t_target in probe_times:
    # Wait until the target time
    elapsed = time.monotonic() - delete_start
    wait = t_target - elapsed
    if wait > 0:
        time.sleep(wait)

    actual_elapsed = time.monotonic() - delete_start
    log(f"--- t={t_target}s (actual {actual_elapsed:.1f}s) ---")

    # Check all sandboxes that haven't been confirmed unreachable yet
    all_done = True
    for sid in sids:
        if unreachable_at[sid] is not None:
            log(f"  {sid}: (already unreachable at t={unreachable_at[sid]}s)")
            continue

        reachable = sandbox_exec_probe(sid)
        if reachable:
            last_reachable_at[sid] = t_target
            log(f"  {sid}: STILL REACHABLE")
            all_done = False
        else:
            unreachable_at[sid] = t_target
            log(f"  {sid}: UNREACHABLE")

    if all_done:
        log(f"All {NUM_CONCURRENT} sandboxes unreachable. Stopping probes.")
        break

log("")

# ===================================================================
# Phase 4: Kill backgrounded deletes
# ===================================================================
for p in delete_procs:
    try:
        p.kill()
        p.wait(timeout=5)
    except Exception:
        pass

# ===================================================================
# Phase 5: Post-delete /proc scan
# ===================================================================
log("Post-delete /proc scan (orphaned processes):")
time.sleep(2)
for sid in sids:
    orphans = scan_proc_for_sandbox(sid)
    orphans = [(p, c) for p, c in orphans if "python" not in c and "validation" not in c]
    if orphans:
        log(f"  {sid}: {len(orphans)} process(es) remaining")
        for pid, cmd in orphans:
            log(f"    PID {pid}: {cmd[:180]}")
    else:
        log(f"  {sid}: clean")

log("")

# ===================================================================
# Phase 6: Results summary
# ===================================================================
log("=== CONCURRENT EFFECTIVENESS RESULTS ===")
log("")

max_unreachable = 0
any_still_reachable = False

for i, sid in enumerate(sids):
    t_unreach = unreachable_at[sid]
    t_last_reach = last_reachable_at[sid]
    if t_unreach is not None:
        log(f"  sandbox {i} ({sid}): unreachable at t={t_unreach}s (last reachable at t={t_last_reach}s)")
        max_unreachable = max(max_unreachable, t_unreach)
    else:
        log(f"  sandbox {i} ({sid}): STILL REACHABLE at t={probe_times[-1]}s")
        any_still_reachable = True

log("")

if any_still_reachable:
    log(f"FAIL: At least one sandbox still reachable at t={probe_times[-1]}s under concurrent delete")
    log(f"  DefaultDeleteTimeout=10s is NOT safe for concurrent teardowns")
elif max_unreachable <= 10:
    log(f"PASS: All {NUM_CONCURRENT} sandboxes unreachable by t={max_unreachable}s under concurrent delete")
    log(f"  DefaultDeleteTimeout=10s is justified even under fan-out contention")
    margin = 10.0 / max_unreachable if max_unreachable > 0 else float('inf')
    log(f"  Safety margin: {margin:.0f}x (10s timeout / {max_unreachable}s worst-case)")
else:
    log(f"FAIL: Worst-case unreachability at t={max_unreachable}s exceeds DefaultDeleteTimeout=10s")
    log(f"  Recommend increasing DefaultDeleteTimeout to at least {max_unreachable + 5}s")

log("")
log("=== SUPPLEMENTAL TEST COMPLETE ===")
log("")
log("Keeping container alive. Check Cloud Logging for results.")
while True:
    time.sleep(3600)
