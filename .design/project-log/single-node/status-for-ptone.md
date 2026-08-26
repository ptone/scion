# Single-node tier: status for ptone

Date: 2026-08-26. Author: sn-impl-arch.
Language: ASD-STE100 Simplified Technical English.

## Numbering convention

Two number sets appear in this document. Do not confuse them.

- `PR 1266`, `issue 1270` — real numbers in the `ptone/scion` GitHub repository.
- `D-37`, `D-45` — our internal defect numbers. They are task IDs. They are not
  GitHub numbers. Only one of them is filed upstream.

## Summary

- The reference deployment passes the §1 goal. It passes only with manual work.
- Eleven product defects remain open. Five of them cause the manual work.
- One defect (D-45) has a fix in an open pull request: PR 1272.
- Four decisions need you.
- The merge path is six pull requests. The order is fixed by one hard constraint.
- The tutorial and the deploy scripts do not exist. This document specifies them.

---

# Part A — unresolved issues in the reference deployment

The reference deployment is instance `sn-ready` in project `ptone-experiments`,
region `us-east4`. Six other instances are also live: `e2e-omni`, `e2e-walk-r2`,
`iap-demo`, `q2-control`, `sn-step6`, `sn-walk`. All seven are present today.

## A.1 What works

An operator can do all of these steps on `sn-ready`:

1. Deploy with one command.
2. Open the `run.app` URL.
3. Log in through IAP.
4. Create a project.
5. Start a Claude agent.
6. Attach to the agent terminal from the browser.
7. Watch the agent commit to a git remote.

We measured this path end to end on 2026-08-25.

## A.2 Defects that force manual work (5)

| ID | Effect on the operator | What closes it |
|---|---|---|
| D-37 / D-48 | Agent create fails with HTTP 500, or the agent starts with the wrong template. The operator must name the harness-config in every request. | Two lines in `deploy-instance`. Then a hub fix. |
| D-44 | The seeded admin email does not make anyone an admin. The operator cannot use the admin UI. | PR 1272 (fixes the root cause D-45). Then re-test. |
| D-49 | The agent workspace is a depth-1 shallow clone. The agent cannot push to any remote except `origin`. | A clone-depth change in the sandbox provisioner. |
| D-41 | The ambient GCP identity is invisible to the auth preflight. Step 4 needs an escape hatch flag. | Make the preflight read the metadata server. |
| D-42 | `noAuth:true` on agent create makes the request fail. The same request succeeds without that field. | Not yet diagnosed. |

D-37 and D-48 are one defect, not two. We root-caused it on 2026-08-25.

The cause: on a deployed instance the hub has no operational `agent_defaults`.
No template name and no harness-config name reach the hub. The hub guard then
skips both the lookup and the identity stamp. The broker receives an empty
identity. The broker falls back to a search of the local disk. In hosted mode
that disk is always empty. The template degrades in silence. The harness-config
fails with HTTP 500.

The stopgap is two settings in `deploy-instance`:

```
agent_defaults.default_template: default
agent_defaults.default_harness_config: claude
```

## A.3 Defects that make failures hard to diagnose (3)

| ID | Effect on the operator |
|---|---|
| D-39 | An image-pull failure gives an ambiguous "not found" message and wrong advice about the tag. |
| D-46 | A git clone failure kills the sandbox and prints no message. |
| D-35 | The hub rejects sandbox session metrics with HTTP 400. The exit code is never saved. |

## A.4 Latent and unknown (3)

| ID | State |
|---|---|
| D-45 | Security-relevant. The web server never sees live access settings. PR 1272 fixes it. Filed as issue 1270. |
| D-32 | Latent. `relocateToScion` deletes the source home directory if the rename fails. Not yet seen live. |
| D-15 | The daemonize defect mechanism is not found. |

## A.5 Filing status

Only D-45 is filed upstream, as issue 1270. Ten defects exist only in our
scratchpad.

The scratchpad is not a git repository. On 2026-08-26 we removed that risk. The
full working record is now on branch `scion/sn-impl-arch` in `ptone/scion`,
under `.design/project-log/single-node/`. It is documentation only. It contains
no code. Commit `8959cbc`.

---

# Part B — decisions that need you

There are four. I give a recommendation for each. I will raise them one at a
time in chat, as you asked before.

## B.1 The merge gate

PR 1265 and PR 1266 are open and wait for you. Both have green checks.

- PR 1265: 259 lines added, 6 files. Security fix. Independent.
- PR 1266: 7575 lines added, 63 files. The tier.

**Recommendation:** merge PR 1265 now. Hold PR 1266 until step C.3.4.

## B.2 Stopgap or full fix for D-37 / D-48

Two options:

- A: add the two settings to `deploy-instance`. This is small and fast. It hides
  the hub defect from the operator. It does not fix the hub.
- B: fix the hub guard. This removes the defect for all callers. It touches
  shared agent-create code. Review is larger.

**Recommendation:** do both. Put A in PR 1268 now. File B as a separate issue
and a separate pull request after the tier lands.

## B.3 A credentialed push test

§1 step 6 says "watch it commit to a git remote". We have proved the commit. We
have not proved a push to a remote that needs credentials, because of D-49.

To prove it, we must mint a narrow-scope token and give it to a test agent.

**Recommendation:** yes, mint one. Limit it to one throwaway repository. Without
this test, §1 step 6 is only half measured.

## B.4 Who writes the tutorial and the scripts

I design. I do not implement. Part C specifies the work in full.

**Recommendation:** dispatch one developer agent. The work is about three
commits. I will review the result against the specification.

---

# Part C — the path to upstream merge

## C.1 Present state of the pull requests

All numbers are in `ptone/scion`. All checks are green.

| PR | Branch | Target | Size | Content |
|---|---|---|---|---|
| 1265 | `scion/security-fix-p0-s1` | `main` | +259 / 6 files | Refuse dev auth on non-loopback interfaces. |
| 1266 | `scion/dev-rebase-1294` | `main` | +7575 / 63 files | The Cloud Run Sandbox single-node tier, P0 to P5. |
| 1268 | `scion/sn-dev-ready` | `scion/dev-rebase-1294` | +465 / 6 files | Sandbox exit detection. `deploy-instance` fixes. Marked DO NOT MERGE. |
| 1269 | `scion/sn-ws-mount` | `scion/dev-rebase-1294` | +806 / 9 files | Workspace mount fix (D-43). Marked DO NOT MERGE. |
| 1272 | `scion/wc-dev` | `main` | +221 / 5 files | Fixes D-45 / issue 1270. Independent. |
| 1264 | `scion/broker-auth-gap` | `main` | +390 / 2 files | Broker caller verification. Independent. |

## C.2 One hard ordering constraint

**Do not merge PR 1266 alone. On its own it ships a deploy command that cannot
work.**

Two environment variables are missing on the PR 1266 branch:

- `SCION_SERVER_MODE=hosted`. Without it the hub enables dev auth and refuses to
  boot. This is D-40.
- `SCION_IMAGE_REGISTRY`. Without it the broker cannot pull agent images. This
  is D-38.

Both fixes exist only on the PR 1268 branch and the PR 1269 branch. PR 1268 and
PR 1269 both target the PR 1266 branch, and both carry a DO NOT MERGE marker.

PR 1268 and PR 1269 must land into `scion/dev-rebase-1294` before PR 1266 goes
to `main`.

## C.3 The merge sequence

1. Merge PR 1265 into `main`. It is small, standalone, and green.
2. Remove the DO NOT MERGE marker from PR 1268 and PR 1269. Merge both into
   `scion/dev-rebase-1294`.
3. Add the D-37 / D-48 stopgap to `scion/dev-rebase-1294` (decision B.2).
4. Rewrite the PR 1266 description (this is D-47). 63 files need a review map:
   which files are the runtime, which are the command, which are tests, and
   which are internal engineering logs.
5. Merge PR 1266 into `main`.
6. Merge PR 1272 into `main`. This can happen at any time. It is independent.
7. File the ten unfiled defects as GitHub issues. Move the design document out
   of the scratchpad and into `.design/`.
8. Land the tutorial and the scripts as one follow-on pull request (C.4).

Step 6 and step 7 do not block step 5.

## C.4 The tutorial and the scripts

### C.4.1 Present state

**No user documentation exists for this tier.**

PR 1266 changes 63 files. It adds no page under `docs-site/`. Its only Markdown
files are internal engineering logs in `.design/project-log/` and one
`image-build/README.md`. There is no reusable script for a third party.

A person outside this project cannot deploy this tier today.

### C.4.2 Where the tutorial goes

The doc site already has the correct section:
`docs-site/src/content/docs/hosted/single-node/`. It holds `overview.md`,
`hub-server.md`, `hub-setup-gce.md`, `auth.md`, `managed-agents.md`,
`metrics.md`, `observability.md`, and `skill-registry.md`.

Add one page and one sidebar entry:

- New file: `docs-site/src/content/docs/hosted/single-node/cloud-run.md`.
- New sidebar entry in `docs-site/astro.config.mjs`, after the line
  `{ label: 'Deploy on a VM (GCE)', slug: 'hosted/single-node/hub-setup-gce' }`.
  Use the label `Deploy on Cloud Run (Instances)`.

### C.4.3 Tutorial contents

Write these sections, in this order. Follow the measured §1 path.

1. **What you get.** One hub. Agents in Cloud Run Sandboxes. IAP login. State the
   cost of an idle instance.
2. **Prerequisites.** A GCP project. Billing enabled. `gcloud` installed. An IAP
   brand and an OAuth consent screen.
3. **Preflight.** Run `scripts/single-node/preflight.sh`.
4. **Choose an image.** Give the public omni image tag. Explain how to build your
   own.
5. **Deploy.** Run `scion deploy-instance --name NAME --image IMAGE --project
   PROJECT --region us-east4`. Show the seven steps that the command prints.
6. **First login.** Open the `run.app` URL. Sign in through IAP.
7. **Create a project.** Use a git remote.
8. **Start an agent.** Use the Claude harness.
9. **Attach.** Open the terminal in the browser.
10. **Watch the commit.** Confirm the push.
11. **Troubleshooting.** One table row for each known symptom. Cover D-39, D-41,
    D-42, D-44, D-46, and D-49.
12. **Teardown.** Run `scripts/single-node/teardown.sh`.

### C.4.4 The scripts

Create `scripts/single-node/`. The repository already uses this pattern in
`scripts/cloudrun/` and `scripts/starter-hub/`. Follow it.

| File | Purpose |
|---|---|
| `README.md` | What each script does. Which one to run first. |
| `preflight.sh` | Enable the required APIs. Create the runtime service account. Grant the IAM roles. Check the IAP brand. Check that the image is readable. Print PASS or FAIL per check. Change nothing that already exists. |
| `deploy.sh` | A thin wrapper on `scion deploy-instance`. Apply the settings that the command does not yet set. Print the URL. |
| `verify.sh` | Walk §1 steps 0 to 6 against a live instance. Print PASS or FAIL per step. Exit non-zero on any failure. |
| `teardown.sh` | Delete the instance, the IAP policy binding, and the service account. Ask for confirmation first. |

Rules for all scripts:

- Use `set -euo pipefail`.
- Accept `--project`, `--region`, and `--name`. Do not hard-code them.
- Make every script safe to run twice.
- Never print an access token to stdout.
- Support `--dry-run`.

`verify.sh` is the most valuable script. It converts our §1 goal into a test that
anyone can run.

### C.4.5 Suggested phases for the developer

- Phase 1: `preflight.sh` and `teardown.sh`, plus `scripts/single-node/README.md`.
- Phase 2: `deploy.sh` and `verify.sh`.
- Phase 3: the tutorial page and the sidebar entry.
- Phase 4: run the tutorial in a clean GCP project. Correct every step that fails.

Phase 4 is not optional. An unmeasured tutorial is not a tutorial.

---

# Acceptance criteria

The work in Part C is done when all of these are true.

1. PR 1265 is merged into `main`.
2. PR 1268 and PR 1269 are merged into `scion/dev-rebase-1294`.
3. The PR 1266 description gives a review map of its 63 files.
4. PR 1266 is merged into `main`.
5. PR 1272 is merged into `main`, and D-44 is re-tested on a fresh instance.
6. Each of the ten unfiled defects has a GitHub issue.
7. DONE. The design document is in `.design/project-log/single-node/` on
   branch `scion/sn-impl-arch`. Merge that branch, or fold the directory into
   PR 1266, before the scratchpad volume goes away.
8. `scripts/single-node/` holds the five files in C.4.4.
9. `verify.sh` exits zero against a fresh instance.
10. A person who did not build this system deploys the tier in a clean GCP
    project. That person uses only the tutorial. That person reaches §1 step 6
    with no help.

Criterion 10 is the real test. The other nine only prepare for it.

---

# Open questions

1. Which omni image tag do we name in the tutorial? It must stay readable to the
   public and it must not move without notice.
2. Do we publish this tutorial to the public doc site now, or hold it until the
   five Part A.2 defects are closed? A public tutorial with six troubleshooting
   rows sets a low first impression.
3. Does the tutorial assume an internal Google GCP project, or any GCP project?
   The IAP brand step is different for each.
