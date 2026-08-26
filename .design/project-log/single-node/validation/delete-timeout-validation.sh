#!/bin/bash
# Delete Timeout Validation Script
# Architect's 5-point validation plan for P4a
#
# Prerequisites:
#   - Running on a Cloud Run Instance with sandboxLauncher: true
#   - /usr/local/gcp/bin/sandbox available
#   - /usr/local/gcp/bin/runsc available
#
# Usage: bash delete-timeout-validation.sh
#
# WARNING: Do NOT tear down the Instance before reporting results.

set -euo pipefail

SANDBOX_BIN="/usr/local/gcp/bin/sandbox"
RUNSC_BIN="/usr/local/gcp/bin/runsc"

echo "=== Delete Timeout Validation ==="
echo "Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo ""

# Verify we're on a Cloud Run Instance
if [ ! -x "$SANDBOX_BIN" ]; then
    echo "ERROR: sandbox binary not found at $SANDBOX_BIN"
    exit 1
fi

# Print runsc version
echo "runsc version:"
"$RUNSC_BIN" --version 2>&1 || echo "(no --version flag)"
echo ""

# -----------------------------------------------------------------------
# Test 1: Effectiveness-vs-time curve
# -----------------------------------------------------------------------
echo "=== TEST 1: Effectiveness-vs-time curve ==="
echo "Creating sandbox with a long-running process..."

SANDBOX_ID="val-eff-$(date +%s)"
"$SANDBOX_BIN" run "$SANDBOX_ID" --detach --rootfs / --write -- /usr/bin/sleep 3600

echo "Issuing delete --force (backgrounded)..."
"$SANDBOX_BIN" delete --force "$SANDBOX_ID" &
DELETE_PID=$!

# Poll at 1, 2, 5, 10, 15, 20, 30, 60 seconds
for DELAY in 1 2 5 10 15 20 30 60; do
    sleep_remaining=$((DELAY - $(date +%s) + $(date +%s)))
    # Wait until the target time
    while [ $(($(date +%s) - $(date +%s))) -lt 0 ]; do sleep 0.1; done

    echo -n "  t=${DELAY}s: "
    if "$SANDBOX_BIN" exec "$SANDBOX_ID" -- /usr/bin/echo "alive" 2>/dev/null; then
        echo "SANDBOX STILL REACHABLE at ${DELAY}s"
    else
        echo "SANDBOX UNREACHABLE at ${DELAY}s"
        echo "  -> Sandbox became unreachable between previous probe and ${DELAY}s"
        break
    fi
done

# Kill the backgrounded delete if still running
kill $DELETE_PID 2>/dev/null || true
wait $DELETE_PID 2>/dev/null || true

echo ""

# -----------------------------------------------------------------------
# Test 2: Reaper live test
# -----------------------------------------------------------------------
echo "=== TEST 2: Reaper live test ==="
echo "Creating sandbox for reaper test..."

SANDBOX_ID2="val-reap-$(date +%s)"
"$SANDBOX_BIN" run "$SANDBOX_ID2" --detach --rootfs / --write -- /usr/bin/sleep 3600

echo "Issuing delete --force (will hang)..."
timeout 120 "$SANDBOX_BIN" delete --force "$SANDBOX_ID2" &
DELETE_PID2=$!

echo "Waiting 15s for the delete to enter hanging state..."
sleep 15

echo "Checking for orphaned runsc processes:"
echo "  ps output:"
ps aux | grep "runsc.*delete.*$SANDBOX_ID2" | grep -v grep || echo "  (none found)"

echo ""
echo "  /proc cmdline scan:"
for pid_dir in /proc/[0-9]*; do
    pid=$(basename "$pid_dir")
    cmdline=$(cat "$pid_dir/cmdline" 2>/dev/null | tr '\0' ' ' || true)
    if echo "$cmdline" | grep -q "runsc.*delete.*$SANDBOX_ID2"; then
        echo "  PID $pid: $cmdline"
    fi
done

# Kill the backgrounded delete
kill $DELETE_PID2 2>/dev/null || true
wait $DELETE_PID2 2>/dev/null || true

echo ""

# -----------------------------------------------------------------------
# Test 3: Post-reap state
# -----------------------------------------------------------------------
echo "=== TEST 3: Post-reap state ==="
echo "After killing delete, checking for surviving processes:"
echo "  runsc-gofer for $SANDBOX_ID2:"
ps aux | grep "runsc-gofer.*$SANDBOX_ID2" | grep -v grep || echo "  (none)"
echo "  runsc-sandbox for $SANDBOX_ID2:"
ps aux | grep "runsc-sandbox.*$SANDBOX_ID2" | grep -v grep || echo "  (none)"
echo "  Any runsc process for $SANDBOX_ID2:"
ps aux | grep "runsc.*$SANDBOX_ID2" | grep -v grep || echo "  (none)"
echo ""

# -----------------------------------------------------------------------
# Test 4: Concurrent deletes (OQ-16)
# -----------------------------------------------------------------------
echo "=== TEST 4: Concurrent deletes (OQ-16) ==="
NUM_CONCURRENT=5
echo "Creating $NUM_CONCURRENT sandboxes..."

SANDBOX_IDS=()
for i in $(seq 1 $NUM_CONCURRENT); do
    sid="val-conc-${i}-$(date +%s)"
    SANDBOX_IDS+=("$sid")
    "$SANDBOX_BIN" run "$sid" --detach --rootfs / --write -- /usr/bin/sleep 3600
    echo "  Created: $sid"
done

echo "Issuing $NUM_CONCURRENT concurrent delete --force..."
DELETE_PIDS=()
START_TIME=$(date +%s%N)

for sid in "${SANDBOX_IDS[@]}"; do
    timeout 30 "$SANDBOX_BIN" delete --force "$sid" &
    DELETE_PIDS+=($!)
done

echo "Waiting for all deletes..."
for dpid in "${DELETE_PIDS[@]}"; do
    wait $dpid 2>/dev/null || true
done
END_TIME=$(date +%s%N)

ELAPSED=$(( (END_TIME - START_TIME) / 1000000 ))
echo "All $NUM_CONCURRENT deletes completed/timed-out in ${ELAPSED}ms"
echo ""

echo "Checking for orphaned processes after concurrent delete:"
for sid in "${SANDBOX_IDS[@]}"; do
    orphans=$(ps aux | grep "runsc.*$sid" | grep -v grep | wc -l)
    if [ "$orphans" -gt 0 ]; then
        echo "  $sid: $orphans orphaned process(es)"
        ps aux | grep "runsc.*$sid" | grep -v grep
    else
        echo "  $sid: clean"
    fi
done
echo ""

# -----------------------------------------------------------------------
# Test 5: sync.Once WARN verification
# -----------------------------------------------------------------------
echo "=== TEST 5: sync.Once WARN verification ==="
echo "This test must be run with the actual scion hub binary."
echo "Expected: on the known-bad runsc build (google-958767651),"
echo "delete --force should HANG (timeout), not return normally."
echo "The sync.Once WARN 'upstream defect may be fixed' should NOT fire."
echo ""
echo "To verify: check scion hub logs after a sandbox delete."
echo "If the WARN fires on the known-bad build, the self-detection logic"
echo "has a false positive and needs investigation."
echo ""

echo "=== VALIDATION COMPLETE ==="
echo "Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo ""
echo "CRITICAL: Do NOT tear down this Instance before reporting results."
