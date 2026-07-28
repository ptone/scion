#!/usr/bin/env bash
# INTERPRETER ASSERTION — must remain the first executable statement, and must
# remain parseable by a POSIX shell.
#
# The shebang only applies when the script is executed directly. Invoked as
# `sh hack/check-authz-guards.sh` the shebang is ignored, and on a system where
# /bin/sh is dash this script died at `set -o pipefail` ("Illegal option") after
# printing nothing else. A caller reading that output saw no findings and a
# stack of shell noise, which is byte-indistinguishable from a clean tree to
# anyone skimming, and identical to one for any log scraper keyed on findings.
#
# That is this script's own subject matter turned on the script: a check that
# reports nothing because it never ran, read as a check that found nothing. It
# matters more here than in most tooling because a green CI check is exactly the
# artifact people stop re-deriving by hand.
#
# Re-exec under bash when it exists; fail loudly and non-zero when it does not.
# Never continue under an interpreter that cannot run the rest of this file.
if [ -z "${BASH_VERSION:-}" ]; then
  if command -v bash >/dev/null 2>&1; then
    exec bash "$0" "$@"
  fi
  echo "check-authz-guards: FATAL — requires bash; not found on PATH." >&2
  echo "check-authz-guards: NOTHING WAS ANALYSED (skipped, not clean)" >&2
  exit 2
fi

# Flags authorization checks that live inside an identity-kind guard with no
# compensating deny for the other identity kinds.
#
# This is the regression guard for ptone/scion#591. The bug was that ~24 handler
# sites wrote their authorization gate as:
#
#     if u := GetUserIdentityFromContext(ctx); u != nil {
#         decision := s.authzService.CheckAccess(ctx, u, res, action)
#         if !decision.Allowed { /* 403 */ }
#     }
#     // no else — agents and brokers skip the whole gate, silently
#
# agentIdentityWrapper implements AgentIdentity but not UserIdentity, and
# brokerIdentityImpl implements neither, so GetUserIdentityFromContext returns
# nil for both and the guard is skipped. Use s.authorize()/s.authorizeMsg()
# (pkg/hub/authorize.go), which is fail-closed for every identity kind.
#
# WHAT THIS CHECKS, AND WHY NOT SOMETHING SIMPLER
#
# It deliberately does NOT flag "uses GetUserIdentityFromContext". Roughly 25
# sites use that getter legitimately, for attribution (CreatedBy/UpdatedBy,
# message sender, list filtering) — see handlers_github_app.go:121. A check that
# fired on all of them would be suppressed within a week.
#
# The signal is the *shape of the guard*, not the name of the getter. A guard is
# reported only when all three hold:
#
#   1. It opens with an identity-kind test, in either syntactic form:
#          if <ident> := GetUserIdentityFromContext(ctx); <ident> != nil {
#          if <ident>, ok := <x>.(UserIdentity); ok {
#   2. Its body performs an authorization decision — authzService.CheckAccess,
#      requireAdmin, or a hand-rolled Role() == / != "admin" comparison.
#   3. Nothing catches the identity kinds that fail the test: the block has no
#      `else`, and it does not end in an unconditional return/continue/break/
#      panic (which would mean the code after the block is the fall-through
#      deny — the shape authorizeProjectImport and handlers_env_secrets.go use).
#
# Attribution guards fail (2). Guards with `} else { Forbidden(w); return }`
# fail (3). Only the bypass shape is reported.
#
# A third shape is reported unconditionally, because it has no benign reading —
# an explicit allow when the caller is not a user:
#
#     userIdent := GetUserIdentityFromContext(ctx)
#     if userIdent == nil {
#         return true    // "not a user, so allow"
#     }
#
# That was checkBrokerDispatchAccess and canDispatchToBroker before ptone/scion#591.
#
# WHAT IT CANNOT CHECK
#
# A handler with no authorization at all — handleUpdateGitHubApp before ptone/scion#591,
# createProjectAgent, getGroupMember — has no guard to key on and is invisible
# to any lexical rule. That gap is the route-authz manifest, issue ptone/scion#598.
#
# So a green run from this script is NOT a statement that the codebase is
# authorized correctly, and it must not be read as one. It says only that nobody
# has reintroduced the specific mis-shaped-guard idiom below; a route that never
# checks anything in the first place passes silently. Verifying that every route
# has an authorization check is a separate problem and is tracked in ptone/scion#598.
#
# Run `hack/check-authz-guards.sh --self-test` to exercise the classifier
# against a fixture covering every verdict above.
#
# EXIT CODES
#
#   0  analysed, no violations
#   1  analysed, violations found — the list is on stderr
#   2  COULD NOT ANALYSE (wrong interpreter, rg missing, no candidate files)
#      — nothing was examined
#
# 2 is separate from 1 on purpose. Both fail a build, which is the point: a run
# that examined no source must not be indistinguishable from a clean one to
# anything reading only the exit code, or this check becomes the thing it exists
# to prevent — a guard that never fires. But the two mean opposite things to
# whoever reads the log. 1 is a security finding against named code; 2 is a
# broken environment and accuses nobody. Note this differs from the exit-0-on-
# missing-rg convention in hack/check-project-compat-literals.sh; the difference
# is deliberate, because a formatting check that silently skips costs a reformat
# later, while an authorization check that silently skips ships a bypass.
set -euo pipefail

# --------------------------------------------------------------------------
# SCAN ROOT
#
# Normally the root is this script's own repository, resolved from $0, and it is
# NOT configurable. The self-test needs to point the reporting path at a fixture
# — that is the only reason an override exists at all, and it is deliberately an
# argument rather than an environment variable.
#
# An env var would be a laundering vector against the very failure this script
# exists to catch: export it to an empty directory in CI and you get "0 sites
# flagged", exit 0, green, with nothing in the build log that looks wrong.
# Printing the override would only help a reader who is reading, which in CI is
# nobody. An argument has to appear in the command line, so it shows up in the
# diff of whatever config invokes it. The variable is rejected outright rather
# than ignored, so a half-remembered recipe fails loudly instead of scanning the
# wrong tree quietly.
scan_root="$(dirname "$0")/.."
scan_root_overridden=""

if [[ -n "${CHECK_AUTHZ_ROOT:-}" ]]; then
  echo "check-authz-guards: CHECK_AUTHZ_ROOT is set. This script has no such" >&2
  echo "  option — the scan root is not configurable by environment. NOTHING WAS" >&2
  echo "  ANALYSED (refused, not clean)." >&2
  exit 2
fi

if [[ "${1:-}" == "--self-test-scan" ]]; then
  # Internal, used only by --self-test below. Not documented in the usage block
  # on purpose: no production run has any business scanning anything but its own
  # repository.
  scan_root="${2:?--self-test-scan requires a root directory}"
  scan_root_overridden="$scan_root"
fi

# Classifier. Walks each candidate guard with a brace-depth counter so the
# verdict depends on the block's real extent rather than on a fixed indentation
# level, and so composite literals in the body (Resource{...}) do not confuse it.
read -r -d '' classifier <<'AWK' || true
# Strip a trailing line comment before testing a verdict. `return true` and
# `return true // broker-to-broker` are the same verdict, and an anchored regex
# that only accepts the first can be defeated by reflowing a comment.
function strip_comment(l) {
  sub(/[[:space:]]*\/\/.*$/, "", l)
  return l
}
function is_opener(line) {
  return (line ~ /^[[:space:]]*(\} else )?if [A-Za-z_][A-Za-z0-9_]* := Get(User)?IdentityFromContext\(.*\); [A-Za-z_][A-Za-z0-9_]* != nil \{[[:space:]]*$/ ||
          line ~ /^[[:space:]]*(\} else )?if [A-Za-z_][A-Za-z0-9_]*, ok := [^;]*\.\(UserIdentity\); ok \{[[:space:]]*$/)
}
function open_block(line) {
  in_block = 1
  start = FNR
  opener = line
  sub(/^[[:space:]]+/, "", opener)
  depth = 1
  has_authz = 0
  last_body = ""
}
function count(line, re,   tmp) { tmp = line; return gsub(re, "&", tmp) }

FNR == 1 { in_block = 0; in_nil_block = 0; nil_var = ""; cur_func = "?" }

# Track the enclosing top-level function so findings can be named rather than
# merely located. Line numbers move whenever anything above them is edited; a
# function name does not, which makes both the allowlist entries and the
# reviewer checklist stable across unrelated changes. Only column-0 `func`
# matches, so closures inside a body do not clobber the name.
/^func / {
  fname = $0
  sub(/^func[[:space:]]+/, "", fname)
  sub(/^\([^)]*\)[[:space:]]*/, "", fname)
  sub(/[[:space:]]*\(.*$/, "", fname)
  cur_func = fname
}

{
  # Shape 3: an explicit "not a user, so allow" early return. Tracked separately
  # because it is a statement pair, not a guarded block.
  if (!in_block && !in_nil_block) {
    if ($0 ~ /^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*[[:space:]]*:?=[[:space:]]*Get(User)?IdentityFromContext\(/) {
      nil_var = $0
      sub(/^[[:space:]]*/, "", nil_var)
      sub(/[[:space:]]*:?=.*$/, "", nil_var)
      next
    }
    if (nil_var != "") {
      # Tolerate blank lines and comments between the assignment and the test.
      if ($0 ~ /^[[:space:]]*$/ || $0 ~ /^[[:space:]]*\/\//) next
      if ($0 ~ ("^[[:space:]]*if[[:space:]]+" nil_var "[[:space:]]*==[[:space:]]*nil[[:space:]]*\\{[[:space:]]*$")) {
        pending_nil = nil_var
        pending_nil_line = FNR
        nil_var = ""
        in_nil_block = 1
        nil_depth = 1
        nil_last = ""
        next
      }
      nil_var = ""
    }
  }

  if (in_nil_block) {
    nil_depth += count($0, "\\{") - count($0, "\\}")
    if (nil_depth <= 0) {
      if (strip_comment(nil_last) ~ /^[[:space:]]*return[[:space:]]+true[[:space:]]*$/) {
        occ[FILENAME SUBSEP cur_func]++
        printf "%s:%d: %s()#%d: if %s == nil { ... return true } (fail-open on non-user caller)\n",
          FILENAME, pending_nil_line, cur_func, occ[FILENAME SUBSEP cur_func], pending_nil
      }
      in_nil_block = 0
      next
    }
    if ($0 !~ /^[[:space:]]*$/ && $0 !~ /^[[:space:]]*\/\//) nil_last = $0
    next
  }

  if (in_block) {
    depth += count($0, "\\{") - count($0, "\\}")
    if (depth <= 0) {
      # This line closes the guard. Report only if an authorization decision was
      # made inside and nothing catches the other identity kinds.
      if (has_authz &&
          $0 !~ /\}[[:space:]]*else\b/ &&
          last_body !~ /^[[:space:]]*(return|continue|break|panic\()/) {
        occ[FILENAME SUBSEP cur_func]++
        printf "%s:%d: %s()#%d: %s\n", FILENAME, start, cur_func, occ[FILENAME SUBSEP cur_func], opener
      }
      in_block = 0
      # A `} else if <ident>, ok := ...` line closes one guard and opens the
      # next, so fall through to opener detection rather than skipping the line.
      if (is_opener($0)) open_block($0)
      next
    }
    if ($0 ~ /authzService\.CheckAccess/ ||
        $0 ~ /requireAdmin\(/ ||
        $0 ~ /\.Role\(\)[[:space:]]*[!=]=[[:space:]]*"admin"/) {
      has_authz = 1
    }
    if ($0 !~ /^[[:space:]]*$/ && $0 !~ /^[[:space:]]*\/\//) last_body = $0
    next
  }

  if (is_opener($0)) open_block($0)
}
AWK

# --------------------------------------------------------------------------
# Self-test: every verdict the classifier is responsible for, in one fixture.
# A `// WANT` marker sits on the line directly above a guard that must be
# flagged; `// WANT-NOT` above one that must not be. The attribution case is the
# canonical pair with the first case — same getter, opposite verdict — because
# keying on the getter name instead of the guard shape is the specific way this
# check would go wrong.
# --------------------------------------------------------------------------
if [[ "${1:-}" == "--self-test" ]]; then
  fixture_dir="$(mktemp -d)"
  fixture="$fixture_dir/fixture.go"
  cat >"$fixture" <<'FIXTURE'
package fixture

// Authorization gate inside an identity-kind guard, no else. The ptone/scion#591 shape.
func (s *Server) gate(w http.ResponseWriter, r *http.Request) {
	// WANT
	if user := GetUserIdentityFromContext(ctx); user != nil {
		decision := s.authzService.CheckAccess(ctx, user, agentResource(a), ActionRead)
		if !decision.Allowed {
			Forbidden(w)
			return
		}
	}
	doTheThing()
}

// Attribution only — handlers_github_app.go:121. Same getter as the gate above,
// but it makes no authorization decision. Flagging this is the failure mode
// this check exists to avoid.
func (s *Server) attribution(w http.ResponseWriter, r *http.Request) {
	userID := ""
	// WANT-NOT
	if user := GetUserIdentityFromContext(ctx); user != nil {
		userID = user.ID()
	}
	s.setGitHubAppSecret(ctx, key, value, desc, userID)
}

// Type-assertion form of the same gate (idiom 2), no else. The composite
// literal in the body must not confuse the brace counter.
func (s *Server) assertGate(w http.ResponseWriter, r *http.Request) {
	// WANT
	if userIdent, ok := identity.(UserIdentity); ok {
		decision := s.authzService.CheckAccess(ctx, userIdent, Resource{
			Type: "project",
			ID:   project.ID,
		}, ActionRead)
		if !decision.Allowed {
			Forbidden(w)
			return
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// Compensated by an else branch.
func (s *Server) withElse(w http.ResponseWriter, r *http.Request) {
	// WANT-NOT
	if userIdent, ok := identity.(UserIdentity); ok {
		decision := s.authzService.CheckAccess(ctx, userIdent, res, ActionRead)
		if !decision.Allowed {
			Forbidden(w)
			return
		}
	} else {
		Forbidden(w)
		return
	}
	doTheThing()
}

// Compensated by a fall-through deny after an unconditional return — the
// authorizeProjectImport / handlers_env_secrets.go shape.
func (s *Server) fallThrough(w http.ResponseWriter, r *http.Request) bool {
	// WANT-NOT
	if userIdent, ok := identity.(UserIdentity); ok {
		decision := s.authzService.CheckAccess(ctx, userIdent, res, ActionRead)
		if !decision.Allowed {
			Forbidden(w)
			return false
		}
		return true
	}
	Forbidden(w)
	return false
}

// Hand-rolled admin comparison rather than CheckAccess, no else.
func (s *Server) handRolled(w http.ResponseWriter, r *http.Request) {
	// WANT
	if userIdent := GetUserIdentityFromContext(ctx); userIdent != nil {
		if userIdent.Role() != "admin" && broker.CreatedBy != userIdent.ID() {
			Forbidden(w)
			return
		}
	}
	doTheThing()
}

// Shape 3 — explicit allow for non-user callers.
func (s *Server) failOpen(ctx context.Context) bool {
	userIdent := GetUserIdentityFromContext(ctx)
	// WANT
	if userIdent == nil {
		return true
	}
	return s.authzService.CheckAccess(ctx, userIdent, res, ActionRead).Allowed
}

// Same shape, but fail-closed.
func (s *Server) failClosed(ctx context.Context) bool {
	userIdent := GetUserIdentityFromContext(ctx)
	// WANT-NOT
	if userIdent == nil {
		return false
	}
	return s.authzService.CheckAccess(ctx, userIdent, res, ActionRead).Allowed
}

// Shape 3 with a comment between the assignment and the nil test. Still the
// same fail-open; the classifier must not be shaken off by the gap.
func (s *Server) failOpenCommented(ctx context.Context) bool {
	userIdent := GetUserIdentityFromContext(ctx)

	// Brokers and agents do not carry a user identity.
	// WANT
	if userIdent == nil {
		return true
	}
	return s.authzService.CheckAccess(ctx, userIdent, res, ActionRead).Allowed
}

// Nil branch mentions `return true` but does not end in an unconditional one:
// the block's own last statement denies. Not the fail-open shape.
func (s *Server) failClosedAfterBranch(ctx context.Context) bool {
	userIdent := GetUserIdentityFromContext(ctx)
	// WANT-NOT
	if userIdent == nil {
		if isInternalCaller(ctx) {
			return true
		}
		return false
	}
	return s.authzService.CheckAccess(ctx, userIdent, res, ActionRead).Allowed
}

// Shape 1 via the broad getter. The opener accepts both getters, and without
// this fixture that half of the widening was untested — my own mutation run
// caught it: reverting the opener to the narrow getter alone left the suite
// green.
func (s *Server) gateBroadGetter(w http.ResponseWriter, r *http.Request) {
	// WANT
	if identity := GetIdentityFromContext(ctx); identity != nil {
		decision := s.authzService.CheckAccess(ctx, identity, agentResource(a), ActionRead)
		if !decision.Allowed {
			Forbidden(w)
			return
		}
	}
	doTheThing()
}

// Shape 3 with the verdict comment moved onto the return line. Same verdict,
// and the anchored `return true$` test used to be defeated by the trailing
// comment. handlers_runtime_brokers.go:435 is this shape with the comment on
// its own line — reflowing that one line would have silently un-detected the
// site this whole check was built for. Found by aid-arch.
func (s *Server) failOpenTrailingComment(ctx context.Context) bool {
	userIdent := GetUserIdentityFromContext(ctx)
	// WANT
	if userIdent == nil {
		return true // broker-to-broker
	}
	return s.authzService.CheckAccess(ctx, userIdent, res, ActionRead).Allowed
}

// Shape 3 via GetIdentityFromContext — the getter our OWN FIX uses. The narrow
// getter returns a literal nil interface that panics CheckAccess, so the
// dispatch fixes deliberately switched to this one. A detector that only knows
// GetUserIdentityFromContext is drift protection pointed at the past: it covers
// the shape the bug had, not the shape the fix has, and would not catch a
// regression in either of the two functions it exists to protect. Found by
// aid-arch, who noticed the getter appears zero times in this script.
func (s *Server) failOpenBroadGetter(ctx context.Context) bool {
	identity := GetIdentityFromContext(ctx)
	// WANT
	if identity == nil {
		return true
	}
	return s.authzService.CheckAccess(ctx, identity, res, ActionRead).Allowed
}

// The same broad getter used correctly — this is canDispatchToBroker's actual
// shape after 89bc203. Must stay unflagged or the fix reports itself as a bug.
func (s *Server) failClosedBroadGetter(ctx context.Context) bool {
	identity := GetIdentityFromContext(ctx)
	// WANT-NOT
	if identity == nil {
		return false
	}
	return s.authzService.CheckAccess(ctx, identity, res, ActionRead).Allowed
}

// Shape 4: the hub-scoped "require" helper. Lexically this is the ptone/scion#591 idiom —
// same getter, same `== nil` test — but the verdict is inverted: the nil branch
// writes 401 and returns false. Fail-CLOSED, and correct.
//
// Copied from pkg/hub/hub_pre_start_hook_handlers.go on origin/main (db8f6fc,
// GoogleCloudPlatform/scion#888), which is not on scion/agent-id-fix yet. aid-arch flagged that these
// would come back as findings once main merges. Verified on a trial merge that
// they do not: the classifier requires the nil branch to END in an
// unconditional `return true`, so the verdict is already part of the match.
// This fixture exists to keep it that way — the property currently holds by
// construction and nothing pinned it, and a check that cries wolf on the one
// hub-scoped handler set that got authorization right teaches the next author
// to ignore its output.
func (s *Server) requireHubAdmin(w http.ResponseWriter, r *http.Request) (UserIdentity, bool) {
	identity := GetUserIdentityFromContext(r.Context())
	// WANT-NOT
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
		return nil, false
	}
	if identity.Role() != store.UserRoleAdmin {
		Forbidden(w)
		return nil, false
	}
	if _, scoped := identity.(*ScopedUserIdentity); scoped {
		Forbidden(w)
		return nil, false
	}
	return identity, true
}

// Shape 4, minimal form: bare helper call in the nil branch rather than an
// explicit writeError. Same verdict, less to pattern-match on.
func (s *Server) requireHubHookReader(w http.ResponseWriter, r *http.Request) (UserIdentity, bool) {
	identity := GetUserIdentityFromContext(r.Context())
	// WANT-NOT
	if identity == nil {
		Unauthorized(w)
		return nil, false
	}
	return identity, true
}
FIXTURE

  want="$(grep -n '^[[:space:]]*// WANT$' "$fixture" | cut -d: -f1 | awk '{print $1 + 1}')"
  got="$(awk "$classifier" "$fixture" | cut -d: -f2)"

  if [[ "$want" == "$got" ]]; then
    echo "check-authz-guards self-test: PASS (classifier — $(grep -c '// WANT$' "$fixture") flagged, $(grep -c '// WANT-NOT$' "$fixture") correctly ignored)"

    # ----------------------------------------------------------------------
    # Second case: THE ZERO-FINDINGS REPORTING PATH.
    #
    # The classifier case above exits before any reporting code runs, so until
    # this existed nothing pinned what the script SAYS when it finds nothing.
    # That path had also never executed in this project's history — every run
    # ever made reported six sites — and it was not observable from outside
    # either, because the scan cds to its own repository root: pointing the
    # script at a synthetic clean tree silently rescanned this one and
    # reported the same six. The behaviour range had exactly one point in it,
    # which is why the gap did not feel like a gap.
    #
    # What it must not do is go quiet. A guard that found nothing and a guard
    # that never ran have to be distinguishable in the output, and the exit
    # code cannot distinguish them — 0 is also what a scan that examined no
    # source would return if it forgot to say so.
    #
    # ASSERT THE LITERAL SUBSTRING. The zero-findings line has a DIFFERENT
    # SHAPE from the findings line: it carries no ", N allowlisted, N reported
    # above" clause. An assertion written against the findings template would
    # be green against text that never appears in any run.
    zero_root="$(mktemp -d)"
    mkdir -p "$zero_root/pkg/hub"
    cat >"$zero_root/pkg/hub/clean.go" <<'CLEANFIXTURE'
package hub

// A candidate with no violation. It matches the file-level prefilter, so the
// scan has something to analyse and does not take the "no candidate files"
// exit-2 path — the clean result has to come from the classifier clearing it,
// not from the scan finding nothing to look at. The guard is fail-CLOSED.
func (s *Server) cleanGate(w http.ResponseWriter, r *http.Request) {
	identity := GetUserIdentityFromContext(r.Context())
	if identity == nil {
		Unauthorized(w)
		return
	}
	doTheThing()
}
CLEANFIXTURE

    zero_rc=0
    zero_out="$(bash "$0" --self-test-scan "$zero_root" 2>&1)" || zero_rc=$?

    if [[ "$zero_rc" -eq 0 && "$zero_out" == *"0 sites flagged"* ]]; then
      echo "check-authz-guards self-test: PASS (zero-findings path states its own total)"
      rm -rf "$fixture_dir" "$zero_root"
      exit 0
    fi
    echo "check-authz-guards self-test: FAIL — zero-findings reporting path" >&2
    echo "expected: exit 0, and output containing the literal '0 sites flagged'" >&2
    echo "actual exit: $zero_rc" >&2
    echo "actual output:" >&2
    echo "$zero_out" >&2
    rm -rf "$fixture_dir" "$zero_root"
    exit 1
  fi
  echo "check-authz-guards self-test: FAIL — classifier" >&2
  echo "expected flagged lines:" >&2
  echo "$want" >&2
  echo "actual flagged lines:" >&2
  echo "$got" >&2
  rm -rf "$fixture_dir"
  exit 1
fi

cd "$scan_root"

# Report which tree was actually analysed, on every path.
#
# A stale checkout runs the tip's script against old source and produces output
# that is well-formed, plausible, and wrong — over-reporting looks exactly like a
# developer having missed work, and under-reporting looks like a clean build.
# Neither is detectable from the output itself, so the output has to carry its
# own provenance. Deciding whether this sha is the branch tip stays with the
# person acting on it: comparing against a remote would need either network I/O
# or a possibly-stale ref, which relocates the same lie somewhere harder to see.
provenance() {
  local sha
  # An overridden root taints every line this function appears on, not just one
  # summary line, so it is stamped here rather than at the call sites. A run
  # against a fixture must not be quotable as a run against the repository.
  if [[ -n "$scan_root_overridden" ]]; then
    printf 'FIXTURE ROOT %s (NOT THIS REPOSITORY)' "$scan_root_overridden"
    return
  fi
  if ! sha="$(git rev-parse --short HEAD 2>/dev/null)"; then
    # Covers both "not a worktree" and "git is not on PATH" — the caller cannot
    # tell those apart from here, so do not claim to.
    printf '(git unavailable or not a worktree)'
    return
  fi
  if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then
    printf '%s-dirty' "$sha"
  else
    printf '%s' "$sha"
  fi
}

if ! command -v rg >/dev/null 2>&1; then
  echo "check-authz-guards: ripgrep (rg) not found — NOTHING WAS ANALYSED (skipped, not clean)" >&2
  exit 2
fi

# Narrow to files that mention the getter or the assertion at all. The
# classifier does the real work; this just keeps it off the other ~1500 Go files.
mapfile -t candidate_files < <(
  rg -l --glob '*.go' --glob '!**/*_test.go' \
    -e 'Get(User)?IdentityFromContext\(' \
    -e '\.\(UserIdentity\); ok \{' \
    cmd pkg extras 2>/dev/null || true
)

if [[ ${#candidate_files[@]} -eq 0 ]]; then
  # In this repo the getter always matches something: ~25 attribution sites use
  # it legitimately and are deliberately not being converted. Zero candidates
  # therefore means the scan ran somewhere unexpected — wrong cwd, empty or
  # partial checkout — not that the codebase is clean. If every last caller is
  # ever removed for real, fix this script deliberately rather than silencing it.
  echo "check-authz-guards: analysed $(provenance) — no candidate files matched, NOTHING WAS ANALYSED (skipped, not clean)" >&2
  exit 2
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

awk "$classifier" "${candidate_files[@]}" >"$tmp" || true

if [[ ! -s "$tmp" ]]; then
  echo "check-authz-guards: analysed $(provenance), no violations"
  echo "check-authz-guards: END — analysed $(provenance), ${#candidate_files[@]} files examined, 0 sites flagged"
  exit 0
fi

# Anchored allowlist. Every entry must carry a comment stating why the guard is
# safe despite its shape. An empty allowlist is the acceptance criterion for
# ptone/scion#591 Part 1 — do not add entries to make a red build green. A new hit is
# either a real bypass (fix the call site with s.authorize) or a classifier
# false positive (fix this script, and extend the self-test fixture above).
#
# If an entry ever does become necessary, anchor it on file + enclosing function
# + guard shape, NOT on the line number. Line numbers move whenever anything
# above them is edited, and a stale line-anchored entry fails in the dangerous
# direction: it can drift onto an unrelated site and mask a real bypass that was
# never reviewed. Findings are printed as `file:line: function()#N: guard` for
# exactly this reason.
#
# The `#N` is the occurrence index of the guard within its enclosing function,
# and it exists because file + function is NOT unique: addGroupMember holds two
# guards of the same shape whose findings are otherwise byte-identical. Without
# the index, a single entry there would silently suppress both — pinning the one
# site somebody reasoned about and a second site nobody ever reviewed. Anchor on
# `function()#N`. The index is deliberately not stable across inserting or
# deleting a guard inside the same function, which is exactly when a stale entry
# should stop matching.
#
# An entry is admissible only if ALL THREE hold:
#   1. the site is fail-open BY DESIGN — intended behaviour, and the entry is
#      recording a decision that was made;
#   2. the entry carries a comment saying why, AND a test pinning the fail-open
#      as intended;
#   3. if the entry's file + function matches more than one flagged guard, it
#      must either name the occurrence explicitly via `#N`, or carry a test
#      pinning EVERY guard in that function. Prefer naming: a test that pins two
#      guards in order to make one exception legible is a test nobody maintains.
#
# Condition 2 is the load-bearing one. Without a test, "allowlisted" and
# "unfixed" are indistinguishable in the tree — both present as a site this
# script does not flag, and no reader can tell a considered exception from
# abandoned work. That is ptone/scion#591's own failure mode with paperwork attached, and
# this script exists because of exactly that shape.
#
# Running out of time is not condition 1. If sites remain unfixed, the honest
# outcome is that this script ships unwired from `make ci` with the reason
# stated — not an allowlist that reports green. "Deliberately open" and "not
# finished" look identical in this file and are entirely different facts.
allowed_paths=(
  # (intentionally empty)
  "^$"
)

allowlist="$(printf '%s\n' "${allowed_paths[@]}" | sed 's/\$$/:/' | paste -sd '|' -)"

violations="$(grep -Ev "$allowlist" "$tmp" || true)"

# Counted, not inferred. `printf '%s\n' "$violations" | wc -l` reports 1 for the
# empty string, which would make a clean run claim one finding.
flagged="$(grep -c '' "$tmp" || true)"
if [[ -z "$violations" ]]; then
  reported=0
else
  reported="$(printf '%s\n' "$violations" | grep -c '')"
fi

if [[ -n "$violations" ]]; then
  echo "check-authz-guards: analysed $(provenance) — confirm this is the branch tip before acting on the list below" >&2
  echo >&2
  echo "Authorization check inside an identity-kind guard with no fail-closed branch:" >&2
  echo "$violations" >&2
  echo >&2
  echo "These guards are skipped entirely for agent and broker callers, which is" >&2
  echo "the bypass class of ptone/scion#591. Replace the guard with the fail-closed" >&2
  echo "helper:" >&2
  echo >&2
  echo "    if !s.authorize(w, r, agentResource(agent), ActionDelete) { return }" >&2
  echo >&2
  echo "See pkg/hub/authorize.go. If a guard is genuinely safe in this shape, add" >&2
  echo "it to allowed_paths in this script with a comment explaining why." >&2
  echo >&2
  # Deliberately the LAST line of output, and it states its own totals. A
  # truncated read of this report (tail -N, a scrollback limit, a paste) is then
  # self-evidently truncated: if you cannot see this line you are not looking at
  # the whole list, and if the total here exceeds what you counted, you are
  # missing sites. A worklist was once published sixteen-of-twenty because this
  # output was transcribed through `tail -25` — the sites were reported
  # correctly and read short. Do not move this line; add no output after it.
  echo "check-authz-guards: END — analysed $(provenance), ${#candidate_files[@]} files examined, ${flagged} sites flagged, $(( flagged - reported )) allowlisted, ${reported} reported above" >&2
  exit 1
fi

echo "check-authz-guards: analysed $(provenance), no violations"
echo "check-authz-guards: END — analysed $(provenance), ${#candidate_files[@]} files examined, ${flagged} sites flagged, all allowlisted"
