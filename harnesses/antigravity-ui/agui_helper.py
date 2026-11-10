#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
"""Runtime helper for the antigravity-ui wrapper (baked into the image).

Subcommands:

  render-nginx      Render nginx.conf.tmpl into a private nginx.conf.
  shim-check        Print "shim" if the running app matches the path shim
                    (see SHIM_PATCHES), else "none" and the reason.
  free-port         Print the preferred loopback port if it is free, else
                    a free ephemeral one.
  listen-addr       Print "loopback", "exposed <addresses>" or "none" for the
                    local addresses of this process tree's listeners on a
                    TCP port.
  port-owner        Print "ours", "foreign" or "none": whether the listener
                    on a TCP port belongs to a given process or one of its
                    descendants.

Stdlib only.
"""

from __future__ import annotations

import argparse
import ipaddress
import os
import re
import socket
import ssl
import sys
import tempfile
import urllib.request

# Placeholders the template may contain. Anything else written as ${...}
# is an error, so a typo cannot leave a literal placeholder in the config.
PLACEHOLDERS = (
    "STATE",
    "AGUI_PROXY_PORT",
    "JETSKI_LS_PORT",
    "SHIM_LOCATIONS",
    "PAGE_RULES",
)
_PLACEHOLDER_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}")

PATH_REWRITE_TIERS = ("none", "shim")

# The path shim (agui_shim.js) and the main.js patches it relies on, for the
# pinned app version. Each token must occur exactly once in main.js;
# shim-check verifies that against the running app before the shim is used.
# Neither side may contain a single quote, a dollar sign or a backslash:
# they are written into single-quoted nginx strings.
#
# The page loads the shim with a relative URL, so it resolves under the
# proxy prefix at any route depth; nginx serves it for any path ending in
# SHIM_URL_NAME. (Inlining it is not possible: nginx rejects config tokens
# over 4 KiB.)
SHIM_FILE = "agui_shim.js"
SHIM_URL_NAME = "agui-shim.js"
SHIM_PATCHES = (
    # Router base path and asset base: the app's own helper returns the
    # proxy prefix the shim found.
    ("function Ri(){if(Qi()&&",
     "function Ri(){if(window.__AGUI_PREFIX__)return window.__AGUI_PREFIX__;if(Qi()&&"),
    # RPC client base URL.
    ("baseUrl:window.location.origin,onWebSocketFallback",
     "baseUrl:window.location.origin+(window.__AGUI_PREFIX__||\"\"),onWebSocketFallback"),
)
SHIM_PAGE_TOKEN = "<head>"
SHIM_PROTOCOL_RELATIVE = '="//'
_NGINX_UNSAFE = ("'", "$", "\\")


class HelperError(Exception):
    pass


# ---------------------------------------------------------------------------
# render-nginx
# ---------------------------------------------------------------------------


def _atomic_write(path: str, data: str, mode: int) -> None:
    directory = os.path.dirname(path) or "."
    fd, tmp = tempfile.mkstemp(prefix=".tmp-", dir=directory)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(data)
        os.chmod(tmp, mode)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def _port(value: str, what: str) -> str:
    if not value.isdigit() or not 1 <= int(value) <= 65535:
        raise HelperError(f"{what} must be a TCP port, got {value!r}")
    return str(int(value))


def render_template(template: str, values: dict[str, str]) -> str:
    unknown = sorted({m.group(1) for m in _PLACEHOLDER_RE.finditer(template)} - set(values))
    if unknown:
        raise HelperError("template has unknown placeholders: " + ", ".join(unknown))
    return _PLACEHOLDER_RE.sub(lambda m: values[m.group(1)], template)


def build_values(
    *,
    state: str,
    proxy_port: str,
    ls_port: str,
    path_rewrite: str,
    share_dir: str = "",
) -> dict[str, str]:
    if not os.path.isabs(state) or any(c in state for c in ' ;{}"\'$\\\n'):
        raise HelperError(f"state dir must be an absolute path without special characters, got {state!r}")
    if path_rewrite not in PATH_REWRITE_TIERS:
        raise HelperError(f"unknown path rewrite tier {path_rewrite!r}")
    ls = _port(ls_port, "language server port")
    shim_locations, page_rules = "", ""
    if path_rewrite == "shim":
        shim_locations, page_rules = shim_rules(ls, share_dir)
    return {
        "STATE": state,
        "AGUI_PROXY_PORT": _port(proxy_port, "proxy port"),
        "JETSKI_LS_PORT": ls,
        "SHIM_LOCATIONS": shim_locations,
        "PAGE_RULES": page_rules,
    }


def _nginx_str(value: str, what: str) -> str:
    for c in _NGINX_UNSAFE:
        if c in value:
            raise HelperError(f"{what} contains {c!r}, which cannot go into an nginx string")
    return "'" + value + "'"


def shim_rules(ls_port: str, share_dir: str) -> tuple[str, str]:
    """nginx directives for the shim: (extra locations, page rules)."""
    if not os.path.isabs(share_dir) or any(c in share_dir for c in ' ;{}"\'$\\\n'):
        raise HelperError(f"shim directory must be an absolute path without special characters, got {share_dir!r}")
    if not os.path.isfile(os.path.join(share_dir, SHIM_FILE)):
        raise HelperError(f"shim file {SHIM_FILE} not found in {share_dir}")
    inject = SHIM_PAGE_TOKEN + '<script src="' + SHIM_URL_NAME + '"></script>'
    page = "\n".join([
        "      sub_filter_once off;",
        "      sub_filter " + _nginx_str(SHIM_PAGE_TOKEN, "page token") + " " + _nginx_str(inject, "inject") + ";",
        # Root-relative attribute URLs become relative; the shim's <base>
        # resolves them under the proxy prefix (or "/" without one).
        "      sub_filter " + _nginx_str('="/', "rule") + " " + _nginx_str('="', "rule") + ";",
    ])
    lines = [
        "    location ~ /" + SHIM_URL_NAME.replace(".", "\\.") + "$ {",
        f"      root {share_dir};",
        "      types { }",
        "      default_type application/javascript;",
        "      add_header Cache-Control no-cache;",
        f"      try_files /{SHIM_FILE} =404;",
        "    }",
        "    location = /main.js {",
        f"      proxy_pass https://127.0.0.1:{ls_port};",
        "      sub_filter_types application/javascript text/javascript;",
        "      sub_filter_once on;",
    ]
    for token, replacement in SHIM_PATCHES:
        lines.append("      sub_filter " + _nginx_str(token, "patch token") + " "
                     + _nginx_str(replacement, "patch") + ";")
    lines.append("    }")
    return "\n".join(lines), page


def render_nginx(args: argparse.Namespace) -> str:
    with open(args.template, encoding="utf-8") as f:
        template = f.read()
    values = build_values(
        state=args.state,
        proxy_port=args.proxy_port,
        ls_port=args.ls_port,
        path_rewrite=args.path_rewrite,
        share_dir=os.path.dirname(os.path.abspath(args.template)),
    )
    for sub in ("nx-body", "nx-proxy", "nx-fastcgi", "nx-uwsgi", "nx-scgi"):
        os.makedirs(os.path.join(args.state, sub), mode=0o700, exist_ok=True)
    _atomic_write(args.out, render_template(template, values), 0o600)
    return "path_rewrite=%s" % args.path_rewrite


# ---------------------------------------------------------------------------
# shim-check
# ---------------------------------------------------------------------------


def _fetch_loopback(port: str, path: str) -> str:
    # Loopback only, to the language server's self-signed certificate.
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    url = f"https://127.0.0.1:{_port(port, 'language server port')}{path}"
    with urllib.request.urlopen(url, context=ctx, timeout=15) as resp:  # noqa: S310 (loopback)
        return resp.read().decode("utf-8", "replace")


def shim_check_text(index_html: str, main_js: str) -> str:
    """Return "shim" if the app matches the shim, else "none: <reason>"."""
    if index_html.count(SHIM_PAGE_TOKEN) != 1:
        return "none: the app page has no single <head> tag"
    # The HTML rule turns ="/ into ="; on a protocol-relative ="//host/x
    # that would give a root-relative URL outside the prefix.
    if SHIM_PROTOCOL_RELATIVE in index_html:
        return "none: the app page has a protocol-relative URL attribute"
    for token, _ in SHIM_PATCHES:
        n = main_js.count(token)
        if n != 1:
            return f"none: main.js has {n} copies of a patch point (app version differs)"
    return "shim"


def shim_check(ls_port: str, force_mismatch: bool = False) -> str:
    if force_mismatch:
        # Test hook: exercises the fallback without a different app version.
        return "none: forced version mismatch (test hook)"
    try:
        index_html = _fetch_loopback(ls_port, "/")
        main_js = _fetch_loopback(ls_port, "/main.js")
    except OSError as exc:
        return f"none: could not read the app page ({exc.__class__.__name__})"
    return shim_check_text(index_html, main_js)


# ---------------------------------------------------------------------------
# free-port
# ---------------------------------------------------------------------------


def _bindable(port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        try:
            s.bind(("127.0.0.1", port))
        except OSError:
            return False
    return True


def free_port(preferred: str) -> int:
    """Return preferred if it can be bound on 127.0.0.1, else a free port.

    Agents on host networking share the host's loopback, so a fixed port can
    already be taken by another agent.
    """
    port = int(_port(preferred, "preferred port"))
    if _bindable(port):
        return port
    return any_free_port()


def any_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


# ---------------------------------------------------------------------------
# port-owner
# ---------------------------------------------------------------------------

_TCP_LISTEN = "0A"


def _decode_addr(hex_addr: str) -> str:
    """The local address of a /proc/net/tcp{,6} entry as text."""
    raw = bytes.fromhex(hex_addr)
    # Each 32-bit word is in host (little-endian) order.
    words = b"".join(raw[i:i + 4][::-1] for i in range(0, len(raw), 4))
    if len(words) == 4:
        return str(ipaddress.IPv4Address(words))
    return str(ipaddress.IPv6Address(words))


def _is_loopback(addr: str) -> bool:
    ip = ipaddress.ip_address(addr)
    if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped is not None:
        ip = ip.ipv4_mapped
    return ip.is_loopback


def listen_entries(port: int, proc_root: str = "/proc") -> dict[str, str]:
    """Socket inode -> local address of TCP listeners on port."""
    out: dict[str, str] = {}
    for name in ("tcp", "tcp6"):
        try:
            with open(os.path.join(proc_root, "net", name), encoding="ascii") as f:
                lines = f.readlines()[1:]
        except OSError:
            continue
        for line in lines:
            fields = line.split()
            if len(fields) < 10 or fields[3] != _TCP_LISTEN:
                continue
            try:
                addr_hex, port_hex = fields[1].rsplit(":", 1)
                if int(port_hex, 16) != port or fields[9] == "0":
                    continue
                out[fields[9]] = _decode_addr(addr_hex)
            except (ValueError, IndexError):
                continue
    return out


def listen_inodes(port: int, proc_root: str = "/proc") -> set[str]:
    """Socket inodes of TCP listeners on port (IPv4 and IPv6)."""
    inodes: set[str] = set()
    for name in ("tcp", "tcp6"):
        try:
            with open(os.path.join(proc_root, "net", name), encoding="ascii") as f:
                lines = f.readlines()[1:]
        except OSError:
            continue
        for line in lines:
            fields = line.split()
            if len(fields) < 10 or fields[3] != _TCP_LISTEN:
                continue
            try:
                local_port = int(fields[1].rsplit(":", 1)[1], 16)
            except (IndexError, ValueError):
                continue
            if local_port == port and fields[9] != "0":
                inodes.add(fields[9])
    return inodes


def _stat(proc_root: str, pid: str) -> list[str]:
    """Fields of /proc/<pid>/stat after the command name: state, ppid,
    pgrp, session, ..."""
    try:
        with open(os.path.join(proc_root, pid, "stat"), encoding="utf-8", errors="replace") as f:
            stat = f.read()
    except OSError:
        return []
    # The command name is in parentheses and may contain spaces.
    return stat[stat.rfind(")") + 2:].split()


def _ppid(proc_root: str, pid: str) -> str | None:
    rest = _stat(proc_root, pid)
    return rest[1] if len(rest) > 1 else None


def _session(proc_root: str, pid: str) -> str | None:
    rest = _stat(proc_root, pid)
    return rest[3] if len(rest) > 3 else None


def _in_tree(proc_root: str, pid: str, root: str) -> bool:
    seen = set()
    while pid and pid not in seen and pid not in ("0", "1"):
        if pid == root:
            return True
        seen.add(pid)
        pid = _ppid(proc_root, pid) or ""
    return pid == root


def _tree_sockets(proc_root: str, root_pid: str) -> set[str]:
    """Socket inodes held by root_pid, its descendants or its session."""
    held: set[str] = set()
    try:
        pids = [d for d in os.listdir(proc_root) if d.isdigit()]
    except OSError as exc:
        raise HelperError(f"cannot list {proc_root}: {exc}") from exc
    for pid in pids:
        if not (_in_tree(proc_root, pid, root_pid) or _session(proc_root, pid) == root_pid):
            continue
        fd_dir = os.path.join(proc_root, pid, "fd")
        try:
            fds = os.listdir(fd_dir)
        except OSError:
            continue
        for fd in fds:
            try:
                link = os.readlink(os.path.join(fd_dir, fd))
            except OSError:
                continue
            if link.startswith("socket:[") and link.endswith("]"):
                held.add(link[len("socket:["):-1])
    return held


def listen_addr(port: str, root_pid: str, proc_root: str = "/proc") -> str:
    """Return "loopback" if every listener of root_pid's tree on port is on a
    loopback address, "exposed <addr,...>" if any is not, or "none"."""
    p = int(_port(port, "port"))
    if not root_pid.isdigit():
        raise HelperError(f"pid must be a number, got {root_pid!r}")
    entries = listen_entries(p, proc_root)
    if not entries:
        return "none"
    ours = _tree_sockets(proc_root, root_pid)
    addrs = sorted({addr for inode, addr in entries.items() if inode in ours})
    if not addrs:
        return "none"
    exposed = [a for a in addrs if not _is_loopback(a)]
    return "exposed " + ",".join(exposed) if exposed else "loopback"


def port_owner(port: str, root_pid: str, proc_root: str = "/proc") -> str:
    """Return "ours" if a listener on port is held by root_pid, one of its
    descendants or a process in its session (the wrapper starts the app and
    nginx with setsid, so a server that double-forks out of the parent chain
    still has their session id), "foreign" if the port has listeners but
    none of ours, and "none" if nothing listens on it."""
    p = int(_port(port, "port"))
    if not root_pid.isdigit():
        raise HelperError(f"pid must be a number, got {root_pid!r}")
    inodes = listen_inodes(p, proc_root)
    if not inodes:
        return "none"
    return "ours" if inodes & _tree_sockets(proc_root, root_pid) else "foreign"


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="agui_helper")
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_render = sub.add_parser("render-nginx")
    p_render.add_argument("--template", required=True)
    p_render.add_argument("--out", required=True)
    p_render.add_argument("--state", required=True)
    p_render.add_argument("--proxy-port", required=True)
    p_render.add_argument("--ls-port", required=True)
    p_render.add_argument("--path-rewrite", choices=PATH_REWRITE_TIERS, default="none")

    p_check = sub.add_parser("shim-check")
    p_check.add_argument("--ls-port", required=True)
    p_check.add_argument("--force-mismatch", action="store_true")

    p_free = sub.add_parser("free-port")
    free_group = p_free.add_mutually_exclusive_group(required=True)
    free_group.add_argument("--preferred")
    free_group.add_argument("--any", action="store_true")

    p_addr = sub.add_parser("listen-addr")
    p_addr.add_argument("--port", required=True)
    p_addr.add_argument("--pid", required=True)
    p_addr.add_argument("--proc-root", default="/proc")

    p_owner = sub.add_parser("port-owner")
    p_owner.add_argument("--port", required=True)
    p_owner.add_argument("--pid", required=True)
    p_owner.add_argument("--proc-root", default="/proc")

    args = parser.parse_args(argv)
    try:
        if args.cmd == "shim-check":
            print(shim_check(args.ls_port, args.force_mismatch))
        elif args.cmd == "free-port":
            print(any_free_port() if args.any else free_port(args.preferred))
        elif args.cmd == "listen-addr":
            print(listen_addr(args.port, args.pid, args.proc_root))
        elif args.cmd == "port-owner":
            print(port_owner(args.port, args.pid, args.proc_root))
        else:
            print("antigravity-ui: nginx config rendered (" + render_nginx(args) + ")")
    except (HelperError, OSError) as exc:
        print(f"antigravity-ui: {args.cmd}: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
