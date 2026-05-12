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

"""CRUD operations on secrets.

This example demonstrates:
- Listing secrets in different scopes (user, project)
- Creating and updating secrets
- Reading secret metadata (values are write-only)
- Deleting secrets

Usage:
    export SCION_API_TOKEN="your-token"

    # List user-scoped secrets
    python manage_secrets.py --hub-url https://hub.example.com list

    # List project-scoped secrets
    python manage_secrets.py --hub-url https://hub.example.com list \
        --scope project --scope-id proj-123

    # Set a secret
    python manage_secrets.py --hub-url https://hub.example.com set \
        MY_API_KEY "sk-secret-value" --description "External API key"

    # Get secret metadata
    python manage_secrets.py --hub-url https://hub.example.com get MY_API_KEY

    # Delete a secret
    python manage_secrets.py --hub-url https://hub.example.com delete MY_API_KEY
"""

from __future__ import annotations

import argparse
import sys

from scion import NotFoundError, ScionClient, ScionError


def cmd_list(client: ScionClient, args: argparse.Namespace) -> None:
    """List secrets for the given scope."""
    response = client.secrets.list(scope=args.scope, scope_id=args.scope_id)

    print(f"Scope: {response.scope} ({response.scope_id or 'default'})")
    print(f"Secrets: {len(response.secrets)}")
    print("-" * 60)

    if not response.secrets:
        print("(no secrets found)")
        return

    for secret in response.secrets:
        print(f"  Key:          {secret.key}")
        print(f"  Type:         {secret.secret_type or 'environment'}")
        print(f"  Description:  {secret.description or '(none)'}")
        print(f"  Injection:    {secret.injection_mode or 'as_needed'}")
        print(f"  Version:      {secret.version}")
        if secret.created:
            print(f"  Created:      {secret.created}")
        if secret.updated:
            print(f"  Updated:      {secret.updated}")
        print()


def cmd_get(client: ScionClient, args: argparse.Namespace) -> None:
    """Get metadata for a single secret."""
    try:
        secret = client.secrets.get(
            args.key,
            scope=args.scope,
            scope_id=args.scope_id,
        )
    except NotFoundError:
        print(f"Secret '{args.key}' not found", file=sys.stderr)
        sys.exit(1)

    print(f"Key:            {secret.key}")
    print(f"ID:             {secret.id}")
    print(f"Type:           {secret.secret_type or 'environment'}")
    print(f"Scope:          {secret.scope}")
    print(f"Scope ID:       {secret.scope_id}")
    print(f"Description:    {secret.description or '(none)'}")
    print(f"Target:         {secret.target or secret.key}")
    print(f"Injection Mode: {secret.injection_mode or 'as_needed'}")
    print(f"Allow Progeny:  {secret.allow_progeny}")
    print(f"Version:        {secret.version}")
    print(f"Created:        {secret.created}")
    print(f"Updated:        {secret.updated}")
    print(f"Created By:     {secret.created_by}")
    print(f"Updated By:     {secret.updated_by}")
    print()
    print("Note: Secret values are write-only and never returned by the API.")


def cmd_set(client: ScionClient, args: argparse.Namespace) -> None:
    """Create or update a secret."""
    result = client.secrets.set(
        args.key,
        args.value,
        scope=args.scope,
        scope_id=args.scope_id,
        description=args.description,
        injection_mode=args.injection_mode,
        secret_type=args.type,
        target=args.target,
        allow_progeny=args.allow_progeny,
    )

    action = "Created" if result.created else "Updated"
    print(f"{action} secret: {args.key}")
    if result.secret:
        print(f"  Scope:   {result.secret.scope}")
        print(f"  Version: {result.secret.version}")


def cmd_delete(client: ScionClient, args: argparse.Namespace) -> None:
    """Delete a secret."""
    try:
        client.secrets.delete(
            args.key,
            scope=args.scope,
            scope_id=args.scope_id,
        )
        print(f"Deleted secret: {args.key}")
    except NotFoundError:
        print(f"Secret '{args.key}' not found", file=sys.stderr)
        sys.exit(1)


def main() -> None:
    parser = argparse.ArgumentParser(description="Manage Scion secrets")
    parser.add_argument("--hub-url", required=True, help="Scion Hub URL")
    parser.add_argument("--scope", default=None, help="Secret scope (user, project, runtime_broker)")
    parser.add_argument("--scope-id", default=None, help="Scope entity ID")

    subparsers = parser.add_subparsers(dest="command", required=True)

    # list
    subparsers.add_parser("list", help="List secrets")

    # get
    get_parser = subparsers.add_parser("get", help="Get secret metadata")
    get_parser.add_argument("key", help="Secret key")

    # set
    set_parser = subparsers.add_parser("set", help="Create or update a secret")
    set_parser.add_argument("key", help="Secret key")
    set_parser.add_argument("value", help="Secret value")
    set_parser.add_argument("--description", help="Human-readable description")
    set_parser.add_argument(
        "--injection-mode",
        choices=["always", "as_needed"],
        help="Injection mode (default: as_needed)",
    )
    set_parser.add_argument(
        "--type",
        choices=["environment", "variable", "file"],
        help="Secret type (default: environment)",
    )
    set_parser.add_argument("--target", help="Projection target (defaults to key)")
    set_parser.add_argument(
        "--allow-progeny",
        action="store_true",
        default=None,
        help="Allow progeny agents to access (user scope only)",
    )

    # delete
    delete_parser = subparsers.add_parser("delete", help="Delete a secret")
    delete_parser.add_argument("key", help="Secret key")

    args = parser.parse_args()

    try:
        with ScionClient(args.hub_url) as client:
            commands = {
                "list": cmd_list,
                "get": cmd_get,
                "set": cmd_set,
                "delete": cmd_delete,
            }
            commands[args.command](client, args)
    except ScionError as e:
        print(f"API error: {e.message}", file=sys.stderr)
        if e.request_id:
            print(f"Request ID: {e.request_id}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
