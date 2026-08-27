# Brief: move the REST credential check before the first mutation, and switch it to ADC

Author: sn-impl-arch (architect). Date: 2026-08-27, 21:30. Task #85 (internal number).

**This is an implementation task on `scripts/single-node/deploy.sh`. Approved by ptone at 21:26.**

---

## 1. The defect, as measured

ptone ran the script in a fresh project (`ptone-emblem`) as a test user. **Step 3a created the
Instance. Step 3b then failed:**

```
PATCH https://us-east4-run.googleapis.com/v2/projects/ptone-emblem/locations/us-east4/
      instances/my-scion-hub?updateMask=iapEnabled,invokerIamDisabled
401 UNAUTHENTICATED — reason ACCESS_TOKEN_TYPE_UNSUPPORTED
```

Root cause: **`deploy.sh:511` uses `gcloud auth print-access-token`**, and on his machine that does not
return a standard OAuth2 access token. Proof: the same token is rejected by
`https://oauth2.googleapis.com/tokeninfo` with `{"error":"invalid_token"}`, an endpoint that only
parses normal OAuth2 access tokens. **`gcloud auth application-default print-access-token` works on
his machine.** He confirmed it at 21:26.

**There are two defects here and the second one matters more.**

- **Defect 1, the token source.** Fixable by switching to ADC.
- **Defect 2, the ORDER.** Step 3b only discovers it cannot authenticate *after* step 3a has created
  the Instance. The operator is left with a **half-built deploy: Instance running, IAP off, no
  rollback**, and an error naming a token type rather than an action. Fixing only defect 1 leaves
  this shape in place for the next credential problem we have not thought of.

**Fix both. Defect 2 is the one that must not be skipped.**

## 2. What to build

### 2a. A preflight that runs BEFORE step 3a mutates anything

Add a preflight step that runs before **any** resource is created or modified. It must:

1. Mint the ADC token: `gcloud auth application-default print-access-token`.
2. **If that fails or returns empty, exit non-zero** with a message naming the exact remedy:
   `gcloud auth application-default login`. Do not continue. Do not fall back silently.
3. Validate the token with **one cheap read** against the same v2 API step 3b will PATCH — a `GET` on
   the instances collection for the target project and region is enough. A non-2xx here must abort
   **before** anything is created, printing the HTTP status and the response body.

Sketch only — not production code:

```bash
di_preflight_rest_credential() {
  local tok
  tok="$(gcloud auth application-default print-access-token 2>/dev/null | tr -d '[:space:]')"
  if [[ -z "$tok" ]]; then
    # name the remedy explicitly
    return 1
  fi
  # one cheap GET against the v2 surface; abort on non-2xx
}
```

### 2b. Print the identity, and COMPARE it with the gcloud account

**This is the part most likely to be under-built, so read it twice.**

A check that only asks *"is ADC configured?"* is not sufficient. `gcloud auth` and ADC are **separate
credential stores**. They are usually the same principal. When they are not:

- step 3a runs as the **gcloud account**,
- step 3b runs as the **ADC account**,
- and you get a permission failure on 3b that looks nothing like a credential mismatch.

So the preflight must:

1. Resolve the identity behind the ADC token and **print it**.
2. Resolve the active gcloud account (`gcloud config get-value account`).
3. **If they differ, warn loudly and name both.** This is a warning, not a hard failure — a
   deliberate mismatch is legitimate. But it must be visible.

To resolve the ADC identity, prefer the token's own `email` claim over guessing. **Note:** the
`tokeninfo` response does **not** always carry `email` — a service-account token scoped only to
`cloud-platform` returns `azp`/`aud`/`scope` and no email. I measured that in this container. So
handle the missing-email case; print whatever identifier you do get rather than printing nothing.

### 2c. Switch step 3b to the ADC token

`deploy.sh:511` becomes the ADC call. `:531` keeps sending it as a bearer. Reuse the token minted in
preflight rather than minting twice.

### 2d. Docs

The tutorial currently asks only for `gcloud` and a login. **Add the ADC step to the prerequisites**
(`docs-site/src/content/docs/hosted/single-node/hub-setup-cloudrun.md`), because the script will now
hard-fail without it. A script that requires a credential the docs never mention is the same class of
defect we are fixing.

## 3. Constraints — several of these have bitten this project already

- **Do NOT remove the REST PATCH.** I checked gcloud 582 directly: `deploy --help` and `update --help`
  expose only `--[no-]invoker-iam-check` and `--public`, and `--public` *disables* IAP. **There is no
  flag that ENABLES IAP.** The PATCH is load-bearing. If you "simplify" it away, IAP silently stops
  being enabled and the tier's whole auth model goes with it.
- **`set -euo pipefail` is a global shell option, not function-scoped.** POSIX ignores `-e` for any
  command of an AND-OR list other than the last, and that suppression propagates into a function
  called in such a position. `local x="$(cmd)"` on one line **masks** a failure; separate `local x`
  then `x="$(cmd)"` does fire `set -e`. This file has already had five such bugs fixed.
- **Do not use `2>/dev/null` on the new checks.** Suppressing stderr is how a failed check becomes a
  passing one. If you must suppress, capture and print on failure.
- **Never print an access token to stdout.** It has happened in this project before. Print the
  identity, never the token. The prefix (`ya29.`) and length are fine.
- Keep the script **self-contained and curl-able**: no `source`, no sibling files, no new external
  commands beyond `awk curl gcloud grep mktemp sed`. **Do not add `jq` or `python3`.** I verified the
  current dependency set; adding to it breaks the fetch-and-run path.
- The script must stay runnable from a file. `--help` reads `${BASH_SOURCE[0]}`, which is a known
  limitation under `curl | bash`; **do not make it worse**, and do not "fix" it here — that is a
  separate decision still with ptone.

## 4. Tests

`cmd/deploy_script_test.go` holds 28 Go tests over this script, and `shellcheck` runs in CI. Both must
stay green.

Add coverage for the new behaviour. At minimum:

- Preflight fails when ADC is unavailable, and the message names
  `gcloud auth application-default login`.
- Preflight aborts **before** any create call when the validating GET returns non-2xx.
- The identity-mismatch path emits a warning naming both identities.

**Note the trap in the existing suite:** two "does not panic" pins on step 6 were dropped by
`GoogleCloudPlatform/scion#1325` and nothing replaced them, so step 6 is currently untested. Do not
assume the existing tests cover a path just because they are numerous.

## 5. Rules

- Branch from upstream `main`. The current relevant commit is
  `c13d910b74245ff096332f38fa3e618da8c9ac2b`. **`/workspace` is the `ptone/scion` fork and it has NOT
  synced that commit** — the fork still carries the old 94-line wrapper and the deleted Go command.
  **Check which tree you are on before you edit anything.** An investigator was misled by this today.
- Push to a branch on the **`ptone/scion` fork**. You have fork write access only, by design.
  **Do not open an upstream PR** — ptone does that.
- Fully qualify every GitHub issue number in prose: `ptone/scion#NNNN` or
  `GoogleCloudPlatform/scion#NNNN`. 48 of 48 numbers in `#1270`–`#1320` exist in **both** repos.
  `#85` here is an internal task number.
- Do not deploy to test this unless you need to. If you do: project `ptone-experiments`, region
  `us-east4`, name it obviously yours. **Your gcloud must have `beta run instances`** — these
  containers ship 575.0.0 where it does not exist; `apt-get` upgrade is the confirmed workaround.
  **Do not use the alpha surface**; it has no `--sandbox-launcher` and produces an Instance whose
  scion server crashes.
- **DO NOT DELETE, RESTART OR TOUCH any Instance that is not yours.** On this tier all state is
  ephemeral, so **a restart IS a deletion**. Protected: `e2e-omni`, `e2e-walk-r2`, `iap-demo`,
  `q2-control`, `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, and **`sn-ready`, which is
  ptone's live instance.** A bold DO-NOT-DELETE has already been ignored once on this project.
- Delete any Instance you create.

## 6. Report

Message `sn-impl-arch` with: the branch name, what you changed, the tests you added, and shellcheck +
test results.

**And tell me anything in this brief that is wrong.** Several people corrected me today and every one
of them was right. If what you read in the code contradicts what I wrote above, **stop and tell me
rather than proceeding on my description.**
