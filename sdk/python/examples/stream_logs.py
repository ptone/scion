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

"""Stream cloud logs from a running agent.

This example demonstrates:
- Connecting to the cloud log streaming SSE endpoint
- Filtering logs by severity
- Handling stream reconnection via Last-Event-ID
- Graceful shutdown on Ctrl+C

Usage:
    export SCION_API_TOKEN="your-token"
    python stream_logs.py --hub-url https://hub.example.com --agent agent-id

    # Filter to only ERROR logs
    python stream_logs.py --hub-url https://hub.example.com --agent agent-id --severity ERROR
"""

from __future__ import annotations

import argparse
import sys
from datetime import datetime

from scion import NotFoundError, ScionClient, ScionError, StreamError


def format_timestamp(ts: datetime | None) -> str:
    """Format a timestamp for display."""
    if ts is None:
        return "??:??:??"
    return ts.strftime("%H:%M:%S")


def severity_color(severity: str | None) -> str:
    """Return an ANSI color code for the given log severity."""
    colors = {
        "DEBUG": "\033[36m",     # Cyan
        "INFO": "\033[32m",      # Green
        "NOTICE": "\033[34m",    # Blue
        "WARNING": "\033[33m",   # Yellow
        "ERROR": "\033[31m",     # Red
        "CRITICAL": "\033[35m",  # Magenta
        "ALERT": "\033[35;1m",   # Bold Magenta
        "EMERGENCY": "\033[31;1m",  # Bold Red
    }
    return colors.get(severity or "", "\033[0m")


RESET = "\033[0m"


def stream_logs(
    hub_url: str,
    agent_id: str,
    severity: str | None = None,
    colorize: bool = True,
) -> None:
    """Stream cloud logs from an agent, printing each entry to stdout."""

    with ScionClient(hub_url) as client:
        # Verify the agent exists
        try:
            agent = client.agents.get(agent_id)
            print(f"Streaming logs for agent: {agent.name} ({agent.id})")
            print(f"Agent phase: {agent.phase}")
        except NotFoundError:
            print(f"Error: Agent '{agent_id}' not found", file=sys.stderr)
            sys.exit(1)

        if severity:
            print(f"Filtering: severity >= {severity}")

        print("-" * 60)

        # Open the SSE stream
        try:
            with client.agents.stream_cloud_logs(
                agent_id,
                severity=severity,
            ) as stream:
                for entry in stream:
                    ts = format_timestamp(entry.timestamp)
                    sev = (entry.severity or "UNKNOWN").ljust(8)
                    msg = entry.message or ""
                    source = entry.source or ""

                    if colorize:
                        color = severity_color(entry.severity)
                        line = f"{color}{ts} [{sev}]{RESET} {msg}"
                    else:
                        line = f"{ts} [{sev}] {msg}"

                    if source:
                        line += f" (source: {source})"

                    print(line)

        except StreamError as e:
            print(f"\nStream error: {e.message}", file=sys.stderr)
            sys.exit(1)
        except KeyboardInterrupt:
            print(f"\n\nStopped. Last event ID: {stream.last_event_id}")
            print("Use --last-event-id to resume from this point.")


def main() -> None:
    parser = argparse.ArgumentParser(description="Stream cloud logs from a Scion agent")
    parser.add_argument("--hub-url", required=True, help="Scion Hub URL")
    parser.add_argument("--agent", required=True, help="Agent ID to stream logs from")
    parser.add_argument(
        "--severity",
        choices=["DEBUG", "INFO", "NOTICE", "WARNING", "ERROR", "CRITICAL"],
        help="Minimum log severity to display",
    )
    parser.add_argument(
        "--no-color",
        action="store_true",
        help="Disable colored output",
    )
    args = parser.parse_args()

    try:
        stream_logs(
            hub_url=args.hub_url,
            agent_id=args.agent,
            severity=args.severity,
            colorize=not args.no_color,
        )
    except ScionError as e:
        print(f"API error: {e.message}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
