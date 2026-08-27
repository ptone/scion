# Brief: remove the false second-login claim from tutorial section 2

Author: sn-impl-arch (architect). Date: 2026-08-27, 18:37. Task #77 follow-up.
Branch: **`scion/sn-backout`** — the one you own. Do not start a new branch.

**This branch now backs an OPEN upstream PR: `GoogleCloudPlatform/scion#1325`.** ptone opened it at
18:31 and has explicitly approved this change onto it: *"yes. remove the wrong sentence. push a
commit to the branch backing the open pr"*. So your push will update a live PR and re-run its CI.
That raises the cost of a mistake — read the whole brief first.

---

## 1. What was measured

`sn-iaplogin-inv` drove a live hosted Instance behind IAP as a browser. Result: **answer B — there
is no second login.** After the IAP challenge the user goes straight into the app.

The published page claims otherwise. File
`docs-site/src/content/docs/hosted/single-node/hub-setup-cloudrun.md`, section 2, lines 184-187:

```
1. **IAP challenge** — Google sign-in. Use the email that was bound as the IAP user
   during deploy (your gcloud account, or the `--admin-email` value).
2. **Hub login** — After IAP, the Hub presents its own login. The deployer is
   automatically seeded as the first admin.
```

## 2. The trap — two claims are bundled, and only ONE is wrong

Read this before you edit.

- *"After IAP, the Hub presents its own login."* — **FALSE. Remove it.**
- *"The deployer is automatically seeded as the first admin."* — **TRUE. Confirmed live by the same
  investigator, in the same session. KEEP IT.**

I nearly deleted both, and the investigator's split test is the only reason I did not. That sentence
is the page's **only** statement of how the operator gets admin rights. Internal defects #44 and #45
were both about exactly that mechanism being broken; it now works, and the page must keep saying so.

## 3. The part that is not a one-line deletion

The list item is **labelled `**Hub login**`**. That label is itself the false claim. If you delete
only the sentence, you leave a numbered step titled "Hub login" whose body talks about admin
seeding — which reads as though a Hub login still exists.

So the true remaining fact needs a home that is not a login step. Two shapes, your call:

- **A.** Reduce the list to the single IAP step, and carry the admin fact as a following sentence or
  a `:::note`.
- **B.** Keep two items but retitle the second to what actually happens — the user lands in the Hub,
  already signed in, and the deployer holds admin.

I prefer whichever reads better to a stranger at the moment they first open the URL. Say which you
chose and why. If a numbered list of one item looks silly, that is a point for B.

Consider also whether the heading **"## 2. First login"** is still right. Singular "First login" is
arguably fine now — it is the login. Do not change it unless you think it misleads.

## 4. Constraints

- **This file only.** One commit. Nothing else in it.
- **Do not restate the IAP mechanism** elsewhere on the page. One authoritative statement.
- **`starlightLinksValidator` is enabled**, so a dangling internal link fails the docs build. If you
  remove or retitle anything that is a link anchor target, find the referrers first. Run the build:
  `cd docs-site && npm ci && npm run build`. It needs Node 22 and D2. **`build-docs` is a required
  check on the open PR** — if you break it you break ptone's PR, so do not push until it is green
  locally.
- **Do not open a PR, rebase, or force-push.** #1325 already exists and force-pushing under an open
  PR is destructive. A normal push onto the branch tip is what is wanted.
- **Do not touch `scripts/single-node/deploy.sh`.** It was cleared by review at `5e01ea5e`.
- Fully qualify issue numbers: `ptone/scion#NNNN` or `GoogleCloudPlatform/scion#NNNN`. 48 of 48
  numbers in `#1270`-`#1320` exist in **both** repositories. `#44`, `#45`, `#77` here are internal
  task numbers.

## 5. Report back

Message `sn-impl-arch` with:

1. the new head SHA — **the moment you push**, because a live PR moves under ptone when you do;
2. the exact before and after text of section 2;
3. which shape you chose, A or B, and why;
4. the result of the docs-site build;
5. anything in this brief you think is wrong. Six people corrected me today and all six were right.
