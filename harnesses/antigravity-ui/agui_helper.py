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
import sys
import tempfile

APP_STORAGE_REL = os.path.join(".config", "Antigravity", "app_storage.json")
UPDATE_KEY = "autoCheckForUpdates"

# Placeholders the template may contain. Anything else written as ${...}
# is an error, so a typo cannot leave a literal placeholder in the config.
PLACEHOLDERS = (
    "STATE",
    "AGUI_PROXY_PORT",
    "JETSKI_LS_PORT",
    "PATH_REWRITE_RULES",
)
_PLACEHOLDER_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}")

PATH_REWRITE_TIERS = {
    "none": "",
    # Root-relative attribute URLs in HTML become page-relative, so they
    # resolve under the Hub proxy's path prefix.
    "html": "\n".join([
        "      sub_filter_once off;",
        "      sub_filter '=\"/' '=\"./';",
    ]),
}


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
) -> dict[str, str]:
    if not os.path.isabs(state) or any(c in state for c in ' ;{}"\'$\\\n'):
        raise HelperError(f"state dir must be an absolute path without special characters, got {state!r}")
    if path_rewrite not in PATH_REWRITE_TIERS:
        raise HelperError(f"unknown path rewrite tier {path_rewrite!r}")
    return {
        "STATE": state,
        "AGUI_PROXY_PORT": _port(proxy_port, "proxy port"),
        "JETSKI_LS_PORT": _port(ls_port, "language server port"),
        "PATH_REWRITE_RULES": PATH_REWRITE_TIERS[path_rewrite],
    }


def render_nginx(args: argparse.Namespace) -> str:
    with open(args.template, encoding="utf-8") as f:
        template = f.read()
    values = build_values(
        state=args.state,
        proxy_port=args.proxy_port,
        ls_port=args.ls_port,
        path_rewrite=args.path_rewrite,
    )
    for sub in ("nx-body", "nx-proxy", "nx-fastcgi", "nx-uwsgi", "nx-scgi"):
        os.makedirs(os.path.join(args.state, sub), mode=0o700, exist_ok=True)
    _atomic_write(args.out, render_template(template, values), 0o600)
    return "path_rewrite=%s" % args.path_rewrite


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
    p_render.add_argument("--path-rewrite", choices=tuple(PATH_REWRITE_TIERS), default="none")

    p_free = sub.add_parser("free-port")
    p_free.add_argument("--preferred", required=True)

    args = parser.parse_args(argv)
    try:
        if args.cmd == "seed-update-off":
            print("antigravity-ui: app_storage.json " + seed_update_off(args.home))
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
