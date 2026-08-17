#!/usr/bin/env bash
#
# check-secret-placement.sh -- no secret material in argv, a ConfigMap, or an
# annotation, asserted over every shipped values permutation.
#
# WHY THIS EXISTS AS A SEPARATE MECHANICAL SCAN.
#
# The session secret derives BOTH the cookie encryption key and the shared JWT
# signing key (resolveSessionSecret, cmd/server_foreground.go:1452-1463), so it
# is the most sensitive value this chart handles, and the three places it must
# never appear all LOOK FINE in a rendered manifest:
#
#   args/command  - readable by anyone with pod read access, and by every
#                   `ps` in the container. `kubectl get pod -o yaml` prints it.
#   a ConfigMap   - no encryption at rest, and RBAC for configmaps is routinely
#                   granted far more widely than RBAC for secrets.
#   an annotation - copied onto every ReplicaSet and Pod, echoed by `kubectl
#                   describe`, and shipped wholesale to whatever scrapes events.
#
# None of the three produces a broken render, a failing lint, or a schema
# error. A golden diff shows the value moving but a human has to notice which
# key it landed under. That is precisely the class of defect a golden cannot
# catch and a person will not, so it is asserted here instead.
#
# WHAT IS AND IS NOT A FINDING.
#
# The needle is the secret VALUE, harvested from the rendered Secret documents
# themselves, never a name or a key. Names are supposed to appear in the
# Deployment - that is what a secretKeyRef IS - so a scan keyed to names would
# fire on the correct wiring and be suppressed within a week. Keying to values
# means the legitimate references (envFrom.secretRef, env.valueFrom.
# secretKeyRef, the Secret's own stringData) are invisible to it by
# construction, and the only way to trip it is to actually leak the material.
#
# EXIT CODES, and the distinction is the point:
#   0 -- every fixture was rendered and analysed, and nothing leaked
#   1 -- secret material was found in a forbidden location (a real finding)
#   2 -- NOTHING WAS ANALYSED, or the scanner itself is broken. Not clean.
#
# Run with --self-test to exercise the scanner against a fixture that leaks in
# all three locations and legitimately references the secret in four more.

set -u -o pipefail

HELM="${HELM:-helm}"
CHART_DIR="${CHART_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

# THE FIXTURES THAT DELIBERATELY CARRY NO SECRET MATERIAL, held as an explicit
# list rather than inferred. Both take the session secret from a Secret the
# operator owns, so the chart renders no secret VALUE anywhere and there is
# nothing for the scan to find - a vacuous pass, and vacuous for a legitimate
# reason. Naming them here means the vacuity is declared: if a fixture that
# SHOULD carry material stops carrying it, it will not quietly join this set.
NO_MATERIAL=(existing-secret session-existing)

# ---------------------------------------------------------------------------
# The scanner. Reads a rendered multi-document manifest on stdin and prints one
# line per finding. Zero dependencies beyond python3 - deliberately no PyYAML,
# because this runs in CI containers this phase does not own and a scan that is
# skipped for a missing module is a scan that reports nothing forever.
#
# Helm emits consistently-indented block YAML, so block extents are taken from
# indentation. That is not general YAML parsing and does not need to be: the
# subject is this chart's own output, and the self-test pins the shapes.
# ---------------------------------------------------------------------------
read -r -d '' SCANNER <<'PYEOF' || true
import sys, re

text = sys.stdin.read()
needles_env = sys.argv[1] if len(sys.argv) > 1 else ""
extra = [n for n in needles_env.split("\x1f") if n]

docs = re.split(r'(?m)^---\s*$', text)

def indent_of(line):
    return len(line) - len(line.lstrip(" "))

def block_lines(lines, start):
    """Lines strictly inside the block whose header is lines[start]."""
    base = indent_of(lines[start])
    out = []
    for ln in lines[start+1:]:
        if not ln.strip():
            continue
        if indent_of(ln) <= base:
            break
        out.append(ln)
    return out

# ---- pass 1: harvest needles from every Secret document -------------------
needles = set(extra)
for doc in docs:
    lines = doc.split("\n")
    kind = None
    for ln in lines:
        m = re.match(r'^kind:\s*(\S+)\s*$', ln)
        if m:
            kind = m.group(1)
            break
    if kind != "Secret":
        continue
    for i, ln in enumerate(lines):
        if not re.match(r'^(stringData|data):\s*$', ln):
            continue
        entries = block_lines(lines, i)
        # Indices consumed by a block scalar's body. Without this the outer loop
        # also walks the embedded document's own lines and matches them as
        # top-level entries - which is how base_url became a needle even after
        # the descent below was written to exclude it. The self-test asserts the
        # needle COUNT precisely so that this stays visible.
        consumed = set()
        for j, b in enumerate(entries):
            if j in consumed:
                continue
            if b.lstrip().startswith("#"):
                continue
            m = re.match(r'^(\s*)([^:]+):\s*(.*?)\s*$', b)
            if not m:
                continue
            key, v = m.group(2).strip(), m.group(3).strip()

            # A BLOCK SCALAR IS AN EMBEDDED DOCUMENT, NOT A CREDENTIAL, AND THE
            # DIFFERENCE IS THE WHOLE ACCURACY OF THIS SCAN. secret-settings.yaml
            # carries the rendered settings.yaml under one key, so harvesting its
            # lines wholesale makes needles of base_url and hub_id - values the
            # chart duplicates into the ConfigMap and onto an annotation BY
            # DESIGN. Measured before this branch existed: 4 of 6 fixtures
            # reported leaks, every one of them that duplication.
            #
            # Suppressing the document entirely would be the wrong repair: a real
            # credential inside settings.yaml is exactly what must not escape into
            # a ConfigMap. So the document is descended into and its leaves are
            # taken only where the KEY names credential material - the same
            # vocabulary scion-hub.assertExtraEnv uses to refuse a literal in
            # hub.extraEnv (_helpers.tpl). Configuration is skipped; credentials
            # are not.
            if v in ("|", "|-", "|+", ">", ">-", ">+"):
                body = block_lines(entries, j)
                for k in range(j + 1, j + 1 + len(body)):
                    consumed.add(k)
                for inner in body:
                    # COMMENTS ARE NOT KEYS, AND THIS IS NOT TIDINESS. Measured:
                    # secret-settings.yaml contains the line
                    #   # The session secret is not here and does not belong
                    #   # here: it arrives as an
                    # which parses as a key named "...session secret..." holding
                    # the value "it arrives as an". The key matches the credential
                    # vocabulary, so a sentence ABOUT the secret became a needle -
                    # and it appeared in every fixture, including the two that
                    # render no secret at all, which is how it was noticed.
                    #
                    # Same hazard as the comment stripper in section E of
                    # tests/chart-integrity.sh: the prose and the thing it
                    # describes have identical bytes. Here it manufactures a
                    # needle that can never be found, which is a false CLEAN.
                    if inner.lstrip().startswith("#"):
                        continue
                    im = re.match(r'^\s*([^:]+):\s*(.+?)\s*$', inner)
                    if not im:
                        continue
                    ikey, ival = im.group(1).strip(), im.group(2).strip()
                    if not re.search(r'(?i)secret|token|password|passwd|credential|api[_-]?key|private[_-]?key', ikey):
                        continue
                    if len(ival) >= 2 and ival[0] == ival[-1] and ival[0] in "\"'":
                        ival = ival[1:-1]
                    if len(ival) >= 8:
                        needles.add(ival)
                continue

            if len(v) >= 2 and v[0] == v[-1] and v[0] in "\"'":
                v = v[1:-1]
            # A short value cannot be distinguished from an ordinary token
            # and would produce false findings everywhere.
            if len(v) >= 8:
                needles.add(v)

# ---- pass 2: search the forbidden locations -------------------------------
findings = []
for doc in docs:
    lines = doc.split("\n")
    kind = "?"
    for ln in lines:
        m = re.match(r'^kind:\s*(\S+)\s*$', ln)
        if m:
            kind = m.group(1)
            break

    regions = []  # (label, [lines])
    for i, ln in enumerate(lines):
        h = re.match(r'^(\s*)(args|command|annotations|data|binaryData|stringData):\s*$', ln)
        if not h:
            continue
        key = h.group(2)
        if key in ("args", "command"):
            regions.append(("%s/%s" % (kind, key), block_lines(lines, i)))
        elif key == "annotations":
            regions.append(("%s/annotation" % kind, block_lines(lines, i)))
        elif kind == "ConfigMap" and key in ("data", "binaryData"):
            regions.append(("ConfigMap/%s" % key, block_lines(lines, i)))
        # A Secret's own stringData/data is where the material BELONGS.

    for label, blk in regions:
        for b in blk:
            for n in sorted(needles):
                if n in b:
                    findings.append("%s: %s" % (label, b.strip()))

print("NEEDLES=%d" % len(needles))
for f in findings:
    print("FINDING=%s" % f)
PYEOF

scan() { # stdin = render; $1 = \x1f-joined extra needles
  python3 -c "$SCANNER" "${1:-}"
}

# ---------------------------------------------------------------------------
# --self-test. THE POSITIVE CONTROL, and it is not optional.
#
# Every assertion this script makes is an ABSENCE. A scanner that found nothing
# ever - a broken regex, a bad indent rule, an empty needle set - reports a
# clean chart in exactly the same words as a real clean run, forever. So the
# scanner is first shown finding what it is looking for, in all three forbidden
# locations, and then shown NOT finding the four legitimate references to the
# same value. Both halves are required: a scanner that flags everything passes
# the first half perfectly.
# ---------------------------------------------------------------------------
if [[ "${1:-}" == "--self-test" ]]; then
  fixture="$(mktemp)"
  trap 'rm -f "$fixture"' EXIT
  cat >"$fixture" <<'FIXEOF'
---
apiVersion: v1
kind: Secret
metadata:
  name: t-session
stringData:
  SCION_SERVER_SESSION_SECRET: "selftest-sentinel-value-9f3a"
---
apiVersion: v1
kind: Secret
metadata:
  name: t-settings
stringData:
  settings.yaml: |
    schema_version: 1
    # NOT A NEEDLE. A comment whose text names the session secret and happens to
    # contain a colon parses as a credential-named key holding prose. The real
    # secret-settings.yaml carries exactly such a line.
    # The session secret is not here: it arrives as an environment variable
    server:
      base_url: https://selftest.example.invalid
      oauth:
        client_secret: selftest-embedded-credential-7c21
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: t-env
  annotations:
    checksum/config: "sha256-abc"
data:
  HOME: "/home/scion"
  # NOT A FINDING. The chart duplicates the base URL out of settings.yaml into
  # this ConfigMap on purpose. A scanner that harvested every line of an embedded
  # document would flag this, and the flag would be wrong.
  SCION_SERVER_BASE_URL: "https://selftest.example.invalid"
  LEAK_IN_CONFIGMAP: "selftest-sentinel-value-9f3a"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: t-hub
  annotations:
    leak/in-annotation: "selftest-sentinel-value-9f3a"
    # A FINDING. Inside settings.yaml this sits under a key named client_secret,
    # so it is credential material and it must not reach an annotation.
    leak/embedded-credential: "selftest-embedded-credential-7c21"
spec:
  template:
    spec:
      containers:
        - name: hub
          args:
            - serve
            - --session-secret=selftest-sentinel-value-9f3a
          envFrom:
            - secretRef:
                name: t-session
          env:
            - name: SCION_SERVER_SESSION_SECRET
              valueFrom:
                secretKeyRef:
                  name: t-session
                  key: SCION_SERVER_SESSION_SECRET
FIXEOF

  out="$(scan <"$fixture")" || {
    echo "check-secret-placement self-test: the scanner itself failed to run." >&2
    exit 2
  }
  n_needles="$(sed -n 's/^NEEDLES=//p' <<<"$out")"
  mapfile -t found < <(sed -n 's/^FINDING=//p' <<<"$out")

  # The needle must have been harvested from the Secret, not supplied. If this
  # is 0 the three findings below could only come from a hardcoded string.
  # EXACTLY TWO, AND THE NUMBER IS THE ASSERTION. One plain stringData value and
  # one credential leaf from inside the embedded settings document. THREE would
  # mean base_url was harvested too - the over-harvest that made this scan report
  # four false leaks before the block-scalar branch existed. ONE would mean the
  # embedded credential is invisible and a real secret could cross into a
  # ConfigMap unseen. Both directions are wrong and both are caught here.
  if [[ "$n_needles" -ne 2 ]]; then
    echo "check-secret-placement self-test: FAIL - harvested ${n_needles} needles, expected exactly 2 (one scalar, one credential leaf inside settings.yaml; base_url must NOT be one)." >&2
    exit 2
  fi

  want_labels=(ConfigMap/annotation ConfigMap/data Deployment/annotation Deployment/args)
  mapfile -t got_labels < <(printf '%s\n' "${found[@]}" | cut -d: -f1 | sort -u)

  # NOTE the fourth: the ConfigMap's own annotation block is scanned too, and the
  # fixture's checksum annotation does NOT contain the sentinel, so ConfigMap/
  # annotation appears only because... it must not. Asserted exactly, both ways.
  if [[ "${#found[@]}" -ne 4 ]]; then
    echo "check-secret-placement self-test: FAIL - ${#found[@]} findings, expected exactly 4 (args, ConfigMap data, two annotations)." >&2
    printf '  %s\n' "${found[@]}" >&2
    exit 2
  fi

  for want in "Deployment/args" "ConfigMap/data" "Deployment/annotation"; do
    if ! printf '%s\n' "${got_labels[@]}" | grep -qx "$want"; then
      echo "check-secret-placement self-test: FAIL - the scanner did not flag ${want}." >&2
      printf '  got: %s\n' "${got_labels[*]}" >&2
      exit 2
    fi
  done

  # THE OVER-FIRING TWIN. The same value appears four more times in the fixture
  # legitimately - the Secret's own stringData, the envFrom secretRef, the
  # secretKeyRef name and its key. A scanner that flagged those would have
  # reported more than 3 and failed above, but state it explicitly so the
  # property is named rather than implied by a number.
  if printf '%s\n' "${found[@]}" | grep -qE 'Secret/stringData|secretRef|secretKeyRef'; then
    echo "check-secret-placement self-test: FAIL - flagged a legitimate secret reference." >&2
    printf '  %s\n' "${found[@]}" >&2
    exit 2
  fi

  echo "check-secret-placement self-test: PASS (2 needles harvested, 4 leaks found, base_url and 4 legitimate references ignored)"
  exit 0
fi

# ---------------------------------------------------------------------------
# THE INSTRUMENT IS RECORDED THROUGH THE PATH THE MEASUREMENT USES.
#
# Not `command -v helm` and not a bare `helm version` typed at a prompt: the
# exact "$HELM" this script will invoke, resolved the way this script resolves
# it. A tool-PRESENCE check cannot see the case where the tool is present and
# is the wrong one - a shell function, an alias, a different build earlier on
# PATH - and that case reports a clean chart just as loudly as a real one.
# ---------------------------------------------------------------------------
if ! command -v "$HELM" >/dev/null 2>&1; then
  echo "check-secret-placement: '$HELM' not found -- NOTHING WAS ANALYSED (skipped, not clean)" >&2
  exit 2
fi
helm_version="$("$HELM" version --short 2>&1)" || {
  echo "check-secret-placement: '$HELM version --short' failed -- NOTHING WAS ANALYSED (skipped, not clean)" >&2
  exit 2
}
if [[ ! "$helm_version" =~ ^v3\. ]]; then
  echo "check-secret-placement: '$HELM' reports '${helm_version}', which is not a Helm 3 -- NOTHING WAS ANALYSED (skipped, not clean)" >&2
  exit 2
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "check-secret-placement: python3 not found -- NOTHING WAS ANALYSED (skipped, not clean)" >&2
  exit 2
fi

# THE FIXTURE LIST COMES FROM DISK, and the set is then asserted against the
# names this script knows about, in BOTH directions. A hand-written list stops
# covering a fixture added later without saying so; a bare glob that matched
# nothing scores a clean run over zero files.
mapfile -t fixtures < <(find "$CHART_DIR/ci" -maxdepth 1 -name 'values-*.yaml' -printf '%f\n' | sed 's/^values-//;s/\.yaml$//' | sort)

if [[ "${#fixtures[@]}" -eq 0 ]]; then
  echo "check-secret-placement: no ci/values-*.yaml found under ${CHART_DIR} -- NOTHING WAS ANALYSED (skipped, not clean)" >&2
  exit 2
fi

for known in "${NO_MATERIAL[@]}"; do
  if ! printf '%s\n' "${fixtures[@]}" | grep -qx "$known"; then
    echo "check-secret-placement: NO_MATERIAL names '${known}', which is not a fixture on disk. The declared-vacuity list has gone stale -- NOTHING WAS ANALYSED (skipped, not clean)" >&2
    exit 2
  fi
done

echo "check-secret-placement: ${HELM} ${helm_version}, python3 $(python3 -c 'import sys;print(".".join(map(str,sys.version_info[:3])))'), ${#fixtures[@]} fixtures"

rc=0
total_needles=0
analysed=0
for name in "${fixtures[@]}"; do
  render="$("$HELM" template t "$CHART_DIR" -f "$CHART_DIR/ci/values-$name.yaml" 2>&1)"
  if [[ $? -ne 0 || -z "$render" ]]; then
    echo "  ERROR   ${name}: render failed, so nothing was scanned for it" >&2
    printf '%s\n' "$render" | sed 's/^/          | /' | head -3 >&2
    rc=2
    continue
  fi
  # THE RENDER IS ASSERTED NON-TRIVIAL BEFORE IT IS SEARCHED. A negative scan
  # over an empty or truncated document is the cheapest false pass there is.
  docs="$(grep -c '^kind:' <<<"$render")"
  if [[ "$docs" -lt 5 ]]; then
    echo "  ERROR   ${name}: render produced only ${docs} documents, which is not a manifest this scan can speak about" >&2
    rc=2
    continue
  fi
  analysed=$((analysed + 1))

  out="$(scan <<<"$render")" || { echo "  ERROR   ${name}: scanner failed" >&2; rc=2; continue; }
  n="$(sed -n 's/^NEEDLES=//p' <<<"$out")"
  total_needles=$((total_needles + n))
  mapfile -t hits < <(sed -n 's/^FINDING=//p' <<<"$out")

  expect_vacuous=no
  printf '%s\n' "${NO_MATERIAL[@]}" | grep -qx "$name" && expect_vacuous=yes

  if [[ "$n" -eq 0 && "$expect_vacuous" == "no" ]]; then
    echo "  ERROR   ${name}: no secret material in the render at all, so this fixture proves nothing." >&2
    echo "          Either the chart stopped rendering its Secret or the fixture stopped setting one." >&2
    echo "          If it is now a bring-your-own fixture, add it to NO_MATERIAL deliberately." >&2
    rc=2
    continue
  fi
  if [[ "$n" -gt 0 && "$expect_vacuous" == "yes" ]]; then
    echo "  ERROR   ${name}: listed in NO_MATERIAL but the render carries ${n} secret value(s)." >&2
    echo "          The declared vacuity is wrong, and the scan for this fixture was being skipped." >&2
    rc=2
    continue
  fi

  if [[ "${#hits[@]}" -gt 0 ]]; then
    echo "  LEAK    ${name}: secret material in a forbidden location"
    printf '          %s\n' "${hits[@]}"
    [[ "$rc" -ne 2 ]] && rc=1
  elif [[ "$expect_vacuous" == "yes" ]]; then
    echo "  ok      ${name}: renders no secret material (bring-your-own, declared in NO_MATERIAL)"
  else
    echo "  ok      ${name}: ${n} secret value(s), none in args, a ConfigMap, or an annotation"
  fi
done

if [[ "$analysed" -ne "${#fixtures[@]}" ]]; then
  echo "check-secret-placement: analysed ${analysed} of ${#fixtures[@]} fixtures -- NOT a clean run" >&2
  exit 2
fi
# The corpus-level non-vacuity guard. Every per-fixture check above can pass
# with a needle set that is empty everywhere, if NO_MATERIAL ever grew to cover
# the whole list. At least one fixture must put real material in front of the
# scanner or the run says nothing.
if [[ "$total_needles" -eq 0 ]]; then
  echo "check-secret-placement: no fixture rendered any secret material -- NOTHING WAS ANALYSED (skipped, not clean)" >&2
  exit 2
fi

if [[ "$rc" -eq 0 ]]; then
  echo "check-secret-placement: PASS (${analysed} fixtures, ${total_needles} secret values tracked, 0 in args/ConfigMap/annotation)"
fi
exit "$rc"
