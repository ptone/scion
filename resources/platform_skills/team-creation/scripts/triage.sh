#!/usr/bin/env bash
# First-pass triage of an agent pack (plugin, skill bundle) for a port to Scion.
# Prints one TSV row per skill, then the evidence behind each bucket.
#
# Usage: bash triage.sh <pack-dir> [host]
#   pack-dir  directory holding skills/<name>/SKILL.md (and optionally agents/, rules/, scripts)
#   host      cursor | claude-code | codex | generic   (default: cursor). Selects the host-term list.
#
# Buckets are a starting point, not a verdict. Read every "adapt" hit before editing,
# because some hits are external facts (a GitHub login, a product name in prose) that stay.
set -uo pipefail
PACK="${1:?usage: triage.sh <pack-dir> [host]}"
HOST="${2:-cursor}"

case "$HOST" in
  cursor) TERMS='subagent_type|\bTask\b|run_in_background|readonly: *true|AskQuestion|/loop\b|/goal\b|\.cursor/|\.mdc\b|agent-transcripts|environment: *"(cloud|local)"|cloud_base_branch|cloud agent|Cursor (dashboard|restart)|create-skill|/babysit' ;;
  claude-code) TERMS='subagent_type|\bTask\b|run_in_background|\.claude/|CLAUDE\.md|AskUserQuestion|TodoWrite|/compact|hooks?\.json|settings\.json|~/.claude/projects' ;;
  codex) TERMS='\.codex/|AGENTS\.md|codex exec|approval[-_]policy|sandbox_mode' ;;
  *) TERMS='subagent|\bTask\b|/loop|transcript|\.cursor/|\.claude/|\.codex/' ;;
esac
# Host-neutral concepts that still assume host machinery (subagents, transcripts, the host's name).
CONCEPTS='\bsubagents?\b|transcripts?\b|\bCursor\b|Claude Code|\bCodex\b'
TERMS="$TERMS|$CONCEPTS"
# Container-boundary terms: these break when one host process becomes many containers.
BOUNDARY='/tmp/|~/|\$HOME/|worktree|localhost|127\.0\.0\.1|PID|lock ?file|same machine|return the paths'
# Model slugs: vendor model names hard-coded in prose or config.
MODELS='(claude|gpt|grok|gemini|o[0-9]|sonnet|opus|haiku|fable)-[0-9a-z.-]*[0-9][0-9a-z.-]*|-xhigh|-thinking-max'
# Slash commands: routing the human did by typing.
SLASH='(^|[ `(])/[a-z][a-z0-9-]+\b'

printf 'skill\tfiles\thost_hits\tboundary_hits\tmodel_hits\tslash_hits\tfrontmatter_keys\tbucket\n'
for d in "$PACK"/skills/*/; do
  s=$(basename "$d")
  files=$(find "$d" -type f ! -path '*/node_modules/*' | wc -l | tr -d ' ')
  count() { { grep -rEoI --exclude-dir=node_modules -- "$1" "$d" 2>/dev/null || true; } | wc -l | tr -d ' '; }
  h=$(count "$TERMS"); b=$(count "$BOUNDARY"); m=$(count "$MODELS"); sl=$(count "$SLASH")
  fm=$(awk 'NR==1&&/^---/{f=1;next} f&&/^---/{exit} f&&/^[a-zA-Z_-]+:/{sub(/:.*/,"");printf "%s,",$0}' "$d/SKILL.md" 2>/dev/null | sed 's/,$//')
  scripts=$(find "$d" -type f \( -name '*.ts' -o -name '*.js' -o -name '*.mjs' -o -name '*.py' -o -name '*.sh' \) ! -path '*/node_modules/*' | wc -l | tr -d ' ')
  if [[ $h -eq 0 && $b -eq 0 && $m -eq 0 && $scripts -eq 0 ]]; then bucket=verbatim
  elif [[ $h -eq 0 && $m -eq 0 && $scripts -eq 0 ]]; then bucket=check-boundary
  else bucket=adapt; fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$s" "$files" "$h" "$b" "$m" "$sl" "${fm:--}" "$bucket"
done

echo
echo "## Frontmatter keys across all skills (check each against every target harness)"
for f in "$PACK"/skills/*/SKILL.md; do awk 'NR==1&&/^---/{f=1;next} f&&/^---/{exit} f&&/^[a-zA-Z_-]+:/{sub(/:.*/,""); print}' "$f"; done | sort | uniq -c | sort -rn

echo
echo "## External names the pack cites (dependency triage: open each before substituting)"
grep -rEohI --exclude-dir=node_modules -- '(from|in|via) (the )?`?[a-z0-9-]+(-kit|-plugin|-pack|-tools)`?|`[a-z0-9-]+` (plugin|kit|pack)' "$PACK" | sort | uniq -c | sort -rn | head -20
ROOT=$(cd "$PACK/.." && pwd)
echo "Sibling packages next to the pack (same repo, same ref; likely referencable by URI):"
ls -1 "$ROOT" | sed 's/^/  /'

echo
echo "## Scripts (they often encode the prose they check; grep them for the same terms)"
find "$PACK" -type f \( -name '*.ts' -o -name '*.js' -o -name '*.mjs' -o -name '*.py' -o -name '*.sh' \) ! -path '*/node_modules/*' ! -name '*.test.*' | sed 's/^/  /'

echo
echo "## Agents and rules (hand-port; importers rarely read foreign agent formats)"
ls -1 "$PACK"/agents "$PACK"/rules 2>/dev/null | sed 's/^/  /' || true
