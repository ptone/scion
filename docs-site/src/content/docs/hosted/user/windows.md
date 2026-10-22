---
title: Windows
description: Use the Windows build of scion as a Hub client — scion ssh, scion ssh-config and scion vscode from a Windows machine — and what it protects, refuses and does not support.
---

**What you will learn**: what the Windows build of `scion` does, how to reach agents with `scion ssh`, `scion ssh-config` and `scion vscode` from a Windows machine, which commands are supported, how scion protects its private files on Windows, and how to fix each refusal.

The Windows build is a **Hub client only**. It talks to a Hub that runs somewhere else (see [Connecting to a Hub](/scion/hosted/user/hosted-user/)). It does not run the Hub, the Runtime Broker or agents, so Workstation mode (`scion server start`) is not available on Windows. Commands outside the supported set fail with a clear error (see [Not supported on Windows](#not-supported-on-windows)).

## Install

Download the zip for your machine from the project's GitHub releases:

| Machine | Release asset |
| :--- | :--- |
| x64 (Intel, AMD) | `scion-windows-amd64.zip` |
| ARM64 | `scion-windows-arm64.zip` |

Each zip holds a single `scion.exe`. Extract it to a folder of your choice and add that folder to your user `PATH`. There is no installer, and the binary is not signed.

You need:

- **A Hub to connect to.** Add it and log in as on any other platform:

  ```powershell
  scion hub add team https://hub.example.com --use
  scion hub auth login
  ```

- **A Hub with tunnels enabled**, for `scion ssh`, `scion ssh-config` and `scion vscode`: the `hub.conduit` [experiment](/scion/reference/experiments/) turned on at the Hub, and agents with a Conduit session. They connect through `scion tunnel`; see [Tunnels from your machine](/scion/hosted/user/port-forwarding/#tunnels-from-your-machine-scion-tunnel). `scion ssh-config` itself only reads from the Hub and works either way, but without tunnels `scion ssh`, `scion vscode` and the hosts installed by `scion ssh-config --write` fail to connect with `does not support tunnels` (see [The Hub does not support tunnels](#the-hub-does-not-support-tunnels)).
- **An OpenSSH client named `ssh` on your `PATH`**, for `scion ssh`, `scion ssh-config` and `scion vscode`. Both the Windows OpenSSH client (`ssh.exe`) and Git for Windows' `ssh` work.
- **For `scion vscode`:** VS Code with its `code` command on your `PATH`, and the **Remote - SSH** extension.

Your scion files live under `%USERPROFILE%\.scion`, and the SSH files under `%USERPROFILE%\.ssh`.

## Reach agents from Windows

These three commands are the main reason to run scion on Windows. Each one connects through `scion tunnel` and the Hub, and pins the agent's SSH host key from the Hub, so you set up no keys or `known_hosts` entries yourself. For the full flag reference, see [`scion ssh`](/scion/reference/cli/#scion-ssh), [`scion ssh-config`](/scion/reference/cli/#scion-ssh-config) and [`scion vscode`](/scion/reference/cli/#scion-vscode).

### Choosing the agent

Name the agent as `<agent>` or `<project>/<agent>`. On Windows, `scion use` (and `scion project use`) is not supported, so you cannot select a current project there. A bare `<agent>` is looked up:

1. in the project given with `--project`;
2. otherwise, when you run the command inside a project directory linked to the Hub (`scion hub link`), in that project. Inside a project directory that is not linked, the command fails;
3. otherwise, as the only agent with that name across all projects on the Hub.

`scion hub add ... --use` selects the current **Hub** only, not a project. When an agent name exists in more than one project, write `<project>/<agent>` or pass `--project`.

### `scion ssh`: open a shell

```powershell
scion ssh my-project/my-agent
scion ssh my-project/my-agent -- git status
```

`scion ssh` runs your `ssh` client with `scion tunnel` as its `ProxyCommand`. It writes the agent's host key to a temporary `known_hosts` file in your temporary folder (`TMP` or `TEMP`) for the session and deletes it afterwards. Flags for `scion ssh` go before the agent; everything after the agent is passed to `ssh` unchanged. It never prompts, and it exits with the remote command's status, or 255 for a local or connection failure.

User names and `TEMP` paths with spaces (for example `C:\Users\Jane Doe\AppData\Local\Temp`) work. The temporary path is refused only if it contains a control character, `%` or `"`; the error then ends with `set TMP and TEMP to a plain path`.

If no `ssh` is found, `scion ssh` fails with `ssh client not found on PATH`.

### `scion ssh-config`: plain `ssh`, `scp` and editors

```powershell
scion ssh-config            # print the files; nothing is written
scion ssh-config --write    # install them
```

With `--write`, scion installs one include file and one known-hosts file per Hub:

| File | Contents |
| :--- | :--- |
| `%USERPROFILE%\.ssh\config` | gains one line at the top, `Include ~/.ssh/config.d/scion-*`, if it is not there yet |
| `%USERPROFILE%\.ssh\config.d\scion-<hub>` | a `Host` block per agent |
| `%USERPROFILE%\.scion\ssh\known_hosts-<hub>` | the pinned host keys |

`<hub>` is a label derived from the Hub's host name: its first DNS label (`hub` for `https://hub.example.com`), or the whole host when another Hub already uses that label. Each agent is then reachable as `scion.<hub>.<project>.<agent>`:

```powershell
ssh scion.hub.my-project.my-agent
```

Without an agent argument every agent on the Hub is included, and blocks of agents that no longer exist are removed; if the listing fails or is incomplete, nothing is written. With an agent, only that agent's block is updated. `~/.ssh/config` is rewritten only when it lacks the `Include` line.

Each `Host` block's `ProxyCommand` records the absolute path of `scion.exe`, quoted so that both the Windows OpenSSH client and Git for Windows' `ssh` read it as one word, spaces included. **If you move `scion.exe`, run `scion ssh-config --write` again.**

### `scion vscode`: open VS Code on an agent

```powershell
scion vscode my-project/my-agent
scion vscode my-project/my-agent --path /workspace/sub
```

`scion vscode` leaves `~/.ssh/config` alone. It first checks the agent with one SSH tunnel (if that fails, nothing is written), then writes the agent's `Host` block and host key to `%USERPROFILE%\.scion\vscode\ssh_config` and `%USERPROFILE%\.scion\vscode\known_hosts`, keeping every other agent's entries, and then runs:

```
code --user-data-dir %USERPROFILE%\.scion\vscode\user-data --remote ssh-remote+<host> /workspace
```

That separate VS Code user data directory sets `remote.SSH.configFile` to scion's `ssh_config`, so your own VS Code settings are never changed. Extensions are shared with your normal VS Code. `--prune` removes the blocks of this Hub's agents that no longer exist. If `code` is missing, the command fails with `VS Code CLI code not found on PATH`.

Two `scion vscode` runs update these files one at a time; a run that waits more than two minutes for another fails with `another scion vscode is still updating these files`.

### Dotfile-managed `~/.ssh`

Dotfile managers such as chezmoi, stow and yadm often make `~/.ssh` or the files in it links. scion supports that on Windows:

- **Links are followed.** Symlinks and junctions on the way to `.ssh`, `.ssh\config.d`, `.scion\ssh` and `.scion\vscode` are followed. When a file scion writes (for example `~/.ssh/config`) is a symlink, the file it points to is replaced and the link is kept.
- **Nobody else may be able to change the target.** After following links, scion checks the real directories and files: each existing directory, and the directory holding each file it writes, must be owned by you, SYSTEM or Administrators, and must grant no write, delete or permission-change rights to anyone else. A file it replaces must be owned by you, SYSTEM or Administrators. Otherwise the write is refused (see [Refusals and how to fix them](#refusals-and-how-to-fix-them)).
- **SSH files owned by Administrators or SYSTEM are accepted.** Files created from an elevated shell are owned by Administrators; that is fine.
- **A file you own but left too open is replaced** by one with an owner-only ACL. The old ACL is not copied, because anyone who can write your SSH config can run commands as you through `ProxyCommand`.
- **Hard-linked files are refused.** A file scion must write that has more than one name is refused, never written.
- **Nothing is written when something is refused.** Every file an operation will write is checked before any directory is created or any file is written.

When a followed link leads into a directory whose parent another user can change, scion prints a warning and carries on:

```
scion: warning: <link> is a link to <target>, and the directory above it, <parent>, can be changed by another user (...); scion protects the files it writes there, not the path to them
```

The files themselves are still checked as above. To silence the warning, remove the other user's write access to `<parent>`, or move the link target somewhere only you can change.

## Supported commands

The Windows build runs these commands, and nothing else. The list is an allowlist: a command added to scion later is unsupported on Windows until it is deliberately added to the Windows allowlist.

- **Agents over the Hub:** `list`, `ssh`, `ssh-config`, `vscode`, `tunnel`, `tunnel status`
- **Messages:** `message`, `message channels`, `messages`, `messages read`
- **Conversations:** `conversation` and `conversation list`, `messages`, `create`, `get`, `get-message`, `set-default`, `participants`, `join`, `leave`, `catch-up`; `conversation group` and its `list`, `get`, `create`, `rename`, `delete`, `add`, `remove`
- **Configuration:** `config` and `config list`, `set`, `get`, `unset`, `validate`, `migrate`, `dir`, `cd-config`, `cd-project`, `schema`
- **Hub list and login:** `hub`, `hub add`, `hub list`, `hub rename`, `hub remove`, `hub status`, `hub shadow`, `hub auth`, `hub auth login`, `hub auth logout`, `hub auth federation-token`
- **Hub projects and linking:** `hub projects`, `hub projects info`, `create`, `delete`; `hub link`, `hub unlink`, `hub enable`, `hub disable`
- **Hub secrets and environment:** `hub secret` and `set`, `get`, `list`, `clear`, `update`; `hub env` and `set`, `get`, `list`, `clear`
- **Hub administration (Hub API calls):** `hub brokers`, `hub brokers info`, `hub brokers delete`, `hub brokers join-token`, `hub brokers join-token create`; `hub users`, `hub users provision`; `hub invite` and `create`, `list`, `revoke`, `delete`; `hub allow-list` and `add`, `remove`, `list`, `import`; `hub hook` and `list`, `show`, `create`, `update`, `activate`, `delete`; `hub messaging`, `get`, `set`; `hub notifications`, `hub notifications ack`
- **Access tokens:** `hub token` and `create`, `list`, `revoke`, `delete`, `scopes`
- **Target-hub profiles and test identities:** `hub profile` and `add`, `list`, `show`, `remove`; `hub test-identity` and `issue`, `token`, `list`, `delete`, `web-code`, `teardown`, `issue-admin`, `cleanup`, `provenance`, `revoke-all`, `residue`, `delegate`, `delegate create`, `delegate list`, `delegate revoke`
- **General:** `version`, `help`, `global-flags`, `completion` (`bash`, `zsh`, `fish`, `powershell`)

`hub brokers` commands only call the Hub's API; nothing broker-side runs on your machine. When `hub test-identity` writes a token file with `--out`, it reports the file as protected by an `owner-only ACL` instead of `mode 0600`.

## Not supported on Windows

Every other command is hidden from `scion --help` on Windows, and running it fails before it does any work:

```
Error: scion server start is not supported on Windows; the Windows build supports hub client commands only (see docs: Windows)
```

The error names the command by its canonical name, so `scion broker join` reports `scion runtime-broker join` and `scion run` reports `scion start`. Global flags are still checked first.

The unsupported commands fall into these groups:

- **The server and the Runtime Broker** (`server`, `runtime-broker`): running a Hub or a broker on Windows is out of scope.
- **Agent-mode commands** (`whoami`, `expose`, `unexpose`): these run inside agent containers and read in-container token files.
- **Agent lifecycle and workspace commands** such as `start`, `create`, `stop`, `suspend`, `resume`, `delete`, `attach`, `logs`, `look`, `sync`, `init`, `templates` and `doctor`: they have not been checked for Windows file handling. Run them from a Linux or macOS machine.
- **Project selection** (`use`, `project use`): see [Choosing the agent](#choosing-the-agent).
- **Hub database tools** (`hub secret migrate`, `hub secret migrate-names`): these run on the Hub's host and open its local database.

## How private files are protected

On Linux and macOS, scion protects its private files (logins, tokens, per-user state) with mode `0600` and directories with `0700`. On Windows, mode bits protect nothing, so scion does not use them. Instead:

- **Private files and directories get an owner-only ACL when they are created**, never afterwards: full access for you and SYSTEM only, inheritance from the parent turned off, and you set explicitly as the owner (even in an elevated shell, where Windows would otherwise make Administrators the owner).
- **Files holding secrets are checked when scion reads them** (`credentials.json`, the cached identities under `hub-profiles`, the tunnel state under `run\tunnels`). A file passes when you own it and its ACL grants access only to you, SYSTEM or Administrators. Administrators are accepted because the default ACL of your user profile includes them. Accounts are compared by SID, never by name.
- **No links inside private folders.** scion refuses, rather than follows, a symlink or junction in these places: in place of `credentials.json`; anywhere below `hub-profiles`; and at `run\tunnels` or below it. Links higher up are followed: `%USERPROFILE%\.scion` itself, `hub-profiles` itself and `run`. So a `%USERPROFILE%\.scion` that is a junction to another NTFS drive works. When scion rewrites a private file such as `context.yaml` and finds a link in its place, it replaces the link with an ordinary private file instead of writing through it.
- **Atomic replacement repairs open files.** When scion rewrites a private file you own, it writes a new owner-only file and swaps it in, so a file whose ACL had become too open ends up private again.

`~/.ssh` is the exception to the "no links" rule; see [Dotfile-managed `~/.ssh`](#dotfile-managed-ssh).

## Refusals and how to fix them

### The Hub does not support tunnels

```
the hub at <hub> does not support tunnels (HTTP <n>)
```

or `the hub does not support tunnels (...)`.

`scion ssh`, `scion vscode` and the hosts installed by `scion ssh-config --write` reach agents through `scion tunnel`, and this Hub does not offer tunnels. `scion vscode` prints the message and exits with code 11. With `scion ssh` and plain `ssh`, `scion tunnel` prints it as the `ProxyCommand`, and `ssh` exits with 255. Ask the Hub's administrator to turn on the `hub.conduit` [experiment](/scion/reference/experiments/); the agent also needs a Conduit session. This is a Hub setting, not something to change on Windows.

The other refusals are about file protection. Most of them include the path and the SID of the account that has too much access. To see a file's ACL, run `icacls <path>`. To remove another account's access, first turn off inheritance on that file or folder (`icacls <path> /inheritance:d`), then remove the account (`icacls <path> /remove:g *<SID>`).

### `credentials.json` is readable by others

scion does not read a credentials file other accounts can access. Commands then behave as if you were not logged in to any Hub, and most of them do not say why. To see the reason, run `scion hub auth logout`, which fails with:

```
failed to remove credentials: failed to read credentials file: refusing to read credentials from C:\Users\<you>\.scion\credentials.json: not private: ... grants access to <SID> (mask 0x...); ... Delete the file and log in again with 'scion hub auth login'; logins to other hubs stored in it must be repeated too
```

Delete the file and run `scion hub auth login` again; the new file is created with an owner-only ACL. The file holds the logins of every Hub you use, so log in to your other Hubs again too. (Logging in without deleting the file first also replaces it, with the same result: only the Hub you just logged in to is kept.)

### A private file has a foreign ACE or owner

```
not private: <path> grants access to <SID> (mask 0x...); scion recreates the file with an owner-only ACL if you delete it
not private: <path> is owned by <SID>, not the current user <SID>
```

For a file (for example in `hub-profiles` or `run\tunnels`), delete it; scion creates it again with an owner-only ACL. A file another account owns is never repaired, only refused.

### An SSH file or folder can be changed by someone else

For example:

```
refusing to write <path>: its directory can be changed by another user: not private: <dir> grants access to <SID> (mask 0x...); ...
refusing to write <path>: not private: <file> is owned by <SID>, which is not the current user <SID>, SYSTEM or Administrators
```

The folder holding an SSH file scion writes (or an existing folder on the way to it) grants write, delete or permission rights to another account, or the file is owned by another account. Remove that account's access to the folder with `icacls` as described above, or take ownership of the file (`takeown /f <file>`) or delete it. If the path is a dotfile manager's link, fix the link's **target**. The error may end with `scion recreates the file with an owner-only ACL if you delete it`; when it names a folder, remove the other account's access to the folder instead of deleting anything.

### A file is hard-linked

```
refusing to write <path>: refusing <file>: it has 2 names: file has more than one name (hard link)
```

For files outside the SSH folders the message contains `refusing <file>: it has 2 names: ...`, without the `refusing to write` prefix.

scion will not write through a hard link, because the same file also lives somewhere else. Replace the hard link with an ordinary file (copy it, delete the original link, and move the copy into place), or make it a symlink, which scion follows.

### A volume without ACLs

```
cannot store private file under <folder>: the volume (FAT32) does not support access control lists, so the file cannot be protected
```

FAT, FAT32, exFAT and some network shares cannot store ACLs, so scion cannot protect a private file there and refuses to create one; there is no fallback. Checking an existing file there can also fail with `has no owner in its security descriptor`. Keep `%USERPROFILE%\.scion` and `%USERPROFILE%\.ssh` (and any link targets) on an NTFS volume.

### A link inside a private folder

```
refusing <path>: symlink, junction or other reparse point refused inside a private tree; tree <folder> (volume ..., file ...)
```

A symlink or junction below one of scion's private folders under `%USERPROFILE%\.scion` is refused, whatever it points to. Replace it with a real folder or file. To keep scion's data on another drive, link `%USERPROFILE%\.scion` itself instead.

## Known limitations

- **No server or broker on Windows.** The Hub, the Runtime Broker and agents run on Linux or macOS (or Kubernetes); see [Choosing a Mode](/scion/choosing-a-mode/).
- **No agent lifecycle commands.** `start`, `stop`, `create`, `delete`, `attach`, `logs` and `look` are not supported on Windows, even against a remote Hub.
- **WSL is not supported for `scion vscode`.**
- **No installer and no code signing**: the release is a zip holding `scion.exe`.
- **Test coverage on Windows is partial.** The Windows CI job runs the private-file, SSH and VS Code tests and a selected set of packages; most of the CLI and configuration test suites still run on Linux only. The Windows job is not yet a required check.
