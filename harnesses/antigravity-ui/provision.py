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
       b. the ADC file Scion maps into the container from
          GOOGLE_APPLICATION_CREDENTIALS, at
          ~/.config/gcloud/application_default_credentials.json (a file
          mapping in the auth candidates, or the file being present);
       c. a dedicated GCP service account assigned to the agent: the broker
          sets SCION_METADATA_SA_EMAIL for it, with SCION_METADATA_MODE
          "assign", or "passthrough" when the runtime turns an assign into
          its own workload identity (Kubernetes).
     Each writes AGY_ADC_AUTH=true into the env overlay, plus
     GOOGLE_APPLICATION_CREDENTIALS for a file. Otherwise the method is
     "none" and this provisioner sets no AGY_ADC_AUTH; the app then waits
     for an interactive sign-in code, which the wrapper reports.

     Plain passthrough (the host's own identity, no SA email) resolves to
     "none" even when vertex-ai was selected, and does not fail. An operator
     who wants the host identity sets AGY_ADC_AUTH=true in the harness-config
     env: the app reads it from the container environment. This provisioner
     cannot see it, because it runs with a minimal environment (HOME, PATH,
     LANG, TZ and SCION_* only).
  2. Turn the app's self-update check off: merge
     {"autoCheckForUpdates": false} into ~/.config/Antigravity/app_storage.json,
     keeping every other key. This runs before every start (pre-start).
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
# Where Scion mounts the ADC file named by GOOGLE_APPLICATION_CREDENTIALS
# (pkg/harness ContainerScriptHarness.ResolveAuth).
ADC_MAPPED_PATH = "~/.config/gcloud/application_default_credentials.json"
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
    """A dedicated SA is assigned: assign mode, or an assign the runtime
    rewrote to passthrough. Both carry the SA email; the host identity
    (plain passthrough) does not."""
    mode = os.environ.get("SCION_METADATA_MODE", "").strip()
    email = os.environ.get("SCION_METADATA_SA_EMAIL", "").strip()
    return mode in ("assign", "passthrough") and bool(email)


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

    mapped = scion_harness.expand_path(ADC_MAPPED_PATH)
    if any(scion_harness.expand_path(p) == mapped for p in ctx.file_paths) or os.path.isfile(mapped):
        return (scion_harness.ResolvedAuth("vertex-ai", spec_entry=method, auth_file=mapped),
                {"AGY_ADC_AUTH": "true", "GOOGLE_APPLICATION_CREDENTIALS": mapped})

    if _assigned_service_account():
        return scion_harness.ResolvedAuth("vertex-ai", spec_entry=method), {"AGY_ADC_AUTH": "true"}

    if os.environ.get("SCION_METADATA_MODE", "").strip() == "passthrough":
        # The host's own identity. Not used unless the operator sets
        # AGY_ADC_AUTH=true in the harness-config env, which the app reads.
        ctx.info("GCP identity passthrough: the host identity is used only if "
                 "AGY_ADC_AUTH=true is set in the harness-config env")
        return scion_harness.ResolvedAuth("none"), {}

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
