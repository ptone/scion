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
# WHAT IT CHECKS
#
# The property is NOT "the last FROM line says runtime". That would pass a file
# where someone appended `USER 1000` after that line. The property is:
#
#   the last stage derives from the same in-file stage that hub-gke derives
#   from, and adds nothing to it
#
# so the checks are:
#
#   1. a stage named `hub-gke` exists                     (it is what we protect)
#   2. `hub-gke` is not the last stage                    (the original trap)
#   3. the last stage contains no instructions at all     (empty, so it adds nothing)
#   4. the last stage's base is the same in-file stage hub-gke's base is
#      (name-agnostic: renaming the runtime stage is fine, repointing either
#      stage at something else is not)
#   5. no `USER` outside `hub-gke`                        (negative)
#   6. `hub-gke` sets USER to a numeric uid 1000          (positive twin of 5;
#      `runAsNonRoot: true` cannot resolve a username)
#   7. no `ENV KUBECONFIG` anywhere                       (negative: an explicit
#      kubeconfig silently disables in-cluster ServiceAccount auth)
#   8. `hub-gke` sets `ENV HOME`                          (positive twin of 7)
#   9. `hub-gke` declares no CMD                          (no baked args/secrets)
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
#   - Its parser is line-based. Exotic-but-legal Dockerfile syntax (a custom
#     `escape` directive, heredocs) is not modelled; the checks would misread it
#     rather than fail loudly.
#
# Run `hack/check-dockerfile-stages.sh --self-test` to see the script fail on
# each mutation it is meant to catch.

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

# ---------------------------------------------------------------------------
# Parse the Dockerfile into a stage table.
#
# Emits, in file order:
#   STAGE <n> <base> <name-or-->
#   INSTR <n> <VERB> <rest of line>
# ---------------------------------------------------------------------------
parse() {
  awk '
    # Join backslash continuations into a single logical line.
    {
      line = $0
      sub(/\r$/, "", line)
      if (cont != "") { line = cont " " line; cont = "" }
      if (line ~ /\\[ \t]*$/) { sub(/\\[ \t]*$/, "", line); cont = line; next }
      logical = line
    }
    logical ~ /^[ \t]*#/ { next }        # comment
    logical ~ /^[ \t]*$/ { next }        # blank
    {
      # verb is the first token, uppercased
      l = logical
      sub(/^[ \t]+/, "", l)
      n = split(l, tok, /[ \t]+/)
      verb = toupper(tok[1])
      if (verb == "FROM") {
        stage++
        base = tok[2]
        name = "-"
        if (n >= 4 && toupper(tok[3]) == "AS") { name = tok[4] }
        printf "STAGE %d %s %s\n", stage, base, name
      } else if (verb == "ARG" && stage == 0) {
        next                              # pre-FROM ARG: not part of any stage
      } else {
        rest = l
        sub(/^[^ \t]+[ \t]*/, "", rest)
        printf "INSTR %d %s %s\n", stage, verb, rest
      }
    }
  ' "$1"
}

# ---------------------------------------------------------------------------
# Self-test. A check nobody has watched fail is not a check: this runs the
# script against the real Dockerfile (must pass) and against one synthetic
# mutation per rule (each must fail).
# ---------------------------------------------------------------------------
self_test() {
  local tmp errors=0
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  local good="FROM alpine AS builder
RUN echo build

FROM debian:trixie-slim AS runtime
COPY --from=builder /x /usr/local/bin/x
EXPOSE 8080
ENTRYPOINT [\"/usr/local/bin/x\"]

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

  # 1. the original trap: hub-gke becomes the last stage
  echo "${good%$'\n\nFROM runtime'}" | expect 1 \
    "hub-gke as the last stage is rejected" "is the LAST stage"

  # 2. a trailing stage that is not empty
  printf '%s\nUSER 1000:1000\n' "$good" | expect 1 \
    "an instruction appended after the trailing FROM is rejected" "contains 1 instruction"
  printf '%s\nENV KUBECONFIG=/home/scion/.kube/config\n' "$good" | expect 1 \
    "an ENV appended after the trailing FROM is rejected" "contains 1 instruction"

  # 3. the trailing stage no longer derives from the runtime stage
  echo "${good%FROM runtime}FROM debian:trixie-slim" | expect 1 \
    "a trailing stage on an external base is rejected" "is not a stage defined in this file"
  echo "${good%FROM runtime}FROM builder" | expect 1 \
    "a trailing stage on the wrong in-file stage is rejected" "They must share a base stage"

  # 4. USER added to a pre-existing stage
  echo "${good/EXPOSE 8080/USER 1000:1000}" | expect 1 \
    "USER added to the runtime stage is rejected" "USER appears outside"

  # 5. KUBECONFIG baked in
  echo "${good/ENV HOME=\/home\/scion/ENV HOME=\/home\/scion
ENV KUBECONFIG=\/home\/scion\/.kube\/config}" | expect 1 \
    "ENV KUBECONFIG in hub-gke is rejected" "ENV KUBECONFIG appears"

  # 6. the stage the chart targets disappears
  echo "${good//hub-gke/hub-k8s}" | expect 1 \
    "renaming the hub-gke stage is rejected" "no stage named"

  # 7. a username instead of a numeric uid
  echo "${good/USER 1000:1000/USER scion}" | expect 1 \
    "a non-numeric USER in hub-gke is rejected" "must be the numeric uid 1000"
  echo "${good/USER 1000:1000/RUN true}" | expect 1 \
    "hub-gke with no USER at all is rejected" "declares no USER"

  # 8. HOME dropped
  echo "${good/ENV HOME=\/home\/scion/RUN true}" | expect 1 \
    "hub-gke with no ENV HOME is rejected" "sets no ENV HOME"

  # 9. a CMD baked into hub-gke
  printf '%s\n' "${good/USER 1000:1000/USER 1000:1000
CMD [\"server\", \"start\", \"--token\", \"hunter2\"]}" | expect 1 \
    "a CMD in hub-gke is rejected" "declares a CMD"

  echo
  if [ "$errors" -eq 0 ]; then
    echo "self-test passed: every mutation above was caught, and the good shape was not."
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
STAGE_COUNT="$(echo "$TABLE" | awk '$1=="STAGE"{n=$2} END{print n+0}')"

if [ "$STAGE_COUNT" -lt 2 ]; then
  fail "expected a multi-stage Dockerfile, found $STAGE_COUNT stage(s) in $DOCKERFILE"
  exit 1
fi

stage_name()  { echo "$TABLE" | awk -v n="$1" '$1=="STAGE" && $2==n {print $4}'; }
stage_base()  { echo "$TABLE" | awk -v n="$1" '$1=="STAGE" && $2==n {print $3}'; }
stage_index() { echo "$TABLE" | awk -v s="$1" '$1=="STAGE" && $4==s {print $2}'; }
stage_instr_count() { echo "$TABLE" | awk -v n="$1" '$1=="INSTR" && $2==n' | wc -l; }
stage_verbs() { echo "$TABLE" | awk -v n="$1" '$1=="INSTR" && $2==n {print $3}'; }
stage_lines_of() { echo "$TABLE" | awk -v n="$1" -v v="$2" '$1=="INSTR" && $2==n && $3==v {$1="";$2="";$3="";sub(/^ +/,"");print}'; }

LAST="$STAGE_COUNT"

# 1. the hub-gke stage exists
GKE_IDX="$(stage_index "$GKE_STAGE")"
if [ -z "$GKE_IDX" ]; then
  fail "no stage named '$GKE_STAGE' in $DOCKERFILE. The GKE chart builds this image with 'docker build --target $GKE_STAGE'; removing or renaming the stage breaks that build."
  echo
  echo "$FAILURES check(s) failed."
  exit 1
fi
ok "stage '$GKE_STAGE' exists (stage $GKE_IDX of $STAGE_COUNT)"

# 2. hub-gke is not the last stage
if [ "$GKE_IDX" = "$LAST" ]; then
  fail "'$GKE_STAGE' is the LAST stage, so 'docker build' with no --target now builds it. Every consumer of the default target (gcloud run deploy --source, gcloud builds submit) would silently receive the non-root uid-1000 image. Keep a trailing empty stage after it."
else
  ok "'$GKE_STAGE' is not the last stage, so it is not the default build target"
fi

# 3. the last stage adds nothing
LAST_INSTRS="$(stage_instr_count "$LAST")"
if [ "$LAST_INSTRS" -ne 0 ]; then
  fail "the last stage (stage $LAST, base '$(stage_base "$LAST")') contains $LAST_INSTRS instruction(s): $(stage_verbs "$LAST" | tr '\n' ' '). It must be empty -- an instruction there changes the image the default build target produces. Add it to the runtime stage or to '$GKE_STAGE' instead."
else
  ok "the last stage (stage $LAST) is empty, so the default target adds nothing to its base"
fi

# 4. the last stage and hub-gke share an in-file base stage
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

# 5. no USER outside hub-gke  (negative)
USER_OUTSIDE=""
i=1
while [ "$i" -le "$STAGE_COUNT" ]; do
  if [ "$i" != "$GKE_IDX" ] && stage_verbs "$i" | grep -qx "USER"; then
    USER_OUTSIDE="$USER_OUTSIDE stage-$i($(stage_name "$i"))"
  fi
  i=$((i + 1))
done
if [ -n "$USER_OUTSIDE" ]; then
  fail "USER appears outside '$GKE_STAGE', in:$USER_OUTSIDE. Changing the uid of a pre-existing stage is a behaviour change for every existing consumer of that image; the non-root user belongs only in '$GKE_STAGE'."
else
  ok "no USER instruction outside '$GKE_STAGE'"
fi

# 6. hub-gke sets a numeric uid 1000  (positive twin of 5)
GKE_USER="$(stage_lines_of "$GKE_IDX" USER | tail -n 1)"
if [ -z "$GKE_USER" ]; then
  fail "'$GKE_STAGE' declares no USER. It exists to run as a non-root uid; without USER it runs as root and 'runAsNonRoot: true' refuses to start the pod."
elif ! echo "$GKE_USER" | grep -Eq '^1000(:[0-9]+)?$'; then
  fail "'$GKE_STAGE' sets USER '$GKE_USER'. It must be the numeric uid 1000 (optionally 1000:<gid>): Kubernetes 'runAsNonRoot: true' cannot resolve a username to a uid and fails the container."
else
  ok "'$GKE_STAGE' sets USER '$GKE_USER' (numeric, as runAsNonRoot requires)"
fi

# 7. no ENV KUBECONFIG anywhere  (negative)
if echo "$TABLE" | awk '$1=="INSTR" && $3=="ENV"' | grep -q 'KUBECONFIG'; then
  fail "an ENV KUBECONFIG appears in $DOCKERFILE. pkg/k8s/client.go prefers an explicit kubeconfig over in-cluster credentials, so baking one in silently disables ServiceAccount auth and the hub cannot schedule agent pods -- a failure that looks like an RBAC problem."
else
  ok "no ENV KUBECONFIG anywhere in the file"
fi

# 8. hub-gke sets HOME  (positive twin of 7)
GKE_HOME="$(stage_lines_of "$GKE_IDX" ENV | grep -E '(^|[[:space:]])HOME=' | tail -n 1)"
if [ -z "$GKE_HOME" ]; then
  fail "'$GKE_STAGE' sets no ENV HOME. \$HOME/.scion is the only lever on where the hub reads and writes settings.yaml -- there is no override env -- so an unset HOME sends it somewhere unwritable."
else
  ok "'$GKE_STAGE' sets ENV HOME ('$GKE_HOME')"
fi

# 9. hub-gke declares no CMD
if stage_verbs "$GKE_IDX" | grep -qx "CMD"; then
  fail "'$GKE_STAGE' declares a CMD. The chart supplies the hub's arguments; baking them into the image risks baking a secret into a published artifact."
else
  ok "'$GKE_STAGE' declares no CMD (the chart supplies the arguments)"
fi

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "Dockerfile stage checks passed: the default build target is stage $LAST, an empty stage over '$LAST_BASE'."
  exit 0
fi
echo "$FAILURES check(s) failed."
exit 1
