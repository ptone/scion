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

"""Error types for the Scion Python SDK.

Maps HTTP status codes and API error responses to typed exceptions,
mirroring the Go apiclient error handling.
"""

from __future__ import annotations

import contextlib
from typing import Any


class ScionError(Exception):
    """Base exception for all Scion SDK errors."""

    status_code: int | None
    code: str | None
    message: str
    request_id: str | None
    details: dict[str, Any] | None

    def __init__(
        self,
        message: str,
        *,
        status_code: int | None = None,
        code: str | None = None,
        request_id: str | None = None,
        details: dict[str, Any] | None = None,
    ) -> None:
        self.message = message
        self.status_code = status_code
        self.code = code
        self.request_id = request_id
        self.details = details
        super().__init__(self._format_message())

    def _format_message(self) -> str:
        parts = []
        if self.code:
            parts.append(self.code)
        parts.append(self.message)
        if self.status_code is not None:
            parts.append(f"(status: {self.status_code})")
        if self.request_id:
            parts.append(f"(request: {self.request_id})")
        return ": ".join(parts[:2]) + (" " + " ".join(parts[2:]) if len(parts) > 2 else "")


class AuthenticationError(ScionError):
    """Raised on 401 Unauthorized responses."""


class PermissionError(ScionError):
    """Raised on 403 Forbidden responses."""


class NotFoundError(ScionError):
    """Raised on 404 Not Found responses."""


class ConflictError(ScionError):
    """Raised on 409 Conflict responses."""


class ValidationError(ScionError):
    """Raised on 400 Bad Request responses."""


class RateLimitError(ScionError):
    """Raised on 429 Too Many Requests responses.

    Attributes:
        retry_after: Seconds to wait before retrying, if provided by the server.
    """

    retry_after: float | None

    def __init__(
        self,
        message: str,
        *,
        retry_after: float | None = None,
        **kwargs: Any,
    ) -> None:
        self.retry_after = retry_after
        super().__init__(message, **kwargs)


class ServerError(ScionError):
    """Raised on 5xx server error responses."""


class ConnectionError(ScionError):
    """Raised when a connection to the API cannot be established."""


class StreamError(ScionError):
    """Raised when an error occurs during streaming responses."""


# Standard error codes matching the Go API specification.
ERR_CODE_INVALID_REQUEST = "invalid_request"
ERR_CODE_VALIDATION_ERROR = "validation_error"
ERR_CODE_UNAUTHORIZED = "unauthorized"
ERR_CODE_FORBIDDEN = "forbidden"
ERR_CODE_NOT_FOUND = "not_found"
ERR_CODE_CONFLICT = "conflict"
ERR_CODE_VERSION_CONFLICT = "version_conflict"
ERR_CODE_UNPROCESSABLE = "unprocessable"
ERR_CODE_RATE_LIMITED = "rate_limited"
ERR_CODE_INTERNAL_ERROR = "internal_error"
ERR_CODE_RUNTIME_ERROR = "runtime_error"
ERR_CODE_UNAVAILABLE = "unavailable"


_STATUS_TO_CODE: dict[int, str] = {
    400: ERR_CODE_INVALID_REQUEST,
    401: ERR_CODE_UNAUTHORIZED,
    403: ERR_CODE_FORBIDDEN,
    404: ERR_CODE_NOT_FOUND,
    409: ERR_CODE_CONFLICT,
    422: ERR_CODE_UNPROCESSABLE,
    429: ERR_CODE_RATE_LIMITED,
    503: ERR_CODE_UNAVAILABLE,
}

_STATUS_TO_EXCEPTION: dict[int, type[ScionError]] = {
    400: ValidationError,
    401: AuthenticationError,
    403: PermissionError,
    404: NotFoundError,
    409: ConflictError,
    429: RateLimitError,
}


def parse_error_response(
    status_code: int,
    body: bytes,
    headers: dict[str, str] | None = None,
) -> ScionError:
    """Parse an HTTP error response into the appropriate ScionError subclass.

    Mirrors the Go ``ParseErrorResponse`` function in ``pkg/apiclient/errors.go``.
    """
    headers = headers or {}

    # Defaults
    code = _STATUS_TO_CODE.get(status_code)
    if code is None:
        code = ERR_CODE_INTERNAL_ERROR if status_code >= 500 else ERR_CODE_INVALID_REQUEST
    message = _default_message_for_status(status_code)
    request_id = headers.get("x-request-id")
    details: dict[str, Any] | None = None

    # Try to parse structured error body
    if body:
        try:
            import json

            data = json.loads(body)
            err_obj = data.get("error", {})
            if isinstance(err_obj, dict):
                if err_obj.get("code"):
                    code = err_obj["code"]
                if err_obj.get("message"):
                    message = err_obj["message"]
                if err_obj.get("details"):
                    details = err_obj["details"]
                if err_obj.get("requestId"):
                    request_id = err_obj["requestId"]
        except (ValueError, KeyError):
            # If body is not valid JSON, use it as message if short enough
            if len(body) < 500:
                message = body.decode("utf-8", errors="replace")

    kwargs: dict[str, Any] = {
        "status_code": status_code,
        "code": code,
        "request_id": request_id,
        "details": details,
    }

    # Select exception class
    exc_cls = _STATUS_TO_EXCEPTION.get(status_code)
    if exc_cls is RateLimitError:
        retry_after_str = headers.get("retry-after")
        retry_after: float | None = None
        if retry_after_str:
            with contextlib.suppress(ValueError):
                retry_after = float(retry_after_str)
        return RateLimitError(message, retry_after=retry_after, **kwargs)

    if exc_cls is not None:
        return exc_cls(message, **kwargs)

    if status_code >= 500:
        return ServerError(message, **kwargs)

    return ScionError(message, **kwargs)


def _default_message_for_status(status_code: int) -> str:
    defaults: dict[int, str] = {
        400: "Bad Request",
        401: "Unauthorized",
        403: "Forbidden",
        404: "Not Found",
        409: "Conflict",
        422: "Unprocessable Entity",
        429: "Too Many Requests",
        500: "Internal Server Error",
        502: "Bad Gateway",
        503: "Service Unavailable",
        504: "Gateway Timeout",
    }
    return defaults.get(status_code, f"HTTP {status_code}")
