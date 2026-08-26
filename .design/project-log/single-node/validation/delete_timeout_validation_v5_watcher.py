#!/usr/bin/env python3
"""
Characterize: Does cancelling `sandbox wait` leak a runsc process?

This is the production path: watchSandbox runs `sandbox wait <name>` for every
sandbox's lifetime, and deleteOrWorkaround cancels it via context on every delete.
If `sandbox wait` internally shells out to `runsc state` (or any runsc subcommand),
cancelling it mid-flight will leak a permanent process -- same defect as
`sandbox exec` -> `runsc state`.

Two sub-questions:
1. Does `sandbox wait` create runsc processes during normal operation (while sandbox
   is just running)? Scan /proc while wait is running, before any delete.
2. When the `sandbox wait` wrapper is killed (simulating context cancel), what
   survives in /proc?

Discriminator: vary watcher presence.
- Test A: delete WITHOUT watcher -- baseline (should match Q2 no-polling result)
- Test B: delete WITH watcher -- does watcher cancellation add orphans?
"""

import subprocess
import time
import os
import sys
import threading
import http.server

# Health check
class HealthHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")
    def log_message(self, *a):
        pass

try:
    health_server = http.server.HTTPServer(("", 8080), HealthHandler)
    threading.Thread(target=health_server.serve_forever, daemon=True).start()
except OSError:
    pass

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

def scan_all_for_sandbox(name):
    """Scan /proc for ALL processes referencing sandbox name."""
    matches = []
    try:
        for entry in os.listdir("/proc"):
            if not entry.isdigit():
                continue
            try:
                with open(f"/proc/{entry}/cmdline", "rb") as f:
                    cmdline_bytes = f.read()
                parts = cmdline_bytes.decode("utf-8", errors="replace").split("\x00")
                argv = [p for p in parts if p]
                cmdline_display = " ".join(argv)
                if name in cmdline_display:
                    # Classify
                    cls = "unknown"
                    if argv and "runsc" in argv[0]:
                        for a in argv:
                            if a in ("delete", "state", "wait", "start", "run",
                                     "exec", "kill", "gofer", "boot"):
                                cls = f"runsc-{a}"
                                break
                        else:
                            cls = "runsc-other"
                    elif argv and "sandbox" in argv[0]:
                        if len(argv) > 1:
                            cls = f"sandbox-{argv[1]}"
                        else:
                            cls = "sandbox"
                    elif "python" in cmdline_display or "validation" in cmdline_display:
                        continue  # skip ourselves
                    matches.append((int(entry), cls, cmdline_display))
            except (FileNotFoundError, PermissionError):
                continue
    except FileNotFoundError:
        pass
    return matches

def report_procs(name, label):
    procs = scan_all_for_sandbox(name)
    if procs:
        log(f"  [{label}] {len(procs)} process(es) for {name}:")
        for pid, cls, cmd in procs:
            log(f"    PID {pid} ({cls}): {cmd[:200]}")
    else:
        log(f"  [{label}] {name}: clean (no processes)")
    return procs


# ===================================================================
log("=== CHARACTERIZE: sandbox wait CANCELLATION ===")
log(f"runsc version: {run([RUNSC_BIN, '--version'])[1]}")
log("")

# ===================================================================
# SUB-QUESTION 1: Does `sandbox wait` create runsc processes during
# normal operation (before any delete)?
# ===================================================================
log("=== SUB-Q1: Does sandbox wait create runsc processes while running? ===")
log("")

sid_q1 = f"val-w1-{int(time.time())}"
log(f"Creating sandbox {sid_q1}...")
sandbox_run(sid_q1, ["/usr/bin/sleep", "3600"])
time.sleep(2)

log(f"Launching sandbox wait {sid_q1} (backgrounded)...")
wait_proc = subprocess.Popen(
    [SANDBOX_BIN, "wait", sid_q1],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
time.sleep(3)  # let it stabilize

log(f"Scanning /proc WHILE sandbox wait is running (no delete issued):")
procs_during_wait = report_procs(sid_q1, "during-wait")

# Classify what we found
wait_has_runsc_state = any(c == "runsc-state" for _, c, _ in procs_during_wait)
wait_has_runsc_wait = any(c == "runsc-wait" for _, c, _ in procs_during_wait)
wait_has_sandbox_wait = any(c == "sandbox-wait" for _, c, _ in procs_during_wait)

log("")
if wait_has_runsc_state:
    log("FINDING: sandbox wait creates runsc state processes during normal operation!")
    log("  This means every running sandbox leaks a permanent runsc state process")
    log("  when its watcher is cancelled.")
elif wait_has_runsc_wait:
    log("FINDING: sandbox wait shells out to runsc wait (not runsc state).")
    log("  Need to check if runsc wait also hangs on kill.")
else:
    log(f"FINDING: sandbox wait has these child processes: {[c for _, c, _ in procs_during_wait]}")
    log("  No runsc state during normal operation.")

log("")

# Clean up Q1 sandbox
try:
    wait_proc.kill()
    wait_proc.wait(timeout=5)
except Exception:
    pass
# Force delete to clean up
run([SANDBOX_BIN, "delete", "--force", sid_q1], timeout=5)

log("")

# ===================================================================
# TEST A: Delete WITHOUT watcher (baseline)
# ===================================================================
log("=== TEST A: Delete WITHOUT watcher (baseline) ===")
log("")

sid_a = f"val-wa-{int(time.time())}"
log(f"Creating sandbox {sid_a} (NO watcher attached)...")
sandbox_run(sid_a, ["/usr/bin/sleep", "3600"])
time.sleep(2)

log("Scan BEFORE delete:")
report_procs(sid_a, "before-delete-no-watcher")

log(f"Issuing delete --force (no watcher, no polling)...")
delete_a = subprocess.Popen(
    [SANDBOX_BIN, "delete", "--force", sid_a],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
time.sleep(15)

log("Scan 15s AFTER delete (no watcher was attached):")
procs_a = report_procs(sid_a, "after-delete-no-watcher")

try:
    delete_a.kill()
    delete_a.wait(timeout=5)
except Exception:
    pass

count_a = len([1 for _, c, _ in procs_a if c.startswith("runsc-")])
log(f"  runsc processes without watcher: {count_a}")
log("")

# ===================================================================
# TEST B: Delete WITH watcher (production path)
# ===================================================================
log("=== TEST B: Delete WITH watcher (production path) ===")
log("This simulates the actual production sequence:")
log("  1. sandbox run -> sandbox wait (watcher starts)")
log("  2. deleteOrWorkaround cancels watcher context")
log("  3. sandbox delete --force issued")
log("  4. delete times out, killProcessGroup + reap")
log("")

sid_b = f"val-wb-{int(time.time())}"
log(f"Creating sandbox {sid_b}...")
sandbox_run(sid_b, ["/usr/bin/sleep", "3600"])
time.sleep(2)

log(f"Starting watcher: sandbox wait {sid_b}...")
wait_b = subprocess.Popen(
    [SANDBOX_BIN, "wait", sid_b],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
time.sleep(3)  # let watcher stabilize

log("Scan BEFORE delete (watcher running):")
report_procs(sid_b, "before-delete-with-watcher")

# Step 2: Kill the watcher (simulating context cancel)
log("Killing watcher (simulating context.Cancel -> SIGKILL)...")
try:
    wait_b.kill()
    wait_b.wait(timeout=5)
except Exception:
    pass
time.sleep(2)

log("Scan AFTER watcher killed, BEFORE delete:")
procs_after_watcher_kill = report_procs(sid_b, "after-watcher-kill")

# Step 3: Issue delete
log(f"Issuing delete --force...")
delete_b = subprocess.Popen(
    [SANDBOX_BIN, "delete", "--force", sid_b],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
time.sleep(15)

log("Scan 15s AFTER delete (watcher was killed before delete):")
procs_b = report_procs(sid_b, "after-delete-with-watcher")

try:
    delete_b.kill()
    delete_b.wait(timeout=5)
except Exception:
    pass

count_b = len([1 for _, c, _ in procs_b if c.startswith("runsc-")])
log(f"  runsc processes with watcher: {count_b}")
log("")

# ===================================================================
# TEST C: Delete WITH watcher killed DURING delete (closer to real timing)
# In production, deleteOrWorkaround cancels the watcher and immediately
# issues delete. The watcher kill and the delete overlap.
# ===================================================================
log("=== TEST C: Watcher killed DURING delete (real timing) ===")
log("")

sid_c = f"val-wc-{int(time.time())}"
log(f"Creating sandbox {sid_c}...")
sandbox_run(sid_c, ["/usr/bin/sleep", "3600"])
time.sleep(2)

log(f"Starting watcher: sandbox wait {sid_c}...")
wait_c = subprocess.Popen(
    [SANDBOX_BIN, "wait", sid_c],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
time.sleep(3)

# Simulate production: kill watcher and issue delete nearly simultaneously
log("Killing watcher + issuing delete simultaneously (production pattern)...")
try:
    wait_c.kill()
    wait_c.wait(timeout=5)
except Exception:
    pass

delete_c = subprocess.Popen(
    [SANDBOX_BIN, "delete", "--force", sid_c],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
time.sleep(15)

log("Scan 15s AFTER simultaneous watcher-kill + delete:")
procs_c = report_procs(sid_c, "simultaneous")

try:
    delete_c.kill()
    delete_c.wait(timeout=5)
except Exception:
    pass

count_c = len([1 for _, c, _ in procs_c if c.startswith("runsc-")])
log(f"  runsc processes with simultaneous kill: {count_c}")
log("")

# ===================================================================
# PERSISTENCE CHECK: Do any watcher-related orphans persist?
# ===================================================================
log("=== PERSISTENCE CHECK ===")
log("Checking all test sandboxes at 30s and 60s for persistent orphans...")

time.sleep(15)
for sid, label in [(sid_a, "no-watcher"), (sid_b, "with-watcher"), (sid_c, "simultaneous")]:
    report_procs(sid, f"{label}-t=30s")

time.sleep(30)
for sid, label in [(sid_a, "no-watcher"), (sid_b, "with-watcher"), (sid_c, "simultaneous")]:
    report_procs(sid, f"{label}-t=60s")

log("")

# ===================================================================
# SUMMARY
# ===================================================================
log("=== WATCHER CHARACTERIZATION SUMMARY ===")
log("")
log(f"Sub-Q1: sandbox wait child processes during normal operation:")
log(f"  runsc-state present: {wait_has_runsc_state}")
log(f"  runsc-wait present:  {wait_has_runsc_wait}")
log(f"  sandbox-wait present: {wait_has_sandbox_wait}")
log("")
log(f"Test A (no watcher):         {count_a} runsc process(es)")
log(f"Test B (watcher killed before delete): {count_b} runsc process(es)")
log(f"Test C (simultaneous):       {count_c} runsc process(es)")
log("")

if count_b > count_a or count_c > count_a:
    extra_b = count_b - count_a
    extra_c = count_c - count_a
    log(f"FINDING: Watcher cancellation adds {max(extra_b, extra_c)} extra orphan(s).")
    log("  The production path (deleteOrWorkaround -> cancel watcher -> delete)")
    log("  leaks additional processes beyond the runsc delete orphan.")
    log("  isOrphanedRunscProcess may need widening, OR killProcessGroup needs to")
    log("  cover the watcher's process group too.")
else:
    log("FINDING: Watcher cancellation does NOT add extra orphans.")
    log("  The production path is safe. No matcher changes needed.")

log("")
log("=== WATCHER CHARACTERIZATION COMPLETE ===")
log("Keeping container alive. Check Cloud Logging for results.")
while True:
    time.sleep(3600)
