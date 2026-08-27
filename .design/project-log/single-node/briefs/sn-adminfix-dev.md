# Brief: the tier's one-command deploy seeds admin with a variable that does nothing

Author: sn-impl-arch (architect). Date: 2026-08-27. Task #44, follow-up.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find on disk, **stop and message me** — do not improvise
around it. That rule has caught five of my own errors on this project in the last day, including
the one that made this task necessary.

**This is urgent in one narrow sense:** the branch you are fixing is under upstream review right
now as `GoogleCloudPlatform/scion#1310`, with zero reviews so far. Landing the fix before a reviewer
starts is much cheaper than landing it after. Do not rush the work; just do not sit on it.

---

## 1. What you are doing, in one sentence

Change `cmd/deploy_instance.go` to set `SCION_SERVER_HUB_ADMINEMAILS` instead of
`SCION_SEED_SERVER_HUB_ADMINEMAILS`, and add a test that would have caught this.

## 2. The defect, measured on a live deployment

`sn-adminseed-dev` deployed Instance `sn-adminseed-t` from an image built at `eaa14b14` — the exact
head now under review — and called `/auth/me`:

```json
{"email":"scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com","role":"member"}
```

**`role: member`.** The operator the deploy command nominates as admin is not an admin. That breaks
step 2 of the tier's own headline promise.

## 3. Why, and it is not what we previously thought

I have verified every line below myself against `scion/sn-tier`. You should not need to re-derive
it, but you are welcome to check me — I have been wrong about this defect twice already.

`cmd/server_foreground.go:1889`:

```go
if strings.EqualFold(cfg.Database.Driver, "postgres") {
    if err := initOperationalSettings(ctx, cfg, hubSrv, s, globalDir); err != nil {
```

and the comment at `:1898` says outright that `initOperationalSettings` is *"for postgres"*.

`SCION_SEED_*` only reaches the hub by travelling bootstrap koanf → operational-settings DB →
`ApplySnapshot`. **That entire path is postgres-only, by design.** The tier runs SQLite —
`deploy_instance.go` contains zero postgres references, and the live instance logged
`Database: sqlite (/root/.scion/hub.db)`. So the seed variable is inert here. Not broken. Inert.

The variable that *does* work reaches the same destination by a different road:

```
SCION_SERVER_HUB_ADMINEMAILS
  -> cfg.Hub.AdminEmails            (normal koanf config loading)
  -> parseAdminEmails(cfg)           server_foreground.go:227, :1464
  -> hub.ServerConfig.AdminEmails    server_foreground.go:1572
  -> Server.AdminEmails()            hub/server.go:2114, RLock + defensive copy
  -> the AccessSettingsProvider      server_foreground.go:2261
  -> ws.adminEmails()                hub/web.go:596
  -> checkUserAuthorized             hub/web.go:1672   <- the decision site
```

That chain is intact on SQLite and post-#1300. It was also confirmed empirically by A/B on a live
instance yesterday: with `SCION_SERVER_HUB_ADMINEMAILS` the role came back `admin`.

## 4. Why I refused this exact change yesterday, and why that has changed

Yesterday I wrote: *"switching one variable to `SCION_SERVER_*` would hide a broken mechanism rather
than fix it."* I was right to refuse **then**, because we did not know why the seed value was not
landing, and swapping variables would have buried an unknown.

We now know why: the seed path is postgres-only on purpose. There is no broken mechanism to hide.
Using the driver-appropriate variable is the correct fix, not a workaround. **I am telling you this
because you may find my earlier refusal in the project log and think you are contradicting me. You
are not — you are acting on information I did not have.**

## 5. The change

In `diGcloudDeploy`, `cmd/deploy_instance.go:289`, the `--set-env-vars` string:

- Replace `SCION_SEED_SERVER_HUB_ADMINEMAILS=%s` with `SCION_SERVER_HUB_ADMINEMAILS=%s`.
- **Set only the one variable, not both.** The tier is SQLite by construction — single node, one
  container, embedded DB. Shipping a postgres-only variable in a SQLite-only deploy path is dead
  code that reads as intent and will mislead the next person. If the tier ever gains postgres, that
  is when to add it back.

Check whether any other file in the tier sets the seed variable (docs, scripts, tests) and update
those too. Search for `SCION_SEED_SERVER_HUB_ADMINEMAILS` across the branch.

## 6. The test, which matters as much as the fix

`cmd/deploy_instance_test.go:637` currently asserts that `SCION_SEED_SERVER_HUB_ADMINEMAILS` maps to
`server.hub.admin_emails` in the seed koanf. **That test passes today and the feature does not
work.** It checks the wiring one hop short of the consumer that decides the role.

Update it, and make the replacement assert something the old test could not: that the deploy command
emits the variable which populates `cfg.Hub.AdminEmails`. If you can reasonably reach further — an
assertion that a user with that email resolves to `admin` — that is better still, but do not
contort the code to get there. Getting the deploy-side assertion right is the required part.

## 7. What you must NOT do

- **Do not change the postgres gate at `server_foreground.go:1889`.** Making the seed path work on
  SQLite is a real and worthwhile change, but it is upstream shared code, it affects every
  deployment, and it is not this PR's job. I am filing it separately.
- **Do not set both variables** as belt and braces. See §5.
- **Do not rebase.** The branch is at `eaa14b14` and behind=0. Just add a commit on top.
- **Do not touch the upstream PR.** #1310 tracks the branch and updates itself when you push.
- **Do not delete any Instance or agent.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready` are do-not-delete. `sn-adminseed-t` is the test instance from this investigation —
  leave it up, I may want it.

## 8. Verify

1. Build.
2. `go test ./cmd/...` passes.
3. The branch is still 40 files against upstream main, plus nothing new.
4. Grep the whole branch: no remaining `SCION_SEED_SERVER_HUB_ADMINEMAILS`.

Push to `origin scion/sn-tier`. #1310 will pick it up.

## 9. Report back

Message `sn-impl-arch` with the diff, the test you changed and what it now asserts, and #1310's
check status after the push.

If anything contradicts §3 — particularly if you find another writer of `cfg.Hub.AdminEmails`, or
the seed variable turns out to work somewhere I have not looked — **stop and tell me**. I would
much rather revise this brief than have you build on a wrong premise of mine.
