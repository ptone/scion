# ATIF-v1.8 Go Types and Schema (`pkg/trajectory/atif/v1_8`)

This package provides Go types, lossless decoding, canonical encoding, and
runtime JSON Schema + semantic validation for Harbor's **ATIF-v1.8** (Agent
Trajectory Interchange Format) specification.

## Provenance and License

- **Upstream project:** [Harbor Framework](https://github.com/harbor-framework/harbor)
- **License:** Apache-2.0 (see [`LICENSE`](./LICENSE), copied from `harbor==0.27.0`)
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
field ordering matching Harbor's Pydantic `model_fields` declaration order.
`TestGeneratedTypesUpToDate` verifies `types_gen.go` matches a fresh generator
run.

## Encoding and Lossless Decoding

- **Encoding:** All writers MUST use `EncodeTrajectory` / `EncodeStep` (or
  `EncodeTrajectoryIndent` for fixtures) so byte-identical determinism holds
  across writers.
- **Decoding:** Callers decoding external or stored ATIF JSON MUST use
  `ParseTrajectory` / `ParseStep` (which use `json.Decoder.UseNumber`) rather
  than plain `json.Unmarshal`, so integers above 2^53 inside `extra`,
  `tool_calls[].arguments`, and `tool_definitions` are preserved without
  `float64` precision loss.
- **Empty objects (`omitempty`):** Empty optional maps such as `"extra": {}`
  are omitted when re-encoded (`omitempty`). In Harbor's ATIF models, an empty
  `extra` object and an omitted `extra` field are semantically equivalent.

## Schema Drift Check

`make check-atif-schema` (implemented by `hack/check-atif-schema.sh`) creates a
throwaway Python ≥ 3.12 venv from `requirements-schema.txt`, re-exports
`Trajectory.model_json_schema()`, diffs it against `schema.json`, validates
any golden `*.atif.json` fixtures under `pkg/trajectory/convert/` against
Harbor's Pydantic `Trajectory.model_validate_json`, and verifies that every
negative fixture under `pkg/trajectory/atif/v1_8/testdata/invalid/` is rejected
by both Go and Harbor Pydantic.

Locally, the check skips with a clear message if `python3` (≥ 3.12), `venv`,
or PyPI network access is unavailable. In CI (`CI=true`), any unavailability
or diff fails the job.
