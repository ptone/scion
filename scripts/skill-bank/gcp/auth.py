"""Auth helpers for GCP Skill Registry API calls."""

import google.auth
import google.auth.transport.requests


def get_auth_headers():
    """Get authorization headers using Application Default Credentials."""
    credentials, project = google.auth.default(
        scopes=["https://www.googleapis.com/auth/cloud-platform"]
    )
    credentials.refresh(google.auth.transport.requests.Request())
    return {
        "Authorization": f"Bearer {credentials.token}",
        "Content-Type": "application/json",
    }
