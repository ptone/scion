#!/usr/bin/env bash
# Forwarding-profile tests for the conduit-0b spike (positive + negative).
# Usage: negative.sh <ssh-config> <host>
# Requires: socat, python3, ssh-agent. Prints PASS/FAIL lines.
set -u
CFG=${1:?ssh config}
HOST=${2:?host alias}
SSH=(ssh -F "$CFG" -o BatchMode=yes -o LogLevel=VERBOSE)
why() { grep -m1 -E "open failed|forwarding failed|request failed" <<<"$1" || echo "$1" | head -1; }
WORK=$(mktemp -d)
trap 'kill $(jobs -p) 2>/dev/null; rm -rf "$WORK"' EXIT
pass=0; fail=0
ok()  { echo "PASS: $*"; pass=$((pass+1)); }
bad() { echo "FAIL: $*"; fail=$((fail+1)); }

# Precondition: the SSH connection itself works (otherwise refusals below are vacuous).
if ! timeout 15 "${SSH[@]}" "$HOST" true >/dev/null 2>&1; then
  echo "FAIL: precondition — cannot run 'true' over ssh to $HOST; aborting"; exit 1
fi
echo "precondition ok: ssh $HOST true succeeded"

# Echo services on the "container" side (same host in the sandbox).
ECHO_PORT=${ECHO_PORT:-17777}
socat TCP-LISTEN:$ECHO_PORT,bind=0.0.0.0,fork,reuseaddr EXEC:cat 2>/dev/null &
socat TCP-LISTEN:9810,bind=127.0.0.1,fork,reuseaddr EXEC:cat 2>/dev/null &
SOCK=$WORK/echo.sock
socat UNIX-LISTEN:$SOCK,fork EXEC:cat 2>/dev/null &
sleep 0.5
NONLOOP=${NONLOOP:-$(hostname -I | awk '{print $1}')}

echo "== direct-tcpip (ssh -W) =="
out=$(echo ping | timeout 10 "${SSH[@]}" -W 127.0.0.1:$ECHO_PORT "$HOST" 2>&1)
[[ "$out" == *ping* ]] && ok "direct-tcpip to 127.0.0.1:$ECHO_PORT allowed" || bad "loopback direct-tcpip: $out"
out=$(echo ping | timeout 10 "${SSH[@]}" -W localhost:$ECHO_PORT "$HOST" 2>&1)
[[ "$out" == *ping* ]] && ok "direct-tcpip to localhost:$ECHO_PORT allowed" || bad "localhost direct-tcpip: $out"
out=$(echo ping | timeout 10 "${SSH[@]}" -W "$NONLOOP:$ECHO_PORT" "$HOST" 2>&1)
[[ "$out" != *ping* ]] && ok "direct-tcpip to non-loopback $NONLOOP:$ECHO_PORT refused: $(why "$out")" || bad "non-loopback allowed!"
out=$(echo ping | timeout 10 "${SSH[@]}" -W "example.com:80" "$HOST" 2>&1)
[[ "$out" != *ping* ]] && ok "direct-tcpip to hostname example.com refused: $(why "$out")" || bad "hostname dest: $out"
out=$(echo ping | timeout 10 "${SSH[@]}" -W 127.0.0.1:9810 "$HOST" 2>&1)
[[ "$out" != *ping* ]] && ok "direct-tcpip to deny-listed 127.0.0.1:9810 refused: $(why "$out")" || bad "9810 allowed!"
out=$(echo ping | timeout 10 "${SSH[@]}" -W 127.0.0.1:18380 "$HOST" 2>&1)
[[ "$out" != *ping* ]] && ok "direct-tcpip to deny-listed 127.0.0.1:18380 refused: $(why "$out")" || bad "18380 allowed!"

echo "== direct-streamlocal (ssh -L port:/socket) =="
"${SSH[@]}" -N -o ExitOnForwardFailure=yes -L 127.0.0.1:17801:$SOCK -L 127.0.0.1:17802:/etc/passwd "$HOST" 2>"$WORK/sl.err" &
SLPID=$!; sleep 1.5
out=$(echo ping | timeout 5 socat - TCP:127.0.0.1:17801 2>&1)
[[ "$out" == *ping* ]] && ok "direct-streamlocal to unix socket allowed" || bad "streamlocal: $out"
out=$(echo ping | timeout 5 socat - TCP:127.0.0.1:17802 2>&1)
[[ "$out" != *ping* ]] && ok "direct-streamlocal to non-socket /etc/passwd refused: $(grep -m1 'open failed' "$WORK/sl.err")" || bad "streamlocal non-socket allowed"
kill $SLPID 2>/dev/null; wait $SLPID 2>/dev/null

echo "== remote forwarding (-R) =="
out=$(timeout 10 "${SSH[@]}" -o ExitOnForwardFailure=yes -R 127.0.0.1:17900:127.0.0.1:22 "$HOST" true 2>&1); rc=$?
[[ $rc -ne 0 && "$out" == *"forwarding failed"* ]] && ok "-R refused (rc=$rc): $(why "$out")" || bad "-R: rc=$rc $out"
out=$(timeout 10 "${SSH[@]}" -o ExitOnForwardFailure=yes -R /tmp/conduit-spike-remote.sock:127.0.0.1:22 "$HOST" true 2>&1); rc=$?
[[ $rc -ne 0 && "$out" == *"forwarding failed"* ]] && ok "-R streamlocal refused (rc=$rc): $(why "$out")" || bad "-R streamlocal: rc=$rc $out"

echo "== agent forwarding (-A) =="
eval "$(ssh-agent -s)" >/dev/null
out=$(timeout 10 "${SSH[@]}" -A "$HOST" 'echo "SOCK=[${SSH_AUTH_SOCK:-}]"' 2>&1)
ssh-agent -k >/dev/null 2>&1
[[ "$out" == *"SOCK=[]"* ]] && ok "-A refused: no SSH_AUTH_SOCK in session ($out); see server log for DENY auth-agent-req" || bad "-A: $out"

echo "== X11 (-X) =="
out=$(DISPLAY=localhost:99 timeout 10 "${SSH[@]}" -X -o ForwardX11Trusted=yes -o XAuthLocation=/bin/false "$HOST" 'echo "DISPLAY=[${DISPLAY:-}]"' 2>&1)
[[ "$out" == *"DISPLAY=[]"* ]] && ok "X11 refused: $(echo "$out" | tr '\n' ' ')" || bad "X11: $out"

echo "== channel cap (32 per connection; session channels count too) =="
"${SSH[@]}" -N -L 127.0.0.1:17803:127.0.0.1:$ECHO_PORT "$HOST" 2>"$WORK/cap.err" &
CAPPID=$!; sleep 1.5
python3 - <<'EOF' > "$WORK/cap.out"
import socket, time
socks = []
res = []
for i in range(1, 35):
    s = socket.create_connection(("127.0.0.1", 17803))
    s.settimeout(3)
    try:
        s.sendall(b"ping%d\n" % i)
        d = s.recv(64)
        res.append((i, bool(d)))
    except Exception as e:
        res.append((i, False))
    socks.append(s)   # keep it open so the channel stays open
ok = [i for i, r in res if r]
no = [i for i, r in res if not r]
print("ok=%d first_refused=%s refused=%s" % (len(ok), no[0] if no else None, no))
EOF
kill $CAPPID 2>/dev/null; wait $CAPPID 2>/dev/null
c=$(cat "$WORK/cap.out")
[[ "$c" == "ok=32 first_refused=33 "* ]] && ok "channel cap: $c; client: $(grep -m1 'open failed' "$WORK/cap.err")" || bad "cap: $c"

echo
echo "summary: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
