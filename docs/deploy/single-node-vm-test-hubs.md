# Running Single-Node VM Hubs as Test Hubs

This guide describes how to deploy and operate small, long-lived Scion Hubs
for testing Hub software versions: integration and UAT hubs, and pairs of
hubs for Hub-to-Hub scenarios. It uses the standard single-node VM
deployment ([single-node-vm.md](single-node-vm.md),
[agent-runbook-single-node-vm.md](agent-runbook-single-node-vm.md)) and
covers what changes when the Hub's main clients are test drivers on the
same VPC, not people using a browser.

Placeholders used throughout: `<gcp-project>`, `<region>`, `<zone>`,
`<hub-name>`, `<hub-vm-name>` (always `scion-hub-<hub-name>`),
`<internal-ip>`, `<static-ip-name>`, `<hub-id>`, `<iap-url>`,
`<service-account>`, `<release-tag>`.

---

## 1. Shape of a test hub

| Concern | Choice | Why |
|---|---|---|
| Deploy path | `scripts/single-node-vm/deploy.sh --config <file>` (headless) | Idempotent and re-runnable; no OAuth-login setup needed |
| Size | `machine_size: small` (e2-standard-4), `disk_size_gb: 200` | Test hubs run a handful of agents on the co-located broker |
| Hub version | `--version <release-tag>` plus `update_policy: disabled` | Tests must grade a known version. With `auto` the Hub updates itself and drifts off the pinned version |
| Images | `container_images.source: registry` pointing at an existing image registry | Avoids the ~15 min / ~10 GB on-VM build. A public registry needs no cross-project grant |
| `hub_sa_minting` | `false` | `true` grants the Hub's service account `roles/iam.serviceAccountAdmin` on the whole project, which is a large blast radius for unreleased Hub builds |
| Chat plugins | `[]` | Not needed for testing |
| Co-located broker | On (the deploy default) | Covers basic agent-lifecycle checks and Hub-to-Hub tests |
| IAP proxy | Kept (it can't be disabled) | Scales to zero. It provides browser login and TLS for occasional human access, and doesn't block direct token access (see §3) |

Example config (one per hub; only `hub_name` differs within a pair):

```json
{
  "hub_name": "<hub-name>",
  "project_id": "<gcp-project>",
  "region": "<region>",
  "machine_size": "small",
  "disk_size_gb": 200,
  "chat_plugins": [],
  "container_images": { "source": "registry", "registry": "<registry-path>", "force_rebuild": false },
  "admin_email": "<admin-email>",
  "hub_sa_minting": false,
  "update_policy": "disabled",
  "release_channel": "nightly"
}
```

```bash
scripts/single-node-vm/deploy.sh --version <release-tag> --config hub.json
```

`--version` must name a **published release** (for example a
`nightly-YYYYMMDD` tag), because the script downloads the binary from the
release and verifies it against the release's `SHA256SUMS`. For unreleased
branches, see §5.

### Preflight notes

- Run the runbook's GCP preflight first. A deployer that lacks
  `serviceusage.services.enable` and
  `serviceusage.services.generateServiceIdentity` can still deploy if every
  required API is already enabled and the IAP service agent already
  exists. The script only enables missing APIs and ignores a failed
  identity-create call.
- Check CPU quota for the specific machine family (`E2_CPUS` for small) in
  the target region, not just `CPUS`.

---

## 2. Deploying a pair: Cloud NAT reuse and teardown order

When two hubs are deployed into the same project and region, the **second**
deploy finds the first hub's Cloud NAT, which already covers the region,
and **reuses** it instead of creating its own:

```
Found an existing Cloud NAT that already covers this network/region; reusing it instead of creating our own.
  Reusing Cloud Router: scion-hub-<first-hub>-router
```

Consequences:

- **Teardown order matters.** `deploy.sh --delete` for the *first* hub
  deletes its router and NAT, which cuts outbound access (image pulls,
  GitHub, model APIs) for the second hub. Tear down the second hub first,
  or re-run the second hub's deploy after deleting the first so that it
  creates its own NAT.
- **Avoid a race.** Start the second deploy only after the first has
  reported `Created Cloud NAT`. Otherwise both runs may decide no NAT
  exists, or both may try to reuse one that's still being created.

---

## 3. Access model: direct tokens on the private IP

The deployed Hub listens on `0.0.0.0:8080` and the VM has **no public
IP**. In proxy/IAP auth mode, IAP is only consulted when a request carries
**no** credential of its own (see `UnifiedAuthMiddleware` in
`pkg/hub/auth.go`). A request sent straight to
`http://<internal-ip>:8080` carrying any of these credentials is accepted
if the credential is valid:

- a Hub-minted user JWT or a PAT (`Authorization: Bearer ...`),
- an agent token,
- broker HMAC authentication.

So test drivers on the same VPC can use the private IP over plain HTTP,
while people use `<iap-url>` in a browser.

### Network reachability

- `deploy.sh` creates a proxy-to-VM rule allowing tcp:8080 only from the
  Hub's own regional subnet.
- Access from **other regions** depends on the project's
  `default-allow-internal` rule (`10.128.0.0/9`, all ports) on an
  auto-mode `default` network. If that rule exists, any VM on the
  `default` network in any region, and the other Hub of a pair, can reach
  port 8080.
- These **cannot** reach the Hub without extra peering, SNAT or firewall
  work:
  - VMs on other VPCs,
  - GKE pods whose pod range is outside `10.128.0.0/9` and whose traffic
    isn't SNAT'd to the node IP.

  Plan that work before a campaign that drives the Hub from those places.

### Static internal IP

The VM's internal IP is ephemeral unless the hybrid tier is enabled.
For long-lived test hubs that other hubs or drivers address by IP, promote
it to a static reservation. Use the same name and marker that the hybrid
tier uses, so the script recognizes it later:

```bash
gcloud compute addresses create scion-hub-<hub-name>-internal-ip \
  --region=<region> --subnet=default --addresses=<internal-ip> \
  --description="scion-deployment=<hub-name>" --project=<gcp-project>
```

### Human browser access

`deploy.sh` grants `roles/iap.httpsResourceAccessor` only to the account
that runs it (often `<service-account>` when an agent deploys).
`admin_email` only sets super-admin in `settings.yaml`. Grant IAP access
to each human explicitly:

```bash
gcloud iap web add-iam-policy-binding --resource-type=cloud-run \
  --service=scion-hub-<hub-name>-iap-proxy --region=<region> --project=<gcp-project> \
  --member=user:<email> --role=roles/iap.httpsResourceAccessor --condition=None
```

---

## 4. Signing keys and minting test tokens

The single-node deploy uses the **local** secrets backend
(`server.secrets.backend: local`), and `hub.env` sets
`SCION_SERVER_SESSION_SECRET`. With a shared session secret configured,
the Hub **derives** its signing keys from that secret instead of storing
them in a secret manager (see `deriveSharedSigningKey` in
`pkg/hub/server.go`):

```
user_signing_key = sha256("scion-hub-signing-key:user_signing_key:" + SCION_SERVER_SESSION_SECRET)
```

Consequences:

- Keys are per Hub, because each deploy generates its own session secret.
  Never copy keys or `hub.env` between hubs.
- Mint test JWTs **on the VM** as root, reading the secret from
  `/home/scion/.scion/hub.env`. Write the token to a 0600 temp file and
  pass it with `curl -K <file>`. Never put it in argv, logs, or chat.
- A validly signed JWT is still rejected with **`401 user_not_found`**
  until its `uid` matches a row in the `users` table. A fresh Hub has no
  users until the first IAP login, which also makes the `admin_email` user
  super-admin.
- Rotating `SCION_SERVER_SESSION_SECRET` rotates every derived key and
  invalidates all outstanding tokens and sessions.

### Validation checklist after deploy

Run these from the VM or another host on the VPC:

1. `curl http://<internal-ip>:8080/healthz` reports `healthy`, including
   the `database` and `colocated_broker` checks, and the expected
   `scionVersion`. For a pair, run it from each Hub against the other
   Hub's IP to confirm Hub-to-Hub reachability.
2. An unauthenticated request to `<iap-url>/api/v1/projects` returns
   `302` to Google sign-in, with the body
   `Invalid IAP credentials: empty token`. This shows IAP is enforcing.
3. `GET http://<internal-ip>:8080/api/v1/projects` with no token returns
   `401`.
4. A JWT signed with the derived key returns `401 user_not_found` before
   the first login (which proves the signature was accepted), and `200`
   on `/api/v1/auth/me` once its `uid` is a real user.

---

## 5. Changing the Hub version

- **Published release:** re-run
  `deploy.sh --version <release-tag> --config hub.json`. The run is
  idempotent and replaces the binary. It **rewrites `settings.yaml`**, so
  re-apply any manual settings changes. It keeps an existing `hub.env`
  (including its session secret, so derived keys and tokens stay valid,
  and any endpoint override from §6).
- **Unreleased branch or commit:** build on the VM and swap the binary:

  ```bash
  # as the scion user, in a checkout of the desired ref (Go and Node required)
  make web && go build -buildvcs=false -o scion ./cmd/scion
  sudo systemctl stop scion-hub
  sudo install -m 0755 scion /usr/local/bin/scion
  sudo systemctl start scion-hub
  curl -s http://localhost:8080/healthz   # confirm scionVersion
  ```

  With `update_policy: disabled`, the sideloaded binary stays in place.

---

## 6. Hub-to-Hub and external brokers: advertised endpoint

By default the Hub advertises an endpoint derived from the IAP audience:
the Cloud Run proxy URL. Agents on the co-located broker are rewritten to
`http://scion-hub.internal:8080` over the Docker bridge, so they work
unchanged. A **peer Hub or an external broker** would be given the IAP URL
and fail to authenticate.

For those campaigns, override the endpoint with the private address:

```bash
# on the VM
echo 'SCION_SERVER_BASE_URL=http://<internal-ip>:8080' | sudo tee -a /home/scion/.scion/hub.env
sudo systemctl restart scion-hub
sudo journalctl -u scion-hub -n 200 --no-pager | grep 'Hub endpoint resolved'
```

- **Side effects:** the Hub's absolute links and redirects now use the
  private URL, so browser use through IAP may be degraded while the
  override is on. Co-located agents call the private IP directly.
- **To revert:** remove the line and restart.
- Re-running `deploy.sh` keeps an existing `hub.env`, so the override
  survives a redeploy. Remove it explicitly when the campaign ends.

---

## 7. Isolation guidance

- A disabled auto-update and `hub_sa_minting: false` keep a test Hub
  contained even when it shares a project with other hubs.
- For campaigns that test Hub-minted service accounts or other
  project-scoped privileges on an **unreleased** Hub build, use a
  **separate GCP project** instead of setting `hub_sa_minting: true` in a
  shared one.
- `deploy.sh` creates one runtime service account and one proxy service
  account per Hub, and has no option to reuse an existing account. Check
  the project's service-account quota before deploying many test hubs.
