#!/usr/bin/env bash
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

# Checks that pkg/trajectory/atif/v1_8/schema.json matches the JSON Schema
# exported by the pinned harbor==0.27.0 Pydantic models, validates any golden
# *.atif.json fixtures under pkg/trajectory/convert/ against Harbor's Pydantic
# Trajectory.model_validate_json, and confirms Harbor rejects every negative
# fixture under pkg/trajectory/atif/v1_8/testdata/invalid/.
#
# Locally (CI unset), skips with exit 0 and a clear message if python3 (>=3.12),
# venv, or PyPI network access is unavailable. Under CI (CI non-empty), fails hard.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCHEMA_PATH="${REPO_ROOT}/pkg/trajectory/atif/v1_8/schema.json"
REQS_PATH="${REPO_ROOT}/pkg/trajectory/atif/v1_8/requirements-schema.txt"

skip_or_fail() {
    local msg="$1"
    if [ -n "${CI:-}" ]; then
        echo "ERROR (CI): ${msg}" >&2
        exit 1
    fi
    echo "SKIP (local): ${msg}"
    exit 0
}

if ! command -v python3 >/dev/null 2>&1; then
    skip_or_fail "python3 not found in PATH; cannot run ATIF-v1.8 schema drift check"
fi

if ! python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 12) else 1)'; then
    PY_VER="$(python3 --version 2>&1 || echo unknown)"
    skip_or_fail "python3 >= 3.12 is required for harbor==0.27.0 (found ${PY_VER})"
fi

TMPDIR_WORK="$(mktemp -d "${TMPDIR:-/tmp}/atif-schema-check.XXXXXX")"
trap 'rm -rf "${TMPDIR_WORK}"' EXIT

VENV="${TMPDIR_WORK}/venv"
if ! python3 -m venv "${VENV}" 2>"${TMPDIR_WORK}/venv.err"; then
    skip_or_fail "python3 -m venv failed ($(head -n 1 "${TMPDIR_WORK}/venv.err"))"
fi

if ! "${VENV}/bin/pip" install --quiet --disable-pip-version-check \
    --require-hashes --no-deps -r "${REQS_PATH}" 2>"${TMPDIR_WORK}/pip.err"; then
    skip_or_fail "pip install of pinned Harbor schema deps failed (network or PyPI unavailable?): $(tail -n 3 "${TMPDIR_WORK}/pip.err" | tr '\n' ' ')"
fi

EXPORTED="${TMPDIR_WORK}/exported.schema.json"
"${VENV}/bin/python" -c '
import json
from harbor.models.trajectories import Trajectory
print(json.dumps(Trajectory.model_json_schema(), indent=2, sort_keys=True))
' > "${EXPORTED}"

if ! diff -u "${SCHEMA_PATH}" "${EXPORTED}"; then
    echo "ERROR: pkg/trajectory/atif/v1_8/schema.json drifted from harbor==0.27.0 Trajectory.model_json_schema()" >&2
    exit 1
fi
echo "OK: pkg/trajectory/atif/v1_8/schema.json matches harbor==0.27.0"

# Validate any checked-in golden ATIF fixtures against Harbor Pydantic models (AC10).
GOLDEN_FILES=()
if [ -d "${REPO_ROOT}/pkg/trajectory/convert" ]; then
    while IFS= read -r -d '' f; do
        GOLDEN_FILES+=("$f")
    done < <(find "${REPO_ROOT}/pkg/trajectory/convert" -path '*/testdata/*' -name '*.atif.json' -print0 | sort -z)
fi

if [ "${#GOLDEN_FILES[@]}" -gt 0 ]; then
    "${VENV}/bin/python" - "${GOLDEN_FILES[@]}" <<'EOF'
import sys
from pathlib import Path
from harbor.models.trajectories import Trajectory

failed = 0
for path_str in sys.argv[1:]:
    p = Path(path_str)
    try:
        Trajectory.model_validate_json(p.read_text(encoding="utf-8"))
        print(f"OK (Harbor Pydantic): {p}")
    except Exception as exc:
        print(f"FAIL (Harbor Pydantic): {p}: {exc}", file=sys.stderr)
        failed += 1
if failed:
    sys.exit(1)
EOF
fi

# Verify Harbor Pydantic rejects every negative fixture (proves Go and Pydantic agree).
INVALID_DIR="${REPO_ROOT}/pkg/trajectory/atif/v1_8/testdata/invalid"
if [ -d "${INVALID_DIR}" ]; then
    INVALID_FILES=()
    while IFS= read -r -d '' f; do
        INVALID_FILES+=("$f")
    done < <(find "${INVALID_DIR}" -maxdepth 1 -name '*.json' -print0 | sort -z)

    if [ "${#INVALID_FILES[@]}" -gt 0 ]; then
        "${VENV}/bin/python" - "${INVALID_FILES[@]}" <<'EOF'
import sys
from pathlib import Path
from pydantic import ValidationError
from harbor.models.trajectories import Step, Trajectory

failed = 0
for path_str in sys.argv[1:]:
    p = Path(path_str)
    raw = p.read_text(encoding="utf-8")
    if p.name.endswith(".step.json"):
        model = Step
    elif p.name.endswith(".trajectory.json"):
        model = Trajectory
    else:
        print(f"FAIL: unknown negative fixture naming convention: {p}", file=sys.stderr)
        failed += 1
        continue
    try:
        model.model_validate_json(raw)
        print(f"FAIL (Harbor unexpectedly accepted negative fixture): {p}", file=sys.stderr)
        failed += 1
    except ValidationError:
        print(f"OK (Harbor rejected negative fixture): {p.name}")
if failed:
    sys.exit(1)
EOF
    fi
fi
