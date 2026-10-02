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

"""Model resolution tests for the gemini-cli provisioner (ptone/scion#2674)."""

from __future__ import annotations

import importlib.util
import json
import os
import shutil
import tempfile
import unittest
from contextlib import contextmanager
from typing import Any

HERE = os.path.dirname(os.path.abspath(__file__))
PROVISION_PATH = os.path.join(HERE, "provision.py")
CONFIG_PATH = os.path.join(HERE, "config.yaml")
IMAGE_SETTINGS_PATH = os.path.join(HERE, "home", ".gemini", "settings.json")

SPEC = importlib.util.spec_from_file_location("gemini_cli_provision", PROVISION_PATH)
assert SPEC is not None and SPEC.loader is not None
provision = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(provision)

scion_harness = provision.scion_harness


def _load_real_model_config() -> dict[str, Any]:
    """Read the top-level `model` and `model_aliases` from config.yaml.

    Stdlib-only (no PyYAML in the image), so this is a minimal reader for
    the two flat keys under test. Tests run against the real file rather
    than a restated copy, so a config.yaml edit that drops the default or
    an alias fails here.
    """
    cfg: dict[str, Any] = {}
    aliases: dict[str, str] = {}
    in_aliases = False
    with open(CONFIG_PATH, encoding="utf-8") as f:
        for raw in f:
            line = raw.split("#", 1)[0].rstrip()
            if not line:
                continue
            if in_aliases and line.startswith("  "):
                key, _, val = line.strip().partition(":")
                aliases[key.strip()] = val.strip()
                continue
            in_aliases = False
            if line.startswith("model:"):
                cfg["model"] = line.partition(":")[2].strip()
            elif line == "model_aliases:":
                in_aliases = True
    cfg["model_aliases"] = aliases
    return cfg


REAL_CONFIG = _load_real_model_config()


@contextmanager
def env_vars(**values: str | None):
    """Temporarily set (or unset, with None) environment variables."""
    previous = {k: os.environ.get(k) for k in values}
    for key, val in values.items():
        if val is None:
            os.environ.pop(key, None)
        else:
            os.environ[key] = val
    try:
        yield
    finally:
        for key, val in previous.items():
            if val is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = val


def _apply(harness_config: dict[str, Any], scion_model: str | None) -> dict[str, Any]:
    """Seed the image settings.json into a temp HOME, run _apply_model, and
    return the resulting model section."""
    with tempfile.TemporaryDirectory() as home:
        os.makedirs(os.path.join(home, ".gemini"))
        settings_path = os.path.join(home, ".gemini", "settings.json")
        shutil.copy(IMAGE_SETTINGS_PATH, settings_path)
        manifest = {
            "harness_bundle_dir": os.path.join(home, ".scion", "harness"),
            "harness_config": harness_config,
        }
        ctx = scion_harness.ProvisionContext("gemini-cli", manifest)
        with env_vars(HOME=home, SCION_MODEL=scion_model):
            provision._apply_model(ctx)
        with open(settings_path, encoding="utf-8") as f:
            return json.load(f).get("model", {})


class HarnessConfigDefaultTest(unittest.TestCase):
    def test_config_yaml_declares_medium_default(self) -> None:
        self.assertEqual(REAL_CONFIG.get("model"), "medium")
        self.assertTrue(REAL_CONFIG["model_aliases"].get("medium"))

    def test_image_settings_pins_no_model(self) -> None:
        with open(IMAGE_SETTINGS_PATH, encoding="utf-8") as f:
            model = json.load(f)["model"]
        self.assertNotIn("name", model)
        self.assertIs(model.get("skipNextSpeakerCheck"), True)


class ApplyModelTest(unittest.TestCase):
    def test_empty_scion_model_falls_back_to_harness_config_default(self) -> None:
        model = _apply(dict(REAL_CONFIG), None)
        self.assertEqual(model.get("name"), REAL_CONFIG["model_aliases"]["medium"])
        self.assertIs(model.get("skipNextSpeakerCheck"), True)

    def test_blank_scion_model_falls_back_to_harness_config_default(self) -> None:
        model = _apply(dict(REAL_CONFIG), "   ")
        self.assertEqual(model.get("name"), REAL_CONFIG["model_aliases"]["medium"])

    def test_explicit_concrete_model_wins(self) -> None:
        model = _apply(dict(REAL_CONFIG), "Gemini-Custom-Preview")
        self.assertEqual(model.get("name"), "Gemini-Custom-Preview")

    def test_explicit_alias_resolves_and_wins(self) -> None:
        model = _apply(dict(REAL_CONFIG), "large")
        self.assertEqual(model.get("name"), REAL_CONFIG["model_aliases"]["large"])

    def test_no_model_anywhere_writes_no_pin(self) -> None:
        cfg = {"model_aliases": dict(REAL_CONFIG["model_aliases"])}
        model = _apply(cfg, None)
        self.assertNotIn("name", model)


if __name__ == "__main__":
    unittest.main()
