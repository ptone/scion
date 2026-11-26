# GitHub App QA Walkthrough: Hub Admin

**Created:** 2026-03-19
**Prerequisites:** A running Scion Hub (combo server is fine), admin access, a registered GitHub App on github.com

---

## Part 1: GitHub App Registration (on GitHub)

If you haven't already registered the app:

1. Go to **GitHub Settings > Developer Settings > GitHub Apps > New GitHub App**.
2. Fill in:
   - **App name**: e.g., `Scion Hub Dev`
   - **Homepage URL**: Your Hub's public URL (e.g., `https://your-hub.example.com`)
   - **Setup URL**: `https://<hub-url>/github-app/setup` (check "Redirect on update")
   - **Webhook URL**: `https://<hub-url>/api/v1/webhooks/github`
   - **Webhook secret**: Generate with `openssl rand -hex 32`, save this value
3. Set **Repository Permissions**:
   - Contents: **Read and write**
   - Metadata: **Read-only**
   - Pull requests: **Read and write**
   - Issues: **Read and write**
4. Subscribe to events:
   - **Installation**
   - **Installation repositories**
5. Set "Where can this GitHub App be installed?" to **Any account** (or your org only).
6. Click **Create GitHub App**. Note the **App ID** from the resulting page.
7. **Generate a private key**: Scroll to "Private keys" > "Generate a private key". Save the downloaded `.pem` file.

---

## Part 2: Configure the Hub

### 2.1 Via API

```bash
# Set your Hub URL and admin token
HUB_URL=https://your-hub.example.com
TOKEN=your-admin-token

# Read the private key from the downloaded PEM file
PRIVATE_KEY=$(cat /path/to/your-app.private-key.pem)

# Configure the GitHub App on the Hub
curl -s -X PUT "$HUB_URL/api/v1/github-app" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{
    \"app_id\": YOUR_APP_ID,
    \"private_key\": $(echo "$PRIVATE_KEY" | jq -Rs .),
    \"webhook_secret\": \"YOUR_WEBHOOK_SECRET\",
    \"webhooks_enabled\": true
  }" | jq .
```

**Expected response:**
```json
{
  "app_id": 123456,
  "api_base_url": "",
  "webhooks_enabled": true,
  "configured": true
}
```

**Verify:** `configured` should be `true`. The private key and webhook secret are never returned.

### 2.2 Via Web UI

1. Navigate to the Hub admin page (gear icon or `/admin`).
2. Look for the **GitHub App** configuration section.
3. Enter the App ID, paste the private key, and webhook secret.
4. Toggle webhooks enabled if Hub is publicly reachable.

### 2.3 Verify Configuration

```bash
curl -s "$HUB_URL/api/v1/github-app" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Check:**
- `configured: true`
- `app_id` matches your registered app
- `webhooks_enabled` matches your setting

---

## Part 3: Install the App on Your GitHub Org/Account

1. Go to your GitHub App's public page:
   ```
   https://github.com/apps/<your-app-slug>/installations/new
   ```
2. Select the org or user account.
3. Choose **Only select repositories** and pick one or more test repos.
4. Click **Install**.
5. GitHub redirects to the Hub's setup URL. You should see a JSON response showing:
   - `installation_id`
   - `account` (org/user login)
   - `repositories` (list of granted repos)
   - `matched_groves` (any groves auto-matched by repo URL)

### 3.1 Verify Installation Recorded

```bash
curl -s "$HUB_URL/api/v1/github-app/installations" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Expected:** Your installation appears in the list with `status: "active"`.

### 3.2 Verify Individual Installation

```bash
INSTALLATION_ID=12345  # from the setup callback response

curl -s "$HUB_URL/api/v1/github-app/installations/$INSTALLATION_ID" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Check:**
- `installation_id` matches
- `account_login` is your org/user
- `repositories` lists the repos you selected
- `status` is `"active"`

---

## Part 4: Test Webhook Delivery

### 4.1 Ping Event (Automatic)

When you first set up the webhook, GitHub sends a `ping` event. Check your Hub logs for:
```
Source webhook received source_kind=github event=ping
```

### 4.2 Verify via GitHub Delivery Log

1. Go to your GitHub App settings > **Advanced** > **Recent Deliveries**.
2. Check that the `ping` event shows a `200` response with `{"status":"pong"}`.

### 4.3 Test Repo Add/Remove (if using "selected repos")

1. Go to your GitHub App installation settings on GitHub.
2. Add a new repository to the installation.
3. Check Hub logs for: `Source webhook received source_kind=github event=installation_repositories`
4. Verify the installation's repo list updated:
   ```bash
   curl -s "$HUB_URL/api/v1/github-app/installations/$INSTALLATION_ID" \
     -H "Authorization: Bearer $TOKEN" | jq .repositories
   ```
5. Remove the repo you just added and verify again.

---

## Part 5: Discover Installations

If you installed the app before configuring the Hub, or want to re-sync:

```bash
curl -s -X POST "$HUB_URL/api/v1/github-app/installations/discover" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Expected:** Lists all installations found on GitHub, their repos, and any auto-matched groves.

---

## Part 6: App Permission Sync

Verify the Hub can read the app's registered permissions from GitHub:

```bash
curl -s -X POST "$HUB_URL/api/v1/github-app/sync-permissions" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Expected response:**
```json
{
  "app_permissions": {
    "contents": "write",
    "metadata": "read",
    "pull_requests": "write",
    "issues": "write"
  },
  "affected_groves": [],
  "affected_count": 0
}
```

**Check:**
- `app_permissions` matches what you configured on GitHub
- `affected_groves` should be empty if all groves' requested permissions are within the app's permissions
- If you deliberately reduced permissions on GitHub, re-run this to see affected groves flagged

---

## Part 7: Grove Association and Token Minting

### 7.1 Create a Test Grove

Create a grove whose `git_remote` matches one of the repos in your installation:

```bash
# Create a grove pointing to a repo that the app has access to
curl -s -X POST "$HUB_URL/api/v1/groves" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-github-app",
    "git_remote": "https://github.com/your-org/your-repo.git"
  }' | jq .
```

Note the grove `id` from the response.

### 7.2 Auto-Discovery for the Grove

If the grove wasn't auto-matched during installation, trigger discovery:

```bash
curl -s -X POST "$HUB_URL/api/v1/github-app/installations/discover" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

Check `matched_groves` in the response for your grove ID.

### 7.3 Manual Association (Alternative)

If auto-matching didn't work, manually associate the grove:

```bash
GROVE_ID=your-grove-id

curl -s -X PUT "$HUB_URL/api/v1/groves/$GROVE_ID/github-installation" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"installation_id\": $INSTALLATION_ID}" | jq .
```

**Expected:**
```json
{
  "grove_id": "your-grove-id",
  "installation_id": 12345,
  "status": "associated"
}
```

### 7.4 Check Grove GitHub Status

```bash
curl -s "$HUB_URL/api/v1/groves/$GROVE_ID/github-status" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Expected:** `state: "unchecked"` (will become `"ok"` after first agent run / token mint).

### 7.5 Start an Agent (Token Minting Test)

Start an agent for this grove. The Hub will mint a GitHub App installation token and inject it as `GITHUB_TOKEN`:

```bash
scion start --grove test-github-app my-agent
```

After the agent starts, check the grove status again:

```bash
curl -s "$HUB_URL/api/v1/groves/$GROVE_ID/github-status" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Expected:** `state: "ok"` with a `last_token_mint` timestamp.

### 7.6 Verify Inside the Agent

Attach to the agent and verify the token works:

```bash
scion attach my-agent

# Inside the agent:
echo $GITHUB_TOKEN        # Should be set (ghs_xxx format)
echo $SCION_GITHUB_APP_ENABLED  # Should be "true"
gh auth status            # Should show authenticated
git ls-remote origin      # Should succeed
```

---

## Part 8: Permission Configuration

### 8.1 View Default Permissions

```bash
curl -s "$HUB_URL/api/v1/groves/$GROVE_ID/github-permissions" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Default:**
```json
{
  "contents": "write",
  "pull_requests": "write",
  "metadata": "read"
}
```

### 8.2 Set Custom Permissions

```bash
curl -s -X PUT "$HUB_URL/api/v1/groves/$GROVE_ID/github-permissions" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": "write",
    "pull_requests": "write",
    "issues": "write",
    "metadata": "read"
  }' | jq .
```

### 8.3 Reset to Defaults

```bash
curl -s -X DELETE "$HUB_URL/api/v1/groves/$GROVE_ID/github-permissions" \
  -H "Authorization: Bearer $TOKEN"
```

---

## Part 9: Git Identity / Commit Attribution

### 9.1 View Current Identity

```bash
curl -s "$HUB_URL/api/v1/groves/$GROVE_ID/git-identity" \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Default:** `{"mode": "bot"}`

### 9.2 Set Custom Identity

```bash
curl -s -X PUT "$HUB_URL/api/v1/groves/$GROVE_ID/git-identity" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "mode": "custom",
    "name": "My CI Bot",
    "email": "ci-bot@example.com"
  }' | jq .
```

### 9.3 Set Co-authored Mode

```bash
curl -s -X PUT "$HUB_URL/api/v1/groves/$GROVE_ID/git-identity" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"mode": "co-authored"}' | jq .
```

### 9.4 Reset to Default (bot)

```bash
curl -s -X DELETE "$HUB_URL/api/v1/groves/$GROVE_ID/git-identity" \
  -H "Authorization: Bearer $TOKEN"
```

---

## Part 10: Health Check Verification

The periodic health check runs automatically (every 6h without webhooks, 24h with webhooks). To verify the mechanism works, check Hub logs for:

```
GitHub App health check starting
GitHub App health check: installations verified checked=N deleted=0 suspended=0
GitHub App health check completed
```

---

## Part 11: Error Scenarios to Test

### 11.1 Revoke the Installation

1. Go to GitHub > Your org's installed apps > Uninstall the Scion app.
2. If webhooks are enabled, the Hub should immediately receive `installation.deleted`.
3. Check the installation status:
   ```bash
   curl -s "$HUB_URL/api/v1/github-app/installations/$INSTALLATION_ID" \
     -H "Authorization: Bearer $TOKEN" | jq .status
   ```
   **Expected:** `"deleted"`
4. Check the grove status:
   ```bash
   curl -s "$HUB_URL/api/v1/groves/$GROVE_ID/github-status" \
     -H "Authorization: Bearer $TOKEN" | jq .
   ```
   **Expected:** `state: "error"`, `error_code: "installation_revoked"`

### 11.2 Remove Repo from Installation

1. Go to the GitHub App installation settings.
2. Remove the grove's target repo from the selected repos.
3. Webhook fires `installation_repositories.removed`.
4. Check grove status:
   **Expected:** `state: "error"`, `error_code: "repo_not_accessible"`

### 11.3 Re-add Repo

1. Add the repo back to the installation.
2. Webhook fires `installation_repositories.added`.
3. Verify the grove status clears (run discovery or start an agent).

### 11.4 Bad Private Key

1. Update the Hub config with an invalid private key via API.
2. Try to start an agent.
3. **Expected:** Token minting fails, grove status shows `error_code: "private_key_invalid"`.
4. Restore the correct key.

---

## Part 12: Cleanup

### 12.1 Remove Grove Association

```bash
curl -s -X DELETE "$HUB_URL/api/v1/groves/$GROVE_ID/github-installation" \
  -H "Authorization: Bearer $TOKEN"
```

### 12.2 Delete Installation Record

```bash
curl -s -X DELETE "$HUB_URL/api/v1/github-app/installations/$INSTALLATION_ID" \
  -H "Authorization: Bearer $TOKEN"
```

---

## Checklist Summary

| # | Test | Pass |
|---|------|------|
| 1 | Hub config via API (`PUT /github-app`) shows `configured: true` | |
| 2 | App installed on GitHub, setup callback returns matched data | |
| 3 | Installation recorded (`GET /installations`) | |
| 4 | Webhook ping returns `pong` (check GitHub delivery log) | |
| 5 | Discover finds installations (`POST /installations/discover`) | |
| 6 | Permission sync returns app permissions (`POST /sync-permissions`) | |
| 7 | Grove auto-matched or manually associated | |
| 8 | Grove status is `unchecked` before first agent run | |
| 9 | Agent starts with `GITHUB_TOKEN` (ghs_ format) | |
| 10 | Grove status becomes `ok` with `last_token_mint` | |
| 11 | `gh auth status` works inside agent | |
| 12 | Custom permissions saved/read correctly | |
| 13 | Git identity modes work (bot/custom/co-authored) | |
| 14 | Repo add/remove webhooks update installation records | |
| 15 | Installation revoke sets grove to `error` state | |
| 16 | Grove disassociation clears installation and status | |
