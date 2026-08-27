#!/usr/bin/env bash
# Guard: no conversation row may be minted outside the messaging layer
# (pkg/messaging/) and the store layer (pkg/store/).
#
# The property this guard enforces is: "no conversation is minted outside the
# messaging layer." It is NOT a function-name check — it is an enumeration of
# every code path that can INSERT a row into the conversations table.
#
# Conversation-minting surface (enumerated 2026-08-27):
#
#   Go method calls (must only appear in pkg/messaging/ and pkg/store/):
#     1. UpsertConversationByExternalRef — the primary resolve-or-create path
#     2. CreateConversation             — direct INSERT (no production callers
#                                         today, but the method is public)
#
#   Ent builder (must only appear in pkg/store/):
#     3. .Conversation.Create()         — raw ent builder; only used inside
#                                         pkg/store/entadapter/conversation_store.go
#
#   Raw SQL INSERT INTO conversations (must only appear in pkg/store/ or in the
#   webchat store's atomic dual-write paths):
#     4. INSERT [OR IGNORE|OR REPLACE] INTO ["]conversations["]
#                                       — case-insensitive match for the full
#                                         INSERT family. SQLite uses OR IGNORE
#                                         for idempotent inserts; Postgres uses
#                                         ON CONFLICT ... DO NOTHING (same line).
#                                         Used by CreateTopic, EnsureGeneralTopic,
#                                         backfillTopicConversations, PromoteDM
#                                         in pkg/hub/webchannel_store*.go. These
#                                         are the §2.6.4 dual-write mechanism and
#                                         are explicitly allowed.
#
# Test files (*_test.go) are excluded: test fixtures legitimately call store
# methods to set up state. The guard protects production code paths.
#
# Enumeration method: grep -rn for each pattern across all .go files, then
# subtract the allowed packages. If a new minting surface is added to the
# store interface, it must be added to this guard.
#
# LIMITATIONS
# This guard is textual and line-oriented. It does NOT detect:
#   - SQL split across lines (e.g., "INSERT INTO\n    conversations ...")
#   - A table name supplied through a format verb or variable
#     (e.g., fmt.Sprintf("INSERT INTO %s ...", tbl))
# Both are low-risk in practice: every existing INSERT site in this codebase
# puts "INSERT INTO conversations" on a single line (house style), and no
# site constructs the table name dynamically. But a green gate from this
# script guarantees only that the enumerated textual patterns are absent
# outside the allowed packages — it is not a proof that no mint path exists.
# The structural fix (hub has no raw SQL path to the conversations table)
# is a phase 5-7 concern.
#
# EXIT CODES
#   0  no violations found
#   1  violations found
set -euo pipefail

cd "$(dirname "$0")/.."

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

rc=0

# --- Check 1: Go method calls that mint conversations ---
# UpsertConversationByExternalRef and CreateConversation must only be called
# from pkg/messaging/ and pkg/store/.
grep -rn 'UpsertConversationByExternalRef\|\.CreateConversation(' \
  --include='*.go' \
  --exclude='*_test.go' \
  . \
  | grep -v '^./pkg/messaging/' \
  | grep -v '^./pkg/store/' \
  | grep -v '^./vendor/' \
  >"$tmp" || true

if [[ -s "$tmp" ]]; then
  echo "Conversation-minting method called outside pkg/messaging/ and pkg/store/:" >&2
  cat "$tmp" >&2
  echo >&2
  echo "Direct calls to UpsertConversationByExternalRef and CreateConversation" >&2
  echo "are not allowed outside pkg/messaging/ and pkg/store/. Use the messaging" >&2
  echo "package's resolution helpers (ResolveOrCreateConversationByKey, etc.)." >&2
  rc=1
fi

# --- Check 2: Ent builder conversation creation ---
# .Conversation.Create() and .Conversation.CreateBulk() must only appear
# inside pkg/store/ (where the ent adapter lives). pkg/ent/ is excluded
# because it contains auto-generated code from the ent framework.
: >"$tmp"
grep -rn '\.Conversation\.Create\b\|\.Conversation\.CreateBulk\b' \
  --include='*.go' \
  --exclude='*_test.go' \
  . \
  | grep -v '^./pkg/store/' \
  | grep -v '^./pkg/ent/' \
  | grep -v '^./vendor/' \
  >"$tmp" || true

if [[ -s "$tmp" ]]; then
  echo "Ent conversation builder used outside pkg/store/:" >&2
  cat "$tmp" >&2
  echo >&2
  echo ".Conversation.Create() must only be used inside pkg/store/entadapter/." >&2
  rc=1
fi

# --- Check 3: Raw SQL INSERT INTO conversations ---
# Matches the full INSERT family: INSERT INTO, INSERT OR IGNORE INTO,
# INSERT OR REPLACE INTO, with optional quoting on the table name, and
# case-insensitive to catch any casing variant.
# Allowed in pkg/store/ and in pkg/hub/webchannel_store*.go (the §2.6.4
# atomic dual-write paths: CreateTopic, EnsureGeneralTopic,
# backfillTopicConversations, PromoteDM).
: >"$tmp"
grep -rni 'INSERT[[:space:]]\+\(OR[[:space:]]\+[A-Z]\+[[:space:]]\+\)\?INTO[[:space:]]\+[\"]\?conversations[\"]\?' \
  --include='*.go' \
  --exclude='*_test.go' \
  . \
  | grep -v '^./pkg/store/' \
  | grep -v '^./pkg/hub/webchannel_store' \
  | grep -v '^./vendor/' \
  >"$tmp" || true

if [[ -s "$tmp" ]]; then
  echo "Raw SQL INSERT INTO conversations outside allowed packages:" >&2
  cat "$tmp" >&2
  echo >&2
  echo "Raw SQL conversation inserts are only allowed in pkg/store/ and" >&2
  echo "pkg/hub/webchannel_store*.go (§2.6.4 dual-write paths)." >&2
  rc=1
fi

if [[ "$rc" -ne 0 ]]; then
  exit 1
fi

echo "check-conversation-upsert-guard: no violations"
