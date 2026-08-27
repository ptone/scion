# Brief: write install docs for the single-node Cloud Run tier — then follow them yourself

Author: sn-impl-arch (architect). Date: 2026-08-27. Approved by ptone 06:24. Task #50.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find, **stop and message me** — do not improvise. That rule
has caught several of my own errors this week.

**Two other agents are running a stress test in parallel.** They own their own instances. Do not
touch them. §6 has the list.

---

## 1. What exists today, which is nothing

The tier merged upstream at `f99a8189`, 37 files. I listed all of them. **Zero user-facing docs.**
The only documentation in that merge is `.design/hosted/cloud-run-single-node.md`, which is an
internal design doc, and `image-build/README.md`, which is about building images rather than
installing them. No `docs-site` page. No scripts.

**There is also a decoy, and it is the first thing a new user will find.** `scripts/cloudrun/`
already exists in the repo. It is **not us**. It is the HA tier: a Cloud Run *service* backed by
Cloud SQL Postgres, GCS and Filestore — different architecture, different cost, different
operational story. Right now the most discoverable thing in the repo named `cloudrun` deploys the
wrong tier. So our status is worse than undocumented; it is undocumented with something misleading
sitting where the docs should be.

## 2. The one rule that matters most

**Write the instructions, then follow them yourself, verbatim, on a clean deploy. Fix what breaks.
Repeat until a literal run works.**

This is not a nice-to-have and it is not a final QA step. It is the point of the task. Last night
this project shipped a repaired Cloud Build path that had been fixed by inspection and never once
executed; it happened to work, and we only learned that when someone ran it for real. **Docs that
have never been followed are the same failure class as a code path that has never been run.**

So: no step you have not personally executed. If you cannot execute a step, mark it explicitly as
unverified rather than writing it as though you had.

## 3. Deliverables

### 3.1 The tutorial

`docs-site/src/content/docs/hosted/single-node/cloud-run.md`, plus its sidebar entry.

Look at the neighbours in `docs-site/src/content/docs/hosted/single-node/` and match their
conventions — frontmatter, heading depth, callout style. Do not invent a new format.

It must cover, end to end:

1. **Prerequisites** — what the operator needs before starting. Be exact about IAM roles; a missing
   role is the most common first-run failure and the error is rarely self-explanatory.
2. **The deploy command**, with the image coordinate.
3. **IAP setup.** ptone asked for this specifically. §6 of the design doc is the reference. Note
   that a deploy with `iapEnabled: false` is **refused**, not merely warned about — that is
   deliberate and the doc should say why.
4. **First login**, and how the first admin is established.
5. **Creating a project and starting an agent**, through to attaching to its terminal.
6. **Durability.** §5 of the design doc. **Workspaces are lost on redeploy.** State this plainly and
   early. An operator who learns it by losing work will not forgive the doc that buried it.
7. **Teardown**, and what it costs to leave running.

### 3.2 The scripts

Under `scripts/single-node/`. Keep them small and readable — an operator should be able to read one
and see what it does to their project.

**Name the directory and the docs so nobody confuses them with `scripts/cloudrun/`.** You may add
**one sentence** at the top of `scripts/cloudrun/README.md` pointing to the other tier. One
sentence, factual, no restructuring — that file belongs to the HA tier and is not yours to rework.
If you think it needs more than a sentence, tell me instead of doing it.

### 3.3 Sizing guidance — BLOCKED, and you must not invent it

ptone asked for sizing guidance in the same breath as docs. **You do not have it and you must not
make it up.**

We have never run more than one agent on an instance. The stress test that will answer this is
running right now in parallel with you. Defaults are `--cpu 4 --memory 8Gi`.

Leave a clearly marked placeholder section. Write **no number** — not a range, not an estimate, not
"roughly". A number in a doc becomes true by being printed, and it will outlive every caveat you
attach to it. I will give you the real figures when the stress agents report, or I will tell you to
ship without the section.

Do also record, from `ptone/scion#1287`, that there are **no per-agent resource limits** — agents
share the instance budget and one heavy agent can starve the others. That is a real, known,
publishable fact, and it is the honest thing to say while the numbers are pending.

## 4. Known defects that will bite you while you follow your own instructions

You will hit these. **They are not yours to fix.** Note them, work around them if the doc can
honestly do so, and tell me.

- **#37 / #48** — an agent create that specifies neither `template` nor `harnessConfig` fails with a
  500. Explicit `template: "default"` and `harnessConfig: "claude"` work. **If your doc tells the
  operator to create an agent, it must be a form that actually works.**
- **`ptone/scion#1291`** — image-pull failure on first deploy is undiagnosable; the error names a
  cache mirror rather than the image you asked for. If a reader can plausibly hit this, a
  troubleshooting note is worth more than a paragraph of prose.
- **`ptone/scion#1293`** — there is no public default image, so `--image` is required. Your doc must
  give a working coordinate.
- **`ptone/scion#1274`** — the agent workspace is a depth-1 shallow clone and cannot push to any
  remote but `origin`.

**A/B before attribution.** If something fails, check whether it also fails in the simplest case
before you conclude your instructions caused it. Several confident wrong conclusions on this project
came from skipping that.

## 5. The image and the environment

Project `ptone-experiments`, region `us-east4`. Credentials come from the metadata server — no key
file. Impersonate `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`. **Do not print
access tokens to stdout; this has happened before on this project.**

Image:
`us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni@sha256:e3eab113675848be634513b1e35bb40a03c0ba109b4ce771eac4b8905beafaaa`

Tags `f99a818` and `latest` currently point at that digest. **In the doc, give the reader the
`f99a818` tag for readability and the digest for pinning, and explain the difference in one line.** A
tag can be moved; only the digest identifies the artifact. For your own verification run, use the
digest.

## 6. What you must NOT do

- **Do not touch the stress agents' instances**, or any of: `e2e-omni`, `e2e-walk-r2`, `iap-demo`,
  `q2-control`, `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t`. All **do-not-delete**. `sn-ready` is
  ptone's live instance — do not touch, restart or delete it. Keep `iap-demo` up.
- **Do not fix any product defect.** Docs only. If the only honest doc is "this is broken", tell me
  and I will decide.
- **Do not invent a sizing number.** See §3.3.
- **Do not document a step you have not run.**
- **Do not restructure `scripts/cloudrun/`** beyond the one permitted sentence.
- **Do not open an upstream PR.** Agents have fork write access only, by design. Push a branch on
  `ptone/scion` named `scion/<something-descriptive>` and report it; ptone opens upstream PRs.
- **Tear down anything you deploy** when you are done.

## 7. Report back

Message `sn-impl-arch` with:

- The branch name and commit SHA.
- **Confirmation that you followed your own instructions on a clean deploy, and what broke the first
  time.** If nothing broke, say so explicitly — I will be mildly suspicious and would rather hear it
  stated than assumed.
- Every defect you tripped over, with its A/B result.
- Anything you had to mark unverified, and why.
- Your judgement on whether the `scripts/cloudrun/` confusion needs more than one sentence.

**If a premise here turns out to be wrong, stop and tell me.** I would much rather revise this brief
than have you build on a wrong premise of mine. One of my briefs this week asserted a count that a
developer checked, disproved, and was right to override.
