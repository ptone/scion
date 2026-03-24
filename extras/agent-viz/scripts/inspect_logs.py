#!/usr/bin/env python3
"""Inspect a GCP log JSON export to understand what agents, files, and events it contains.

Usage: python3 scripts/inspect_logs.py path/to/logs.json
"""

import json
import sys
from collections import Counter, defaultdict


def main():
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <log-file.json>")
        sys.exit(1)

    with open(sys.argv[1]) as f:
        entries = json.load(f)

    print(f"Total log entries: {len(entries)}")

    # Count by log type
    log_types = Counter()
    for e in entries:
        parts = e.get("logName", "").split("/")
        log_types[parts[-1]] += 1
    print("\nLog types:")
    for lt, count in log_types.most_common():
        print(f"  {lt}: {count}")

    # Find all agents from scion-agents
    agents_from_logs = {}
    for e in entries:
        if e.get("logName", "").endswith("scion-agents"):
            aid = e.get("labels", {}).get("agent_id", "")
            if aid and aid not in agents_from_logs:
                agents_from_logs[aid] = {
                    "harness": e.get("labels", {}).get("scion.harness", ""),
                }

    # Map agent UUIDs to names from messages
    name_map = {}
    for e in entries:
        if e.get("logName", "").endswith("scion-messages"):
            jp = e.get("jsonPayload", {})
            labels = e.get("labels", {})
            for prefix in ["sender", "recipient"]:
                name = jp.get(prefix, "") or labels.get(prefix, "")
                aid = jp.get(f"{prefix}_id", "") or labels.get(f"{prefix}_id", "")
                if name.startswith("agent:") and aid:
                    name_map[aid] = name.removeprefix("agent:")

    print(f"\nAgents found: {len(agents_from_logs)}")
    for aid, info in sorted(agents_from_logs.items(), key=lambda x: name_map.get(x[0], x[0])):
        name = name_map.get(aid, aid[:8])
        print(f"  {name} (id={aid}, harness={info['harness']})")

    # Agents only in messages (not in scion-agents)
    msg_only = set(name_map.keys()) - set(agents_from_logs.keys())
    if msg_only:
        print(f"\nAgents only in messages (no scion-agents entries):")
        for aid in msg_only:
            print(f"  {name_map[aid]} (id={aid})")

    # Lifecycle events timeline
    print("\nLifecycle timeline:")
    lifecycle_events = []
    for e in sorted(entries, key=lambda x: x.get("timestamp", "")):
        if e.get("logName", "").endswith("scion-agents"):
            msg = e.get("jsonPayload", {}).get("message", "")
            if "lifecycle" in msg or "session" in msg:
                aid = e.get("labels", {}).get("agent_id", "")
                name = name_map.get(aid, aid[:8])
                ts = e.get("timestamp", "")
                print(f"  {ts}  {msg:30s}  {name}")

    # File-modifying tool calls
    file_tools = {"write_file", "create_file", "Write", "edit_file", "Edit", "patch_file"}
    print("\nFile-modifying tool calls:")
    file_count = 0
    for e in sorted(entries, key=lambda x: x.get("timestamp", "")):
        if e.get("logName", "").endswith("scion-agents"):
            jp = e.get("jsonPayload", {})
            tool = jp.get("tool_name", "")
            msg = jp.get("message", "")
            if tool in file_tools and msg == "agent.tool.call":
                aid = e.get("labels", {}).get("agent_id", "")
                name = name_map.get(aid, aid[:8])
                fp = jp.get("file_path", "") or jp.get("path", "") or "(no path in payload)"
                print(f"  {e['timestamp']}  {name}: {tool} -> {fp}")
                file_count += 1
    if file_count == 0:
        print("  (none found)")

    # Message summary
    print(f"\nMessages ({log_types.get('scion-messages', 0)} total):")
    for e in sorted(entries, key=lambda x: x.get("timestamp", "")):
        if e.get("logName", "").endswith("scion-messages"):
            jp = e.get("jsonPayload", {})
            labels = e.get("labels", {})
            sender = (jp.get("sender", "") or labels.get("sender", "")).removeprefix("agent:")
            recipient = (jp.get("recipient", "") or labels.get("recipient", "")).removeprefix("agent:")
            msg_type = jp.get("msg_type", "") or labels.get("msg_type", "")
            content = jp.get("message_content", "")
            if len(content) > 80:
                content = content[:77] + "..."
            print(f"  {e['timestamp']}  {sender} -> {recipient} [{msg_type}] {content}")


if __name__ == "__main__":
    main()
