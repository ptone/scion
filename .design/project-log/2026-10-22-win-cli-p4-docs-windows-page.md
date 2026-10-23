# 2026-10-22 win-cli Phase 4: Windows docs page

Agent: win-cli-dev-p4docs (EM win-cli-em). Branch `win-cli/p4docs`, base `win-cli/integration` 1904c8826.
Design: win-cli-design.md (sha256 7cb5d107…), §7 and §8 Phase 4 ("Docs page").

## What was done

- New page `docs-site/src/content/docs/hosted/user/windows.md` (slug `hosted/user/windows`, sidebar label
  "Windows", so the gate's error text "(see docs: Windows)" matches it). It sits in the shared hosted User Guide,
  because the Windows build is a Hub client only (D1).
- The page leads with `scion ssh`, `scion ssh-config` and `scion vscode` (ptone: those are the main uses on Windows), then
  covers the supported-command allowlist (copied from `cmd/platform_gate.go` `windowsAllowed`), the "not supported" error text
  (copied from `unsupportedOnWindowsError.Error`), how private files are protected (privatefs), and how to fix each refusal
  (error strings copied from privatefs, credentials, cmd/ssh*).
- A "On Windows?" note in `getting-started/install.md` points to the page.

## Decisions

- Release zips: the release matrix is not merged at the base. EM ruling: state the zip install plainly with #189's
  asset names (`scion-windows-amd64.zip` / `scion-windows-arm64.zip` holding `scion.exe`). The p4ci release change will use
  exactly those names.
- `scion use` / `project use` are gated on Windows. The page documents how a bare `<agent>` is resolved as the code does
  it now (`--project`, then the linked project of the current directory, then a unique name across projects). The EM asked ptone
  whether to allow `use`. That section is self-contained, so it is easy to edit.
- Error excerpts use `...` and `<placeholders>` where the code puts in paths, SIDs or masks.

## Mismatches found and reported to win-cli-em

1. Release zips not in code at base (see above).
2. `scion use` gated, although the vscode and tunnel help text point users to it.
3. C1r/D3: the credentials.json refusal and its fix hint are dropped by most callers. `GetAccessToken` and `IsAuthenticated`
   swallow the error, `getAuthInfo` ignores it, and `Store` (login) replaces the file silently. Only `hub auth logout` and
   `hub remove --logout` print it. The page documents current behaviour. The EM started win-cli-dev-p3fix. When the fix lands, the
   credentials paragraph needs a one-paragraph update.

## Open issues / adjacent observations

- The privatefs `checkACL` message suffix "scion recreates the file with an owner-only ACL if you delete it" is also
  printed for directories (the ssh parent-directory refusal). The page tells users to remove access to the folder instead.
  A follow-up could make the hint depend on whether the object is a file or a directory.
- `hubprofile.HomeDir` prefers `$HOME` over `os.UserHomeDir`, but `config.GetGlobalDir` uses `os.UserHomeDir` (`%USERPROFILE%`).
  Under a shell that sets `HOME` differently (Git Bash, MSYS), hub-profiles could land under a different `.scion` from the rest. Not
  documented. Worth a look.
- The docs workflow triggers only on PRs into `main`. For this fork PR it was started with `workflow_dispatch` on the branch
  (deploy steps are gated to `main`).
- The docs site was not built locally: `d2` and `node_modules` are not installed in this container. Links and anchors were
  checked by hand. The docs workflow run is the build gate.

## Review r1 (win-cli-rev-p4docs-r1, REQUEST CHANGES) and dispositions

- R1 fixed: added "A Hub with tunnels enabled" (the `hub.conduit` experiment and agents with a Conduit session) under "You need",
  with links to the experiments reference and port-forwarding#tunnels-from-your-machine-scion-tunnel. Added a "The Hub does not support
  tunnels" refusal entry (exit 11 text from pkg/tunnelclient/session.go).
- R2: EM ruling. A dev-time astro build is impossible in the LIGHT envelope: V8 runs out of memory under ulimit -v 8000000, even with
  --disable-wasm-trap-handler. No 16G request. The gate is the upstream docs CI at landing, and reviewer r1's full hand check of links and
  anchors is on record.
- Nits 3-7 fixed: the full hard-link error text; vscode order (check, then write, then launch); per-folder link rule
  (`run\tunnels` itself is refused); `hub auth` added to the list; "deliberately added to the Windows allowlist".
- 8 kept as plain wording (EM: docs merge only with or after the p4ci release-matrix change).
- FYI: "For example:" added before the SSH refusal excerpts (the intermediate-directory form is `open <dir>: not private: ...`).

## Batch update after p3fix (integration aa5e88676)

- Rebased onto win-cli/integration aa5e88676 with no conflicts.
- Credentials paragraph rewritten to match the merged pkg/credentials/notice.go. Read paths print `Warning: <refusal with fix hint>`
  on stderr once per process. `hub auth login` over a refused file warns that it replaces the file and that other hubs need a fresh
  login. Both texts are quoted from the code.
- `scion use` is supported (allowlist `use` and `project.use`, but no `project` parent). "Choosing the agent" now has the
  lookup order from cmd/invocation_target.go: --project, then the linked directory project, then the `scion use` project, then a unique
  name. The supported list notes that `scion project` itself stays refused.
