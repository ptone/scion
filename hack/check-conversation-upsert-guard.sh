#!/usr/bin/env bash
# Guard: UpsertConversationByExternalRef must only be called from pkg/messaging/
# and pkg/store/ (including pkg/store/entadapter/). Any direct call from handler
# code or other packages is a layering violation — route through the messaging
# package's resolution helpers instead.
#
# EXIT CODES
#   0  no violations found
#   1  violations found
set -euo pipefail

cd "$(dirname "$0")/.."

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# Find all non-test .go files that call UpsertConversationByExternalRef,
# excluding the packages that own the method.
grep -rn 'UpsertConversationByExternalRef' \
  --include='*.go' \
  --exclude='*_test.go' \
  . \
  | grep -v '^./pkg/messaging/' \
  | grep -v '^./pkg/store/' \
  | grep -v '^./vendor/' \
  >"$tmp" || true

if [[ -s "$tmp" ]]; then
  echo "UpsertConversationByExternalRef called outside pkg/messaging/ and pkg/store/:" >&2
  cat "$tmp" >&2
  echo >&2
  echo "Direct calls to UpsertConversationByExternalRef are not allowed outside" >&2
  echo "pkg/messaging/ and pkg/store/. Use the messaging package's conversation" >&2
  echo "resolution helpers (ResolveOrCreateConversationByKey, etc.) instead." >&2
  exit 1
fi

echo "check-conversation-upsert-guard: no violations"
