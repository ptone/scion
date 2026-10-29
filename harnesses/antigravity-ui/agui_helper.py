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

  seed-update-off   Merge {"autoCheckForUpdates": false} into the app's
                    app_storage.json, keeping every other key.
  render-nginx      Render nginx.conf.tmpl into a private nginx.conf.
  shim-check        Print "shim" if the running app matches the path shim
                    (see SHIM_PATCHES), else "none" and the reason.
  free-port         Print the preferred loopback port if it is free, else
                    a free ephemeral one.

Stdlib only.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import socket
import ssl
import sys
import tempfile
import urllib.request

APP_STORAGE_REL = os.path.join(".config", "Antigravity", "app_storage.json")
UPDATE_KEY = "autoCheckForUpdates"

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
_NGINX_UNSAFE = ("'", "$", "\\")


class HelperError(Exception):
    pass


# ---------------------------------------------------------------------------
# seed-update-off
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


def seed_update_off(home: str) -> str:
    """Set autoCheckForUpdates=false in app_storage.json under home.

    Other keys are preserved. A file that is not a JSON object is kept as
    app_storage.json.invalid and replaced. Returns a short status string.
    """
    path = os.path.join(home, APP_STORAGE_REL)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    data: dict = {}
    status = "created"
    try:
        with open(path, encoding="utf-8") as f:
            raw = f.read()
    except FileNotFoundError:
        raw = None
    if raw is not None:
        try:
            parsed = json.loads(raw) if raw.strip() else {}
        except json.JSONDecodeError:
            parsed = None
        if isinstance(parsed, dict):
            data = parsed
            status = "merged"
        else:
            os.replace(path, path + ".invalid")
            status = "replaced-invalid"
    if data.get(UPDATE_KEY) is False and status == "merged":
        return "unchanged"
    data[UPDATE_KEY] = False
    _atomic_write(path, json.dumps(data, indent=2) + "\n", 0o600)
    return status


# ---------------------------------------------------------------------------
# render-nginx
# ---------------------------------------------------------------------------


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
    for token, _ in SHIM_PATCHES:
        n = main_js.count(token)
        if n != 1:
            return f"none: main.js has {n} copies of a patch point (app version differs)"
    return "shim"


def shim_check(ls_port: str) -> str:
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
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="agui_helper")
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_seed = sub.add_parser("seed-update-off")
    p_seed.add_argument("--home", default=os.environ.get("HOME") or os.path.expanduser("~"))

    p_render = sub.add_parser("render-nginx")
    p_render.add_argument("--template", required=True)
    p_render.add_argument("--out", required=True)
    p_render.add_argument("--state", required=True)
    p_render.add_argument("--proxy-port", required=True)
    p_render.add_argument("--ls-port", required=True)
    p_render.add_argument("--path-rewrite", choices=PATH_REWRITE_TIERS, default="none")

    p_check = sub.add_parser("shim-check")
    p_check.add_argument("--ls-port", required=True)

    p_free = sub.add_parser("free-port")
    p_free.add_argument("--preferred", required=True)

    args = parser.parse_args(argv)
    try:
        if args.cmd == "seed-update-off":
            print("antigravity-ui: app_storage.json " + seed_update_off(args.home))
        elif args.cmd == "shim-check":
            print(shim_check(args.ls_port))
        elif args.cmd == "free-port":
            print(free_port(args.preferred))
        else:
            print("antigravity-ui: nginx config rendered (" + render_nginx(args) + ")")
    except (HelperError, OSError) as exc:
        print(f"antigravity-ui: {args.cmd}: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
