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

The Antigravity desktop app is driven through its own web UI. This
provisioner stages no instructions or MCP servers. Its jobs:

  1. Auth. The app signs in with Google Application Default Credentials
     when AGY_ADC_AUTH=true. vertex-ai is resolved, in this order, from:
       a. a staged gcloud-adc file secret;
       b. GOOGLE_APPLICATION_CREDENTIALS in the container environment;
       c. a GCP service account the broker assigned to the agent
          (SCION_METADATA_MODE=assign with SCION_METADATA_SA_EMAIL set; the
          broker sets SCION_METADATA_MODE only for assign and block, never
          for passthrough).
     Each writes AGY_ADC_AUTH=true into the env overlay, plus
     GOOGLE_APPLICATION_CREDENTIALS when a file is involved. With none of
     them the method is "none" and AGY_ADC_AUTH is not set: the app then
     waits for an interactive sign-in code, which the wrapper reports.
  2. Turn the app's self-update check off: merge
     {"autoCheckForUpdates": false} into ~/.config/Antigravity/app_storage.json,
     keeping every other key. The wrapper does the same at start.
  3. Write outputs/resolved-auth.json and outputs/env.json.

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

AUTH = scion_harness.AuthSpec(
    "antigravity-ui",
    [
        # The gcloud-adc file secret carries this credential (config.yaml
        # declares it with GOOGLE_APPLICATION_CREDENTIALS as the alternative
        # env key). resolve_auth below adds the assigned-SA path.
        scion_harness.env_method(
            "vertex-ai",
            any_of=["GOOGLE_APPLICATION_CREDENTIALS"],
            hint="stage a gcloud-adc file, set GOOGLE_APPLICATION_CREDENTIALS, "
                 "or assign a dedicated GCP service account to the agent",
        ),
    ],
)

ADC_SECRET = "gcloud-adc"
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


def _assigned_service_account() -> bool:
    return (os.environ.get("SCION_METADATA_MODE", "").strip() == "assign"
            and bool(os.environ.get("SCION_METADATA_SA_EMAIL", "").strip()))


def resolve_auth(ctx: scion_harness.ProvisionContext) -> tuple[scion_harness.ResolvedAuth, dict[str, str]]:
    """Return the resolved auth and its env overlay (see the module doc)."""
    explicit = ctx.explicit_type
    if explicit and explicit not in AUTH.valid_types():
        raise scion_harness.ProvisionError(
            f"antigravity-ui: unknown auth type {explicit!r}; valid types are: "
            + ", ".join(AUTH.valid_types())
        )
    method = AUTH.methods[0]

    staged = ctx.file_secret_files.get(ADC_SECRET)
    if staged:
        path = scion_harness.expand_path(staged)
        return (scion_harness.ResolvedAuth("vertex-ai", spec_entry=method, auth_file=path),
                {"AGY_ADC_AUTH": "true", "GOOGLE_APPLICATION_CREDENTIALS": path})

    gac = os.environ.get("GOOGLE_APPLICATION_CREDENTIALS", "").strip()
    if not gac and "GOOGLE_APPLICATION_CREDENTIALS" in ctx.env_keys:
        gac = (ctx.read_secret("GOOGLE_APPLICATION_CREDENTIALS", env_fallback=True) or "").strip()
    if gac:
        return (scion_harness.ResolvedAuth("vertex-ai", "GOOGLE_APPLICATION_CREDENTIALS", method),
                {"AGY_ADC_AUTH": "true", "GOOGLE_APPLICATION_CREDENTIALS": gac})

    if _assigned_service_account():
        return scion_harness.ResolvedAuth("vertex-ai", spec_entry=method), {"AGY_ADC_AUTH": "true"}

    if explicit:
        raise scion_harness.ProvisionError(
            f"antigravity-ui: auth type {explicit!r} selected but no credentials found; {method.hint}"
        )
    return scion_harness.ResolvedAuth("none"), {}


def provision(ctx: scion_harness.ProvisionContext) -> None:
    resolved, env = resolve_auth(ctx)
    status = seed_update_off(ctx)
    extra = {"vertex_ai": True} if resolved.method == "vertex-ai" else None
    ctx.write_outputs(resolved, env=env, extra=extra)
    ctx.info(f"method={resolved.method} app_storage={status}")


if __name__ == "__main__":
    scion_harness.run("antigravity-ui", provision)
