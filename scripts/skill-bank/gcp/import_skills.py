#!/usr/bin/env python3
"""Import skills from mattpocock/skills into the GCP Skill Registry.

Clones the repo (if not already present), reads each skill's SKILL.md for
metadata, zips the skill directory, and uploads it via the REST API.
"""

import base64
import io
import os
import re
import subprocess
import sys
import zipfile

import requests
import yaml

from auth import get_auth_headers
from config import SKILLS_URL

SKILLS_REPO = "https://github.com/mattpocock/skills.git"
CLONE_DIR = "/tmp/mattpocock-skills"
SKIP_CATEGORIES = {"deprecated", "in-progress"}


def clone_repo():
    if os.path.isdir(os.path.join(CLONE_DIR, ".git")):
        print(f"Repo already cloned at {CLONE_DIR}, pulling latest...")
        subprocess.run(["git", "-C", CLONE_DIR, "pull", "--ff-only"],
                       capture_output=True)
    else:
        print(f"Cloning {SKILLS_REPO}...")
        subprocess.run(["git", "clone", "--depth", "1", SKILLS_REPO, CLONE_DIR],
                       check=True, capture_output=True)


def parse_skill_md(skill_dir):
    """Extract name and description from SKILL.md frontmatter."""
    skill_md = os.path.join(skill_dir, "SKILL.md")
    if not os.path.isfile(skill_md):
        return None, None

    with open(skill_md, "r") as f:
        content = f.read()

    match = re.match(r"^---\s*\n(.*?)\n---", content, re.DOTALL)
    if not match:
        return None, None

    try:
        frontmatter = yaml.safe_load(match.group(1))
    except yaml.YAMLError:
        return None, None

    name = frontmatter.get("name", "")
    description = frontmatter.get("description", "")
    return name, description


def zip_skill_dir(skill_dir):
    """Zip a skill directory and return base64-encoded content."""
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        for root, dirs, files in os.walk(skill_dir):
            for fname in files:
                fpath = os.path.join(root, fname)
                arcname = os.path.relpath(fpath, skill_dir)
                zf.write(fpath, arcname)
    return base64.b64encode(buf.getvalue()).decode("ascii")


def discover_skills():
    """Find all skill directories, skipping deprecated/in-progress."""
    skills_root = os.path.join(CLONE_DIR, "skills")
    results = []

    for category in sorted(os.listdir(skills_root)):
        cat_path = os.path.join(skills_root, category)
        if not os.path.isdir(cat_path):
            continue
        if category in SKIP_CATEGORIES:
            print(f"  Skipping category: {category}")
            continue

        for skill_name in sorted(os.listdir(cat_path)):
            skill_path = os.path.join(cat_path, skill_name)
            if not os.path.isdir(skill_path):
                continue

            name, description = parse_skill_md(skill_path)
            if not name:
                print(f"  Skipping {category}/{skill_name}: no valid SKILL.md")
                continue

            results.append({
                "category": category,
                "dir_name": skill_name,
                "path": skill_path,
                "name": name,
                "description": description or f"Skill: {name}",
            })

    return results


def create_skill(headers, skill_info):
    """Upload a single skill to the registry."""
    skill_id = skill_info["name"]
    display_name = skill_info["name"]
    description = skill_info["description"]
    zipped = zip_skill_dir(skill_info["path"])

    payload = {
        "displayName": display_name,
        "description": description,
        "zippedFilesystem": zipped,
    }

    resp = requests.post(
        f"{SKILLS_URL}?skillId={skill_id}",
        headers=headers,
        json=payload,
    )
    return resp


def import_all():
    clone_repo()
    skills = discover_skills()
    print(f"\nFound {len(skills)} skills to import.\n")

    if not skills:
        print("No skills found.")
        return

    headers = get_auth_headers()
    succeeded = 0
    failed = 0
    skipped = 0

    for skill in skills:
        label = f"{skill['category']}/{skill['dir_name']}"
        sys.stdout.write(f"  Importing {label}... ")
        sys.stdout.flush()

        resp = create_skill(headers, skill)

        if resp.status_code == 200:
            print("OK")
            succeeded += 1
        elif resp.status_code == 409:
            print("ALREADY EXISTS (skipped)")
            skipped += 1
        else:
            print(f"FAILED ({resp.status_code})")
            try:
                err = resp.json().get("error", {}).get("message", resp.text)
            except Exception:
                err = resp.text
            print(f"    Error: {err}")
            failed += 1

    print(f"\nResults: {succeeded} imported, {skipped} already existed, {failed} failed")
    print(f"Total skills in repo: {len(skills)}")


if __name__ == "__main__":
    import_all()
