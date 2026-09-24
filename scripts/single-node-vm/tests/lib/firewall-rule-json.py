#!/usr/bin/env python3
"""tests/lib/firewall-rule-json.py — builds a fake firewall-rule JSON body,
in the same shape `gcloud compute firewall-rules describe --format=json`
returns, from plain positional fields. Shared by the stub gcloud's
`create` handler and by the test harness's fixture-seeding helper, so a
rule the stub "creates" and a rule a test seeds directly are described
identically to hybrid-tier.sh's parser. Never contacts GCP.

Usage:
  firewall-rule-json.py DESC NETWORK DIRECTION ACTION PROTO PORTS \
      SOURCE_TAGS SOURCE_RANGES TARGET_TAGS PRIORITY

PROTO/PORTS/SOURCE_TAGS/SOURCE_RANGES/TARGET_TAGS are comma-separated (a
single empty string means "not set"). Prints the JSON body to stdout.
"""
import json
import sys


def csv(s):
    return [x for x in s.split(",") if x]


def main():
    (desc, network, direction, action, proto, ports,
     source_tags, source_ranges, target_tags, priority) = sys.argv[1:11]

    body = {
        "description": desc,
        "network": f"https://www.googleapis.com/compute/v1/projects/x/global/networks/{network}",
        "direction": direction,
        "targetTags": csv(target_tags),
        "priority": int(priority) if priority else None,
    }
    rule = {"IPProtocol": proto}
    port_list = csv(ports)
    if port_list:
        rule["ports"] = port_list
    if action == "ALLOW":
        body["allowed"] = [rule]
    elif action == "DENY":
        body["denied"] = [rule]

    if source_tags:
        body["sourceTags"] = csv(source_tags)
    if source_ranges:
        body["sourceRanges"] = csv(source_ranges)

    json.dump(body, sys.stdout)


if __name__ == "__main__":
    main()
