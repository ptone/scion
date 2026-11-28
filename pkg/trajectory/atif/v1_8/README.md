# ATIF-v1.8 Go Types and Schema (`pkg/trajectory/atif/v1_8`)

This package provides Go types and runtime JSON Schema validation for Harbor's
**ATIF-v1.8** (Agent Trajectory Interchange Format) specification.

## Provenance and License

- **Upstream project:** [Harbor Framework](https://github.com/harbor-framework/harbor)
- **License:** Apache-2.0
- **Pinned version:** `harbor==0.27.0`
- **Export command:**

```bash
python3 -m venv /tmp/harbor-schema-venv
/tmp/harbor-schema-venv/bin/pip install --require-hashes --no-deps -r pkg/trajectory/atif/v1_8/requirements-schema.txt
/tmp/harbor-schema-venv/bin/python -c '
import json
from harbor.models.trajectories import Trajectory
print(json.dumps(Trajectory.model_json_schema(), indent=2, sort_keys=True))
' > pkg/trajectory/atif/v1_8/schema.json
```

## Regenerating Go Types

```bash
go generate ./pkg/trajectory/atif/v1_8/...
```

`gen_types.go` reads `schema.json` and generates `types_gen.go` with stable
field ordering matching Harbor's Pydantic models. Runtime validation uses
`github.com/santhosh-tekuri/jsonschema/v6` against the embedded `schema.json`
without any Python runtime dependency.

## Schema Drift Check

`make check-atif-schema` (implemented by `hack/check-atif-schema.sh`) creates a
throwaway Python venv from `requirements-schema.txt`, re-exports
`Trajectory.model_json_schema()`, diffs it against `schema.json`, and validates
any golden `*.atif.json` fixtures under `pkg/trajectory/convert/` against
Harbor's Pydantic `Trajectory.model_validate_json`.

Locally, the check skips with a clear message if `python3`, `venv`, or PyPI
network access is unavailable. In CI (`CI=true`), any unavailability or diff
fails the job.
