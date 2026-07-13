# GCP Skill Registry Scripts

Scripts for managing skills in the GCP Skill Registry using the Agent Platform API.

## GCP Setup

- **Project**: `deploy-demo-test`
- **Location**: `us-central1`
- **API**: Agent Platform API (`aiplatform.googleapis.com`)
- **IAM Role**: `roles/aiplatform.user`

### Enable the API

```bash
gcloud services enable aiplatform.googleapis.com --project=deploy-demo-test
```

### Authentication

Scripts use Application Default Credentials (ADC). Authenticate with:

```bash
gcloud auth application-default login
```

Or use a service account key:

```bash
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/key.json
```

## Install Dependencies

```bash
pip install -r requirements.txt
```

## Scripts

### `create_registry.py`

Verifies the Skill Registry is accessible. The registry is a project-level service — no separate creation step is needed.

```bash
python3 create_registry.py
```

### `import_skills.py`

Clones `mattpocock/skills` and imports all skill directories into the registry. Skips `deprecated/` and `in-progress/` categories. Skills that already exist are skipped (409 conflict).

```bash
python3 import_skills.py
```

### `list_skills.py`

Lists all skills currently in the registry.

```bash
python3 list_skills.py
```

## API Reference

- [Create and manage skills](https://docs.google.com/gemini-enterprise-agent-platform/build/skill-registry/create-manage)
- REST endpoint: `https://us-central1-aiplatform.googleapis.com/v1beta1/projects/deploy-demo-test/locations/us-central1/skills`

## Source Skills

Imported from: https://github.com/mattpocock/skills

Categories imported: `engineering/`, `misc/`, `personal/`, `productivity/`
Categories skipped: `deprecated/`, `in-progress/`
