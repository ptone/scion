# Deploying a Cloud Run Instance with `sandboxLauncher`

> # ✅ THE GAP IS CLOSED — use gcloud, not REST (2026-08-25)
>
> **gcloud 582.0.0 ships `--sandbox-launcher` on `gcloud beta run instances`.**
> Verified directly, not inferred:
>
> ```
> --[no-]sandbox-launcher
>     Set the container as a sandbox supervisor to launch sandboxes.
> ```
>
> **The raw-REST sections below are superseded.** They are retained because they
> document platform behaviour (defaults injected, `launchStage` normalisation, region
> capacity) that is still true, and because they are the record of how
> `sandboxLauncher` was first confirmed. **Do not build anything new on them.**
>
> ### Get the right gcloud first
>
> Containers ship 575.0.0, which does **not** have the flag.
>
> ```sh
> bash /scion-volumes/scratchpad/update-gcloud.sh   # → 582.0.0, 2–4 min
> ```
>
> ### The flag is on both verbs, and they differ
>
> | Verb | Semantics |
> |---|---|
> | `create` | Imperative. Fails if the instance exists. |
> | `deploy` | **Create-or-update.** Creates if absent, updates the container config if present. |
>
> Earlier drafts of this doc assumed `create` was the destination. **`deploy` is the
> better fit for our tooling** (§4.10, P5): Tier 0 is pure ephemeral and redeploy is
> the *normal* lifecycle, so a verb that is idempotent across "not there yet" and
> "already there" removes an exists-check we would otherwise have to write.
>
> ```sh
> gcloud beta run instances deploy scion-hub-1 \
>   --image "$OMNI_IMAGE" --sandbox-launcher \
>   --service-account "$SA" --memory 8Gi --cpu 4 \
>   --region us-east4 --project "$PROJECT"
> ```
>
> ### Other verbs now available — worth knowing about
>
> Listed verbs: `add-iam-policy-binding create delete deploy describe get-iam-policy
> list proxy remove-iam-policy-binding replace restart set-iam-policy start stop
> update`.
>
> - **`ssh` — EXISTS, and is HIDDEN from the group listing.** Flagged by ptone,
>   verified 2026-08-26 on gcloud 582.0.0. It does not appear under `COMMANDS` in
>   `gcloud beta run instances --help`, but `gcloud beta run instances ssh --help`
>   returns a complete, documented page on both `beta` and `alpha`:
>
>   ```
>   gcloud beta run instances ssh INSTANCE [--container=CONTAINER] [--region=REGION]
>   "Starts a secure, interactive shell session with a Cloud Run instance."
>   ```
>
>   **This kills the §6.1a IAP-tunnel plumbing** — see gotcha 4 below, now struck.
>   Note `--container`: the verb is multi-container aware, which matters if the omni
>   image is ever split.
>
>   **The methodological lesson is the more important half.** I derived the verb list
>   from the group help and reported an absence. The group help does not enumerate
>   hidden commands, so it cannot support a negative claim. *Absence from a listing is
>   not absence from the CLI — probe the specific verb before asserting it is missing.*
>   This is the same failure shape as the "sandbox CLI does not exist on Instances"
>   claim in `ac0-results.md`.
> - **`proxy`** — real, and listed. Now understood as the *port-forwarding* path
>   (authenticated localhost proxy to a port), **not** the shell path; `ssh` is the
>   shell path. The two are complementary, not alternatives.
> - **`start` / `stop` / `restart`** — a lifecycle we did not know existed and did not
>   design for. **Do not read durability into this**: the filesystem is still
>   ephemeral, so a stop/start almost certainly does not preserve agent state, and §5
>   (Tier 0, pure ephemeral) stands until someone tests it. Flagged as a *question*,
>   not a capability.

**Status: `sandboxLauncher` on Instances is CONFIRMED ACCEPTED by the API**, verified
2026-08-25 against `ptone-experiments`. ptone confirms `--sandbox-launcher` is coming
to `gcloud beta run instances`; until it lands, use the calls below.

## Why raw REST

`gcloud alpha run instances create` has no `--sandbox-launcher` flag (verified against
the full synopsis). The flag currently exists only on `gcloud beta run deploy`
(services) — not jobs, worker-pools, or instances, in gcloud 575.0.0. The Cloud Run
**v2 API** does support it: `GoogleCloudRunV2Instance.containers[]` holds
`GoogleCloudRunV2Container`, which carries `sandboxLauncher: boolean` —
*"Indicates that this container can act as a sandbox supervisor and launch sandboxes."*

## Step 0 — update gcloud first

Agent containers ship a **stale gcloud**. Anything older than ~572 is missing
`gcloud alpha run instances` entirely, and an absent subcommand looks like a
permissions or project problem rather than a stale CLI.

```sh
bash /scion-volumes/scratchpad/update-gcloud.sh   # 2–4 min
```

Guide: `gcloud-update-guide.md` (same directory). Do this even though *create* goes
through raw REST — token minting and `describe`/`list`/`delete` all use gcloud.

## Auth

```sh
SA=scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com
TOKEN=$(gcloud auth print-access-token --impersonate-service-account="$SA")
PROJECT=ptone-experiments
REGION=us-east4          # NOT us-central1 — capacity exhausted there, see note
```

## 1. Validate without creating anything (`validateOnly=true`)

This is the cheap proof, and the one that was actually run:

```sh
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://run.googleapis.com/v2/projects/$PROJECT/locations/$REGION/instances?instanceId=sbx-validate&validateOnly=true" \
  -d '{
        "launchStage": "ALPHA",
        "containers": [{
          "image": "us-docker.pkg.dev/cloudrun/container/hello",
          "sandboxLauncher": true
        }]
      }'
```

**Observed result:** HTTP 200, and the returned resource echoes
`"sandboxLauncher": true` inside `metadata.containers[0]`. The API normalises
`launchStage` `ALPHA` → `BETA`. Container keys returned were exactly
`['image', 'ports', 'resources', 'sandboxLauncher']`.

**This is the decisive evidence** that the field is honoured on Instances rather than
merely tolerated by a shared message definition.

## 2. Real create

Same call without `validateOnly`. Realistic shape for our tier:

```sh
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "https://run.googleapis.com/v2/projects/$PROJECT/locations/$REGION/instances?instanceId=scion-hub-1" \
  -d '{
        "launchStage": "ALPHA",
        "serviceAccount": "'"$SA"'",
        "containers": [{
          "image": "'"$OMNI_IMAGE"'",
          "sandboxLauncher": true,
          "resources": { "limits": { "memory": "8Gi", "cpu": "4000m" } },
          "ports": [{ "containerPort": 8080 }],
          "env": [
            { "name": "SCION_HUB_ID", "value": "scion-hub-1" }
          ]
        }]
      }'
```

Returns a long-running operation. Poll it:

```sh
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://run.googleapis.com/v2/projects/$PROJECT/locations/$REGION/operations/<OP_ID>"
```

`hub_id` is pinned explicitly above **because AC-0 found the hostname is always
`localhost`** on an Instance, never instance-derived (§4.6 row 4). The fallback is
unusable, not merely fragile.

## 3. Describe / delete

```sh
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://run.googleapis.com/v2/projects/$PROJECT/locations/$REGION/instances/scion-hub-1"

curl -s -X DELETE -H "Authorization: Bearer $TOKEN" \
  "https://run.googleapis.com/v2/projects/$PROJECT/locations/$REGION/instances/scion-hub-1"
```

`gcloud alpha run instances {describe,list,delete}` work fine — only *create* needs
raw REST, because only create carries `sandboxLauncher`.

## Gotchas found while testing

1. **`us-central1` is capacity-exhausted for Instances.** The error is explicit and
   helpfully lists available regions:
   `REGION_CAPACITY_EXHAUSTED ... available_regions: ... us-east4, us-east5, ...`.
   Note it reaches capacity checking only *after* schema validation passes, so a
   capacity error is itself weak evidence the payload was well-formed.
2. **`launchStage` is normalised to `BETA`.** Send `ALPHA`; do not assert on it
   coming back unchanged.
3. **Defaults are injected** — `serviceAccount` defaults to the compute default SA,
   `resources.limits` to 2Gi/2000m. Set both explicitly.
4. ~~**There is no `ssh` verb** on `gcloud alpha|beta run instances`. Access is via
   **IAP tunnel** — the gym SA already holds `roles/iap.tunnelResourceAccessor`.
   This is the same path the `cloudrun-instances-runtime` branch's `iap_exec.go`
   implements (§6.1a), so there is working prior art.~~
   **WRONG — retracted 2026-08-26.** `gcloud beta run instances ssh` exists; it is
   merely hidden from the group listing. See the banner above. **Do not build the
   §6.1a IAP-tunnel plumbing**; `iap_exec.go` is prior art for an approach we no
   longer need.

## ~~Alternative: `gcloud alpha run instances replace`~~ — moot

I had flagged the `replace FILE` (YAML spec) verb as a possible way to avoid a
hand-rolled REST client. **Moot given the scope note above:** there is no REST client
to avoid, and `--sandbox-launcher` on `create` is the destination anyway.

## What still needs verifying

`validateOnly` proves the **API accepts** the field. It does **not** prove the
platform then **injects** `/usr/local/gcp/bin/sandbox` into the running container.
That gap is exactly the class of thing that has bitten this design repeatedly, so:

- [ ] Real create with `sandboxLauncher: true`
- [ ] IAP into it and confirm `/usr/local/gcp/bin/sandbox` exists
- [ ] Then the three arity checks (§3.2b): `--mount` twice, `-e` twice, mount key syntax
