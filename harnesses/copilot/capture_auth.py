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
"""Capture-auth for Copilot: not supported from an agent.

Copilot keeps its sign-in in ~/.copilot/config.json, which is also its
settings file. The hub refuses that file as a target for a secret written
from an agent, so this script does not call the hub. It explains how to
provide Copilot credentials instead and exits non-zero.
"""

import sys

UNSUPPORTED = (
    "capture-auth: Copilot sign-in (~/.copilot/config.json) cannot be stored "
    "from an agent: it is also Copilot's settings file, so the hub refuses it. "
    "Set COPILOT_GITHUB_TOKEN or GH_TOKEN as a profile or project secret "
    "instead (for example with 'scion hub secret set'), then restart the agent."
)


def main() -> int:
    print(UNSUPPORTED, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
