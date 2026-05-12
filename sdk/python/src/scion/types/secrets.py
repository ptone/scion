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

"""Secret type models for the Scion Python SDK.

Mirrors Go types in ``pkg/hubclient/types.go`` and ``pkg/hubclient/secrets.go``.
Note: Secret values are write-only and never returned by the API.
"""

from __future__ import annotations

from datetime import datetime
from typing import Any

from pydantic import ConfigDict, Field

from scion.types.common import _ScionModel


class Secret(_ScionModel):
    """Secret metadata from the Hub API.

    Note: Secret values are write-only and never returned by the API.

    Attributes:
        id: Hub UUID.
        key: Secret key name.
        secret_ref: External secret reference (e.g. GCP Secret Manager URI).
        secret_type: Secret type (``"environment"``, ``"variable"``, ``"file"``).
        target: Projection target (defaults to ``key``).
        scope: Scope type (``"user"``, ``"project"``, ``"runtime_broker"``).
        scope_id: ID of the scoped entity.
        description: Human-readable description.
        injection_mode: ``"always"`` or ``"as_needed"``.
        allow_progeny: Whether creator's progeny agents can access this secret.
        version: Secret version number (incremented on update).
        created: Creation timestamp.
        updated: Last-updated timestamp.
    """

    id: str = ""
    key: str = ""
    secret_ref: str | None = Field(None, alias="secretRef")
    secret_type: str | None = Field(None, alias="type")
    target: str | None = None
    scope: str = ""
    scope_id: str = Field("", alias="scopeId")
    description: str | None = None
    injection_mode: str | None = Field(None, alias="injectionMode")
    allow_progeny: bool | None = Field(None, alias="allowProgeny")
    version: int = 0
    created: datetime | None = None
    updated: datetime | None = None
    created_by: str | None = Field(None, alias="createdBy")
    updated_by: str | None = Field(None, alias="updatedBy")


class SetSecretRequest(_ScionModel):
    """Request body for setting a secret."""

    value: str
    scope: str | None = None
    scope_id: str | None = Field(None, alias="scopeId")
    description: str | None = None
    injection_mode: str | None = Field(None, alias="injectionMode")
    secret_type: str | None = Field(None, alias="type")
    target: str | None = None
    allow_progeny: bool | None = Field(None, alias="allowProgeny")

    model_config = ConfigDict(
        populate_by_name=True,
        extra="ignore",
        ser_json_by_alias=True,
    )

    def model_dump_api(self) -> dict[str, Any]:
        """Dump model for API requests, using camelCase aliases and excluding None."""
        return self.model_dump(by_alias=True, exclude_none=True)


class SetSecretResponse(_ScionModel):
    """Response from setting a secret."""

    secret: Secret | None = None
    created: bool = False


class ListSecretResponse(_ScionModel):
    """Response from listing secrets."""

    secrets: list[Secret] = Field(default_factory=list)
    scope: str = ""
    scope_id: str = Field("", alias="scopeId")
