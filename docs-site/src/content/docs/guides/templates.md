---
title: Scion Templates Guide
---

Templates are the blueprint for your agents. They define the configuration, tools, and environment that an agent will have when it starts. This guide explains how to manage and use templates effectively.

## What is a Template?

A template in Scion is a directory containing configuration files that are copied to an agent's home directory upon creation. It includes:

- `scion-agent.json`: The core configuration file
- `home/`: the home folder (~) for the agent user inside the container.

The home folder can contain any number of unix user setups most notably:

- `settings.json`: Harness-specific settings (e.g., tools, allowlists). This allows a Scion agent to be configured to the full extent that harness allows (see for example, [gemini](http://geminicli.com) [claude](http://claude) [codex](https://openai.com))
- `system_prompt.md`: Instructions for the LLM. A system prompt can give the agent a stronger core sense of purpose than is possible with user-level instruction files (gemini.md, agents.md, claude.md, codex.md etc)
- `.bashrc`: Shell customization for the agent.


## Managing Templates

Scion provides a suite of commands to manage templates.

### Listing Templates

To see available templates (both project-specific and global):

```bash
scion templates list
```

### creating a New Template

To create a new template, specify a name and optionally the harness type (default is `gemini`):

```bash
# Create a Gemini-based template
scion templates create security-auditor

# Create a Claude-based template
scion templates create react-expert --harness claude

# Create a Codex-based template
scion templates create codex-helper --harness codex
```

This creates a new directory in `.scion/templates/security-auditor/` populated with default files.

### Customizing a Template

Navigate to `.scion/templates/<template-name>/` and edit the files.

- **Modify `system_prompt.md`**: Give the agent a specific persona and set of instructions.
- **Edit `scion-agent.json`**: Change environment variables, mounting volumes, or set the model.
- **Update `settings.json`**: Configure tools and permissions specific to the harness.

### Cloning a Template

You can clone an existing template to use as a starting point:

```bash
scion templates clone security-auditor security-auditor-v2
```

### Deleting a Template

To delete a template, use `scion templates delete` (or `rm`). The command checks both local and Hub locations and prompts for confirmation before deleting:

```bash
# Delete a template (will prompt for confirmation)
scion templates delete security-auditor

# Skip confirmation
scion templates delete security-auditor --yes

# Delete local only (skip Hub check)
scion templates delete security-auditor --no-hub
```

If the template exists in both locations, you'll be prompted to choose whether to delete the local copy, the remote copy, or both.

### Global vs. Project Templates

- **Project Templates**: Stored in `.scion/templates/` within your project. Shared with the team via git.
- **Global Templates**: Stored in `~/.scion/templates/`. Available across all your projects.

Use the `--global` flag to operate on global templates:

```bash
scion templates list --global
scion templates create my-global-agent --global
```

## Using Templates

When starting an agent, specify the template using the `--type` or `-t` flag:

```bash
scion start auditor "Audit the login flow" --type security-auditor
```

If no type is specified, Scion defaults to the `gemini` template (if available) or another harness-specific default (like `claude`, `opencode`, or `codex`).
