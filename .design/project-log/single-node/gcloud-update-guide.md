# Updating gcloud to Latest in Agent Environments

This guide covers how to get the latest `gcloud` CLI in Scion agent containers, which ship with an older version by default.

---

## Quick Method (Recommended for Agents)

A pre-written script is available in the shared scratchpad. Run it as **Step 0** before any gcloud commands:

```bash
bash /scion-volumes/scratchpad/update-gcloud.sh
```

This script:
1. Adds the official Google Cloud apt repository with a signed keyring
2. Installs the latest `google-cloud-cli` package
3. Verifies the installation with `gcloud --version`

Typical runtime: **2-4 minutes**. Required when `gcloud alpha run instances` commands are missing or the installed version is older than ~572.

---

## Manual Method

If you prefer to run the steps yourself:

```bash
# Install prerequisites
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates gnupg curl

# Add Google Cloud signing key
curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg | \
  sudo gpg --dearmor --yes -o /usr/share/keyrings/cloud.google.gpg

# Add repository
echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | \
  sudo tee /etc/apt/sources.list.d/google-cloud-cli.list

# Install
sudo apt-get update
sudo apt-get install -y google-cloud-cli

# Verify
gcloud --version
```

---

## If gcloud Is Already Installed (Component Update)

If gcloud is already installed and relatively recent, you can update components directly:

```bash
gcloud components update --quiet
```

Note: This only works if gcloud was installed via the component manager, not via apt. In Scion agent containers, use the script above instead.

---

## Verify the Version

After updating, confirm you have the version needed for Cloud Run Instances alpha commands:

```bash
gcloud --version
gcloud alpha run instances --help  # Should show create, list, describe, ssh, etc.
```

Current known-good version as of testing: **580.0.0+**

---

## Why This Is Needed

Scion agent containers ship with an older gcloud. `gcloud alpha run instances` commands are not available in older versions. The script pins the latest version from the official upstream apt repository.

**This must be done once per agent container.** The upgrade persists for the lifetime of the container.
