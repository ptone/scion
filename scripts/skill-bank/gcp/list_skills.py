#!/usr/bin/env python3
"""List all skills in the GCP Skill Registry."""

import sys
import requests

from auth import get_auth_headers
from config import SKILLS_URL, PROJECT_ID, LOCATION


def list_skills():
    headers = get_auth_headers()
    print(f"Listing skills in {PROJECT_ID} / {LOCATION}\n")

    all_skills = []
    url = SKILLS_URL

    while url:
        resp = requests.get(url, headers=headers)
        if resp.status_code != 200:
            print(f"ERROR: {resp.status_code}")
            print(resp.text)
            sys.exit(1)

        data = resp.json()
        skills = data.get("skills", [])
        all_skills.extend(skills)
        next_token = data.get("nextPageToken")
        url = f"{SKILLS_URL}?pageToken={next_token}" if next_token else None

    if not all_skills:
        print("No skills found in the registry.")
        return

    print(f"{'Name':<50} {'Display Name':<30} {'State':<10}")
    print("-" * 90)
    for skill in all_skills:
        name = skill.get("name", "").split("/")[-1]
        display = skill.get("displayName", "")
        state = skill.get("state", "")
        print(f"{name:<50} {display:<30} {state:<10}")

    print(f"\nTotal: {len(all_skills)} skill(s)")


if __name__ == "__main__":
    list_skills()
