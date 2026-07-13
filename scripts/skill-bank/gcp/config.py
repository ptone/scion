"""Shared configuration for GCP Skill Registry scripts."""

PROJECT_ID = "deploy-demo-test"
LOCATION = "us-central1"
API_BASE = f"https://{LOCATION}-aiplatform.googleapis.com/v1beta1"
SKILLS_PARENT = f"projects/{PROJECT_ID}/locations/{LOCATION}"
SKILLS_URL = f"{API_BASE}/{SKILLS_PARENT}/skills"
