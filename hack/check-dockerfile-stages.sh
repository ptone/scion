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
# A review of an earlier version got six wrong Dockerfiles past it, and every
# one of them worked by confusing the PARSER rather than by violating a rule.
# So the most important property no longer depends on the parser:
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
#      stage derived from it, except `hub-gke`; and no `ONBUILD USER` anywhere
#      in that chain, since its trigger fires in the trailing stage (a `USER`
#      in an unrelated build stage is normal and is not this script's business)
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
# plus three structural preconditions, fatal rather than counted because every
# check above assumes them:
#
#  11. stage names are unique. A second `AS runtime` later in the file silently
#      repoints the trailing stage at a different image.
#  12. no `escape` parser directive (it redefines the continuation character).
#  13. no heredocs (their bodies are data, not instructions).
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
#   - It sees `ONBUILD USER` only in the runtime chain (rule 6). An ONBUILD in
#     an unrelated stage affects images built FROM that stage, which is not this
#     script's business.
#   - Its parser models a subset of Dockerfile syntax. Constructs that would be
#     MISREAD rather than merely missed are refused (12, 13) instead of guessed
#     at, and the tail allowlist means a misread cannot silently satisfy the
#     main property -- but the subset is still a subset.
#
# EVERY GATE LIVES INSIDE parse()'s awk, ON PURPOSE. An earlier version put two
# gates in `grep -E`, where `[ \t]` is the set {space, backslash, t} and not a
# tab, so both were walkable by pressing Tab -- and the self-test never noticed,
# because it only ever reached the awk. A gate the self-test cannot reach is not
# tested. If you add a gate, add it where the fixtures already go.
#
# Run `hack/check-dockerfile-stages.sh --self-test` to see the script fail on
# each mutation it is meant to catch, and pass the legitimate variations that
# look like them.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SELF="${BASH_SOURCE[0]}"
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
# Reading 2: parse the Dockerfile into a stage table.
#
# Emits, in file order:
#   DIRECTIVE <name> <value>      parser directive at the top of the file
#   HEREDOC <line> <word>         a heredoc redirection, as Docker detects one
#   STAGE <n> <base> <name-or--> <line>
#   INSTR <n> <VERB> <rest of line>
#
# Stage names are case-insensitive to Docker, so names and the base refs that
# point at them are lowercased. Image refs are lowercase anyway.
# ---------------------------------------------------------------------------
parse() {
  awk '
    function emit(logical, lno,   l, n, tok, verb, base, name, rest, i) {
      if (logical ~ /^[ \t]*$/) return
      l = logical
      sub(/^[ \t]+/, "", l)
      n = split(l, tok, /[ \t]+/)
      verb = toupper(tok[1])

      # Heredoc detection, done the way Docker does it: only on the
      # instructions that support heredocs, by word-splitting the line and
      # matching each word against BuildKits form  ^([0-9]*)<<(-?)...
      # Doing it this way (rather than grepping the raw text) is what keeps a
      # comment that MENTIONS <<EOF, or a LABEL containing a left-shift, from
      # being refused -- neither is a heredoc to Docker either.
      if (verb == "RUN" || verb == "COPY" || verb == "ADD" || verb == "ONBUILD") {
        for (i = 2; i <= n; i++) {
          if (tok[i] ~ /^[0-9]*<<-?[^<]/) printf "HEREDOC %d %s\n", lno, tok[i]
        }
      }

      if (verb == "FROM") {
        stage++
        i = 2
        while (i <= n && tok[i] ~ /^--/) i++      # FROM --platform=... AS x
        base = tolower(tok[i])
        name = "-"
        if (i + 2 <= n && toupper(tok[i+1]) == "AS") name = tolower(tok[i+2])
        printf "STAGE %d %s %s %d\n", stage, base, name, lno
      } else if (verb == "ARG" && stage == 0) {
        return                                    # pre-FROM ARG: not in a stage
      } else {
        rest = l
        sub(/^[^ \t]+[ \t]*/, "", rest)
        printf "INSTR %d %s %s\n", stage, verb, rest
      }
    }

    BEGIN { in_directives = 1 }
    {
      line = $0
      sub(/\r$/, "", line)

      # Parser directives are only honoured in the run of directive-comments at
      # the very top of the file; the first line that is not one ends the block.
      # Modelling that is what stops a comment merely DISCUSSING `escape=` from
      # being treated as one.
      if (in_directives) {
        if (line ~ /^#[ \t]*[a-zA-Z][a-zA-Z0-9]*[ \t]*=/) {
          d = line
          sub(/^#[ \t]*/, "", d)
          split(d, dd, /[ \t]*=[ \t]*/)
          printf "DIRECTIVE %s %s\n", tolower(dd[1]), dd[2]
          next
        }
        in_directives = 0
      }

      # A comment is dropped BEFORE continuation handling, and leaves any open
      # continuation untouched -- which is what Docker does (BuildKit runs
      # trimComments before trimContinuationCharacter). Doing it the other way
      # round lets `# note \` swallow the line after it.
      if (line ~ /^[ \t]*#/) next

      if (cont != "") { line = cont " " line; cont = "" }
      if (line ~ /\\[ \t]*$/) { sub(/\\[ \t]*$/, "", line); cont = line; next }
      emit(line, FNR)
    }
    # A continuation left open at EOF is still an instruction to Docker.
    # Dropping it would let `USER 1000 \` as the final line of the file -- one
    # stray backslash, no exotic syntax -- pass as an empty last stage.
    END { if (cont != "") emit(cont, FNR) }
  ' "$1"
}

# ---------------------------------------------------------------------------
# Reading 1: the tail of the file, as raw text, with no parsing.
#
# Emits:
#   LASTFROM <line>            the last whole-line FROM in the file
#   TAILJUNK <line> <text>     a line after it that is neither blank nor comment
#   TAILCONT <line> <text>     a line from it onwards that opens a continuation
# ---------------------------------------------------------------------------
raw_tail() {
  awk '
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
  local tmp errors=0
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

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
    # Anti-vacuity: a fixture identical to the known-good file tests nothing.
    # The one case that is legitimately byte-identical is named explicitly.
    if [ "$label" != "no trailing newline at all is fine" ] &&
       printf '%s' "$good" | cmp -s - "$f"; then
      echo "self-test FAILED: $label -- the fixture is byte-identical to the known-good file, so it mutated nothing" >&2
      errors=$((errors + 1))
      return
    fi
    out="$("$SELF" "$f" 2>&1)"
    got=$?
    if [ "$got" != "$want" ]; then
      echo "self-test FAILED: $label -- expected exit $want, got $got" >&2
      echo "$out" | sed 's/^/    /' >&2
      errors=$((errors + 1))
      return
    fi
    if [ -n "$needle" ] && ! echo "$out" | grep -q -- "$needle"; then
      echo "self-test FAILED: $label -- exited $got but not for the expected reason ('$needle' not in output)" >&2
      echo "$out" | sed 's/^/    /' >&2
      errors=$((errors + 1))
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
  # ONBUILD USER: not a USER in the runtime stage, a USER in everything built
  # FROM it -- which includes the trailing stage. No USER appears at the top
  # level of any stage, so rule 6's plain-USER half cannot claim this one.
  echo "$good" | sed 's|^EXPOSE 8080$|EXPOSE 8080\
ONBUILD USER 1000:1000|' | expect 1 \
    "ONBUILD USER in the runtime stage is rejected" "ONBUILD USER appears in the runtime chain"
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

  echo
  if [ "$errors" -eq 0 ]; then
    echo "self-test passed: every mutation above was caught, and every legal variation was not."
    return 0
  fi
  echo "self-test: $errors case(s) behaved unexpectedly." >&2
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

TABLE="$(parse "$DOCKERFILE")"
TAIL="$(raw_tail "$DOCKERFILE")"

# --- 12. no escape directive (fatal: it changes what every line means) ------
ESCAPE="$(echo "$TABLE" | awk '$1=="DIRECTIVE" && $2=="escape" {print $3}')"
if [ -n "$ESCAPE" ]; then
  fail "$DOCKERFILE sets the parser directive 'escape=$ESCAPE', which redefines the line-continuation character. This script joins continuations itself and would misread the file rather than fail on it. Remove the directive, or teach parse() to honour it before re-enabling this check."
  die
fi

# --- 13. no heredocs (fatal: their bodies are data, not instructions) -------
HEREDOCS="$(echo "$TABLE" | awk '$1=="HEREDOC" {printf "line %s: %s\n", $2, $3}')"
if [ -n "$HEREDOCS" ]; then
  fail "$DOCKERFILE uses a heredoc ($(echo "$HEREDOCS" | tr '\n' ';' | sed 's/;$//')). This script treats every line as an instruction, so heredoc bodies are misread -- a body line starting with FROM invents a stage, and with a comment-shaped delimiter that invented stage impersonates the empty trailing stage this script exists to require. Rewrite without the heredoc, or teach parse() to skip heredoc bodies before re-enabling this check."
  die
fi
ok "no construct this parser cannot read (no 'escape' directive, no heredoc)"

STAGE_COUNT="$(echo "$TABLE" | awk '$1=="STAGE"{n=$2} END{print n+0}')"
if [ "$STAGE_COUNT" -lt 2 ]; then
  fail "expected a multi-stage Dockerfile, found $STAGE_COUNT stage(s) in $DOCKERFILE"
  die
fi

# --- 11. stage names are unique (fatal: name resolution underpins the rest) -
DUPE_NAMES="$(echo "$TABLE" | awk '$1=="STAGE" && $4!="-" {print $4}' | sort | uniq -d | tr '\n' ' ')"
if [ -n "$DUPE_NAMES" ]; then
  fail "duplicate stage name(s) in $DOCKERFILE: ${DUPE_NAMES% }. A later 'FROM <name>' resolves to the last stage of that name, so a duplicate silently changes what the default build target is built from. Stage names must be unique."
  die
fi
ok "stage names are unique, so each 'FROM <stage>' resolves to one stage"

stage_name()  { echo "$TABLE" | awk -v n="$1" '$1=="STAGE" && $2==n {print $4}'; }
stage_base()  { echo "$TABLE" | awk -v n="$1" '$1=="STAGE" && $2==n {print $3}'; }
stage_line()  { echo "$TABLE" | awk -v n="$1" '$1=="STAGE" && $2==n {print $5}'; }
stage_index() { echo "$TABLE" | awk -v s="$1" '$1=="STAGE" && $4==s {print $2}'; }
stage_instr_count() { echo "$TABLE" | awk -v n="$1" '$1=="INSTR" && $2==n' | wc -l; }
stage_verbs() { echo "$TABLE" | awk -v n="$1" '$1=="INSTR" && $2==n {print $3}'; }
stage_lines_of() { echo "$TABLE" | awk -v n="$1" -v v="$2" '$1=="INSTR" && $2==n && $3==v {$1="";$2="";$3="";sub(/^ +/,"");print}'; }

LAST="$STAGE_COUNT"

# ---------------------------------------------------------------------------
# Reading 1 vs Reading 2, and the tail allowlist.
# ---------------------------------------------------------------------------
RAW_LASTFROM="$(echo "$TAIL" | awk '$1=="LASTFROM"{print $2}')"
PARSED_LASTFROM="$(stage_line "$LAST")"
if [ "$RAW_LASTFROM" != "$PARSED_LASTFROM" ]; then
  fail "the two readings of $DOCKERFILE disagree about where the last stage begins: scanning raw text says line $RAW_LASTFROM, parsing says line $PARSED_LASTFROM. One of them is misreading the file and this script will not guess which. Write the final FROM as a single plain line."
else
  ok "both readings agree the last stage begins at line $RAW_LASTFROM"
fi

TAILJUNK="$(echo "$TAIL" | awk '$1=="TAILJUNK"{$1="";sub(/^ /,"");print}')"
if [ -n "$TAILJUNK" ]; then
  fail "there is content after the final FROM line in $DOCKERFILE that is neither blank nor a comment, so the default build target is not an empty stage: $(echo "$TAILJUNK" | tr '\n' '|' | sed 's/|$//'). Nothing may follow the trailing FROM."
else
  ok "nothing but blank lines and comments follows the final FROM line"
fi

TAILCONT="$(echo "$TAIL" | awk '$1=="TAILCONT"{$1="";sub(/^ /,"");print}')"
if [ -n "$TAILCONT" ]; then
  fail "a line at or after the final FROM in $DOCKERFILE ends in a continuation backslash: $(echo "$TAILCONT" | tr '\n' '|' | sed 's/|$//'). Whether that joins the next line depends on Docker's parser and on whether the line is a comment; the trailing stage must not depend on either."
else
  ok "no continuation backslash at or after the final FROM line"
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

# --- 6. no USER in the runtime stage or its descendants, except hub-gke -----
# Scoped to that chain on purpose: `USER node` in a build stage is normal, and
# rule 6's rationale -- a uid change is a behaviour change for consumers of the
# published image -- only applies to stages that become one.
if [ -n "$BASE_IDX" ]; then
  # Upward: a USER in a stage the runtime stage derives from is inherited by it.
  # Downward: a USER in a stage derived from the runtime stage is in the image
  # only if that stage is a build target -- and the default target is one.
  # Sideways is not included: `USER node` in an unrelated build stage is normal
  # and refusing it is how a guard earns its deletion.
  IN_CHAIN=" $BASE_IDX "
  b="$BASE_IDX"
  while :; do
    p="$(stage_index "$(stage_base "$b")")"
    [ -n "$p" ] || break
    IN_CHAIN="$IN_CHAIN$p "
    b="$p"
  done
  i=1
  while [ "$i" -le "$STAGE_COUNT" ]; do
    pb="$(stage_index "$(stage_base "$i")")"
    if [ -n "$pb" ] && [ "${IN_CHAIN#* $pb }" != "$IN_CHAIN" ]; then IN_CHAIN="$IN_CHAIN$i "; fi
    i=$((i + 1))
  done
  USER_OUTSIDE=""
  ONBUILD_USER=""
  for i in $IN_CHAIN; do
    if [ "$i" != "$GKE_IDX" ] && stage_verbs "$i" | grep -qx "USER"; then
      USER_OUTSIDE="$USER_OUTSIDE stage-$i($(stage_name "$i"))"
    fi
    # ONBUILD USER is not a USER in this stage -- it is a USER in every stage
    # built FROM it, which here includes the empty trailing stage. It is
    # therefore a way to set the default target's uid without the word USER ever
    # appearing at the top level of any stage. It is caught for the whole chain,
    # hub-gke included, because unlike a plain USER it never belongs here.
    if stage_lines_of "$i" ONBUILD | grep -Eqi '^USER([[:space:]]|$)'; then
      ONBUILD_USER="$ONBUILD_USER stage-$i($(stage_name "$i"))"
    fi
  done
  if [ -n "$USER_OUTSIDE" ]; then
    fail "USER appears in the runtime stage, in a stage it derives from, or in a stage derived from it, outside '$GKE_STAGE':$USER_OUTSIDE. Changing the uid of a pre-existing published image is a behaviour change for every existing consumer of it; the non-root user belongs only in '$GKE_STAGE'."
  else
    ok "no USER instruction in the runtime chain outside '$GKE_STAGE'"
  fi
  if [ -n "$ONBUILD_USER" ]; then
    fail "ONBUILD USER appears in the runtime chain:$ONBUILD_USER. An ONBUILD trigger fires in every stage built FROM that stage -- which includes the empty trailing stage that is the default build target -- so this sets the uid of the default target's image without a USER instruction appearing anywhere in it."
  else
    ok "no ONBUILD USER trigger in the runtime chain"
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
if echo "$TABLE" | awk '$1=="INSTR" && $3=="ENV"' | grep -q 'KUBECONFIG'; then
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
