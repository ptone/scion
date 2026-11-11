# Skill URI reference

Loaded from the **team-creation** skill when a template references skills by URI.

## Formats

Three forms are accepted:

```
skill://<registry>/<scope>/<name>@<version>                   # skill bank (Hub registry)
gh://<owner>/<repo>/<name>[@<ref>][?token=<SECRET>]            # GitHub: resolves <repo>/skills/<name> ONLY
https://github.com/<owner>/<repo>/tree/<ref>/<path>[?token=<SECRET>]   # GitHub: any directory holding a SKILL.md
```

- `gh://` addresses only a top-level `skills/<name>` directory. For a monorepo or plugin layout (for example `cursor/plugins` with `pstack/skills/<name>`), use the full `https://github.com/.../tree/<ref>/<path>` form. A `tree/<ref>/skills/<name>` URL is normalized to `gh://`, and a `blob/.../SKILL.md` URL is accepted too.
- Pin `<ref>` to a **commit SHA** for anything you did not write. The team then does not change when upstream does, and resolution caches better.
- `?token=<SECRET_NAME>` names a secret holding a GitHub token, for private repos. The secret is resolved on the broker by the local resolver, not by the Hub. Without `?token=`, the Hub's GitHub App installation for the project is used, and that is often enough for repos in the same org.

## `skill://` fields

- **registry**: The registry host (typically `scion`)
- **scope**: Where the skill lives — `core` (platform skills), `global` (hub-wide), `project/<id>` (project-scoped), or `user/<id>` (user-scoped)
- **name**: The skill name
- **version**: Version constraint — an exact version (`1.0.0`), a range (`^1.0`), or `latest`

## Examples
- `skill://scion/global/idea-refine@latest` — a global-scope skill from the Hub
- `skill://scion/core/scion@^1.0` — a core platform skill
- `skill://scion/project/my-custom-skill@1.0.0` — a project-scoped skill
- `https://github.com/cursor/plugins/tree/5ff7a359dec6be2128b15c0fe53c1c12a46e41c8/pstack/skills/unslop` — an upstream skill in a monorepo, pinned

## Resolution behavior

- URI skills resolve at provision time, through the Hub. A non-optional skill that fails to resolve fails provisioning.
- URI skills install **after** template-local skills and overwrite a local skill of the same name.
- GitHub URI skills install the whole directory tree, at any depth, with file modes (`0755`/`0644`) preserved. This comes from the git trees API (ptone/scion#5781, #5657). Builds before that fix fetched only top-level files and dropped exec bits. If a skill arrives with only `SKILL.md`, the hub is older. Deliver multi-level skills template-locally until it is upgraded.
- GitHub resolution is cached per ref. SHA pins cache best. A template with about 30 GitHub URIs provisioned without hitting rate limits in practice.
- There is no in-agent command to test resolution. Start one agent from the template and list its skills directory (see **Verify a Team** in SKILL.md).
