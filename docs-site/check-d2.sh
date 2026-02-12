#!/usr/bin/env bash
# Validate all ```d2 code blocks in content markdown files.
# Requires the d2 CLI: https://d2lang.com
#
# Usage: ./check-d2.sh [dir]
#   dir  — content directory to scan (default: src/content)

set -euo pipefail

CONTENT_DIR="${1:-src/content}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

if ! command -v d2 &>/dev/null; then
  echo "error: d2 is not installed (https://d2lang.com/install)" >&2
  exit 1
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

failed=0
checked=0

for mdfile in $(grep -rl '```d2' "$CONTENT_DIR" --include='*.md' --include='*.mdx'); do
  block=0
  in_d2=false

  while IFS= read -r line; do
    if [[ "$in_d2" == false && "$line" =~ ^\`\`\`d2 ]]; then
      in_d2=true
      block=$((block + 1))
      outfile="$tmpdir/block_${block}.d2"
      : > "$outfile"
      continue
    fi

    if [[ "$in_d2" == true && "$line" =~ ^\`\`\` ]]; then
      in_d2=false
      checked=$((checked + 1))
      if ! d2 "$outfile" /dev/null 2>"$tmpdir/err.txt"; then
        echo "FAIL: $mdfile (block $block)"
        sed 's/^/  /' "$tmpdir/err.txt"
        failed=$((failed + 1))
      fi
      continue
    fi

    if [[ "$in_d2" == true ]]; then
      echo "$line" >> "$outfile"
    fi
  done < "$mdfile"
done

echo ""
echo "Checked $checked d2 block(s), $failed failure(s)."
exit $((failed > 0 ? 1 : 0))
