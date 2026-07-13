#!/usr/bin/env python3
"""Verify GCP Skill Registry is accessible in the deploy-demo-test project.

The Skill Registry is a project-level service — there's no separate "create
registry" step. This script verifies the API is enabled and accessible by
listing existing skills.
"""

import sys
import requests

from auth import get_auth_headers
from config import SKILLS_URL, PROJECT_ID, LOCATION


def verify_registry():
    headers = get_auth_headers()
    print(f"Project:  {PROJECT_ID}")
    print(f"Location: {LOCATION}")
    print(f"Endpoint: {SKILLS_URL}")
    print()

    resp = requests.get(SKILLS_URL, headers=headers)

    if resp.status_code == 200:
        skills = resp.json().get("skills", [])
        print(f"Registry is accessible. {len(skills)} skill(s) found.")
        return True
    elif resp.status_code == 403:
        print("ERROR: Permission denied. Ensure the service account has roles/aiplatform.user.")
        print(resp.text)
        return False
    elif resp.status_code == 404:
        print("ERROR: API not found. Ensure aiplatform.googleapis.com is enabled:")
        print("  gcloud services enable aiplatform.googleapis.com --project=deploy-demo-test")
        print(resp.text)
        return False
    else:
        print(f"ERROR: Unexpected status {resp.status_code}")
        print(resp.text)
        return False


if __name__ == "__main__":
    success = verify_registry()
    sys.exit(0 if success else 1)
