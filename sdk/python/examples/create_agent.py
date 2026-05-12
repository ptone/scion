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

"""Create an agent, start it, and wait for completion.

This example demonstrates:
- Creating a Scion client with authentication
- Creating a new agent in a project
- Polling the agent's status until it completes
- Streaming events to watch agent progress in real-time

Usage:
    export SCION_API_TOKEN="your-token"
    python create_agent.py --hub-url https://hub.example.com --project proj-123
"""

from __future__ import annotations

import argparse
import sys
import time

from scion import NotFoundError, ScionClient, ScionError


def create_and_watch_agent(
    hub_url: str,
    project_id: str,
    agent_name: str = "example-agent",
    task: str = "Hello from the Python SDK!",
) -> None:
    """Create an agent and watch it until completion."""

    with ScionClient(hub_url) as client:
        # Verify connectivity
        health = client.health()
        print(f"Connected to hub (status: {health.status})")

        # Create the agent
        print(f"\nCreating agent '{agent_name}' in project '{project_id}'...")
        response = client.agents.create(
            name=agent_name,
            project_id=project_id,
            task=task,
        )

        if response.warnings:
            for warning in response.warnings:
                print(f"  Warning: {warning}")

        agent = response.agent
        if agent is None:
            print("Error: No agent returned in response")
            sys.exit(1)

        agent_id = agent.id
        print(f"Agent created: {agent_id} (phase: {agent.phase})")

        # Poll for completion (simple approach)
        print("\nWaiting for agent to complete...")
        terminal_phases = {"stopped", "completed", "failed", "error"}
        while True:
            try:
                agent = client.agents.get(agent_id)
            except NotFoundError:
                print("Agent was deleted")
                break

            phase = agent.phase or "unknown"
            activity = agent.activity or ""
            status_line = f"  Phase: {phase}"
            if activity:
                status_line += f" | Activity: {activity}"
            print(status_line)

            if phase in terminal_phases:
                print(f"\nAgent reached terminal phase: {phase}")
                break

            time.sleep(5)

        # Print final agent state
        try:
            agent = client.agents.get(agent_id)
            print(f"\nFinal state:")
            print(f"  Name:    {agent.name}")
            print(f"  Phase:   {agent.phase}")
            print(f"  Status:  {agent.status}")
            if agent.task_summary:
                print(f"  Summary: {agent.task_summary}")
        except NotFoundError:
            print("Agent no longer exists")


def create_and_stream_events(
    hub_url: str,
    project_id: str,
    agent_name: str = "stream-example-agent",
    task: str = "Hello from the Python SDK (streaming)!",
) -> None:
    """Create an agent and stream its events in real-time."""

    with ScionClient(hub_url) as client:
        # Create the agent
        print(f"Creating agent '{agent_name}'...")
        response = client.agents.create(
            name=agent_name,
            project_id=project_id,
            task=task,
        )
        agent = response.agent
        if agent is None:
            print("Error: No agent returned")
            sys.exit(1)

        agent_id = agent.id
        print(f"Agent created: {agent_id}")

        # Stream events
        print("\nStreaming events (Ctrl+C to stop)...")
        try:
            with client.agents.stream_events(agent_id) as stream:
                for event in stream:
                    print(f"[{event.type}] status={event.status}, phase={event.phase}")
                    if event.message:
                        print(f"  {event.message}")

                    # Stop when the agent reaches a terminal state
                    if event.status in ("completed", "failed", "error"):
                        print(f"\nAgent finished: {event.status}")
                        break
        except KeyboardInterrupt:
            print("\nStopped by user")


def main() -> None:
    parser = argparse.ArgumentParser(description="Create and watch a Scion agent")
    parser.add_argument("--hub-url", required=True, help="Scion Hub URL")
    parser.add_argument("--project", required=True, help="Project ID")
    parser.add_argument("--name", default="example-agent", help="Agent name")
    parser.add_argument("--task", default="Hello from the Python SDK!", help="Agent task")
    parser.add_argument(
        "--stream",
        action="store_true",
        help="Use SSE streaming instead of polling",
    )
    args = parser.parse_args()

    try:
        if args.stream:
            create_and_stream_events(args.hub_url, args.project, args.name, args.task)
        else:
            create_and_watch_agent(args.hub_url, args.project, args.name, args.task)
    except ScionError as e:
        print(f"API error: {e.message}", file=sys.stderr)
        if e.request_id:
            print(f"Request ID: {e.request_id}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
