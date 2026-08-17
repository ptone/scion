#!/usr/bin/env bash
# Asserts that the root Dockerfile's DEFAULT build target is still the plain
# runtime image, and not the non-root hub-gke image.
#
# WHY THIS EXISTS
#
# `docker build` with no `--target` builds the LAST stage in the file. The root
# Dockerfile's external consumers (`gcloud run deploy --source`, `gcloud builds
# submit`) pass no `--target`, so the last stage is the image they get. The
# hub-gke stage added for GKE runs as uid 1000; if it ever becomes the last
# stage, every one of those consumers silently starts receiving a non-root
# image, and nothing in the build fails. The symptom appears later, in
# production, as permission errors.
#
# The file guards against that with a trailing empty `FROM <runtime>` stage
# whose only job is to be last, plus a comment saying so. A comment is a
# request, not a guard -- this script is the guard.
#
# HOW IT CHECKS: TWO READINGS, DELIBERATELY UNEQUAL
#
# Two reviews of earlier versions got NINE wrong Dockerfiles past this script,
# and every one of them worked by confusing the PARSER rather than by violating
# a rule. Two things follow, and the script is built out of both:
#
#   The line model is not ours any more. Reading 2 is BuildKit's own parser
#   (hack/dockerfile-stages), so "our parser disagrees with Docker" is not a
#   category that exists here. See the decision record above parse().
#
#   That is not sufficient, so the most important property still does not
#   depend on any parser:
#
#   Reading 1 (no parser): the file's TAIL is matched as raw text. Find the last
#   line that is a whole-line `FROM ...` and require that everything after it is
#   blank or a comment, that nothing in that region ends in a continuation, and
#   that this line is the one the parser also thinks starts the last stage. This
#   is an ALLOWLIST: anything the parser might misread is rejected on sight
#   rather than interpreted.
#
#   Reading 2 (parser): the stage table, used for the properties that genuinely
#   are about specific instructions (which uid, which env, which stage derives
#   from which), where a denylist is the right tool.
#
# The two readings must AGREE about where the last stage begins. Disagreement is
# itself a failure: it means one of them is misreading the file, and the script
# refuses to say which.
#
# WHAT IT CHECKS
#
# The property is NOT "the last FROM line says runtime". That would pass a file
# where someone appended `USER 1000` after that line. Nor is it only "the last
# stage derives from whatever hub-gke derives from" -- that is satisfied by
# repointing BOTH at the builder stage, which passes while making the toolchain
# image the default target. So the checks are:
#
#   1. a stage named `hub-gke` exists                     (it is what we protect)
#   2. `hub-gke` is not the last stage                    (the original trap)
#   3. the last stage contains no instructions at all     (empty, so it adds nothing)
#   4. the last stage's base is the same in-file stage hub-gke's base is
#      (name-agnostic: renaming the runtime stage is fine, repointing either
#      stage at something else is not)
#   5. EXACTLY ONE stage in the file both takes an artifact from another
#      in-file stage and declares an ENTRYPOINT, and that stage is the shared
#      base. Uniqueness, not resemblance -- see the rule for why matching a
#      contract does not work here
#   6. no `USER` in the runtime stage, in a stage it derives from, or in a
#      stage derived from it, except `hub-gke` (a `USER` in an unrelated build
#      stage is normal and is not this script's business); and NO `ONBUILD` AT
#      ALL in that chain, whatever the verb, since its trigger fires in the
#      empty trailing stage and so adds an instruction to the default build
#      target's image that appears in no stage the script can read
#   7. `hub-gke` sets USER to a numeric uid 1000          (positive twin of 6;
#      `runAsNonRoot: true` cannot resolve a username)
#   8. no `ENV KUBECONFIG` anywhere                       (negative: an explicit
#      kubeconfig silently disables in-cluster ServiceAccount auth)
#   9. `hub-gke` sets `ENV HOME`                          (positive twin of 8)
#  10. no CMD on `hub-gke` OR ON ANY STAGE IT INHERITS FROM (no baked
#      args/secrets: a CMD in the runtime stage is inherited by hub-gke)
#  11. no ENTRYPOINT in that same chain carries arguments, which is the other
#      half of 10: a shell-form ENTRYPOINT bakes exactly what a CMD would
#
# plus seven structural preconditions, fatal rather than counted because every
# check above assumes them:
#
#  12. stage names are unique. A second `AS runtime` later in the file silently
#      repoints the trailing stage at a different image.
#  13. no `escape` parser directive. Reading 2 honours it; reading 1 is raw text
#      and cannot, and a file that needs it gets one reading instead of two.
#  14. no heredocs. NOT because the parser cannot read them -- it can. Because
#      Docker 20.10.24's built-in frontend and docker/dockerfile:1.9 were
#      measured DISAGREEING WITH EACH OTHER about the same heredoc file.
#  15. the stage table was actually produced and is non-empty. Reading 2 is now
#      an external program; with no table, every rule above passes vacuously.
#  16. no empty continuation line. BuildKit skips the blank line and holds the
#      continuation open, which is how a blank line deletes the trailing stage.
#  17. every base in the runtime chain is written literally. `FROM ${B} AS
#      runtime` truncates the chain walk that 6, 10 and 11 are about, hiding a
#      real ancestor -- and `--build-arg` means the value is not in the file.
#      Checked where the chain is first computed, after 4 establishes it.
#  18. awk is on PATH. Reading 1 is an awk program and every table query is an
#      awk one-liner; without it the rules run against empty strings that
#      compare equal to each other, and the script reports that its two
#      readings agree about a file neither of them read.
#
# and one more raw-text check that is not a rule about the Dockerfile so much as
# about this script: the last real line ABOVE the final FROM must not open a
# continuation, because that line is the only one that can absorb the FROM.
#
# WHAT IT CANNOT CHECK, AND MUST NOT BE READ AS SAYING
#
# It parses one text file. It does not build anything, so:
#
#   - It says nothing about whether the runtime stage itself is *unchanged*.
#     Adding a package to it, or changing its base image, is a normal change and
#     this script stays green. The property is "the default target is the
#     runtime stage", not "the runtime stage never changes".
#   - It cannot see the produced image. `User`, `Env` and layer identity of the
#     built artifact were verified once, by build, when the hub-gke stage landed;
#     nothing re-verifies them per commit.
#   - It knows nothing about the other three hub-producing Dockerfiles in this
#     repo (Dockerfile.hub, scripts/cloudrun/Dockerfile, image-build/hub/
#     Dockerfile). A green run says nothing about them.
#   - It assumes the current Docker semantics that an untargeted build resolves
#     to the last stage. If that ever changes, this script is measuring the
#     wrong thing and will not notice.
#   - It refuses `ONBUILD` only in the runtime chain (rule 6). An ONBUILD in an
#     unrelated stage affects images built FROM that stage, which is not this
#     script's business.
#   - Reading 2 is BuildKit v0.31.2's parser, which is not necessarily the
#     frontend that builds the image. Constructs measured to be read DIFFERENTLY
#     BY DIFFERENT FRONTENDS are refused (13, 14) rather than interpreted, and
#     reading 1 does not parse at all -- but a frontend that disagrees with
#     v0.31.2 about something nobody has measured is not covered by either.
#
# A GATE THE SELF-TEST CANNOT REACH IS NOT TESTED. An earlier version put two
# gates in `grep -E`, where `[ \t]` is the set {space, backslash, t} and not a
# tab, so both were walkable by pressing Tab -- and 19 green self-test cases
# never noticed, because none of them reached those two lines. Add gates where
# the fixtures already go, and give each one a fixture that fails without it.
#
# Run `hack/check-dockerfile-stages.sh --self-test` to see the script fail on
# each mutation it is meant to catch, and pass the legitimate variations that
# look like them.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Absolute, because the self-test runs the script from another directory and a
# relative $0 would not resolve there.
SELF="$REPO_ROOT/hack/$(basename "${BASH_SOURCE[0]}")"
GKE_STAGE="hub-gke"

FAILURES=0
fail() {
  echo "::error file=Dockerfile::$*" >&2
  echo "FAIL: $*" >&2
  FAILURES=$((FAILURES + 1))
}
ok() { echo "ok: $*"; }
die() { echo; echo "$FAILURES check(s) failed."; exit 1; }

# ---------------------------------------------------------------------------
# Reading 2: the stage table -- produced by BUILDKIT'S OWN PARSER.
#
# THE DECISION, AND WHY IT WAS MADE (2026-08-17)
#
# This used to be ~60 lines of awk that modelled BuildKit's line model:
# continuations, comments, the escape directive, heredocs. Over two review
# rounds, two different authors found SEVEN bugs in it -- a comment ending in a
# backslash swallowing the next line, a tab-separated parser directive, four
# flavours of heredoc, an empty continuation line, a continuation join that
# inserted a space Docker does not insert, and a doubled trailing backslash.
#
# Every one of them shipped a GREEN guard. The rules were right and were being
# handed a file that was not the one Docker builds; a rule cannot defend against
# being given the wrong input. The seventh was found by attacking a part nobody
# had attacked yet, which is the signature of a class that does not converge.
#
# So the line model is no longer ours. `hack/dockerfile-stages` is a ~120-line
# Go program, in its own module, that calls
# `github.com/moby/buildkit/frontend/dockerfile/parser` -- the code Docker
# itself uses -- and prints the AST as the flat table below. It contains no
# rules. The eleven rules stayed exactly where they were, in this file, with
# their messages and their self-test cases unchanged.
#
# Pinned at buildkit v0.31.2 (see hack/dockerfile-stages/go.mod, which is the
# citation: "modelled on parser.go" with no revision is a claim nobody can
# re-check). It is a SEPARATE module on purpose: adding buildkit to the root
# go.mod bumps eleven unrelated dependencies -- otel, protobuf, grpc-gateway,
# klog -- and the go directive, and the result does not build without further
# remediation. The repo already carries ten nested modules under extras/.
#
# v0.31.2 rather than the current v0.32.2, and the reason is CI, not the parser.
# v0.32's go directive is 1.26.3; the root go.mod says 1.26.1, which is what
# `actions/setup-go` installs -- AND that action sets `GOTOOLCHAIN=local`, which
# was read off the CI job's own env block rather than inferred. So v0.32.2 does
# not "download a toolchain and carry on" on the runner; it fails outright, and
# this guard would have been red on arrival. v0.31.2 needs 1.25.9 and builds
# under `GOTOOLCHAIN=local` at 1.26.1 -- checked by building it that way.
# The two versions were also run against each other over the whole corpus and
# every Dockerfile in the tree: identical tables everywhere, identical verdicts.
# If you bump this, check the go directive against the root go.mod first.
#
# WHAT THIS DOES NOT FIX, WHICH IS WHY READING 1 STAYS
#
# BuildKit's parser tells you what buildkit v0.31.2 thinks the file means. It
# does not tell you what the frontend that actually builds the image thinks:
# Docker 20.10.24's built-in frontend and `docker/dockerfile:1.9` were MEASURED
# disagreeing with each other about the same file (see rule 14). The dependency
# can also be wrong, unpinned, or drift. Reading 1 does not parse, so it
# survives all of that, and it is the reason rules 13 and 14 are still refusals
# rather than interpretations.
#
# Emits, in file order:
#   DIRECTIVE escape <token>      when the escape token is not a backslash
#   WARN <text>                   a BuildKit parser warning
#   HEREDOC <line> <<word>        a heredoc redirection, as BuildKit detects one
#   STAGE <n> <base> <name-or--> <line>
#   INSTR <n> <VERB> <rest>
#
# Stage names and base refs are lowercased: Docker matches them
# case-insensitively. Instructions before the first FROM are reported as stage 0,
# which no rule reads.
#
# DOCKERFILE_STAGES_CMD overrides the emitter. It exists for the self-test cases
# that assert this script FAILS CLOSED when the emitter produces nothing, which
# is not otherwise constructible. Nothing else should set it.
# ---------------------------------------------------------------------------
# GO_BIN is a variable rather than a literal `go` for one reason: the
# "Go is not installed" branch of rule 15 is otherwise unreachable from the
# self-test, and an unreachable branch is an untested branch -- which is the
# defect that let two walkable gates sit under nineteen green cases. Pointing
# it at a name that does not exist exercises the real failure end to end,
# rather than testing that the message string is spelled correctly.
GO_BIN="${DOCKERFILE_STAGES_GO:-go}"

# AWK_BIN exists for the same reason, and for a second one. Reading 1 IS an awk
# program and every query against reading 2's table is an awk one-liner, so awk
# missing is not a degraded run -- it is seven `ok:` lines for checks that never
# executed, including all four of reading 1's and "both readings agree the last
# stage begins at line " with an empty value on both sides, because "" = "".
# The two-readings design silently becomes zero readings and reports agreement.
# The seam is what lets the self-test walk that, rather than describe it.
AWK_BIN="${DOCKERFILE_STAGES_AWK:-awk}"

parse() {
  local abs
  abs="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
  if [ -n "${DOCKERFILE_STAGES_CMD:-}" ]; then
    $DOCKERFILE_STAGES_CMD "$abs"
    return $?
  fi
  "$GO_BIN" -C "$REPO_ROOT/hack/dockerfile-stages" run . "$abs"
}

# ---------------------------------------------------------------------------
# Reading 1: the tail of the file, as raw text, with no parsing.
#
# Emits:
#   LASTFROM <line>            the last whole-line FROM in the file
#   TAILJUNK <line> <text>     a line after it that is neither blank nor comment
#   TAILCONT <line> <text>     a line from it onwards that opens a continuation
#   PRECONT <line> <text>      the first real line ABOVE it opens a continuation
#
# PRECONT is not decoration. The window used to start at LASTFROM, and an empty
# continuation line two lines ABOVE the final FROM defeated both readings at
# once: BuildKit skips the blank, keeps the continuation open, and swallows the
# final FROM into the instruction above it, so there is no trailing stage and
# hub-gke is what an untargeted build ships. Reading 1 was present and did not
# save reading 2 because it was not looking up. Walking up past blanks and
# comments to the first real line is the whole fix: that line is the only one
# that can absorb the final FROM, under any continuation model.
# ---------------------------------------------------------------------------
raw_tail() {
  "$AWK_BIN" '
    { line = $0; sub(/\r$/, "", line); lines[NR] = line }
    line ~ /^[ \t]*[Ff][Rr][Oo][Mm]([ \t]+--[^ \t]+)*[ \t]+[^ \t]+([ \t]+[Aa][Ss][ \t]+[^ \t]+)?[ \t]*$/ { lastfrom = NR }
    END {
      printf "LASTFROM %d\n", lastfrom + 0
      for (i = lastfrom + 1; i <= NR; i++) {
        if (lines[i] ~ /^[ \t]*$/) continue
        if (lines[i] ~ /^[ \t]*#/) continue
        printf "TAILJUNK %d %s\n", i, lines[i]
      }
      for (i = (lastfrom > 0 ? lastfrom : 1); i <= NR; i++) {
        if (lines[i] ~ /\\[ \t]*$/) printf "TAILCONT %d %s\n", i, lines[i]
      }
      # THE ORDER OF THESE THREE TESTS IS DELIBERATE. Continuation is tested
      # BEFORE comment, so a comment ending in a backslash directly above the
      # final FROM is reported as PRECONT and the file is refused -- even though
      # BuildKit strips comments before joining continuations and therefore
      # builds that file correctly. Reading 1 exists because our comment-versus-
      # continuation model was wrong four times; having it re-derive that model
      # in order to be lenient would remove the only reason to have it. It
      # refuses what it cannot resolve. Pinned by corpus fixture
      # 103-comment-backslash-directly-above-final-from, so the trade is
      # something a future reader has to argue with rather than rediscover.
      for (i = lastfrom - 1; i >= 1; i--) {
        if (lines[i] ~ /^[ \t]*$/) continue
        if (lines[i] ~ /\\[ \t]*$/) { printf "PRECONT %d %s\n", i, lines[i]; break }
        if (lines[i] ~ /^[ \t]*#/) continue
        break
      }
    }
  ' "$1"
}

# ---------------------------------------------------------------------------
# Self-test. A check nobody has watched fail is not a check: this runs the
# script against a synthetic known-good shape (must pass), against one mutation
# per rule (each must fail, and fail FOR ITS OWN REASON), and against the legal
# variations that resemble those mutations (each must pass, or the guard cries
# wolf and gets deleted).
#
# CI runs this AND `make dockerfile-stages`, which is what covers the real
# Dockerfile; this function never reads it.
#
# Cases are only ever added here. A case that a later, broader check catches
# first is re-pointed at the new failure message, not deleted: if the broader
# check is ever weakened, these are the only things that notice.
#
# WHETHER EACH CASE ACTUALLY TESTS ITS RULE IS MEASURED, NOT ASSERTED. Every
# guard call site -- a line that begins with the `fail` builtin below -- is
# neutered one at a time, the call rewritten to a `true` carrying a NEUTERED
# marker, and the whole suite re-run against the mutant. (Guards are counted by
# an ANCHORED match, because prose like this line mentions them too.)
# Last full run: 29 guards, 49 negative cases. Every guard reddens at least one
# case, every case has exactly one claimant, none unclaimed, none unreachable,
# and every mutant exits non-zero -- that last one is asserted rather than
# printed, because a guard whose removal the suite mentions but does not report
# in $? is invisible to CI.
#
# READ THE SPLIT, NOT THE TOTAL. Of the 49, only 24 are HOLES -- cases where
# removing the guard makes a bad Dockerfile PASS. The other 25 are MESSAGE-ONLY:
# the file is still refused, by the `die` on the next line or by another rule,
# and the case goes red only because it matches on its own rule's message. That
# is defence in depth and it is worth having, but "49 cases, one claimant each"
# would invite the reader to conclude 49 holes are closed, and 24 are. Twelve of
# the 29 guards open a hole when removed; seventeen do not.
#
# Two guards were unreachable the first time this ran, which is what put GO_BIN
# and the single-stage case in the file. Re-run it after adding or moving a rule;
# a guard with no case is a comment, and a case no guard owns is decoration.
#
# One trap, learned twice: do not edit this file while the matrix is running.
# Selecting guards by line number and mutating a file that shifts underneath you
# reports guards as unreachable that are not, which is worse than not measuring.
# Select by occurrence index and confirm the mutation before trusting the row.
#
# RE-POINTED WHEN THE AWK LINE MODEL WAS REPLACED BY BUILDKIT'S PARSER, with the
# mutation that makes each fail, because a case whose subject no longer exists
# is a deleted test wearing the name of a live one. All five were checked by
# actually making the mutation and watching the case go red:
#
#   comment-backslash before a USER  was: the awk dropped comments before
#     joining continuations. now: reading 1's TAILJUNK. Mutation: neuter
#     TAILJUNK -> red. Sole claimant.
#   comment-backslash hiding an ENV  was: the same, away from the tail.
#     now: rule 8, and it still fails if comments ever start swallowing the
#     line below. Mutation: neuter rule 8 -> red.
#   escape directive (2 cases)       was: the awk's directive scanner, incl.
#     the tab form. now: rule 13, fed by BuildKit's EscapeToken. Mutation:
#     neuter rule 13 -> both red.
#   heredocs (4 cases)               was: the awk's heredoc regex. now: rule 14,
#     fed by BuildKit's Heredocs. Mutation: neuter rule 14 -> all four go red,
#     but not for the same reason, and the difference is recorded rather than
#     hidden: the two plain forms are then ACCEPTED, while the two comment-shaped
#     ones are still REJECTED by a second rule (the readings disagree, and
#     hub-gke ends up last). Those two go red anyway because a case matches on
#     its own rule's message, not on "exit 1".
#   heredoc false-positive twins (3) was: the awk regex must not over-match.
#     now: the emitter must not report a heredoc that BuildKit does not see.
#     Mutation: re-introduce a raw-text `grep '<<'` gate -> all three red.
#
# TWO RULES FOR ADDING A CASE, both learned the hard way:
#
#   ASSERT THE MECHANISM, NOT THE OUTCOME. "This input is rejected" says the
#   input was rejected; it does not say BY WHAT. If some other rule fires first,
#   the rule the case is named after can be deleted, weakened, or made
#   unreachable and nothing turns red. So every failing case matches a substring
#   unique to its own rule's message, and where a fixture could be claimed by
#   more than one rule that is called out in a comment on the case. Prefer a
#   fixture only one rule can possibly claim; where the shape of the file makes
#   that impossible (hub-gke cannot be the last stage without also being
#   non-empty or missing its USER), the substring is what does the work.
#
#   A FIXTURE THAT DID NOT MUTATE IS GREEN FOR NO REASON. `expect` refuses any
#   input byte-identical to the known-good file, because a bash substitution
#   that silently matched nothing produces a passing case that tests the
#   known-good file for the second time. That is not hypothetical: the two
#   ENTRYPOINT cases below were written with ${good/ENTRYPOINT ["..."]/...},
#   where bash reads ["..."] as a glob bracket expression matching one
#   character, so neither ever replaced anything. One of them was green.
# ---------------------------------------------------------------------------
self_test() {
  local tmp errors ran
  tmp="$(mktemp -d)"
  # The meta case below writes an executable copy of this script into hack/.
  # It is removed on the way out of every path, including an interrupted run:
  # a stray copy of the guard, sitting in the directory the guard lives in, is
  # exactly the kind of thing that gets committed by accident.
  META_MUTANT="$REPO_ROOT/hack/.selftest-meta-mutant.$$.sh"
  trap 'rm -rf "$tmp"; rm -f "$META_MUTANT"' RETURN
  trap 'rm -rf "$tmp"; rm -f "$META_MUTANT"; exit 130' INT TERM

  # THE FAILURE COUNTER IS A FILE, AND THAT IS THE WHOLE POINT.
  #
  # Every case below is written `... | expect ...`, and the right-hand side of a
  # pipeline runs in a SUBSHELL. This counter was an integer variable, so every
  # increment happened in a subshell and died with it: 70 of the 71 cases could
  # not move the exit code. The suite printed `self-test FAILED: ...` and then
  # `self-test passed`, and exited 0. `make dockerfile-stages` runs it with
  # stdout on /dev/null and CI reads only the exit code, so for the life of that
  # bug the suite guarding eleven rules was a constant.
  #
  # A file survives the subshell. `shopt -s lastpipe` would also work and is not
  # used: it changes the execution model of every pipeline in the file to fix one
  # of them, and it is silently inert in an interactive shell.
  #
  # shellcheck names this SC2031 and shellcheck is not in this repo's CI.
  FAILLOG="$tmp/failures"; : > "$FAILLOG"
  RANLOG="$tmp/ran";       : > "$RANLOG"

  local good="FROM alpine AS builder
RUN echo build

FROM debian:trixie-slim AS runtime
COPY --from=builder /x /usr/local/bin/scion
EXPOSE 8080
ENTRYPOINT [\"/usr/local/bin/scion\"]

FROM runtime AS hub-gke
RUN useradd -u 1000 -m -d /home/scion scion \\
 && mkdir -p /home/scion/.scion
ENV HOME=/home/scion
USER 1000:1000

FROM runtime"

  # expect <expected-exit> <label> [substring the failure message must contain]
  # The substring matters: a fixture that fails for a rule other than the one it
  # was written to trip would otherwise look like a pass.
  expect() {
    local want="$1" label="$2" needle="${3:-}" f out got
    f="$tmp/Dockerfile.$RANDOM"
    cat > "$f"
    # Recorded BEFORE the case runs. The suite's verdict asserts a count of
    # cases actually executed as well as an absence of failures; a suite that
    # silently stops running cases is the same defect as one that cannot report
    # them.
    echo "$label" >> "$RANLOG"
    # Anti-vacuity: a fixture identical to the known-good file tests nothing.
    # The one case that is legitimately byte-identical is named explicitly.
    if [ "$label" != "no trailing newline at all is fine" ] &&
       printf '%s' "$good" | cmp -s - "$f"; then
      echo "self-test FAILED: $label -- the fixture is byte-identical to the known-good file, so it mutated nothing" >&2
      echo "$label" >> "$FAILLOG"
      return
    fi
    out="$("$SELF" "$f" 2>&1)"
    got=$?
    if [ "$got" != "$want" ]; then
      echo "self-test FAILED: $label -- expected exit $want, got $got" >&2
      echo "$out" | sed 's/^/    /' >&2
      echo "$label" >> "$FAILLOG"
      return
    fi
    if [ -n "$needle" ] && ! echo "$out" | grep -q -- "$needle"; then
      echo "self-test FAILED: $label -- exited $got but not for the expected reason ('$needle' not in output)" >&2
      echo "$out" | sed 's/^/    /' >&2
      echo "$label" >> "$FAILLOG"
      return
    fi
    echo "self-test ok: $label (exit $got)"
  }

  echo "$good" | expect 0 "the known-good shape passes"

  # ---- the original trap ---------------------------------------------------
  echo "${good%$'\n\nFROM runtime'}" | expect 1 \
    "hub-gke as the last stage is rejected" "is the LAST stage"

  # ---- a trailing stage that is not empty ----------------------------------
  printf '%s\nUSER 1000:1000\n' "$good" | expect 1 \
    "an instruction appended after the trailing FROM is rejected" "contains 1 instruction"
  printf '%s\nENV KUBECONFIG=/home/scion/.kube/config\n' "$good" | expect 1 \
    "an ENV appended after the trailing FROM is rejected" "contains 1 instruction"

  # ---- the trailing stage no longer derives from the runtime stage ---------
  echo "${good%FROM runtime}FROM debian:trixie-slim" | expect 1 \
    "a trailing stage on an external base is rejected" "is not a stage defined in this file"
  echo "${good%FROM runtime}FROM builder" | expect 1 \
    "a trailing stage on the wrong in-file stage is rejected" "They must share a base stage"

  # R5: both stages repointed together. Rule 4 is satisfied -- they DO share an
  # in-file base -- and the default build target is the toolchain image.
  echo "${good//FROM runtime/FROM builder}" | expect 1 \
    "hub-gke and the trailing stage both repointed to builder is rejected" \
    "but the stage that looks like the runtime stage is"

  # R5, the version a fixed contract cannot catch: a copy-paste sibling of the
  # runtime stage, on the toolchain image, carrying every property the real one
  # has. It is rejected by the COUNT, not by resemblance -- which is the whole
  # argument for uniqueness, so this case is what would notice the count going.
  printf '%s\n' "${good%$'\n\nFROM runtime'}" | sed 's|^FROM runtime AS hub-gke|FROM alpine AS toolbox\
COPY --from=builder /x /usr/local/bin/scion\
ENTRYPOINT ["/usr/local/bin/scion"]\
\
FROM toolbox AS hub-gke|' | { cat; printf '\nFROM toolbox\n'; } | expect 1 \
    "a copy-paste sibling of the runtime stage is rejected" \
    "is not uniquely identifiable"

  # ---- USER ----------------------------------------------------------------
  # Inserted rather than substituted for EXPOSE: removing the EXPOSE would leave
  # the fixture tripping a second rule, and then the needle is doing all the
  # work of telling the two apart.
  echo "$good" | sed 's|^EXPOSE 8080$|EXPOSE 8080\
USER 1000:1000|' | expect 1 \
    "USER added to the runtime stage is rejected" "USER appears in the runtime stage"
  echo "${good/RUN echo build/USER node}" | expect 0 \
    "USER in a build stage is allowed (running npm as node is normal)"
  # ONBUILD: not an instruction in the runtime stage, an instruction in
  # everything built FROM it -- which includes the trailing stage. Nothing
  # appears at the top level of any stage, so the rule each of these defeats
  # cannot claim the fixture; only the ONBUILD rule can. One case per verb,
  # because this was found as a class after `ONBUILD USER` was filed as a
  # singleton and three more turned up the moment anyone looked.
  for ob in "USER 1000:1000" \
            "ENV KUBECONFIG=/tmp/kc" \
            "CMD [\"server\", \"start\", \"--token\", \"hunter2\"]" \
            "ENTRYPOINT [\"/bin/sh\", \"-c\", \"scion server start --token hunter2\"]"; do
    echo "$good" | sed "s|^EXPOSE 8080\$|EXPOSE 8080\\
ONBUILD $ob|" | expect 1 \
      "ONBUILD ${ob%% *} in the runtime stage is rejected" \
      "stage-2(runtime):ONBUILD ${ob%% *}"
  done
  # The twin. An ONBUILD in a stage the runtime chain has nothing to do with
  # affects images built FROM that stage, which is not this script's business,
  # and refusing it would be the cry-wolf failure.
  echo "${good/RUN echo build/ONBUILD COPY . \/src}" | expect 0 \
    "ONBUILD in an unrelated build stage is allowed"
  # ---- the chain itself (rule 17) ------------------------------------------
  # The two above read a CHAIN, and a build-arg base truncates it. The hidden
  # ancestor here really is built by Docker and really does export its USER to
  # the published image; before rule 17 both of these scored a clean pass with
  # rules 6/10/11 all green, which is the vacuity the needle is guarding.
  printf 'ARG B=shady\nFROM alpine AS shady\nUSER 1000:1000\n\n%s\n' \
    "${good/FROM debian:trixie-slim AS runtime/FROM \$\{B\} AS runtime}" | expect 1 \
    "a USER hidden in a build-arg-named ancestor of runtime is rejected" \
    "names its base with a build-arg expansion"
  printf 'ARG B=shady\nFROM alpine AS shady\nONBUILD USER 1000:1000\n\n%s\n' \
    "${good/FROM debian:trixie-slim AS runtime/FROM \$\{B\} AS runtime}" | expect 1 \
    "an ONBUILD hidden in a build-arg-named ancestor of runtime is rejected" \
    "names its base with a build-arg expansion"
  # The twin, and it is the whole reason rule 17 is scoped upward rather than
  # applied to every FROM: `FROM ${NODE} AS frontend` is how this repo's real
  # Dockerfile would pin a base, and no chain member derives from it.
  printf 'ARG NODE=node:22-alpine\nFROM ${NODE} AS frontend\nRUN npm run build\n\n%s\n' "$good" | expect 0 \
    "a build-arg base on a stage outside the runtime chain is allowed"
  echo "${good/USER 1000:1000/USER scion}" | expect 1 \
    "a non-numeric USER in hub-gke is rejected" "must be the numeric uid 1000"
  echo "${good/USER 1000:1000/RUN true}" | expect 1 \
    "hub-gke with no USER at all is rejected" "declares no USER"

  # ---- KUBECONFIG / HOME ---------------------------------------------------
  echo "${good/ENV HOME=\/home\/scion/ENV HOME=\/home\/scion
ENV KUBECONFIG=\/home\/scion\/.kube\/config}" | expect 1 \
    "ENV KUBECONFIG in hub-gke is rejected" "ENV KUBECONFIG appears"
  echo "${good/ENV HOME=\/home\/scion/RUN true}" | expect 1 \
    "hub-gke with no ENV HOME is rejected" "sets no ENV HOME"
  echo "${good/ENV HOME=\/home\/scion/ENV HOME \/home\/scion}" | expect 0 \
    "the legacy space form 'ENV HOME /home/scion' is accepted"

  # ---- the stage the chart targets -----------------------------------------
  echo "${good//hub-gke/hub-k8s}" | expect 1 \
    "renaming the hub-gke stage is rejected" "no stage named"

  # ---- CMD, including the inherited case (R7) ------------------------------
  # The two differ only in WHICH stage the CMD is in, and the message names the
  # stage -- so the needles are the stage, not the words around it. With
  # "declares a CMD" alone, deleting the base-chain walk would leave both green.
  printf '%s\n' "${good/USER 1000:1000/USER 1000:1000
CMD [\"server\", \"start\", \"--token\", \"hunter2\"]}" | expect 1 \
    "a CMD in hub-gke is rejected" "a CMD is declared in 'hub-gke' or a stage it inherits from: stage-3(hub-gke)"
  printf '%s\n' "${good/EXPOSE 8080/EXPOSE 8080
CMD [\"server\", \"start\", \"--token\", \"hunter2\"]}" | expect 1 \
    "a CMD in the runtime stage, inherited by hub-gke, is rejected" "stage-2(runtime)"

  # ---- ENTRYPOINT (R5, R8, and the rider on loosening R5) ------------------
  # sed, not ${good/.../...}: in a bash substitution ["/usr/local/bin/scion"]
  # is a glob bracket expression matching ONE character, so the pattern never
  # matched and both of these cases used to run against the known-good file.
  # `expect` now refuses a fixture that did not mutate, but the lesson is to
  # keep pattern metacharacters out of the pattern.
  echo "$good" | sed '/^ENTRYPOINT/d' | expect 1 \
    "deleting ENTRYPOINT from the runtime stage is rejected" "is not uniquely identifiable"
  echo "$good" | sed 's|^ENTRYPOINT .*$|ENTRYPOINT ["/bin/sh", "-c", "exec scion \\"$@\\""]|' | expect 0 \
    "an ENTRYPOINT that runs scion via a shell wrapper is accepted"
  echo "$good" | sed 's|^ENTRYPOINT .*$|ENTRYPOINT ["/bin/sh", "-c", "scion server start --token hunter2"]|' | expect 1 \
    "a shell-form ENTRYPOINT carrying arguments is rejected" "carries arguments"
  echo "$good" | sed 's|^ENTRYPOINT .*$|ENTRYPOINT ["/usr/local/bin/scion", "server", "start"]|' | expect 1 \
    "an exec-form ENTRYPOINT carrying arguments is rejected" "carries arguments"
  echo "$good" | sed 's|^COPY --from=builder .*$|COPY x /usr/local/bin/scion|' | expect 1 \
    "a base stage that does not ingest a built binary is rejected" "is not uniquely identifiable"

  # ---- structural preconditions --------------------------------------------
  echo "${good/FROM alpine AS builder/FROM alpine AS runtime}" | expect 1 \
    "a duplicate stage name is rejected" "duplicate stage name"
  printf '%s\nUSER 1000 \\\n' "$good" | expect 1 \
    "a dangling continuation in the last stage is rejected" "contains 1 instruction"

  # ---- R1: a comment ending in a backslash swallowing the next line --------
  # Two cases on purpose, because two independent mechanisms have to hold.
  # (a) the TAIL ALLOWLIST, which does not parse at all and so is right even
  # when the parser is wrong -- this is the reading that would have caught R1
  # before it was fixed:
  printf '%s\n# harmless note \\\n USER 1000:1000\n' "$good" | expect 1 \
    "a comment ending in a backslash before a USER is rejected" \
    "neither blank nor a comment"
  # (b) the PARSER, on a mutation the tail allowlist cannot see because it is
  # nowhere near the tail. Only rule 8 can claim this, and only if the comment
  # was dropped before the continuation was joined:
  echo "${good/ENV HOME=\/home\/scion/# note \\
ENV KUBECONFIG=\/tmp\/kc
ENV HOME=\/home\/scion}" | expect 1 \
    "an ENV KUBECONFIG hidden behind a comment-continuation is rejected" "ENV KUBECONFIG appears"
  # ...and the positive twin for the comment drop. If the fix were "drop the
  # comment AND whatever follows it", this passes vacuously -- so the fixture
  # hides the EXPOSE line behind the comment, and rule 5 fails if it went
  # missing. A comment-eats-the-next-line bug cannot be green here.
  echo "$good" | sed 's|^EXPOSE 8080$|# a comment ending in a backslash \\\
EXPOSE 8080|' | expect 0 \
    "a comment ending in a backslash does not swallow the instruction after it"

  # ---- the tail allowlist's own mechanism ----------------------------------
  # A backslash at the end of a line at or after the final FROM is refused even
  # in a comment, where Docker would ignore it: whether it joins depends on the
  # parser, and the trailing stage must not depend on the parser.
  printf '%s\n# trailing note \\\n' "$good" | expect 1 \
    "a comment ending in a backslash after the final FROM is refused" \
    "ends in a continuation backslash"

  # ---- escape directive: refused at the top, ignored elsewhere -------------
  printf '# escape=`\n%s\n' "$good" | expect 1 \
    "an 'escape' parser directive is refused" "escape"
  printf '#\tescape=`\n%s\n' "$good" | expect 1 \
    "a TAB-indented 'escape' directive is refused too" "escape"
  printf '%s\n# escape=` is banned here, see hack/check-dockerfile-stages.sh\n' "$good" | expect 0 \
    "a comment mentioning escape= below the top of the file is not a directive"

  # ---- heredocs: refused, in every form Docker accepts ---------------------
  echo "${good/RUN echo build/RUN <<EOT
echo build
EOT}" | expect 1 "a heredoc is refused" "heredoc"
  printf '%s' "${good/RUN echo build/RUN	<<EOT
echo build
EOT}" | expect 1 "a TAB before a heredoc marker is refused too" "heredoc"
  printf '%s\nRUN 0<<#EOF\nFROM runtime\n#EOF\n' "${good%$'\n\nFROM runtime'}" | expect 1 \
    "an fd-prefixed heredoc with a comment-shaped delimiter is refused" "heredoc"
  printf '%s\nRUN <<\\#EOF\nFROM runtime\n#EOF\n' "${good%$'\n\nFROM runtime'}" | expect 1 \
    "a backslash-quoted comment-shaped delimiter is refused" "heredoc"

  # ...and the twins. A refusal that fires on legal files becomes a nuisance
  # and gets deleted, which is its own silent pass.
  echo "${good/RUN echo build/RUN echo \$((1<<3))}" | expect 0 \
    "a shell left-shift in a RUN is not mistaken for a heredoc"
  printf '%s\n# NB: do not use a heredoc (RUN <<EOF) here -- the guard refuses it\n' "$good" | expect 0 \
    "a COMMENT mentioning <<EOF is not a heredoc"
  echo "${good/EXPOSE 8080/LABEL note=\"shift left <<a bits\"
EXPOSE 8080}" | expect 0 \
    "a LABEL containing << is not a heredoc (LABEL takes no heredoc)"

  # ---- the C-class: the line model is BuildKit's now -----------------------
  # These four are the cases that BROKE the awk. They are kept as fixtures
  # rather than deleted with it, because the file that defeated the guard is
  # still a file someone can write, and because the next person to touch the
  # emitter needs to fail if they get it wrong. All four were settled on Cloud
  # Build by gd-p7-rev-2 on both frontends, not read off parser.go.

  # C1. An empty continuation line, two lines ABOVE the final FROM. BuildKit
  # skips the blank and HOLDS THE CONTINUATION OPEN, so the final FROM is
  # absorbed into the RUN above it and hub-gke becomes the last stage: the
  # untargeted build ships uid 1000. One backslash and one empty line.
  printf '%s\nRUN true \\\n\nFROM runtime\n' "${good%$'\n\nFROM runtime'}" | expect 1 \
    "an empty continuation line above the final FROM is refused" "empty continuation line"
  # ...the same shape without the blank line. Different mechanism: nothing is
  # deprecated here, the continuation simply eats the FROM. This is reading 1's
  # PRECONT window, which is why the needle is PRECONT's message -- rule 16
  # cannot claim this one and the readings-disagree check must not be what the
  # case rests on.
  printf '%s\nRUN true \\\nFROM runtime\n' "${good%$'\n\nFROM runtime'}" | expect 1 \
    "a continuation on the line above the final FROM is refused" "absorb the final FROM"

  # C2. Continuations are joined with NO separator, so an identifier can be
  # split across one. Three cases because three different rules are defeated by
  # the same trick, and each must be caught by its own rule.
  echo "$good" | sed 's|^EXPOSE 8080$|ENV KUBECONF\\\
IG=/home/scion/.kube/config\
EXPOSE 8080|' | expect 1 \
    "an ENV KUBECONFIG split across a continuation is still an ENV KUBECONFIG" \
    "ENV KUBECONFIG appears"
  echo "$good" | sed 's|^EXPOSE 8080$|CM\\\
D ["server", "start", "--token", "hunter2"]\
EXPOSE 8080|' | expect 1 \
    "a CMD whose verb is split across a continuation is still a CMD" \
    "stage-2(runtime)"
  echo "$good" | sed 's|^EXPOSE 8080$|USE\\\
R 0\
EXPOSE 8080|' | expect 1 \
    "a USER whose verb is split across a continuation is still a USER" \
    "stage-2(runtime)"

  # C3. A DOUBLED trailing backslash does not continue the line, so the USER
  # below is a real instruction in the runtime stage rather than part of the RUN.
  echo "$good" | sed 's|^EXPOSE 8080$|RUN echo a\\\\\
USER 0\
EXPOSE 8080|' | expect 1 \
    "a doubled trailing backslash does not swallow the line below it" \
    "stage-2(runtime)"

  # ...and the positive twin for the whole class. An ordinary continuation must
  # still join, or the guard has traded one misreading for another.
  echo "$good" | sed 's|^EXPOSE 8080$|RUN echo one \\\
 \&\& echo two\
EXPOSE 8080|' | expect 0 \
    "an ordinary continuation still joins into one instruction"

  # ---- FAIL CLOSED: no table is a failure, not an empty file ---------------
  # Reading 2 now needs a Go toolchain, a nested module and a module download.
  # If any of them is missing, `parse` prints nothing -- and eleven rules over
  # an empty table is seventeen oks and exit 0. That is not a hypothetical
  # worth a comment; it is the same shape as every other vacuous-pass bug found
  # on this project, so it is constructed and asserted. DOCKERFILE_STAGES_CMD
  # exists for these two cases and nothing else.
  export DOCKERFILE_STAGES_CMD=true
  echo "$good" | expect 1 \
    "an emitter that prints nothing fails the run rather than passing it" \
    "stage table for"
  export DOCKERFILE_STAGES_CMD=false
  echo "$good" | expect 1 \
    "an emitter that exits non-zero fails the run rather than passing it" \
    "failed to read"
  unset DOCKERFILE_STAGES_CMD
  # The third branch of rule 15, and the one a CI runner can actually reach:
  # no Go at all. Distinguished from "the emitter failed" because the remedies
  # are different, and a message that tells you to debug the emitter when the
  # real problem is a missing toolchain is a message that gets the guard
  # deleted. Reached through GO_BIN, so this exercises the branch and not just
  # the string; without that seam it was the one unreachable failure path in
  # the file.
  export DOCKERFILE_STAGES_GO="definitely-not-a-go-toolchain"
  echo "$good" | expect 1 \
    "no Go toolchain on PATH fails the run, with its own message" \
    "is not on PATH"
  unset DOCKERFILE_STAGES_GO

  # The same shape for awk, which is the other thing this script cannot work
  # without and the more dangerous of the two: a missing Go toolchain empties
  # the table and rule 15 says so, while a missing awk leaves the rules running
  # against empty strings that compare equal to each other. Measured before the
  # guard existed: seven `ok:` lines for checks that never ran, including "both
  # readings agree" with nothing on either side. Its own needle, because "not on
  # PATH" alone would also be satisfied by the Go message above.
  export DOCKERFILE_STAGES_AWK="definitely-not-an-awk"
  echo "$good" | expect 1 \
    "no awk on PATH fails the run before it can report agreement with itself" \
    "are awk programs"
  unset DOCKERFILE_STAGES_AWK

  # ---- a single-stage file -------------------------------------------------
  # The whole script is about the relationship between stages, so one stage is
  # not a mild case of the good file -- it is a file none of the rules mean
  # anything about. Left unhandled it reaches rule 1 and reports "no stage
  # named hub-gke", which is true and misleading.
  printf 'FROM debian:trixie-slim\nRUN true\n' | expect 1 \
    "a single-stage file is refused as such, not misreported" \
    "expected a multi-stage Dockerfile"

  # ---- a RELATIVE path argument --------------------------------------------
  # Nothing else in this suite can catch this: `expect` and the review corpus
  # both pass absolute paths, so a shim that resolved paths against the module
  # directory would be green here and broken for every human who typed
  # `hack/check-dockerfile-stages.sh Dockerfile`. A false REJECT reachable only
  # outside the test harness is still a false REJECT.
  printf '%s\n' "$good" > "$tmp/Dockerfile.rel"
  echo "a relative path argument is resolved from the caller's cwd" >> "$RANLOG"
  if ( cd "$tmp" && "$SELF" Dockerfile.rel >/dev/null 2>&1 ); then
    echo "self-test ok: a relative path argument is resolved from the caller's cwd (exit 0)"
  else
    echo "self-test FAILED: a relative path argument is resolved from the caller's cwd" >&2
    echo "a relative path argument is resolved from the caller's cwd" >> "$FAILLOG"
  fi

  # ---- legal variations of the tail: the allowlist must not cry wolf -------
  printf '%s\n' "$good" | expect 0 "a trailing newline after the final FROM is fine"
  printf '%s' "$good" | expect 0 "no trailing newline at all is fine"
  printf '%s\n\n\n' "$good" | expect 0 "trailing blank lines are fine"
  printf '%s\n# the default build target. Do not add anything below this line.\n' "$good" | expect 0 \
    "a comment after the final FROM is fine"
  printf '%s\n' "$good" | sed 's/$/\r/' | expect 0 "CRLF line endings are fine"
  echo "${good%FROM runtime}  FROM runtime" | expect 0 "leading whitespace on the final FROM is fine"
  echo "${good%FROM runtime}FROM runtime AS final" | expect 0 "a named trailing stage is fine"
  echo "${good%FROM runtime}FROM RunTime" | expect 0 \
    "a case-variant reference to the runtime stage still passes"
  echo "${good/FROM debian:trixie-slim AS runtime/FROM --platform=\$BUILDPLATFORM debian:trixie-slim AS runtime}" | expect 0 \
    "FROM --platform=... is parsed, not mistaken for a base image"
  printf '%s\n%s\n' "${good%$'\n\nFROM runtime'}" "FROM hub-gke AS extra
RUN true

FROM runtime" | expect 0 "an extra stage between hub-gke and the trailing stage is fine"

  # ---- the two readings must agree -----------------------------------------
  echo "${good%FROM runtime}FROM runtime \\
AS final" | expect 1 \
    "a final FROM split across a continuation is refused (the readings disagree)" "disagree"

  # ---- an ARG in the trailing stage ----------------------------------------
  printf '%s\nARG FOO=bar\n' "$good" | expect 1 \
    "an ARG after the trailing FROM is rejected" "contains 1 instruction"

  # ---- META: does a failing case actually reach the exit code? -------------
  #
  # Everything above only prints. None of it is worth anything unless a printed
  # FAILED changes $?, because the CI invocation is `make dockerfile-stages`,
  # which runs this with stdout on /dev/null. That link was broken for the whole
  # life of the suite (see FAILLOG above), and a fix scoped to the reported
  # instance leaves the mechanism unguarded, so the mechanism is what this
  # asserts: run the entire suite against a COPY OF ITSELF with one guard
  # neutered -- the same mutation operator the archived guard-defeat matrix uses
  # -- and require a non-zero exit, the FAILED line naming the case that belongs
  # to the neutered guard, and the absence of the word "passed".
  #
  # DOCKERFILE_STAGES_META bounds the recursion at exactly one level. The copy
  # lives beside this script because REPO_ROOT is derived from BASH_SOURCE: a
  # copy in /tmp would look for the emitter in /tmp and fail every case, which
  # would produce the same exit 1 for a reason that has nothing to do with the
  # guard.
  #
  # THERE IS DELIBERATELY NO "an unmutated copy exits 0" TWIN. Neutering ANY
  # guard would redden it, so it would attach itself to every row of the
  # defeat matrix and destroy the one-claimant property that makes the matrix
  # readable. The anti-vacuity work that twin would do is done here instead, by
  # requiring that the copy differ by exactly one line, that the line be the
  # named guard, and that the FAILED line name that guard's own case -- an
  # exit 1 from a copy that failed to build or failed to mutate does not satisfy
  # any of the three.
  if [ -z "${DOCKERFILE_STAGES_META:-}" ]; then
    echo "a neutered guard makes the whole suite exit non-zero" >> "$RANLOG"
    meta_mutant="$META_MUTANT"
    # ANCHORED, and the anchor is load-bearing. The first version of this matched
    # /fail "USER appears/ unanchored -- and the only line it found was the awk
    # program on THIS line, which contains that text as a pattern. The suite then
    # reported the mutation as confirmed (the mutant did contain the string it
    # grepped for) and the case passed while testing nothing. A guard call site
    # is `fail "` at the start of a line; a mention of one is not.
    "$AWK_BIN" '/^[[:space:]]*fail "USER appears in the runtime stage/ && !done { sub(/fail "/, "true \"NEUTERED "); done=1 } { print }' \
      "$SELF" > "$meta_mutant"
    chmod +x "$meta_mutant"
    # Confirm the mutation is the mutation intended, from BOTH sides of the diff:
    # one line replaced, the line that left was a real guard call site, and the
    # line that arrived is that same guard neutered. Grepping the mutant for the
    # string is what was fooled above.
    meta_gone="$(diff "$SELF" "$meta_mutant" | sed -n 's/^< //p')"
    meta_new="$(diff "$SELF" "$meta_mutant" | sed -n 's/^> //p')"
    meta_changed="$(diff "$SELF" "$meta_mutant" | grep -c '^[<>] ')"
    # The diagnostic is BOUNDED because the failure mode with the widest diff is
    # also the one that produces the least readable diff: if awk is missing the
    # mutant is empty, every line of this script counts as "gone", and an
    # unbounded interpolation buries the sentence that explains what happened
    # under a copy of the file. First line, first 100 characters, plus a count.
    meta_gone_brief="$(printf '%s\n' "$meta_gone" | sed -n '1p' | cut -c1-100)"
    if [ "$meta_changed" -gt 2 ] 2>/dev/null; then
      meta_gone_brief="$meta_gone_brief [... $meta_changed diff lines in total, truncated]"
    fi
    if [ "$meta_changed" != "2" ] ||
       ! printf '%s\n' "$meta_gone" | grep -q '^[[:space:]]*fail "USER appears in the runtime stage' ||
       ! printf '%s\n' "$meta_new" | grep -q '^[[:space:]]*true "NEUTERED USER appears in the runtime stage'; then
      echo "self-test FAILED: a neutered guard makes the whole suite exit non-zero -- the mutation did not land as intended (diff touched $meta_changed line(s); gone: '${meta_gone_brief# }'). The operator no longer matches the guard it names, so this case would have tested nothing." >&2
      echo "a neutered guard makes the whole suite exit non-zero" >> "$FAILLOG"
    else
      meta_out="$(DOCKERFILE_STAGES_META=1 "$meta_mutant" --self-test 2>&1)"
      meta_rc=$?
      if [ "$meta_rc" -eq 0 ]; then
        echo "self-test FAILED: a neutered guard makes the whole suite exit non-zero -- the mutant exited 0. Failing cases cannot reach the exit code, so CI cannot see a broken guard." >&2
        echo "$meta_out" | grep '^self-test FAILED' | sed 's/^/    /' >&2
        echo "a neutered guard makes the whole suite exit non-zero" >> "$FAILLOG"
      elif ! echo "$meta_out" | grep -q "^self-test FAILED: USER added to the runtime stage is rejected"; then
        echo "self-test FAILED: a neutered guard makes the whole suite exit non-zero -- the mutant exited $meta_rc, but not because of the case belonging to the neutered guard. An exit 1 for some other reason is not evidence that this link works." >&2
        echo "a neutered guard makes the whole suite exit non-zero" >> "$FAILLOG"
      elif echo "$meta_out" | grep -q "self-test passed"; then
        echo "self-test FAILED: a neutered guard makes the whole suite exit non-zero -- the mutant printed 'self-test passed' alongside its failures." >&2
        echo "a neutered guard makes the whole suite exit non-zero" >> "$FAILLOG"
      else
        echo "self-test ok: a neutered guard makes the whole suite exit non-zero (exit $meta_rc, $(echo "$meta_out" | grep -c '^self-test FAILED') case(s) red)"
      fi
    fi
    rm -f "$meta_mutant"
  fi

  echo
  errors="$(wc -l < "$FAILLOG" | tr -d ' ')"
  ran="$(wc -l < "$RANLOG" | tr -d ' ')"

  # THE PASS CONDITION IS PRESENCE OF N SUCCESSES, NOT ABSENCE OF FAILURE.
  # "no case failed" is also what a suite that ran no cases reports. The count
  # is asserted so that deleting a case, or an early `return` that skips a
  # block of them, is a failure rather than a quieter pass. Adding a case means
  # bumping this number, which is the intended friction.
  want_cases=73
  [ -n "${DOCKERFILE_STAGES_META:-}" ] && want_cases=72   # the meta case skips itself
  if [ "$ran" -ne "$want_cases" ]; then
    echo "self-test: expected $want_cases cases, $ran ran. A case was added, deleted or skipped; a suite that quietly runs fewer cases still reports no failures." >&2
    echo "$ran cases ran" >> "$FAILLOG"
    errors="$(wc -l < "$FAILLOG" | tr -d ' ')"
  fi

  if [ "$errors" -eq 0 ]; then
    echo "self-test passed: $ran cases -- every mutation above was caught, every legal variation was not, and a neutered guard still moves the exit code."
    return 0
  fi
  echo "self-test: $errors case(s) behaved unexpectedly:" >&2
  sed 's/^/  - /' "$FAILLOG" >&2
  return 1
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit $?
fi

DOCKERFILE="${1:-$REPO_ROOT/Dockerfile}"

if [ ! -f "$DOCKERFILE" ]; then
  echo "FAIL: no such file: $DOCKERFILE" >&2
  exit 1
fi

# --- 18. awk is present (fatal: FAIL CLOSED) --------------------------------
# Reading 1 IS an awk program, and every query against reading 2's table is an
# awk one-liner. Without awk this script does not degrade, it lies: measured
# with awk removed, it printed SEVEN `ok:` lines for checks that never ran --
# all four of reading 1's, and "both readings agree the last stage begins at
# line " with both sides empty, because "" = "". The two-readings design
# becomes zero readings and reports agreement. It also produced a false
# accusation ("no stage named 'hub-gke'" on a file that has one) and an
# `integer expression expected` error from the stage-count guard.
#
# The old exit code was 1 in that state, so this looks like it changes nothing.
# It changes everything that matters: exit 1 came from rule 1 happening to be
# fatal on garbage input. Fail-closed by luck is not fail-closed, and the seven
# false `ok:` lines were being read by humans.
#
# Checked before the table is built, so nothing downstream has to wonder.
if ! command -v "$AWK_BIN" >/dev/null 2>&1; then
  fail "cannot check $DOCKERFILE: all of reading 1 and every query against reading 2's table are awk programs, and '$AWK_BIN' is not on PATH. This is not skippable -- without awk the readings return empty strings, compare equal to each other, and report agreement about a file nobody parsed."
  die
fi

# --- 15. the table was actually produced (fatal: FAIL CLOSED) ---------------
# Reading 2 now depends on a Go toolchain, a nested module and a module
# download. If any of those is missing, `parse` prints nothing -- and eleven
# rules over an empty table is seventeen `ok`s and exit 0. That failure shape
# (the pass condition being absence of failure rather than presence of N
# successes) is exactly what this whole script exists to prevent elsewhere, so
# it is asserted here rather than assumed away in a comment.
PARSE_ERR="$(mktemp)"
TABLE="$(parse "$DOCKERFILE" 2>"$PARSE_ERR")"
PARSE_RC=$?
PARSE_MSG="$(cat "$PARSE_ERR")"
rm -f "$PARSE_ERR"

if [ "$PARSE_RC" -ne 0 ]; then
  if ! command -v "$GO_BIN" >/dev/null 2>&1; then
    fail "cannot read $DOCKERFILE: this script gets its stage table from hack/dockerfile-stages, which needs a Go toolchain, and '$GO_BIN' is not on PATH. It is not skippable: with no table there are no instructions to check and every rule below would pass vacuously. CI runs this after actions/setup-go; locally, install Go or run 'make dockerfile-stages' from a checkout."
  else
    fail "hack/dockerfile-stages failed to read $DOCKERFILE (exit $PARSE_RC): ${PARSE_MSG:-no error output}. A file BuildKit's own parser will not read is not a file this guard can have an opinion about."
  fi
  die
fi
if ! echo "$TABLE" | grep -q '^STAGE '; then
  fail "the stage table for $DOCKERFILE is empty -- no STAGE rows. Either the file declares no stages, or the emitter produced nothing. Every rule below reads that table, so continuing would report success for a file nobody looked at."
  die
fi
ok "stage table built by buildkit's own parser ($(echo "$TABLE" | grep -c '^STAGE ') stages)"

TAIL="$(raw_tail "$DOCKERFILE")"

# --- 13. no escape directive (fatal: it changes what every line means) ------
ESCAPE="$(echo "$TABLE" | "$AWK_BIN" '$1=="DIRECTIVE" && $2=="escape" {print $3}')"
if [ -n "$ESCAPE" ]; then
  fail "$DOCKERFILE sets the parser directive 'escape=$ESCAPE', which redefines the line-continuation character. Reading 2 honours it, because it is BuildKit's parser -- but READING 1 does not and cannot: it is raw text, it assumes a backslash continues a line, and it is the check that survives the parser being wrong. A file that needs the escape directive gets one reading instead of two. Remove the directive."
  die
fi

# --- 14. no heredocs (fatal: their bodies are data, not instructions) -------
HEREDOCS="$(echo "$TABLE" | "$AWK_BIN" '$1=="HEREDOC" {printf "line %s: %s\n", $2, $3}')"
if [ -n "$HEREDOCS" ]; then
  fail "$DOCKERFILE uses a heredoc ($(echo "$HEREDOCS" | tr '\n' ';' | sed 's/;$//')). Reading 2 now understands heredocs -- this refusal is NOT because the parser cannot read them. It is because Docker 20.10.24's built-in frontend and docker/dockerfile:1.9 were measured DISAGREEING WITH EACH OTHER about the same heredoc file: under one, a comment-shaped delimiter hides a phantom FROM; under the other, it hides hub-gke at the end of the file. A better parser tells you what buildkit v0.31.2 thinks, not what the frontend building the image thinks, so the construct is refused rather than interpreted. Rewrite without the heredoc."
  die
fi
ok "no construct this parser cannot read (no 'escape' directive, no heredoc)"

STAGE_COUNT="$(echo "$TABLE" | "$AWK_BIN" '$1=="STAGE"{n=$2} END{print n+0}')"
if [ "$STAGE_COUNT" -lt 2 ]; then
  fail "expected a multi-stage Dockerfile, found $STAGE_COUNT stage(s) in $DOCKERFILE"
  die
fi

# --- 12. stage names are unique (fatal: name resolution underpins the rest) -
DUPE_NAMES="$(echo "$TABLE" | "$AWK_BIN" '$1=="STAGE" && $4!="-" {print $4}' | sort | uniq -d | tr '\n' ' ')"
if [ -n "$DUPE_NAMES" ]; then
  fail "duplicate stage name(s) in $DOCKERFILE: ${DUPE_NAMES% }. A later 'FROM <name>' resolves to the last stage of that name, so a duplicate silently changes what the default build target is built from. Stage names must be unique."
  die
fi
ok "stage names are unique, so each 'FROM <stage>' resolves to one stage"

stage_name()  { echo "$TABLE" | "$AWK_BIN" -v n="$1" '$1=="STAGE" && $2==n {print $4}'; }
stage_base()  { echo "$TABLE" | "$AWK_BIN" -v n="$1" '$1=="STAGE" && $2==n {print $3}'; }
stage_line()  { echo "$TABLE" | "$AWK_BIN" -v n="$1" '$1=="STAGE" && $2==n {print $5}'; }
stage_index() { echo "$TABLE" | "$AWK_BIN" -v s="$1" '$1=="STAGE" && $4==s {print $2}'; }
stage_instr_count() { echo "$TABLE" | "$AWK_BIN" -v n="$1" '$1=="INSTR" && $2==n' | wc -l; }
stage_verbs() { echo "$TABLE" | "$AWK_BIN" -v n="$1" '$1=="INSTR" && $2==n {print $3}'; }
stage_lines_of() { echo "$TABLE" | "$AWK_BIN" -v n="$1" -v v="$2" '$1=="INSTR" && $2==n && $3==v {$1="";$2="";$3="";sub(/^ +/,"");print}'; }

LAST="$STAGE_COUNT"

# ---------------------------------------------------------------------------
# Reading 1 vs Reading 2, and the tail allowlist.
# ---------------------------------------------------------------------------
RAW_LASTFROM="$(echo "$TAIL" | "$AWK_BIN" '$1=="LASTFROM"{print $2}')"
PARSED_LASTFROM="$(stage_line "$LAST")"
if [ "$RAW_LASTFROM" != "$PARSED_LASTFROM" ]; then
  fail "the two readings of $DOCKERFILE disagree about where the last stage begins: scanning raw text says line $RAW_LASTFROM, parsing says line $PARSED_LASTFROM. One of them is misreading the file and this script will not guess which. Write the final FROM as a single plain line."
else
  ok "both readings agree the last stage begins at line $RAW_LASTFROM"
fi

TAILJUNK="$(echo "$TAIL" | "$AWK_BIN" '$1=="TAILJUNK"{$1="";sub(/^ /,"");print}')"
if [ -n "$TAILJUNK" ]; then
  fail "there is content after the final FROM line in $DOCKERFILE that is neither blank nor a comment, so the default build target is not an empty stage: $(echo "$TAILJUNK" | tr '\n' '|' | sed 's/|$//'). Nothing may follow the trailing FROM."
else
  ok "nothing but blank lines and comments follows the final FROM line"
fi

TAILCONT="$(echo "$TAIL" | "$AWK_BIN" '$1=="TAILCONT"{$1="";sub(/^ /,"");print}')"
if [ -n "$TAILCONT" ]; then
  fail "a line at or after the final FROM in $DOCKERFILE ends in a continuation backslash: $(echo "$TAILCONT" | tr '\n' '|' | sed 's/|$//'). Whether that joins the next line depends on Docker's parser and on whether the line is a comment; the trailing stage must not depend on either."
else
  ok "no continuation backslash at or after the final FROM line"
fi

PRECONT="$(echo "$TAIL" | "$AWK_BIN" '$1=="PRECONT"{$1="";sub(/^ /,"");print}')"
if [ -n "$PRECONT" ]; then
  fail "the last real line above the final FROM in $DOCKERFILE opens a continuation: $(echo "$PRECONT" | tr '\n' '|' | sed 's/|$//'). That line, and only that line, can absorb the final FROM into itself -- and if it does, the trailing empty stage does not exist and hub-gke is what an untargeted build ships. Blank lines in between do not save it: BuildKit skips them and holds the continuation open."
else
  ok "the last real line above the final FROM does not open a continuation"
fi

# --- 16. no empty continuation line (fatal: deprecated AND load-bearing) ----
# BuildKit skips a blank line inside a continuation and KEEPS THE CONTINUATION
# OPEN, emitting a deprecation warning. That is how one backslash and one empty
# line, two lines above the final FROM, make the untargeted build ship the
# uid-1000 image. It is caught three ways now -- here, by reading 1's PRECONT
# window, and by the two readings disagreeing about where the last stage begins --
# because it is the single worst input this script has been shown, and because a
# construct whose meaning is deprecated is a construct whose meaning can change.
EMPTYCONT="$(echo "$TABLE" | "$AWK_BIN" '$1=="WARN" && /[Ee]mpty continuation/ {$1="";sub(/^ /,"");print}')"
if [ -n "$EMPTYCONT" ]; then
  fail "$DOCKERFILE has an empty continuation line: $(echo "$EMPTYCONT" | tr '\n' ';' | sed 's/;$//'). BuildKit skips the blank line and keeps the continuation open, so the next non-blank line is absorbed into the instruction above -- which is how a blank line silently deletes the trailing stage. Docker deprecates this; remove the blank line or the backslash above it."
  die
fi

# --- 1. the hub-gke stage exists --------------------------------------------
GKE_IDX="$(stage_index "$GKE_STAGE")"
if [ -z "$GKE_IDX" ]; then
  fail "no stage named '$GKE_STAGE' in $DOCKERFILE. The GKE chart builds this image with 'docker build --target $GKE_STAGE'; removing or renaming the stage breaks that build."
  die
fi
ok "stage '$GKE_STAGE' exists (stage $GKE_IDX of $STAGE_COUNT)"

# --- 2. hub-gke is not the last stage ---------------------------------------
if [ "$GKE_IDX" = "$LAST" ]; then
  fail "'$GKE_STAGE' is the LAST stage, so 'docker build' with no --target now builds it. Every consumer of the default target (gcloud run deploy --source, gcloud builds submit) would silently receive the non-root uid-1000 image. Keep a trailing empty stage after it."
else
  ok "'$GKE_STAGE' is not the last stage, so it is not the default build target"
fi

# --- 3. the last stage adds nothing -----------------------------------------
LAST_INSTRS="$(stage_instr_count "$LAST")"
if [ "$LAST_INSTRS" -ne 0 ]; then
  fail "the last stage (stage $LAST, base '$(stage_base "$LAST")') contains $LAST_INSTRS instruction(s): $(stage_verbs "$LAST" | tr '\n' ' '). It must be empty. Most instructions there change the image the default build target produces; a bare ARG does not, but it is the first half of someone using one, and a bright line is the point. Add it to the runtime stage or to '$GKE_STAGE' instead."
else
  ok "the last stage (stage $LAST) is empty, so the default target adds nothing to its base"
fi

# --- 4. the last stage and hub-gke share an in-file base stage --------------
LAST_BASE="$(stage_base "$LAST")"
GKE_BASE="$(stage_base "$GKE_IDX")"
BASE_IDX="$(stage_index "$LAST_BASE")"
if [ -z "$BASE_IDX" ]; then
  fail "the last stage's base '$LAST_BASE' is not a stage defined in this file. The default build target must be the in-file runtime stage, not an external image."
elif [ "$LAST_BASE" != "$GKE_BASE" ]; then
  fail "the last stage derives from '$LAST_BASE' but '$GKE_STAGE' derives from '$GKE_BASE'. They must share a base stage: '$GKE_STAGE' is meant to be the runtime image plus a non-root user, and the default target is meant to be that same runtime image."
else
  ok "the last stage and '$GKE_STAGE' both derive from in-file stage '$LAST_BASE' (stage $BASE_IDX)"
fi

# --- 5. that shared base is the runtime stage, by UNIQUENESS ----------------
# Rule 4 asks whether the two stages AGREE. It is satisfied by repointing both
# at some third stage, which passes while making that stage's image the default
# build target. This rule asks what they agree ON.
#
# Not by name ('runtime' is a comment on the file, and renaming it is a change
# the design deliberately tolerates) and not by position (stage 3 today, stage 4
# after any insertion). And -- this is the part that took a second attempt --
# not by MATCHING A CONTRACT either. Any fixed contract can simply be satisfied:
# a stage on the Go toolchain image that copies in the binary and declares the
# same ENTRYPOINT matches every anchor worth writing, and pointing both stages
# at it publishes the toolchain image as the default target. Worse, the first
# contract tried here (COPY --from=) is already satisfied by the builder stage
# of the real file, which copies the built web assets in from the frontend
# stage -- so it would not even have rejected the case it was written for.
#
# What an attacker cannot satisfy is UNIQUENESS. The contract is:
#
#     the stage ingests an artifact from another in-file stage (COPY --from=)
#     AND it declares an ENTRYPOINT
#
# Neither conjunct is discriminating alone -- builder satisfies the first -- and
# together exactly one stage in this file satisfies them. The assertion is that
# the count is one AND that the one is the stage both hub-gke and the default
# target derive from. Adding a decoy stage now makes the count two and fails;
# getting the count back to one means deleting or degrading the real runtime
# stage, which is a loud edit in the diff rather than a silent repointing.
#
# EXPOSE 8080 is deliberately NOT part of the contract: changing the port is a
# legitimate change that has nothing to do with which stage is the runtime
# stage, so including it is false-positive surface buying no discrimination.
if [ -n "$BASE_IDX" ]; then
  stage_matches_contract() { # <stage index>
    local s="$1" from f
    stage_lines_of "$s" ENTRYPOINT | grep -q . || return 1
    for from in $(stage_lines_of "$s" COPY | tr ' \t' '\n\n' | sed -n 's/^--from=//p'); do
      f="$(echo "$from" | tr 'A-Z' 'a-z')"
      # An in-file stage, by name or by build-stage index. --from=<image> does
      # not count: ingesting from a registry image is not this relationship.
      if [ -n "$(stage_index "$f")" ]; then return 0; fi
      if echo "$f" | grep -Eq '^[0-9]+$' && [ "$f" -lt "$STAGE_COUNT" ]; then return 0; fi
    done
    return 1
  }
  MATCHES=""
  i=1
  while [ "$i" -le "$STAGE_COUNT" ]; do
    if stage_matches_contract "$i"; then MATCHES="$MATCHES $i"; fi
    i=$((i + 1))
  done
  MATCH_COUNT="$(echo $MATCHES | wc -w)"
  MATCH_DESC="$(for s in $MATCHES; do printf 'stage-%s(%s) ' "$s" "$(stage_name "$s")"; done)"
  if [ "$MATCH_COUNT" -ne 1 ]; then
    fail "the runtime stage is not uniquely identifiable: $MATCH_COUNT stage(s) in $DOCKERFILE take an artifact from another in-file stage AND declare an ENTRYPOINT (${MATCH_DESC:-none}). That contract is how this script recognises the runtime stage without depending on its name or its position, and it only means anything while exactly one stage satisfies it. Two matches usually means a new stage was added that looks like a runtime stage -- if that is deliberate, this check needs updating deliberately. Zero usually means the ENTRYPOINT was removed from the runtime stage."
  elif [ "${MATCHES# }" != "$BASE_IDX" ]; then
    fail "'$GKE_STAGE' and the default build target both derive from stage $BASE_IDX ('$LAST_BASE'), but the stage that looks like the runtime stage is ${MATCH_DESC% }. They must be the same stage: as written, the default build target publishes an image built from something other than the runtime stage."
  else
    ok "exactly one stage takes an artifact from another in-file stage and declares an ENTRYPOINT, and it is the shared base (${MATCH_DESC% })"
  fi
fi

# --- 17. every base in the runtime chain is written literally (fatal) -------
# Rules 6, 10 and 11 do not read a stage, they read a CHAIN, and the chain is
# computed by following `FROM <base>` upwards. `FROM ${B} AS runtime` truncates
# that walk: the ancestor is real, Docker builds it, and this script cannot see
# it -- so a `USER 1000:1000` or an `ONBUILD` parked in that hidden ancestor is
# inherited by the runtime stage, by the trailing stage, and by the published
# image, while rules 6/10/11 report ok on the two stages they can still see.
# That is a vacuous pass, not a missed rule, which is why it is fatal.
#
# Refused rather than resolved, and the reason is not effort. `docker build
# --build-arg B=other` changes the answer at build time, so which stage the
# chain contains is NOT A PROPERTY OF THIS FILE. There is no value to compute.
#
# Scoped to the upward chain only. `FROM ${NODE} AS frontend` on a stage nobody
# in the chain derives from is ordinary and stays green (corpus 101), and the
# trailing stage's own base is already required to be a literal in-file name by
# rule 4 (corpus 54).
if [ -n "$BASE_IDX" ]; then
  IN_CHAIN=" $BASE_IDX "
  VARBASE=""
  b="$BASE_IDX"
  while :; do
    bb="$(stage_base "$b")"
    case "$bb" in
      *'$'*) VARBASE="$VARBASE stage-$b($(stage_name "$b")):FROM $bb" ;;
    esac
    p="$(stage_index "$bb")"
    [ -n "$p" ] || break
    IN_CHAIN="$IN_CHAIN$p "
    b="$p"
  done
  # Downward: stages built FROM a chain member. Not var-checked -- a variable
  # base there cannot silently ADD an ancestor to the published image.
  i=1
  while [ "$i" -le "$STAGE_COUNT" ]; do
    pb="$(stage_index "$(stage_base "$i")")"
    if [ -n "$pb" ] && [ "${IN_CHAIN#* $pb }" != "$IN_CHAIN" ]; then IN_CHAIN="$IN_CHAIN$i "; fi
    i=$((i + 1))
  done
  if [ -n "$VARBASE" ]; then
    fail "a stage in the runtime chain names its base with a build-arg expansion:$VARBASE. The chain is what rules 6, 10 and 11 are actually about, and this truncates it: the real ancestor is built by Docker and is invisible here, so a USER or an ONBUILD parked in it reaches the published image while those rules report ok on the stages they can still see. It is refused rather than resolved because 'docker build --build-arg' can change the value at build time -- which stage the chain contains is not a property of this file. Write the base literally. A variable base on a stage outside the runtime chain is fine."
    die
  fi
  ok "every base in the runtime chain is written literally, so the chain can be computed from the file"
fi

# --- 6. no USER in the runtime stage or its descendants, except hub-gke -----
# Scoped to that chain on purpose: `USER node` in a build stage is normal, and
# rule 6's rationale -- a uid change is a behaviour change for consumers of the
# published image -- only applies to stages that become one.
#
# Upward: a USER in a stage the runtime stage derives from is inherited by it.
# Downward: a USER in a stage derived from the runtime stage is in the image
# only if that stage is a build target -- and the default target is one.
# Sideways is not included: `USER node` in an unrelated build stage is normal
# and refusing it is how a guard earns its deletion.
if [ -n "$BASE_IDX" ]; then
  USER_OUTSIDE=""
  ONBUILD_IN_CHAIN=""
  for i in $IN_CHAIN; do
    if [ "$i" != "$GKE_IDX" ] && stage_verbs "$i" | grep -qx "USER"; then
      USER_OUTSIDE="$USER_OUTSIDE stage-$i($(stage_name "$i"))"
    fi
    # ONBUILD, ANY VERB, ANYWHERE IN THE RUNTIME CHAIN.
    #
    # An ONBUILD instruction is not an instruction in this stage -- it is an
    # instruction in every stage built FROM it, which here includes the empty
    # trailing stage that is the default build target. So it is a way to add
    # something to the published images with that something never appearing as
    # an instruction in either of them, and every rule below reads instructions.
    #
    # This refuses the CONSTRUCT rather than enumerating the dangerous verbs,
    # which is deliberate. `ONBUILD USER` was filed as a curiosity and turned out
    # to be a live bypass; probing the rest of the class immediately found three
    # more (`ONBUILD ENV KUBECONFIG`, `ONBUILD CMD`, `ONBUILD ENTRYPOINT` with
    # arguments), each defeating a different rule. Enumerating verbs would leave
    # the next one open, and the whole class is unnecessary here: nothing in a
    # leaf application image needs an ONBUILD trigger.
    #
    # WHAT THIS RULE DEPENDS ON, AND THEREFORE DOES NOT ITSELF GUARANTEE.
    # Refusing the construct is only as strong as (a) the line model spelling
    # ONBUILD the way Docker does -- `ONBUIL\` + `D USER 1000:1000` is not this
    # verb to a reader that joins continuations wrongly, and it was not this
    # verb to the awk this file used to carry -- and (b) IN_CHAIN containing
    # every stage the runtime image actually inherits from. Both are enforced
    # elsewhere: (a) by reading 2 being buildkit's own parser rather than a
    # second implementation of it, (b) by rule 17's fatal refusal of a
    # build-arg-substituted base inside the chain. If either is weakened, this
    # rule weakens with it SILENTLY, because it will still print its ok line.
    # Both scopes were live bypasses (corpus 100, 98/99) at the head where the
    # sentence above was first written.
    ONBUILD_VERB="$(stage_lines_of "$i" ONBUILD | "$AWK_BIN" 'NF {print toupper($1)}' | sort -u | tr '\n' ',' | sed 's/,$//')"
    if [ -n "$ONBUILD_VERB" ]; then
      ONBUILD_IN_CHAIN="$ONBUILD_IN_CHAIN stage-$i($(stage_name "$i")):ONBUILD $ONBUILD_VERB"
    fi
  done
  if [ -n "$USER_OUTSIDE" ]; then
    fail "USER appears in the runtime stage, in a stage it derives from, or in a stage derived from it, outside '$GKE_STAGE':$USER_OUTSIDE. Changing the uid of a pre-existing published image is a behaviour change for every existing consumer of it; the non-root user belongs only in '$GKE_STAGE'."
  else
    ok "no USER instruction in the runtime chain outside '$GKE_STAGE'"
  fi
  if [ -n "$ONBUILD_IN_CHAIN" ]; then
    fail "an ONBUILD trigger appears in the runtime chain:$ONBUILD_IN_CHAIN. An ONBUILD fires in every stage built FROM that stage -- which includes the empty trailing stage that is the default build target -- so it adds an instruction to the published image that appears nowhere in the stage the image is built from. Every rule in this script reads instructions, so every one of them can be walked past this way: ONBUILD USER sets the default target's uid, ONBUILD ENV bakes a KUBECONFIG, ONBUILD CMD and ONBUILD ENTRYPOINT bake arguments. The construct is refused outright rather than filtered by verb, because a leaf application image has no use for it and enumerating the dangerous verbs leaves the next one open."
  else
    ok "no ONBUILD trigger in the runtime chain"
  fi
fi

# --- 7. hub-gke sets a numeric uid 1000  (positive twin of 6) ---------------
GKE_USER="$(stage_lines_of "$GKE_IDX" USER | tail -n 1)"
if [ -z "$GKE_USER" ]; then
  fail "'$GKE_STAGE' declares no USER. It exists to run as a non-root uid; without USER it runs as root and 'runAsNonRoot: true' refuses to start the pod."
elif ! echo "$GKE_USER" | grep -Eq '^1000(:[0-9]+)?$'; then
  fail "'$GKE_STAGE' sets USER '$GKE_USER'. It must be the numeric uid 1000 (optionally 1000:<gid>): Kubernetes 'runAsNonRoot: true' cannot resolve a username to a uid and fails the container."
else
  ok "'$GKE_STAGE' sets USER '$GKE_USER' (numeric, as runAsNonRoot requires)"
fi

# --- 8. no ENV KUBECONFIG anywhere  (negative) ------------------------------
if echo "$TABLE" | "$AWK_BIN" '$1=="INSTR" && $3=="ENV"' | grep -q 'KUBECONFIG'; then
  fail "an ENV KUBECONFIG appears in $DOCKERFILE. pkg/k8s/client.go prefers an explicit kubeconfig over in-cluster credentials, so baking one in silently disables ServiceAccount auth and the hub cannot schedule agent pods -- a failure that looks like an RBAC problem."
else
  ok "no ENV KUBECONFIG anywhere in the file"
fi

# --- 9. hub-gke sets HOME  (positive twin of 8) -----------------------------
# Both the `HOME=value` and the legacy `HOME value` forms are legal.
GKE_HOME="$(stage_lines_of "$GKE_IDX" ENV | grep -E '(^|[[:space:]])HOME([=[:space:]])' | tail -n 1)"
if [ -z "$GKE_HOME" ]; then
  fail "'$GKE_STAGE' sets no ENV HOME. \$HOME/.scion is the only lever on where the hub reads and writes settings.yaml -- there is no override env -- so an unset HOME sends it somewhere unwritable."
else
  ok "'$GKE_STAGE' sets ENV HOME ('$GKE_HOME')"
fi

# --- 10. no CMD on hub-gke or on anything it inherits from ------------------
# A CMD in the runtime stage is inherited by hub-gke, so checking only hub-gke's
# own instructions misses the case the rule exists for.
CMD_CHAIN="$GKE_IDX"
b="$GKE_IDX"
while :; do
  p="$(stage_index "$(stage_base "$b")")"
  [ -n "$p" ] || break
  CMD_CHAIN="$CMD_CHAIN $p"
  b="$p"
done
CMD_IN=""
for s in $CMD_CHAIN; do
  if stage_verbs "$s" | grep -qx "CMD"; then CMD_IN="$CMD_IN stage-$s($(stage_name "$s"))"; fi
done
if [ -n "$CMD_IN" ]; then
  fail "a CMD is declared in '$GKE_STAGE' or a stage it inherits from:$CMD_IN. '$GKE_STAGE' inherits CMD from its base chain, so the published image would carry those arguments. The chart supplies the hub's arguments; baking them into the image risks baking a secret into a published artifact."
else
  ok "no CMD on '$GKE_STAGE' or any stage it inherits from (the chart supplies the arguments)"
fi

# --- 11. no ENTRYPOINT in that chain carries arguments either ---------------
# Rule 10 keeps arguments out of CMD. ENTRYPOINT is the same surface:
# ENTRYPOINT ["/bin/sh", "-c", "scion server start --token hunter2"] bakes
# exactly what the rejected CMD would have. Rule 5 no longer requires the
# ENTRYPOINT to take any particular form, so nothing else looks at it.
#
# Allowed: a single program (exec or shell form), and a shell wrapper whose
# script runs one program with argv passed through -- `exec scion "$@"` is a
# real pattern and carries no arguments of its own.
entrypoint_carries_args() { # <the text after the ENTRYPOINT verb>
  local rest="$1" elems n first second script extra
  if [ "${rest#[}" != "$rest" ]; then
    elems="$(printf '%s' "$rest" | sed 's/^\[//; s/\][[:space:]]*$//' | tr ',' '\n' |
             sed 's/^[[:space:]]*"\{0,1\}//; s/"\{0,1\}[[:space:]]*$//' | grep -c .)"
    n="$elems"
    elems="$(printf '%s' "$rest" | sed 's/^\[//; s/\][[:space:]]*$//' | tr ',' '\n' |
             sed 's/^[[:space:]]*"\{0,1\}//; s/"\{0,1\}[[:space:]]*$//')"
  else
    elems="$(printf '%s' "$rest" | tr -s ' \t' '\n\n' | grep -v '^$')"
    n="$(printf '%s\n' "$elems" | grep -c .)"
  fi
  [ "$n" -le 1 ] && return 1
  first="$(printf '%s\n' "$elems" | sed -n 1p)"
  second="$(printf '%s\n' "$elems" | sed -n 2p)"
  if printf '%s' "$first" | grep -Eq '(^|/)(sh|bash|ash|dash)$' && [ "$second" = "-c" ]; then
    script="$(printf '%s\n' "$elems" | sed -n '3,$p')"
    # Everything in the script that is not `exec` and not an argv passthrough.
    extra="$(printf '%s' "$script" | tr -s ' \t' '\n\n' | grep -v '^$' |
             grep -Ev '^(exec|\\?"?[$]\{?@\}?\\?"?)$' | grep -c .)"
    [ "$extra" -le 1 ] && return 1
  fi
  return 0
}
EP_ARGS=""
for s in $CMD_CHAIN; do
  while IFS= read -r ep; do
    [ -n "$ep" ] || continue
    if entrypoint_carries_args "$ep"; then EP_ARGS="$EP_ARGS stage-$s($(stage_name "$s")): $ep;"; fi
  done <<EOF
$(stage_lines_of "$s" ENTRYPOINT)
EOF
done
if [ -n "$EP_ARGS" ]; then
  fail "an ENTRYPOINT in '$GKE_STAGE' or a stage it inherits from carries arguments:${EP_ARGS%;}. This is the same surface rule 10 closes for CMD -- arguments baked into a published image, where a token or a flag ends up in the artifact rather than in the chart. The ENTRYPOINT should name the program; the chart supplies what follows it."
else
  ok "no ENTRYPOINT in '$GKE_STAGE' or its base chain carries arguments"
fi

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "Dockerfile stage checks passed: the default build target is stage $LAST, an empty stage over '$LAST_BASE'."
  exit 0
fi
echo "$FAILURES check(s) failed."
exit 1
