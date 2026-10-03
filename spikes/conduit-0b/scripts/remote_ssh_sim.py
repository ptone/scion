#!/usr/bin/env python3
"""Scripted approximation of the VS Code Remote-SSH client (conduit-0b spike).

Reproduces, with the OpenSSH client, the sequence the Remote-SSH extension
drives (default settings: useLocalServer=true => `ssh -T -D <port> host bash`):

  1. exec `bash` over SSH with the server install script on stdin; parse
     listeningOn / connectionToken from the start/end markers; keep the exec
     channel open.
  2. open a forwarded channel to the server: direct-tcpip via the -D SOCKS
     port (TCP mode), or direct-streamlocal via -L <port>:<socket> (socket
     mode, remote.SSH.remoteServerListenOnSocket=true).
  3. through that channel: HTTP (/version, /vscode-remote-resource), the
     WebSocket upgrade, and VS Code's own PersistentProtocol handshake
     (auth -> sign -> connectionType -> ok) for a Management connection, then
     IPC calls on the `remoteFilesystem` channel (writeFile/readFile/stat
     under /workspace) — the same RPCs the workbench issues for file I/O.
  4. a terminal (PTY over SSH, as `scion ssh` / the SSH layer provides).
  5. forced interruption (SIGKILL the ssh client = transport drop), then a
     reconnect: rerun the script (server reused), new forwarded channel,
     WebSocket with reconnection=true and the same reconnectionToken, and a
     further IPC call on the resumed management connection.

Usage: remote_ssh_sim.py --ssh-config CFG --host HOST [--socket] [--workdir /workspace/x]
Exit code 0 iff all checks pass.
"""
import argparse, base64, json, os, random, select, signal, socket, struct, subprocess
import sys, tempfile, time, uuid, urllib.request

CHECKS = []


def check(name, cond, detail=""):
    CHECKS.append((name, bool(cond)))
    print(("PASS" if cond else "FAIL") + ": " + name + (f" — {detail}" if detail else ""), flush=True)
    return cond


def free_port():
    s = socket.socket(); s.bind(("127.0.0.1", 0)); p = s.getsockname()[1]; s.close(); return p


# ---------------------------------------------------------------- ssh driving
class RemoteSSH:
    def __init__(self, args, commit, listen_flag):
        self.args, self.commit, self.listen_flag = args, commit, listen_flag
        self.proc = None
        self.fwd = None
        self.socks_port = None
        self.local_port = None

    def start(self):
        marker = uuid.uuid4().hex
        tmpl = open(os.path.join(os.path.dirname(__file__), "remote-ssh-install.sh.tmpl")).read()
        script = (tmpl.replace("@@COMMIT@@", self.commit).replace("@@QUALITY@@", "stable")
                  .replace("@@LISTEN_FLAG@@", self.listen_flag).replace("@@MARKER@@", marker))
        self.socks_port = free_port()
        cmd = ["ssh", "-F", self.args.ssh_config, "-T", "-D", f"127.0.0.1:{self.socks_port}",
               "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", self.args.host, "bash"]
        t0 = time.time()
        self.proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=subprocess.PIPE, bufsize=0)
        self.proc.stdin.write(script.encode() + b"\n")
        self.proc.stdin.flush()
        out, buf = {}, b""
        deadline = time.time() + 300
        inblock = False
        while time.time() < deadline:
            r, _, _ = select.select([self.proc.stdout], [], [], 1)
            if not r:
                if self.proc.poll() is not None:
                    break
                continue
            chunk = os.read(self.proc.stdout.fileno(), 65536)
            if not chunk:
                break
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                line = line.decode(errors="replace")
                if line == f"{marker}: start":
                    inblock = True
                elif line == f"{marker}: end":
                    self.elapsed = time.time() - t0
                    self.result = out
                    return out
                elif inblock and "==" in line:
                    k, v = line.split("==", 1)
                    out[k] = v.rstrip("=")
                else:
                    print("   [remote] " + line, flush=True)
        err = self.proc.stderr.read().decode(errors="replace") if self.proc.poll() is not None else ""
        raise RuntimeError("install script did not complete: " + err)

    def open_socket_forward(self, sock_path):
        """socket mode: the real client forwards a local port to the remote socket."""
        self.local_port = free_port()
        cmd = ["ssh", "-F", self.args.ssh_config, "-N", "-o", "ExitOnForwardFailure=yes",
               "-L", f"127.0.0.1:{self.local_port}:{sock_path}", self.args.host]
        self.fwd = subprocess.Popen(cmd, stderr=subprocess.PIPE)
        for _ in range(50):
            try:
                socket.create_connection(("127.0.0.1", self.local_port), 0.2).close()
                return
            except OSError:
                time.sleep(0.2)
        raise RuntimeError("socket forward did not come up")

    def kill(self):
        for p in (self.proc, self.fwd):
            if p and p.poll() is None:
                p.send_signal(signal.SIGKILL)
                p.wait()

    def close(self):
        if self.proc and self.proc.poll() is None:
            try:
                self.proc.stdin.write(b"exit\n"); self.proc.stdin.flush()
                self.proc.wait(5)
            except Exception:
                pass
        self.kill()


def connect_server(rs, port_or_sock):
    """Open a stream to the VS Code server through the SSH forwarded channel."""
    if rs.local_port:  # socket mode via -L (direct-streamlocal)
        return socket.create_connection(("127.0.0.1", rs.local_port), 10)
    s = socket.create_connection(("127.0.0.1", rs.socks_port), 10)  # SOCKS5 -> direct-tcpip
    s.sendall(b"\x05\x01\x00")
    if s.recv(2) != b"\x05\x00":
        raise RuntimeError("socks greeting failed")
    host = b"127.0.0.1"
    s.sendall(b"\x05\x01\x00\x03" + bytes([len(host)]) + host + struct.pack(">H", int(port_or_sock)))
    rep = recvn(s, 10)
    if rep[1] != 0:
        raise RuntimeError(f"socks connect failed: code {rep[1]}")
    return s


def recvn(s, n):
    b = b""
    while len(b) < n:
        c = s.recv(n - len(b))
        if not c:
            raise EOFError("connection closed")
        b += c
    return b


def http_get(rs, target, path):
    s = connect_server(rs, target)
    s.sendall(f"GET {path} HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n".encode())
    data = b""
    s.settimeout(10)
    while True:
        c = s.recv(65536)
        if not c:
            break
        data += c
    s.close()
    head, _, body = data.partition(b"\r\n\r\n")
    status = int(head.split(b" ")[1])
    if b"transfer-encoding: chunked" in head.lower():
        out, rest = b"", body
        while rest:
            ln, _, rest = rest.partition(b"\r\n")
            n = int(ln, 16)
            if n == 0:
                break
            out += rest[:n]; rest = rest[n + 2:]
        body = out
    return status, body


# ---------------------------------------------------------------- websocket
class WS:
    def __init__(self, sock):
        self.s, self.buf = sock, b""

    @classmethod
    def open(cls, rs, target, path):
        s = connect_server(rs, target)
        key = base64.b64encode(os.urandom(16)).decode()
        s.sendall((f"GET {path} HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                   f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode())
        resp = b""
        while b"\r\n\r\n" not in resp:
            c = s.recv(4096)
            if not c:
                raise EOFError("closed during upgrade")
            resp += c
        head, _, rest = resp.partition(b"\r\n\r\n")
        ws = cls(s); ws.buf = rest
        ws.status_line = head.split(b"\r\n")[0].decode()
        return ws

    def send(self, payload, opcode=2):
        mask = os.urandom(4)
        n = len(payload)
        hdr = bytes([0x80 | opcode])
        if n < 126:
            hdr += bytes([0x80 | n])
        elif n < 65536:
            hdr += bytes([0x80 | 126]) + struct.pack(">H", n)
        else:
            hdr += bytes([0x80 | 127]) + struct.pack(">Q", n)
        self.s.sendall(hdr + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))

    def _need(self, n, timeout):
        self.s.settimeout(timeout)
        while len(self.buf) < n:
            c = self.s.recv(65536)
            if not c:
                raise EOFError("ws closed")
            self.buf += c
        d, self.buf = self.buf[:n], self.buf[n:]
        return d

    def recv(self, timeout=15):
        msg = b""
        while True:
            h = self._need(2, timeout)
            fin, op, ln = h[0] & 0x80, h[0] & 0x0F, h[1] & 0x7F
            if ln == 126:
                ln = struct.unpack(">H", self._need(2, timeout))[0]
            elif ln == 127:
                ln = struct.unpack(">Q", self._need(8, timeout))[0]
            if h[1] & 0x80:
                self._need(4, timeout)
            data = self._need(ln, timeout)
            if op == 8:
                raise EOFError("ws close frame: " + data[2:].decode(errors="replace"))
            if op == 9:
                self.send(data, 10); continue
            if op == 10:
                continue
            msg += data
            if fin:
                return msg

    def close(self):
        try:
            self.s.close()
        except Exception:
            pass


# ------------------------------------------------- VS Code PersistentProtocol
T_REGULAR, T_CONTROL, T_ACK, T_KEEPALIVE = 1, 2, 3, 9


class Proto:
    """Minimal client side of vs/base/parts/ipc/common/ipc.net.ts PersistentProtocol."""

    def __init__(self, ws):
        self.ws, self.rbuf = ws, b""
        self.out_id, self.in_last = 0, 0

    def attach(self, ws):
        self.ws, self.rbuf = ws, b""

    def _send(self, t, mid, data):
        self.ws.send(struct.pack(">BIII", t, mid, self.in_last, len(data)) + data)

    def control(self, obj):
        self._send(T_CONTROL, 0, json.dumps(obj).encode())

    def regular(self, data):
        self.out_id += 1
        self._send(T_REGULAR, self.out_id, data)

    def recv(self, timeout=20):
        """Return (type, data) for the next control/regular message (dedup, ack)."""
        while True:
            while len(self.rbuf) < 13 or len(self.rbuf) < 13 + struct.unpack(">I", self.rbuf[9:13])[0]:
                self.rbuf += self.ws.recv(timeout)
            t, mid, ack, ln = struct.unpack(">BIII", self.rbuf[:13])
            data, self.rbuf = self.rbuf[13:13 + ln], self.rbuf[13 + ln:]
            if t == T_REGULAR:
                if mid <= self.in_last:
                    continue  # replayed duplicate
                self.in_last = mid
                self._send(T_ACK, 0, b"")
                return t, data
            if t == T_CONTROL:
                return t, data
            # ack / keepalive / pause / resume: ignore


# ------------------------------------------------ vs/base/parts/ipc serializer
def vql(n):
    out = b""
    while True:
        b = n & 0x7F; n >>= 7
        if n:
            out += bytes([b | 0x80])
        else:
            return out + bytes([b])


def ser(v):
    if v is None:
        return b"\x00"
    if isinstance(v, str):
        b = v.encode(); return b"\x01" + vql(len(b)) + b
    if isinstance(v, (bytes, bytearray)):
        return b"\x03" + vql(len(v)) + bytes(v)  # VSBuffer
    if isinstance(v, list):
        return b"\x04" + vql(len(v)) + b"".join(ser(x) for x in v)
    if isinstance(v, int) and v >= 0:
        return b"\x06" + vql(v)
    b = json.dumps(v).encode(); return b"\x05" + vql(len(b)) + b


def de(buf, i=0):
    def rvql(i):
        n, shift = 0, 0
        while True:
            b = buf[i]; i += 1
            n |= (b & 0x7F) << shift; shift += 7
            if not b & 0x80:
                return n, i
    t = buf[i]; i += 1
    if t == 0:
        return None, i
    if t in (1, 2, 3, 5):
        n, i = rvql(i); raw = buf[i:i + n]; i += n
        return (raw.decode() if t == 1 else json.loads(raw) if t == 5 else bytes(raw)), i
    if t == 4:
        n, i = rvql(i); arr = []
        for _ in range(n):
            x, i = de(buf, i); arr.append(x)
        return arr, i
    if t == 6:
        return rvql(i)
    raise ValueError(f"unknown ipc type {t}")


class Mgmt:
    """Management connection + IPC client over the PersistentProtocol."""

    def __init__(self, proto, authority):
        self.p, self.authority, self.req = proto, authority, 0
        self.events = []  # (listen id, body)

    def listen(self, channel, event, arg=None):
        self.req += 1
        self.p.regular(ser([102, self.req, channel, event]) + ser(arg))
        return self.req

    def pump_until(self, pred, timeout=20):
        """Read messages until pred(events) is true."""
        end = time.time() + timeout
        while time.time() < end and not pred(self.events):
            t, data = self.p.recv(max(0.1, end - time.time()))
            self._stash(t, data)
        return pred(self.events)

    def _stash(self, t, data):
        if t != T_REGULAR:
            return None
        hdr, i = de(data)
        if isinstance(hdr, list) and hdr and hdr[0] == 204:
            body, _ = de(data, i)
            self.events.append((hdr[1], body))
            return None
        return hdr, i

    def hello(self):
        self.p.regular(ser({"remoteAuthority": self.authority, "clientId": "conduit-0b-sim"}))
        t, data = self.p.recv()
        hdr, _ = de(data)
        return hdr

    def call(self, channel, command, arg, timeout=20):
        self.req += 1
        rid = self.req
        self.p.regular(ser([100, rid, channel, command]) + ser(arg))
        end = time.time() + timeout
        while time.time() < end:
            t, data = self.p.recv(timeout)
            r = self._stash(t, data)
            if r is None:
                continue
            hdr, i = r
            if isinstance(hdr, list) and len(hdr) >= 2 and hdr[1] == rid:
                body, _ = de(data, i)
                if hdr[0] == 201:
                    return body
                raise RuntimeError(f"ipc error {hdr[0]}: {body}")
        raise TimeoutError(f"no reply to {channel}.{command}")


def handshake(rs, target, token, commit, recon_token, reconnection, proto=None):
    ws = WS.open(rs, target, f"/?reconnectionToken={recon_token}&reconnection={'true' if reconnection else 'false'}&skipWebSocketFrames=false")
    if not ws.status_line.startswith("HTTP/1.1 101"):
        raise RuntimeError("upgrade failed: " + ws.status_line)
    if proto is None:
        proto = Proto(ws)
    else:
        proto.attach(ws)
    proto.control({"type": "auth", "auth": token, "data": uuid.uuid4().hex})
    t, data = proto.recv()
    sign = json.loads(data)
    if sign.get("type") != "sign":
        return ws, proto, sign, None
    # The MS build validates signedData with vsda; the server also accepts the
    # connection token itself as signedData (see server-main.js), which is what
    # we send since we do not ship vsda.
    proto.control({"type": "connectionType", "commit": commit, "signedData": token,
                   "desiredConnectionType": 1})
    t, data = proto.recv()
    return ws, proto, sign, json.loads(data)


# ---------------------------------------------------------------- main
def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ssh-config", required=True)
    ap.add_argument("--host", required=True)
    ap.add_argument("--commit", default="")
    ap.add_argument("--socket", action="store_true", help="server listens on a unix socket (direct-streamlocal)")
    ap.add_argument("--workdir", default="/workspace/.conduit-0b-probe")
    ap.add_argument("--server-pid-cmd", default="", help="shell cmd that kills the SSH server side (server-side drop) instead of the client")
    args = ap.parse_args()

    commit = args.commit or json.load(urllib.request.urlopen(
        "https://update.code.visualstudio.com/api/update/server-linux-x64/stable/latest"))["version"]
    print(f"== VS Code commit {commit}; mode={'socket' if args.socket else 'tcp'}", flush=True)
    authority = "ssh-remote+" + args.host
    listen = "--port=0"
    if args.socket:
        listen = f"--socket-path=/tmp/code-{commit[:12]}-{uuid.uuid4().hex[:8]}.sock"
        # a fresh server is needed for socket mode: stop any TCP-mode server first
        subprocess.run(["ssh", "-F", args.ssh_config, args.host,
                        f"pkill -f 'bin/{commit}/node.*--port=0' ; rm -f ~/.vscode-server/.{commit}.pid; true"],
                       capture_output=True)

    # ---- step 1: install/start via exec'd bash
    rs = RemoteSSH(args, commit, listen)
    res = rs.start()
    check("install script ran over exec channel and reported a listener",
          res.get("exitCode") == "0" and res.get("listeningOn"),
          f"listeningOn={res.get('listeningOn')} reused={res.get('reused')} in {rs.elapsed:.1f}s")
    target, token = res["listeningOn"], res["connectionToken"]
    if args.socket:
        check("server listens on a unix socket", target.startswith("/"), target)
        rs.open_socket_forward(target)

    # ---- step 2/3: forwarded channel + HTTP + WS + protocol
    st, body = http_get(rs, target, "/version")
    check("HTTP /version through forwarded channel", st == 200 and body.decode() == commit, f"{st} {body[:40]!r}")

    subprocess.run(["ssh", "-F", args.ssh_config, args.host, f"mkdir -p {args.workdir}"], check=True)
    probe = f"{args.workdir}/remote-resource.txt"
    subprocess.run(["ssh", "-F", args.ssh_config, args.host, f"echo resource-ok-{os.getpid()} > {probe}"], check=True)
    st, body = http_get(rs, target, f"/vscode-remote-resource?path={probe}&tkn={token}")
    check("file read via /vscode-remote-resource (with token)", st == 200 and b"resource-ok" in body, f"{st} {body.strip()[:40]!r}")
    st, _ = http_get(rs, target, f"/vscode-remote-resource?path={probe}&tkn=wrong")
    check("remote-resource with wrong token is refused by the server", st == 403, f"{st}")

    ws, _, sign, ok = handshake(rs, target, "wrong-token", commit, str(uuid.uuid4()), False)
    check("WS handshake with wrong connection token rejected by server", sign.get("type") == "error", json.dumps(sign)[:100])
    ws.close()

    recon = str(uuid.uuid4())
    ws, proto, sign, ok = handshake(rs, target, token, commit, recon, False)
    check("WebSocket upgrade + auth -> sign", sign.get("type") == "sign", ws.status_line)
    check("connectionType(Management) -> ok", ok and ok.get("type") == "ok", json.dumps(ok))
    m = Mgmt(proto, authority)
    init = m.hello()
    check("IPC channel server initialized", init == [200], str(init))

    uri = lambda p: {"$mid": 1, "scheme": "vscode-remote", "authority": authority, "path": p}
    fpath = f"{args.workdir}/edited-by-vscode.txt"
    content = f"hello from the conduit-0b sim {time.time()}\n".encode()
    m.call("remoteFilesystem", "writeFile", [uri(fpath), content, {"create": True, "overwrite": True, "unlock": False, "atomic": False}])
    got = m.call("remoteFilesystem", "readFile", [uri(fpath), {}])
    check("remoteFilesystem.writeFile + readFile under /workspace", got == content, f"{len(got or b'')} bytes")
    stat = m.call("remoteFilesystem", "stat", [uri(fpath)])
    check("remoteFilesystem.stat", isinstance(stat, dict) and stat.get("size") == len(content), json.dumps(stat))
    on_disk = subprocess.run(["ssh", "-F", args.ssh_config, args.host, f"cat {fpath}"], capture_output=True).stdout
    check("file written via VS Code RPC is visible over SSH exec", on_disk == content)
    listing = m.call("remoteFilesystem", "readdir", [uri("/workspace")])
    check("remoteFilesystem.readdir /workspace", isinstance(listing, list) and len(listing) > 0, f"{len(listing)} entries")

    # bulk bytes through the forwarded channel (4 MiB write + read back)
    blob = os.urandom(4 << 20)
    t0 = time.time()
    m.call("remoteFilesystem", "writeFile", [uri(f"{args.workdir}/blob.bin"), blob, {"create": True, "overwrite": True, "unlock": False, "atomic": False}], timeout=60)
    back = m.call("remoteFilesystem", "readFile", [uri(f"{args.workdir}/blob.bin"), {}], timeout=60)
    check("4 MiB round-trip through the channel is byte-identical", back == blob, f"{time.time() - t0:.2f}s")

    # ---- step 4a: terminal the way the workbench creates it (remoteterminal / ptyHost)
    try:
        created = m.call("remoteterminal", "$createProcess", {
            "shellLaunchConfig": {"executable": "/bin/bash", "args": [], "cwd": args.workdir},
            "configuration": {}, "resolverEnv": {}, "workspaceFolders": [], "activeWorkspaceFolder": None,
            "activeFileResource": None, "resolvedVariables": {}, "envVariableCollections": [],
            "cols": 80, "rows": 24, "unicodeVersion": "11", "options": {"shellIntegration": {"enabled": False, "suggestEnabled": False, "nonce": "c0b"}, "windowsEnableConpty": False, "windowsUseConptyDll": False}, "shouldPersistTerminal": False,
            "workspaceId": "conduit-0b", "workspaceName": "conduit-0b"}, timeout=40)
        tid = created["persistentTerminalId"]
        lid = m.listen("remoteterminal", "$onProcessDataEvent")
        m.call("remoteterminal", "$start", [tid])
        m.call("remoteterminal", "$input", [tid, "echo VSC_TERM_OK $((6*7)) $PWD\r"])
        def term_text(evs):
            return "".join((b.get("event") if isinstance(b.get("event"), str) else (b.get("event") or {}).get("data", ""))
                           for l, b in evs if l == lid and isinstance(b, dict) and b.get("id") == tid)
        okt = m.pump_until(lambda evs: f"VSC_TERM_OK 42 {args.workdir}" in term_text(evs), timeout=20)
        check("VS Code terminal (remoteterminal.$createProcess/$start/$input) runs a shell", okt,
              [l.strip() for l in term_text(m.events).splitlines() if "VSC_TERM_OK 42" in l][-1:])
        m.call("remoteterminal", "$shutdown", [tid, True])
    except Exception as e:
        check("VS Code terminal (remoteterminal.$createProcess/$start/$input) runs a shell", False, repr(e)[:1500])

    # ---- step 4b: terminal (PTY over SSH)
    term = subprocess.run(["ssh", "-F", args.ssh_config, "-tt", args.host],
                          input=f"cd {args.workdir} && echo TERM_OK $((6*7)) $(tty) && exit\n".encode(),
                          capture_output=True, timeout=30)
    tout = term.stdout.decode(errors="replace")
    check("interactive PTY shell (terminal)", "TERM_OK 42 /dev/pts/" in tout, [l for l in tout.splitlines() if "TERM_OK 42" in l][-1:])

    # ---- step 5: forced interruption + reconnect
    if args.server_pid_cmd:
        subprocess.run(args.server_pid_cmd, shell=True)
        how = "server-side kill"
    else:
        rs.kill()
        how = "SIGKILL of the ssh client"
    time.sleep(1)
    try:
        ws.s.settimeout(3)
        dead = ws.s.recv(1) == b""
    except (OSError, socket.timeout) as e:
        dead = not isinstance(e, socket.timeout)
    check(f"forced interruption ({how}) drops the forwarded channel", dead)
    ws.close()
    rs.close()

    rs2 = RemoteSSH(args, commit, listen)
    res2 = rs2.start()
    check("reconnect: install script reuses the running server", res2.get("reused") == "1" and res2.get("listeningOn") == target,
          f"listeningOn={res2.get('listeningOn')} reused={res2.get('reused')} in {rs2.elapsed:.1f}s")
    if args.socket:
        rs2.open_socket_forward(target)
    ws2, proto, sign, ok = handshake(rs2, target, token, commit, recon, True, proto=proto)
    check("reconnect: WS reconnection=true with same reconnectionToken -> ok", ok and ok.get("type") == "ok", json.dumps(ok))
    m.p = proto
    got = m.call("remoteFilesystem", "readFile", [uri(fpath), {}])
    check("reconnect: resumed management connection serves readFile", got == content)
    ws2.close()
    rs2.close()
    if args.socket:  # leave no socket-mode server behind (it would be reused by a TCP-mode run)
        subprocess.run(["ssh", "-F", args.ssh_config, args.host,
                        f"kill -- -$(cat ~/.vscode-server/.{commit}.pid) ; rm -f ~/.vscode-server/.{commit}.pid; true"],
                       capture_output=True)

    passed = sum(1 for _, c in CHECKS if c)
    print(f"\nsummary: {passed}/{len(CHECKS)} checks passed", flush=True)
    sys.exit(0 if passed == len(CHECKS) else 1)


if __name__ == "__main__":
    main()
