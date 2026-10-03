#!/usr/bin/env bash
# Reproduce the conduit-0b spike results inside a Linux container.
# Needs: go, ssh (OpenSSH client), socat, python3, curl/wget, internet access
# to update.code.visualstudio.com. Optional: /usr/sbin/sshd for the TCP{22}
# comparison. Writes logs/results to $OUT (default /tmp/c0b-run).
set -u
HERE=$(cd "$(dirname "$0")/.." && pwd)
OUT=${OUT:-/tmp/c0b-run}
mkdir -p "$OUT"
export GOCACHE=${GOCACHE:-/scion-volumes/gocache}
(cd "$HERE" && CGO_ENABLED=0 go build -buildvcs=false -o "$OUT/conduit-sshd" .) || exit 1
BIN=$OUT/conduit-sshd
"$BIN" -init-hostkey -hostkey "$OUT/hostkey" >/dev/null
echo "conduit-spike $("$BIN" -init-hostkey -hostkey "$OUT/hostkey")" > "$OUT/known_hosts"

cat > "$OUT/ssh_config" <<CFG
Host conduit
  HostName conduit-spike
  HostKeyAlias conduit-spike
  User scion
  ProxyCommand $BIN -stdio -hostkey $OUT/hostkey -log $OUT/server.log
  UserKnownHostsFile $OUT/known_hosts
  StrictHostKeyChecking yes
  LogLevel ERROR
Host conduit-listen
  HostName conduit-spike
  HostKeyAlias conduit-spike
  User scion
  ProxyCommand socat - TCP:127.0.0.1:2223
  UserKnownHostsFile $OUT/known_hosts
  StrictHostKeyChecking yes
  LogLevel ERROR
CFG

SIM="python3 $HERE/scripts/remote_ssh_sim.py --ssh-config $OUT/ssh_config"
NEG="$HERE/scripts/negative.sh $OUT/ssh_config"
rc=0
run() { local name=$1; shift; echo "### $name"; "$@" > "$OUT/$name.txt" 2>&1; local r=$?; grep -E '^(PASS|FAIL)|summary' "$OUT/$name.txt"; [[ $r -ne 0 ]] && rc=1; }

run neg-embedded-stdio $NEG conduit
run sim-embedded-stdio-tcp $SIM --host conduit
run sim-embedded-stdio-socket $SIM --host conduit --socket
run sim-embedded-stdio-serverkill $SIM --host conduit --server-pid-cmd "pkill -KILL -o -f '[c]onduit-sshd -stdio'"
"$BIN" -listen 127.0.0.1:2223 -hostkey "$OUT/hostkey" -log "$OUT/server-listen.log" & LPID=$!
sleep 0.5
run neg-embedded-listen $NEG conduit-listen
run sim-embedded-listen-tcp $SIM --host conduit-listen
kill $LPID
echo "overall: $([[ $rc -eq 0 ]] && echo PASS || echo FAIL)  (results in $OUT)"
exit $rc
