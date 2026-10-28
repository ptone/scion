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
"""antigravity-ui container-side provisioner (experimental).

Runs inside the agent container during the pre-start lifecycle hook, invoked
by `sciontool harness provision --manifest ...`.

The Antigravity desktop app is driven through its own web UI, and the user
signs in there, so this provisioner stages no credentials, instructions or
MCP servers. Its jobs:

  1. Turn the app's self-update check off: merge
     {"autoCheckForUpdates": false} into ~/.config/Antigravity/app_storage.json,
     keeping every other key. The wrapper does the same at start.
  2. Write outputs/resolved-auth.json (method "none") and an empty
     outputs/env.json.

The script is stdlib-only.
"""

from __future__ import annotations

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import scion_harness  # type: ignore[import-not-found]

assert scion_harness.INTERFACE_VERSION >= 2, (
    f"scion_harness INTERFACE_VERSION {scion_harness.INTERFACE_VERSION} < 2"
)

APP_STORAGE = "~/.config/Antigravity/app_storage.json"
UPDATE_KEY = "autoCheckForUpdates"


def seed_update_off(ctx: scion_harness.ProvisionContext) -> str:
    """Set autoCheckForUpdates=false, preserving other keys.

    A file that is not a JSON object is kept as app_storage.json.invalid and
    replaced. Returns "created", "merged", "unchanged" or "replaced-invalid".
    """
    path = scion_harness.expand_path(APP_STORAGE)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    data: dict = {}
    status = "created"
    if os.path.exists(path):
        try:
            with open(path, encoding="utf-8") as f:
                raw = f.read()
            parsed = json.loads(raw) if raw.strip() else {}
        except (OSError, json.JSONDecodeError):
            parsed = None
        if isinstance(parsed, dict):
            data = parsed
            status = "merged"
        else:
            os.replace(path, path + ".invalid")
            ctx.warn(f"{path} was not a JSON object; kept as {path}.invalid")
            status = "replaced-invalid"
    if status == "merged" and data.get(UPDATE_KEY) is False:
        return "unchanged"
    data[UPDATE_KEY] = False
    scion_harness.atomic_write_json(path, data)
    os.chmod(path, 0o600)
    return status


def provision(ctx: scion_harness.ProvisionContext) -> None:
    explicit = ctx.explicit_type
    if explicit and explicit != "none":
        raise scion_harness.ProvisionError(
            f"auth type {explicit!r} is not supported: this harness takes no "
            "Scion credentials; sign in inside the Antigravity UI"
        )
    status = seed_update_off(ctx)
    ctx.write_outputs(scion_harness.ResolvedAuth("none"), env={})
    ctx.info(f"method=none app_storage={status}")


if __name__ == "__main__":
    scion_harness.run("antigravity-ui", provision)
