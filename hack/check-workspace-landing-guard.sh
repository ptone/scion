#!/usr/bin/env bash
# Guard: every place that downloads workspace content into a hub-managed
# project directory must go through landProjectWorkspace
# (pkg/hub/handlers_projects_core.go), not call the download step directly.
#
# landProjectWorkspace pairs the download with a mandatory rebuild step for
# whatever lands in that directory's .git before any host-side git command
# runs there. A call site that downloads directly instead of going through
# landProjectWorkspace skips that rebuild entirely for its directory.
#
# This has regressed before: additional call sites were found downloading
# into the same directory outside landProjectWorkspace after the helper was
# introduced. The property "every landing goes through the one helper" has
# no other enforcement, so a textual guard is the only thing standing
# between "fixed" and "fixed at three of four call sites."
#
# WHAT THIS CHECKS
# Within pkg/hub production Go files (excluding *_test.go), the call-syntax
# tokens `gcp.SyncFromGCS(` and `syncFromGCSFunc(` may only appear in the
# file that defines landProjectWorkspace. Any other occurrence in pkg/hub
# production code is a violation: it means a new (or reintroduced) call site
# is downloading directly instead of routing through the helper.
#
# syncFromGCSFunc is a package-level indirection landProjectWorkspace calls
# instead of calling gcp.SyncFromGCS directly, so tests can substitute a
# local stand-in for the download step. Matching both tokens means a
# violation is caught whether it calls the real function or the
# indirection.
#
# The `var syncFromGCSFunc = gcp.SyncFromGCS` declaration in the defining
# file is not itself a violation: it assigns the function value without
# calling it, so it does not match the call-syntax patterns below (no
# trailing parenthesis).
#
# WHAT THIS DOES NOT CHECK
# - Anything outside pkg/hub. pkg/runtimebroker has its own, separate
#   download call sites (broker-side workspace provisioning); those are
#   intentionally out of scope for this guard and out of scope for
#   landProjectWorkspace, which is a hub-only concern.
# - Test files. Tests legitimately reference the syncFromGCSFunc seam to
#   substitute a fake download step; that is what the seam exists for.
# - Any other download path that doesn't use these exact two call tokens
#   (e.g. a hand-rolled HTTP client hitting the storage backend directly).
#   This is a textual guard, not a proof that no other download path exists.
#
# EXIT CODES
#   0  no violations found
#   1  violations found
set -euo pipefail

cd "$(dirname "$0")/.."

# The one file allowed to call these tokens: it defines landProjectWorkspace
# and is the only place the download step may be invoked directly.
allowed_file="pkg/hub/handlers_projects_core.go"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

grep -rEn 'gcp\.SyncFromGCS\(|syncFromGCSFunc\(' \
  --include='*.go' \
  --exclude='*_test.go' \
  pkg/hub \
  | grep -v "^${allowed_file}:" \
  >"$tmp" || true

if [[ -s "$tmp" ]]; then
  echo "landing into a hub project directory must route through landProjectWorkspace;" >&2
  echo "direct reference found outside ${allowed_file}:" >&2
  cat "$tmp" >&2
  echo >&2
  echo "Route the download through landProjectWorkspace instead of calling" >&2
  echo "gcp.SyncFromGCS or syncFromGCSFunc directly." >&2
  exit 1
fi

echo "check-workspace-landing-guard: no violations"
