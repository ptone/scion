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

"""Common type models shared across resources."""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, ConfigDict


class _ScionModel(BaseModel):
    """Base model for all Scion types.

    Configures sensible defaults: camelCase aliases, extra fields
    ignored, and population by field name.
    """

    model_config = ConfigDict(
        populate_by_name=True,
        extra="ignore",
    )


class HealthResponse(_ScionModel):
    """Response from the /healthz endpoint."""

    status: str
    version: str = ""
    scion_version: str = ""
    uptime: str = ""
    checks: dict[str, str] | None = None
    web: dict[str, Any] | None = None
    hub: dict[str, Any] | None = None
    broker: dict[str, Any] | None = None

    model_config = ConfigDict(
        populate_by_name=True,
        extra="ignore",
        alias_generator=None,
    )


class PaginationParams(_ScionModel):
    """Parameters for paginated list requests."""

    cursor: str | None = None
    limit: int | None = None

    def to_query(self) -> dict[str, str]:
        """Convert to query parameter dict."""
        params: dict[str, str] = {}
        if self.cursor is not None:
            params["cursor"] = self.cursor
        if self.limit is not None:
            params["limit"] = str(self.limit)
        return params


class ListMeta(_ScionModel):
    """Pagination metadata returned in list responses."""

    next_cursor: str | None = None
    total_count: int | None = None
