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

"""Scion Python SDK — client library for the Scion Hub API."""

from scion._async_client import AsyncScionClient
from scion._client import ScionClient
from scion._errors import (
    AuthenticationError,
    ConflictError,
    ConnectionError,
    NotFoundError,
    PermissionError,
    RateLimitError,
    ScionError,
    ServerError,
    StreamError,
    ValidationError,
)
from scion._pagination import AsyncPage, SyncPage

__version__ = "0.1.0"

__all__ = [
    # Clients
    "ScionClient",
    "AsyncScionClient",
    # Errors
    "ScionError",
    "AuthenticationError",
    "PermissionError",
    "NotFoundError",
    "ConflictError",
    "ValidationError",
    "RateLimitError",
    "ServerError",
    "ConnectionError",
    "StreamError",
    # Pagination
    "SyncPage",
    "AsyncPage",
]
