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

"""Tests for the error handling module."""

import json

from scion._errors import (
    AuthenticationError,
    ConflictError,
    NotFoundError,
    PermissionError,
    RateLimitError,
    ScionError,
    ServerError,
    ValidationError,
    parse_error_response,
)


class TestScionError:
    def test_basic_error(self) -> None:
        err = ScionError("something went wrong", status_code=500, code="internal_error")
        assert err.message == "something went wrong"
        assert err.status_code == 500
        assert err.code == "internal_error"
        assert "internal_error" in str(err)
        assert "something went wrong" in str(err)

    def test_error_with_request_id(self) -> None:
        err = ScionError("fail", status_code=500, request_id="req-123")
        assert "req-123" in str(err)

    def test_error_without_optional_fields(self) -> None:
        err = ScionError("plain error")
        assert err.status_code is None
        assert err.code is None
        assert err.request_id is None
        assert err.details is None


class TestParseErrorResponse:
    def test_parse_400_structured(self) -> None:
        body = json.dumps({
            "error": {
                "code": "validation_error",
                "message": "name is required",
                "requestId": "abc-123",
            }
        }).encode()
        err = parse_error_response(400, body)
        assert isinstance(err, ValidationError)
        assert err.code == "validation_error"
        assert err.message == "name is required"
        assert err.request_id == "abc-123"
        assert err.status_code == 400

    def test_parse_401(self) -> None:
        body = json.dumps({"error": {"code": "unauthorized", "message": "bad token"}}).encode()
        err = parse_error_response(401, body)
        assert isinstance(err, AuthenticationError)

    def test_parse_403(self) -> None:
        body = json.dumps({"error": {"code": "forbidden", "message": "no access"}}).encode()
        err = parse_error_response(403, body)
        assert isinstance(err, PermissionError)

    def test_parse_404(self) -> None:
        body = json.dumps({"error": {"code": "not_found", "message": "agent not found"}}).encode()
        err = parse_error_response(404, body)
        assert isinstance(err, NotFoundError)

    def test_parse_409(self) -> None:
        body = json.dumps({"error": {"code": "conflict", "message": "already exists"}}).encode()
        err = parse_error_response(409, body)
        assert isinstance(err, ConflictError)

    def test_parse_429_with_retry_after(self) -> None:
        body = json.dumps({
            "error": {"code": "rate_limited", "message": "too many requests"}
        }).encode()
        headers = {"retry-after": "30"}
        err = parse_error_response(429, body, headers=headers)
        assert isinstance(err, RateLimitError)
        assert err.retry_after == 30.0

    def test_parse_429_without_retry_after(self) -> None:
        body = json.dumps({
            "error": {"code": "rate_limited", "message": "slow down"}
        }).encode()
        err = parse_error_response(429, body)
        assert isinstance(err, RateLimitError)
        assert err.retry_after is None

    def test_parse_500(self) -> None:
        body = json.dumps({
            "error": {"code": "internal_error", "message": "oops"}
        }).encode()
        err = parse_error_response(500, body)
        assert isinstance(err, ServerError)

    def test_parse_502(self) -> None:
        body = b"Bad Gateway"
        err = parse_error_response(502, body)
        assert isinstance(err, ServerError)
        assert err.status_code == 502

    def test_parse_empty_body(self) -> None:
        err = parse_error_response(500, b"")
        assert isinstance(err, ServerError)
        assert err.status_code == 500

    def test_parse_non_json_body(self) -> None:
        err = parse_error_response(400, b"not json")
        assert isinstance(err, ValidationError)
        assert "not json" in err.message

    def test_parse_with_details(self) -> None:
        body = json.dumps({
            "error": {
                "code": "validation_error",
                "message": "invalid fields",
                "details": {"field": "name", "reason": "too short"},
            }
        }).encode()
        err = parse_error_response(400, body)
        assert err.details is not None
        assert err.details["field"] == "name"

    def test_request_id_from_header(self) -> None:
        body = json.dumps({"error": {"code": "not_found", "message": "gone"}}).encode()
        headers = {"x-request-id": "hdr-req-456"}
        err = parse_error_response(404, body, headers=headers)
        # The body doesn't have requestId, so the header value is used
        assert err.request_id == "hdr-req-456"

    def test_request_id_body_overrides_header(self) -> None:
        body = json.dumps({
            "error": {
                "code": "not_found",
                "message": "gone",
                "requestId": "body-req",
            }
        }).encode()
        headers = {"x-request-id": "hdr-req"}
        err = parse_error_response(404, body, headers=headers)
        # Body requestId should override header
        assert err.request_id == "body-req"
