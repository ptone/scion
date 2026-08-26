#!/usr/bin/env python3
"""
Supplemental: Characterize the `runsc state` orphan.

Three questions from the architect:
1. Does it persist? Scan at 5s, 30s, 60s after delete.
2. Is it ours (measurement artifact from sandbox exec polling) or the platform's?
   Test: delete with NO reachability polling. If runsc state still appears, it's
   real. If it doesn't, the polling created it.
3. Does it appear serially with identical polling? If yes, it tracks polling, not
   concurrency.
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

def sandbox_exec_probe(name):
    rc, out, err = run([SANDBOX_BIN, "exec", name, "--", "/bin/echo", "alive"], timeout=5)
    return rc == 0 and "alive" in out

def scan_proc_for_sandbox(name):
    """Scan /proc for ALL processes referencing sandbox name. Returns list of (pid, cmdline_str, argv_list)."""
    matches = []
    try:
        for entry in os.listdir("/proc"):
            if not entry.isdigit():
                continue
            try:
                with open(f"/proc/{entry}/cmdline", "rb") as f:
                    cmdline_bytes = f.read()
                # NUL-split for accurate argv (same as isOrphanedRunscProcess)
                parts = cmdline_bytes.decode("utf-8", errors="replace").split("\x00")
                argv = [p for p in parts if p]
                cmdline_display = " ".join(argv)
                if name in cmdline_display:
                    matches.append((int(entry), cmdline_display, argv))
            except (FileNotFoundError, PermissionError):
                continue
    except FileNotFoundError:
        pass
    return matches

def classify_proc(argv):
    """Classify a process by its runsc subcommand."""
    if not argv or "runsc" not in argv[0]:
        return "non-runsc"
    for a in argv:
        if a in ("delete", "state", "start", "run", "exec", "kill", "gofer"):
            return f"runsc-{a}"
    return "runsc-unknown"

def scan_and_report(name, label):
    """Scan for processes and report with classification."""
    procs = scan_proc_for_sandbox(name)
    procs = [(p, c, a) for p, c, a in procs if "python" not in c and "validation" not in c]
    if procs:
        log(f"  [{label}] {len(procs)} process(es) for {name}:")
        for pid, cmd, argv in procs:
            cls = classify_proc(argv)
            log(f"    PID {pid} ({cls}): {cmd[:180]}")
    else:
        log(f"  [{label}] {name}: clean (no processes)")
    return procs


# ===================================================================
log("=== CHARACTERIZE runsc state ORPHAN ===")
log(f"runsc version: {run([RUNSC_BIN, '--version'])[1]}")
log("")

# ===================================================================
# QUESTION 2 (discriminator -- run first, it's the decisive test)
# Does runsc state appear after delete with NO reachability polling?
# ===================================================================
log("=== QUESTION 2: Is it a measurement artifact? ===")
log("Test: delete with NO sandbox exec polling. If runsc state still appears,")
log("it is a platform artifact. If it does not, our polling created it.")
log("")

# Test 2a: Single delete, no polling
sid_2a = f"val-q2a-{int(time.time())}"
log(f"Creating sandbox {sid_2a}...")
sandbox_run(sid_2a, ["/usr/bin/sleep", "3600"])
time.sleep(2)
# Verify reachable once, then stop polling
sandbox_exec_probe(sid_2a)
log(f"Sandbox {sid_2a} confirmed reachable.")
time.sleep(2)  # brief gap so any state proc from the probe can exit

log(f"Issuing delete --force (NO further exec probes)...")
delete_2a = subprocess.Popen(
    [SANDBOX_BIN, "delete", "--force", sid_2a],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
# Wait 15s WITHOUT any sandbox exec calls
time.sleep(15)
log(f"Scanning at t=15s (no exec probes were issued):")
procs_2a = scan_and_report(sid_2a, "no-polling")

# Kill delete
try:
    delete_2a.kill()
    delete_2a.wait(timeout=5)
except Exception:
    pass

has_state_no_poll = any("runsc-state" in classify_proc(a) for _, _, a in procs_2a)
has_delete_no_poll = any("runsc-delete" in classify_proc(a) for _, _, a in procs_2a)

log("")
if has_state_no_poll:
    log("FINDING: runsc state process present WITHOUT any exec polling.")
    log("  -> It is NOT a measurement artifact. It is created by delete itself.")
else:
    log("FINDING: NO runsc state process without exec polling.")
    log("  -> The runsc state process is likely a measurement artifact (from sandbox exec).")
log(f"  runsc delete present: {has_delete_no_poll}")
log("")

# Test 2b: Single delete WITH polling (control for comparison)
sid_2b = f"val-q2b-{int(time.time())}"
log(f"Control: Creating sandbox {sid_2b}...")
sandbox_run(sid_2b, ["/usr/bin/sleep", "3600"])
time.sleep(2)
sandbox_exec_probe(sid_2b)
log(f"Sandbox {sid_2b} confirmed reachable.")

log(f"Issuing delete --force WITH exec probes at 1s, 2s, 5s, 10s...")
delete_2b = subprocess.Popen(
    [SANDBOX_BIN, "delete", "--force", sid_2b],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
delete_2b_start = time.monotonic()

for t_target in [1, 2, 5, 10]:
    elapsed = time.monotonic() - delete_2b_start
    wait = t_target - elapsed
    if wait > 0:
        time.sleep(wait)
    sandbox_exec_probe(sid_2b)

time.sleep(5)  # wait a bit after last probe
log(f"Scanning at t=15s (after exec probes at 1,2,5,10s):")
procs_2b = scan_and_report(sid_2b, "with-polling")

try:
    delete_2b.kill()
    delete_2b.wait(timeout=5)
except Exception:
    pass

has_state_with_poll = any("runsc-state" in classify_proc(a) for _, _, a in procs_2b)
log("")
if has_state_with_poll and not has_state_no_poll:
    log("DISCRIMINATOR: runsc state appears WITH polling but NOT without.")
    log("  -> CONFIRMED measurement artifact. sandbox exec shells out to runsc state.")
elif has_state_no_poll:
    log("DISCRIMINATOR: runsc state appears BOTH with and without polling.")
    log("  -> NOT an artifact. It is part of the delete lifecycle.")
else:
    log("DISCRIMINATOR: runsc state absent in both cases.")
    log("  -> May be transient. Check persistence (Question 1).")
log("")

# ===================================================================
# QUESTION 1: Does it persist?
# ===================================================================
log("=== QUESTION 1: Does the runsc state process persist? ===")
log("Test: After delete --force with exec polling (to create the state proc),")
log("scan at 5s, 30s, 60s after killing the wrapper.")
log("")

sid_1 = f"val-q1-{int(time.time())}"
log(f"Creating sandbox {sid_1}...")
sandbox_run(sid_1, ["/usr/bin/sleep", "3600"])
time.sleep(2)
sandbox_exec_probe(sid_1)

log(f"Issuing delete --force with exec probes...")
delete_1 = subprocess.Popen(
    [SANDBOX_BIN, "delete", "--force", sid_1],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
# Do some exec probes to potentially create state processes
for _ in range(3):
    time.sleep(2)
    sandbox_exec_probe(sid_1)

# Kill the wrapper (simulating our timeout path)
try:
    delete_1.kill()
    delete_1.wait(timeout=5)
except Exception:
    pass

log("Delete wrapper killed. Scanning for persistence...")

for delay_label, delay_s in [("5s", 5), ("30s", 25), ("60s", 30)]:
    time.sleep(delay_s)
    log(f"Scan at t={delay_label} after kill:")
    procs = scan_and_report(sid_1, f"t={delay_label}")
    if not any(True for _, _, _ in procs if True):
        break

log("")

# ===================================================================
# QUESTION 3: Does it appear serially with identical polling?
# ===================================================================
log("=== QUESTION 3: Does it appear serially with identical polling? ===")
log("Test: Single sandbox delete with the SAME polling pattern as the concurrent test.")
log("If runsc state appears, it tracks polling (not concurrency).")
log("")

sid_3 = f"val-q3-{int(time.time())}"
log(f"Creating sandbox {sid_3}...")
sandbox_run(sid_3, ["/usr/bin/sleep", "3600"])
time.sleep(2)
sandbox_exec_probe(sid_3)
log(f"Sandbox {sid_3} confirmed reachable.")

log(f"Issuing delete --force with same polling pattern as concurrent test...")
delete_3 = subprocess.Popen(
    [SANDBOX_BIN, "delete", "--force", sid_3],
    stdout=subprocess.PIPE, stderr=subprocess.PIPE
)
delete_3_start = time.monotonic()

# Same probe times as the concurrent effectiveness test
for t_target in [1, 2, 5, 10, 15, 20, 30]:
    elapsed = time.monotonic() - delete_3_start
    wait = t_target - elapsed
    if wait > 0:
        time.sleep(wait)
    sandbox_exec_probe(sid_3)

log(f"Scanning after serial polling (same pattern as concurrent test):")
procs_3 = scan_and_report(sid_3, "serial-with-polling")

try:
    delete_3.kill()
    delete_3.wait(timeout=5)
except Exception:
    pass

has_state_serial = any("runsc-state" in classify_proc(a) for _, _, a in procs_3)
log("")
if has_state_serial:
    log("FINDING: runsc state appears serially with identical polling pattern.")
    log("  -> It tracks the polling, not the concurrency.")
else:
    log("FINDING: runsc state does NOT appear serially with identical polling.")
    log("  -> It may be concurrency-specific.")
log("")

# ===================================================================
# Summary
# ===================================================================
log("=== CHARACTERIZATION SUMMARY ===")
log("")
log(f"Q1 persistence:       See scan results above")
log(f"Q2 no-poll:           runsc state present = {has_state_no_poll}")
log(f"Q2 with-poll:         runsc state present = {has_state_with_poll}")
log(f"Q3 serial-with-poll:  runsc state present = {has_state_serial}")
log("")

if not has_state_no_poll and (has_state_with_poll or has_state_serial):
    log("CONCLUSION: The runsc state process is a MEASUREMENT ARTIFACT.")
    log("  sandbox exec shells out to runsc state to test reachability.")
    log("  Our reachability polling created it; delete alone does not.")
    log("  The reaper does NOT need to match runsc state processes.")
    log("  'No code changes needed' stands.")
elif has_state_no_poll:
    log("CONCLUSION: The runsc state process is a REAL ORPHAN from the delete lifecycle.")
    log("  The reaper's isOrphanedRunscProcess matcher needs widening to match")
    log("  runsc state processes, or a separate cleanup path is needed.")
    log("  CODE CHANGE REQUIRED.")
else:
    log("CONCLUSION: The runsc state process was transient and did not reproduce.")
    log("  No action needed, but document the observation.")

log("")
log("=== CHARACTERIZATION COMPLETE ===")
log("Keeping container alive. Check Cloud Logging for results.")
while True:
    time.sleep(3600)
