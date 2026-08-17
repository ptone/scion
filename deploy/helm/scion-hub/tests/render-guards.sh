#!/usr/bin/env bash
# Render-time guard assertions for deploy/helm/scion-hub, other than the
# reserved-flag list (tests/reserved-flags.sh) and the updateStrategy
# derivation (tests/update-strategy.sh). Those two are separate because they
# were separately verified; between the three, every render-time refusal in
# _helpers.tpl has at least one case.
#
# FAILS CLOSED, same contract as its two siblings: it asserts the number of
# assertions EXECUTED against a committed total and exits 2 on a short run,
# distinct from 1 for a real failure. "The harness did not run" and "the chart
# is broken" need different reactions from whoever sees red. Absence of a
# failure is not evidence of a check.
#
# NO CI WIRING, deliberately. Phase 6 owns that and may relocate this file.
#
# WHY SO MANY CASES ASSERT THE MESSAGE AND NOT JUST THE EXIT CODE: several of
# these guards are layered, and a layered guard silently becomes a single point
# of failure the moment you assert only the outcome. Where two independent
# layers refuse the same value, there is one case per layer, and the lower layer
# is reached with --skip-schema-validation - which is how an operator reaches it
# too.
set -u

EXPECTED_TOTAL=78   # 57 + 14 oauth credential section (Phase 3) + 7 C4 secret-name collision (F1).
CHART="${CHART:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
HELM="${HELM:-helm}"
# auth.sessionSecret became REQUIRED in the session-secret phase, and it is here for the same
# reason hub.baseUrl is: scion-hub.assertSessionSecret fails the render without it, so every
# BASE render would return an error string instead of manifests and every check below would
# accuse the chart of a fault it does not have. The chart will not default it - a generated
# secret rotates on every helm upgrade, invalidating every session and the JWT signing key.
BASE=(--set image.repository=r --set hub.hubId=ci-minimal --set hub.baseUrl=https://ci-minimal.example.invalid --set auth.sessionSecret=harness-not-a-real-secret)   # hub.baseUrl became REQUIRED in Phase 1; see the arm below.

# A COMPLETE WEB CLIENT CREDENTIAL, for the rows that need auth.mode=oauth to
# render at all. Not folded into BASE, because several rows below exist
# specifically to assert that oauth WITHOUT this is refused - putting it in BASE
# would make those rows unwriteable, and worse, would make them look written.
# It replaces --set auth.acknowledgeOAuthUnlanded=true, which is what these rows
# carried while the credentials had no channel.
OAUTH_WEB=(--set auth.oauth.web.google.clientId=rg-web-google-id --set auth.oauth.web.google.clientSecret=rg-web-google-secret)

# TOOL-PRESENCE ARM. A MISSING TOOLCHAIN MUST NOT BE REPORTED AS A BROKEN CHART.
# Without this every helm invocation fails, every assertion fails, and the output
# accuses the chart of dropping templates when the truth is that helm is not
# installed. Found by the first person to run this suite who was not its author,
# in a container without helm, in four minutes. A mutation suite inherits its
# author's environment, so the environment is the one variable it cannot mutate
# from the inside - the same shape as axis (d), answerable only from outside.
# "Nothing was analysed" is a THIRD outcome, distinct from clean and from failing,
# and it exits 2 with the other harness errors rather than 1.
#
# 🔴 AND THE PREFLIGHT DESTROYED THE MEASUREMENT THAT AUDITS THE PREFLIGHT.
# Adding it was correct and it stays. But the way to check whether an assertion
# in this file is reason-matched or fail-open is to run it with no toolchain and
# read which lines go green - and after this arm exists, there are no lines to
# read: the run reports ASSERTIONS_EXECUTED=0 and exits before any of them. The
# two vacuous greens at :110 and :184 were found that way; :212 survived because
# by then the probe that would have shown it could no longer be run.
# IMPROVING A MEASUREMENT CAN DESTROY ITS DIAGNOSTIC VALUE.
#
# So: AUDIT=1 bypasses the arm and lets the assertions run against a broken or
# absent toolchain, which is the only way to see which of them go green anyway.
# IT CAN NEVER CERTIFY ANYTHING - an audit run exits 2 unconditionally at the
# bottom, whatever the assertions say, so it cannot be pasted into a review as a
# pass and cannot be wired into run-all.sh by accident.
AUDIT="${AUDIT:-0}"
_missing=""
for _t in "$HELM" sha256sum; do command -v "$_t" >/dev/null 2>&1 || _missing="${_missing} ${_t}"; done
if [ -n "$_missing" ] && [ "$AUDIT" != "1" ]; then
  echo "HARNESS ERROR: required tool(s) not on PATH:${_missing}"
  echo "NOTHING WAS ANALYSED. This is not a passing run, and it is NOT a chart failure."
  echo "  To see WHICH assertions go green anyway - the fail-open audit - re-run with AUDIT=1."
  echo "  That mode exits 2 no matter what it prints. It is an instrument, not a verdict."
  echo "ASSERTIONS_EXECUTED=0"
  exit 2
fi
if [ "$AUDIT" = "1" ]; then
  echo "=================================================================="
  echo "AUDIT MODE. The tool-presence arm is BYPASSED. Missing:${_missing:- (none)}"
  echo "Every 'ok' below is a SUSPECT, not a result: it is an assertion that"
  echo "passed without the toolchain it claims to exercise. This run exits 2."
  echo "=================================================================="
fi

# BASE-VIABILITY ARM, AND IT IS THE SAME ARGUMENT AS THE TOOL-PRESENCE ARM ABOVE
# ONE STEP IN. A missing toolchain makes every render fail; so does a BASE that
# no longer satisfies the chart's required values, and the output is worse,
# because it is not empty - it is every assertion confidently blaming the guard
# it was aimed at. MEASURED: Phase 1 made hub.baseUrl required, and this suite
# emitted 77 failures reading "refused, but NOT by the reserved-flag guard" and
# "rejected with the WRONG message". Every one of those sentences was false. The
# guards were fine; BASE was.
#
# A REQUIRED VALUE ADDED BY A LATER PHASE INVALIDATES EVERY BASE IN THIS SUITE AT
# ONCE, AND NOTHING ABOUT IT IS SPECIFIC TO hub.baseUrl. Adding the flag fixes
# today; this arm is what makes the NEXT one arrive as one honest line instead of
# seventy-seven misleading ones. Exit 2, not 1: an unrenderable BASE means the run
# is not evidence about the chart, in either direction.
if ! _bv="$("$HELM" template t "$CHART" "${BASE[@]}" 2>&1)" || [ -z "$_bv" ]; then
  echo "HARNESS ERROR: BASE alone does not render, so no assertion below tests what it says it tests."
  echo "  This is almost always a newly-REQUIRED value that BASE does not set."
  printf '%s\n' "$_bv" | sed 's/^/  | /' | head -5
  echo "NOTHING WAS ANALYSED. This is not a passing run, and it is NOT a chart failure."
  echo "ASSERTIONS_EXECUTED=0"
  exit 2
fi
unset _bv

executed=0
failed=0

render() { "$HELM" template t "$CHART" "${BASE[@]}" "$@" 2>&1; }

# reject <label> <substring the message MUST contain> <helm args...>
reject() {
  local label="$1" want="$2"; shift 2
  executed=$((executed + 1))
  local out; out="$(render "$@")"
  if [ $? -eq 0 ]; then
    echo "FAIL  rendered but must reject: ${label}"; failed=$((failed + 1)); return
  fi
  case "$out" in
    *'%!'*)
       # gd-p1-dev's guard, adopted: a printf verb mismatch in _helpers.tpl
       # renders %!s(<nil>) inside a message whose wording still matches, so the
       # substring check below would go green on a diagnostic that shows the
       # operator nothing. Checked first because it is the more specific failure.
       echo "FAIL  ${label}: the refusal message could not render its own value (%!)"
       echo "        got:  $(printf '%s' "$out" | tr '\n' ' ' | cut -c1-200)"
       failed=$((failed + 1)) ;;
    *"$want"*) echo "ok    rejected: ${label}" ;;
    *) echo "FAIL  ${label}: rejected with the WRONG message"
       echo "        want: ${want}"
       echo "        got:  $(printf '%s' "$out" | tr '\n' ' ' | cut -c1-200)"
       failed=$((failed + 1)) ;;
  esac
}

# accept <label> <helm args...>
accept() {
  local label="$1"; shift
  executed=$((executed + 1))
  local out; out="$(render "$@")"
  if [ $? -eq 0 ]; then
    echo "ok    accepted: ${label}"
  else
    echo "FAIL  rejected but must accept: ${label}"
    echo "        $(printf '%s' "$out" | tr '\n' ' ' | cut -c1-200)"
    failed=$((failed + 1))
  fi
}

echo "== credential guard, NAME axis =="
# The round-1 secret check inspected values only, so --admin-token=hunter2 was
# accepted: the value "hunter2" has no credential shape. The name axis is what
# catches it, and this row is the reason the axis exists.
reject "--admin-token=hunter2"    "names credential material" --set hub.args[0]=--admin-token=hunter2
reject "--api-key"                "names credential material" --set hub.args[0]=--api-key=abc
reject "--gh-pat"                 "names credential material" --set hub.args[0]=--gh-pat=abc
reject "--private-key"            "names credential material" --set hub.args[0]=--private-key=abc
reject "--upstream-password"      "names credential material" --set hub.args[0]=--upstream-password=abc
reject "--x-credential"           "names credential material" --set hub.args[0]=--x-credential=abc

echo "== credential guard, VALUE axis =="
# 🛑 THESE FIXTURES CARRY A LENGTH, AND THE LENGTH IS LOAD-BEARING. The matcher
# is under revision to add an entropy/length floor after each prefix, because
# `sk-[A-Za-z0-9]` with no floor refuses ordinary GKE nodepool names like
# `sk-pool` - measured, 27 of 138 legitimate renders. A floor makes any fixture
# SHORTER than the floor render, and this section then goes red.
#
# Measured on real helm v3.16.3: with `sk-AAAAAAAAAAAA` (12 characters of body)
# this row went red under BOTH published candidate matchers, because the
# proposed floor is 16. The bodies below are sized to real credentials - an
# OpenAI key is ~48 characters, a GitHub PAT ~36, an AWS access key ID exactly
# 16 after `AKIA` - so they clear any floor anyone has proposed and they clear
# the current unfloored matcher too. Verified green at both.
#
# 🛑 IF A FLOOR LANDS AND THIS SECTION GOES RED, LENGTHEN THE FIXTURE, DO NOT
# LOWER THE FLOOR. A test fixture that is shorter than a real credential is a
# defect in the fixture; lowering the floor to fit it lets tests/ set a security
# parameter and re-opens the false positive the floor exists to close.
#
# These plant through `--set hub.args[0]=` - the ARGV path - which is why this
# section exists separately from chart-integrity.sh's E12, which plants through
# values files onto object surfaces.
#
# ⚠️ CORRECTION, RECORDED RATHER THAN QUIETLY EDITED. An earlier version of this
# comment said a values-only corpus "cannot reach these rows at all." THAT IS
# FALSE and gd-consumer disproved it by doing it: `hub.args` is an ordinary
# .Values key, so a `-f` overlay reaches this identical code path with no `--set`
# involved, and their overlay reproduced the 12-character blocker independently.
#
# The real distinction is narrower and worth keeping straight, because the wrong
# version makes the gap sound unavoidable when it is one line of work: the
# corpora in flight did not ENUMERATE hub.args as a surface. That is the same
# enumeration blindness this suite keeps finding in the chart, this time in the
# instruments pointed at it - not a property of the values-file method. The
# `--set-string` ban is also narrower than it is sometimes quoted: it bans
# planting CREDENTIAL VALUES through helm's escape parser, which rewrites them,
# not exercising the argv surface, which is what these rows legitimately do.
reject "DSN with userinfo"  "embeds credentials in a URL" --set 'hub.args[0]=--upstream=postgres://scion:hunter2@10.0.0.1/scion'
reject "ghp_ prefix"        "shape of a credential"       --set 'hub.args[0]=--x=ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
reject "sk- prefix"         "shape of a credential"       --set 'hub.args[0]=--x=sk-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
reject "AKIA prefix"        "shape of a credential"       --set 'hub.args[0]=--x=AKIAABCDEFGH12345678'
# A PEM header contains spaces, so the whitespace guard reaches it FIRST. Both
# are rejections; asserting which message fires keeps the ordering honest rather
# than letting the credential axis take credit for a catch it did not make.
reject "PEM header (whitespace guard wins)" "contains whitespace" --set 'hub.args[0]=--x=-----BEGIN RSA PRIVATE KEY-----'
# The PEM alternative is only reachable on argv through a non-flag-shaped entry.
# Do not delete it as dead: the helper is shared, and Phase 1 and Phase 3 call it
# on environment values where a multi-line PEM is legal and this is the catch.
reject "PEM in a positional"                "shape of a credential" --set 'hub.args[0]=x=-----BEGIN RSA PRIVATE KEY-----'

echo "== the failure message must not print what it caught =="
# TWO CONDITIONS, AND THE FIRST ONE IS THE POINT. This was a bare negative -
# "the output does not contain hunter2" - which an EMPTY output satisfies
# perfectly. With no helm the render produced nothing, grep found nothing, and
# this printed "ok credential redacted", certifying a redaction guard that had
# not been consulted. Same family as a reject() satisfied by a missing binary,
# found by applying rev-2's rule to my own file rather than to theirs.
# So: establish that the guard actually fired, THEN assert the absence.
executed=$((executed + 1))
_out="$(render --set 'hub.args[0]=--upstream=postgres://scion:hunter2@10.0.0.1/scion')"
case "$_out" in
  *"embeds credentials in a URL"*)
    case "$_out" in
      *hunter2*) echo "FAIL  the credential guard leaked the password into its own error message"; failed=$((failed + 1)) ;;
      *) echo "ok    credential redacted in the failure message" ;;
    esac ;;
  *) echo "FAIL  credential redaction: the guard did not fire, so redaction was never tested"
     echo "        got: $(printf '%s' "$_out" | tr '\n' ' ' | cut -c1-160)"
     failed=$((failed + 1)) ;;
esac

echo "== underscore reachability =="
# Not input validation. The name pattern needs a hyphen or start-of-string
# before "secret", so SESSION_SECRET passed through unchanged matches NOTHING -
# the guard would render, read as applied, and miss precisely the value it was
# added for. These rows assert it fails loudly instead.
reject "--session_secret" "contains an underscore" --set hub.args[0]=--session_secret=x
reject "--api_key"        "Translate"              --set hub.args[0]=--api_key=x
reject "--pod_namespace"  "contains an underscore" --set hub.args[0]=--pod_namespace=x

echo "== whitespace on argv =="
reject "leading space"  "leading or trailing whitespace" --set 'hub.args[0]= --verbose'
reject "embedded space" "contains whitespace"            --set 'hub.args[0]=--log-level debug'

echo "== POSITIVE TWINS: the false-positive baseline =="
# Without these the suite passes by refusing everything. Each is a near-miss
# chosen to sit just inside a guard's boundary: -token-ttl and -max-tokens
# contain "token", -secret-manager-project contains "secret",
# -password-min-length contains "password", -keycloak-realm is auth-adjacent.
accept "--token-ttl"              --set hub.args[0]=--token-ttl=5m
accept "--max-tokens"             --set hub.args[0]=--max-tokens=100
accept "--secret-manager-project" --set hub.args[0]=--secret-manager-project=p
accept "--keycloak-realm"         --set hub.args[0]=--keycloak-realm=r
accept "--password-min-length"    --set hub.args[0]=--password-min-length=8
accept "--enable-debug"           --set hub.args[0]=--enable-debug
accept "a plain positional"       --set hub.args[0]=extra
accept "no extra args at all"

echo "== hub.hubId =="
reject "hubId empty"      "hubId" --set hub.hubId=""
reject "hubId whitespace" "hubId" --set 'hub.hubId= h '

echo "== two-layer guards: ONE CASE PER LAYER =="
# uid/gid 0 and an empty image.repository are each refused twice, independently.
# The schema fires first; --skip-schema-validation removes that layer and proves
# the helper stands alone. Asserting only the outcome would let either layer be
# deleted with the row still green - which is how a real two-layer guard once got
# "corrected" into a schema-only one here.
reject "runAsUser 0, schema layer"   "greater than or equal to 1" --set hub.securityContext.runAsUser=0
reject "runAsGroup 0, schema layer"  "greater than or equal to 1" --set hub.securityContext.runAsGroup=0
reject "runAsUser 0, helper layer"   "runAsUser may not be 0"     --skip-schema-validation --set hub.securityContext.runAsUser=0
reject "runAsGroup 0, helper layer"  "runAsGroup may not be 0"    --skip-schema-validation --set hub.securityContext.runAsGroup=0
reject "image.repository empty, schema layer" "String length must be greater than or equal to 1" --set image.repository=""
reject "image.repository empty, helper layer" "image.repository is required" --skip-schema-validation --set image.repository=""

echo "== startup budget: A PRODUCT, WHICH NO PER-FIELD SCHEMA BOUND CAN EXPRESS =="
# Every one of these rendered clean with the schema FULLY ACTIVE before the
# assertion existed. There is no --skip-schema-validation row because there is
# nothing in the schema to skip - that absence is the finding, not an omission.
reject "periodSeconds=1 gives 60s"  "the startup budget is too short" --set probes.startup.periodSeconds=1
reject "2 x 60 gives 120s"          "= 120s"                          --set probes.startup.periodSeconds=2
reject "startup off, liveness on"   "holds the liveness probe off"    --set probes.startup.enabled=false --set probes.liveness.enabled=true
accept "default 5 x 60 = 300s"
accept "startup off AND liveness off"  --set probes.startup.enabled=false
# FIXTURE FOR A REMOVED SCHEMA BOUND. failureThreshold's minimum went from 60 to
# 1 because the old bound rejected SAFE configurations (10 x 30 = 300s) while
# admitting unsafe ones (1 x 60 = 60s) - wrong, not merely weak. Both rows below
# set failureThreshold BELOW 60, which the old bound refused outright, and they
# differ only in the period: the pair can pass only if the guard reads the product.
reject "threshold 30, product 150s" "the startup budget is too short" --set probes.startup.failureThreshold=30
accept "threshold 30, product 300s" --set probes.startup.failureThreshold=30 --set probes.startup.periodSeconds=10
reject "sub-60 threshold is the HELPER's refusal, not a shadow of the old bound" \
       "the startup budget is too short" --skip-schema-validation --set probes.startup.failureThreshold=30

echo "== the HA-unlanded gate: THREE ROUTES, TRANSCRIBED FROM THE HUB =="
# scion-hub.assertHAUnlanded refuses the shapes this chart can render but cannot
# start. The routes are not this chart's invention: they are isHADeployment
# (cmd/server_foreground.go:927), and the hub's own tripwire test asserts the
# same three at cmd/server_ha_preflight_test.go:248-256 on ab0d227. THE ROUTES
# ARE TRANSCRIBED, SO THEY CAN DRIFT, which is what these rows are for.
#
# EACH NEGATIVE ASSERTS *WHICH* ROUTE FIRED, NOT MERELY THAT SOMETHING DID. The
# three routes overlap in the value space - postgres implies gcs by schema rule,
# and gcs plus proxy is route 3 - so a row that only checked for a refusal would
# stay green with two of the three routes deleted. Row r2 uses auth.mode oauth
# for exactly that reason: it is the only way to reach the postgres route with
# the gcs-plus-proxy route switched off. My first matrix did not do this and
# three of its rows were confounded by pre-existing schema allOf rules doing the
# refusing instead of this guard.
reject "r1: hub.extraEnv sets K_SERVICE" "hub.extraEnv sets K_SERVICE" \
  --set 'hub.extraEnv[0].name=K_SERVICE' --set 'hub.extraEnv[0].value=svc'
reject "r2: postgres driver, route 3 held off with oauth" "database.driver is postgres" \
  --set database.driver=postgres --set storage.provider=gcs --set storage.bucket=b \
  --set auth.mode=oauth "${OAUTH_WEB[@]}"
reject "r3: gcs storage with proxy auth" "storage.provider is gcs and auth.mode is proxy" \
  --set storage.provider=gcs --set storage.bucket=b

# POSITIVE TWINS, ONE PER ROUTE. The flag is the escape hatch for rendering the
# settings.yaml without installing, so each refusal must be clearable - a guard
# with no way past it is a removed feature, not a warning.
accept "r1 + acknowledgeHAUnlanded" \
  --set 'hub.extraEnv[0].name=K_SERVICE' --set 'hub.extraEnv[0].value=svc' --set acknowledgeHAUnlanded=true
accept "r2 + acknowledgeHAUnlanded" \
  --set database.driver=postgres --set storage.provider=gcs --set storage.bucket=b \
  --set auth.mode=oauth "${OAUTH_WEB[@]}" --set acknowledgeHAUnlanded=true
accept "r3 + acknowledgeHAUnlanded" \
  --set storage.provider=gcs --set storage.bucket=b --set acknowledgeHAUnlanded=true

# THE OVER-TRIGGER DIRECTION, WHICH IS THE HALF A REFUSAL SUITE USUALLY OMITS.
# Demanding an acknowledgement from a deployment that is not HA is a defect of
# the same size as failing to demand one from a deployment that is: it teaches
# operators to set the flag reflexively, and then the flag protects nobody.
# K_SERVICE with an empty value is the near-miss that matters - the hub reads
# os.Getenv != "" (:928), so an empty value is not a route, and a guard written
# on the name alone would refuse it.
accept "K_SERVICE present but with an explicit empty value" \
  --set 'hub.extraEnv[0].name=K_SERVICE' --set 'hub.extraEnv[0].value='
accept "an unrelated extraEnv name"       --set 'hub.extraEnv[0].name=NOT_K_SERVICE' --set 'hub.extraEnv[0].value=svc'
accept "sqlite + local storage + oauth"   --set auth.mode=oauth "${OAUTH_WEB[@]}"
accept "the chart defaults, no route"

# THE GATE LIST IS PART OF THE CONTRACT, SO IT IS ASSERTED RATHER THAN TRUSTED.
# gke-deploy-lead's Critical 1 requires the refusal to name every unlandable
# gate in hub order. The number was reported as five for most of a day because
# a prober stopped at the first gate it could not construct and its extent was
# read as the preflight's extent; the count below is the guard against that
# happening again silently. THE DENOMINATOR IS ASSERTED, not the presence of at
# least one - a message that named three of the seven would satisfy any
# per-substring check written as a loop with no total.
#
# SEVEN, AND IT WAS EIGHT. "durable session/signing secret" was the second entry
# of this corpus until the session-secret phase landed it: the chart now renders
# a session Secret and wires SCION_SERVER_SESSION_SECRET into the container, so
# the refusal correctly stopped naming it and this row correctly went red.
#
# LOWERING A DESIGN-COUPLED COUNT IS ALMOST ALWAYS THE WRONG REPAIR, and it is
# the right one here for exactly one reason: the gate was SATISFIED, not the
# assertion weakened. The distinction is not rhetorical and it is not left to
# this comment either - chart-integrity.sh section E14 measures that the render
# really does supply that variable to the container before it will accept its
# absence from this list. If a future phase lands a gate, strike it here and
# extend E14's pairing in the same diff; if a row goes red and you cannot show
# the render supplying the thing, the refusal is what regressed, not this list.
executed=$((executed + 1))
_ha_out="$(render --set database.driver=postgres --set storage.provider=gcs --set storage.bucket=b 2>&1)"
_ha_want='server.database.url
server.auth.proxy.provider=iap
server.auth.proxy.iap.audience
server.auth.transport
server.auth.transport.mode=iap
server.auth.transport.oidc_audience
server.auth.transport.platform_auth_sa'
_ha_seen=0
while IFS= read -r _w; do
  case "$_ha_out" in *"$_w"*) _ha_seen=$((_ha_seen + 1)) ;; esac
done <<EOF
$_ha_want
EOF
_ha_total="$(printf '%s\n' "$_ha_want" | grep -c .)"
if [ "$_ha_total" -ne 7 ]; then
  # THE PROBE'S OWN CORPUS, ASSERTED. A truncated heredoc would make _ha_seen
  # equal _ha_total on zero gates and print ok. This is the defect the section
  # above exists to describe, reproduced one level up, so it gets a meta-failure.
  echo "HARNESS ERROR: the gate-name list holds ${_ha_total} entries, not 7. NOTHING WAS MEASURED."
  echo "ASSERTIONS_EXECUTED=${executed}"
  exit 2
fi
if [ "$_ha_seen" -eq 7 ]; then
  echo "ok    the HA refusal names all 7 unlandable gates"
else
  echo "FAIL  the HA refusal names ${_ha_seen}/7 unlandable gates"
  echo "        got: $(printf '%s' "$_ha_out" | tr '\n' ' ' | cut -c1-300)"
  failed=$((failed + 1))
fi

echo "== oauth mode requires a complete web client credential =="
# THIS SECTION REPLACES auth.acknowledgeOAuthUnlanded, and the replacement is not
# like-for-like. The acknowledgement asked the operator to CONFIRM the deployment
# would be unusable; these rows assert the chart REFUSES to render it. An
# acknowledgement is satisfiable by anyone in a hurry, and the thing it guarded
# against - a hub that starts, binds, passes /readyz and refuses every login - is
# not a thing an operator should be able to opt into by typing true.
#
# TWO LAYERS, so two rows per case, per this file's convention. The schema layer
# is what an operator hits; the template layer is what they hit with
# --skip-schema-validation, and it is the one that must hold, because config.extra
# reaches the settings document without passing through the schema at all.
#
# THE SCHEMA ROWS DISCRIMINATE, and that is asserted rather than assumed: the
# id-only row demands the message name clientSecret and the secret-only row
# demands clientId. A schema that listed both halves whatever was missing would
# satisfy a looser pair of substrings while telling the operator nothing.
reject "oauth, no credentials, schema layer"  "auth.oauth.web.google.clientId: String length" \
  --set auth.mode=oauth
reject "oauth, clientId only, schema layer"   "auth.oauth.web.google.clientSecret: String length" \
  --set auth.mode=oauth --set auth.oauth.web.google.clientId=rg-id
reject "oauth, clientSecret only, schema layer" "auth.oauth.web.google.clientId: String length" \
  --set auth.mode=oauth --set auth.oauth.web.google.clientSecret=rg-sec

reject "oauth, no credentials, template layer" "no complete OAuth web client credential is present" \
  --skip-schema-validation --set auth.mode=oauth
reject "oauth, clientId only, template layer"  "google (has client_id, missing client_secret)" \
  --skip-schema-validation --set auth.mode=oauth --set auth.oauth.web.google.clientId=rg-id
reject "oauth, clientSecret only, template layer" "google (has client_secret, missing client_id)" \
  --skip-schema-validation --set auth.mode=oauth --set auth.oauth.web.google.clientSecret=rg-sec

# CLI CREDENTIALS DO NOT SUBSTITUTE FOR WEB ONES, and this row is the reason the
# guard walks the web subtree specifically instead of asking "is server.oauth
# non-empty". The hub keys its login check by client type (pkg/hub/oauth.go:194),
# so a complete cli credential renders, validates, looks like configuration in
# the Secret, and satisfies no browser login. A guard written on presence rather
# than on client type passes this input.
reject "complete cli credential does not satisfy oauth mode" "no complete OAuth web client credential is present" \
  --set auth.mode=oauth \
  --set config.extra.server.oauth.cli.google.client_id=rg-cli-id \
  --set config.extra.server.oauth.cli.google.client_secret=rg-cli-secret

# THE SPELLING GUARD, WHICH IS THE ONE THAT CAUGHT ME. settings.yaml binds
# client_id/client_secret (V1OAuthProviderConfig, pkg/config/settings_v1.go:635);
# clientId/clientSecret is the SCION_SERVER_* environment mapper's spelling
# (pkg/config/hub_config.go:334). Measured both directions in
# harness/zz_p3_oauth_settings_probe_test.go: snake_case binds, camelCase leaves
# the field empty with no error from yaml.v3. I wrote the positive case in
# camelCase and expected it to pass.
#
# The first row carries a COMPLETE google credential as well, so the missing- and
# incomplete-credential guards above cannot be what refuses it. Without that, the
# row would go green on the wrong refusal and the spelling guard could be deleted
# without turning anything red.
reject "camelCase via config.extra, oauth mode" "server.oauth.web.github.clientId" \
  --set auth.mode=oauth "${OAUTH_WEB[@]}" \
  --set config.extra.server.oauth.web.github.clientId=rg-camel
# AND IT IS NOT GATED ON auth.mode. A misspelled credential is inert in proxy
# mode too - it just is not load-bearing there yet, which is exactly the state in
# which a wrong spelling gets committed and survives until the mode changes.
reject "camelCase via config.extra, proxy mode" "server.oauth.web.github.clientId" \
  --set auth.mode=proxy \
  --set config.extra.server.oauth.web.github.clientId=rg-camel

accept "oauth with a complete google web credential" --set auth.mode=oauth "${OAUTH_WEB[@]}"
accept "oauth with a complete github web credential" --set auth.mode=oauth \
  --set auth.oauth.web.github.clientId=rg-gh-id --set auth.oauth.web.github.clientSecret=rg-gh-secret
# config.extra IS A FIRST-CLASS WAY TO MEET THE REQUIREMENT, not a bypass of it.
# The guard reads the rendered document, so an operator who supplies
# server.oauth.web themselves has supplied it, and a guard that insisted on
# auth.oauth.web specifically would refuse a correct deployment.
accept "credentials supplied through config.extra in snake_case" --set auth.mode=oauth \
  --set config.extra.server.oauth.web.google.client_id=rg-extra-id \
  --set config.extra.server.oauth.web.google.client_secret=rg-extra-secret
# WITH AN EXTERNAL SETTINGS SECRET THE CHART RENDERS NO SETTINGS DOCUMENT, so
# there is nothing to inspect and nothing to refuse. Asserting this keeps the
# guard from growing into a claim about a file the chart cannot see.
accept "oauth with no credentials but an external settings Secret" \
  --set auth.mode=oauth --set config.existingSecret=operator-owned

# THE POSITIVE TWIN OF THE SPELLING GUARD: the chart's own render must land on
# the side of the guard it enforces. A chart that refused camelCase from
# config.extra while emitting camelCase itself would pass every row above -
# the guard runs on the merged document, so it would refuse its own output, and
# the accept rows would be the ones to fail... unless the guard were ever
# narrowed to config.extra only. This asserts the emitted spelling directly.
executed=$((executed + 1))
_oa="$(render --set auth.mode=oauth "${OAUTH_WEB[@]}")"
if [ -z "$_oa" ] || ! printf '%s\n' "$_oa" | grep -qE '^kind: Secret$'; then
  echo "FAIL  rendered oauth credentials: no Secret in the output, so nothing was inspected"
  failed=$((failed + 1))
elif printf '%s\n' "$_oa" | grep -qE '^ +client(Id|Secret):'; then
  echo "FAIL  rendered oauth credentials: the chart emits camelCase, which binds nothing in settings.yaml"
  failed=$((failed + 1))
elif printf '%s\n' "$_oa" | grep -qE '^ *client_id: rg-web-google-id$' \
  && printf '%s\n' "$_oa" | grep -qE '^ *client_secret: rg-web-google-secret$'; then
  echo "ok    the chart emits client_id/client_secret, the spelling settings.yaml binds"
else
  echo "FAIL  rendered oauth credentials: neither spelling reached the settings document"
  echo "        got: $(printf '%s' "$_oa" | grep -cE '.') lines, no client_id/client_secret"
  failed=$((failed + 1))
fi
unset _oa

echo "== hub identity is stable across upgrade and independent of the release name =="
# hub.hubId must be used verbatim and must never be derived from anything Helm
# regenerates. A chart that interpolated .Release.Name or .Release.Revision would
# pass every case above and still re-scope the hub's storage on upgrade.
# ESTABLISH THAT THERE IS A RENDER BEFORE COMPARING RENDERS. Comparing two
# hashes is a bare negative wearing a positive's clothes: two failed renders
# agree, and this printed "ok render is identical for install and upgrade" on a
# machine where nothing rendered at all. It is the strongest false pass in this
# file, because the assertion it fakes - that hub identity survives an upgrade -
# is the one the whole hubId design exists for.
#
# 🔴 THE FIRST FIX FOR THAT WAS INCOMPLETE AND SHIPPED, AND THE REASON IS WORTH
# MORE THAN THE FIX. It tested `[ -z "$_a" ]`. But the defect was never "the
# output was empty" - it was "nothing rendered, and the check could not tell."
# Emptiness was the symptom in the world it was found in: no helm, so nothing
# on stdout. `render()` ends `2>&1`, so when helm IS present and the render
# fails, the output is not empty - it is the error message, and the two error
# messages are identical. Measured at 8cc8d9b with `hub.baseUrl` added to the
# schema's required list (helm v3.16.3+gcfd0749, /tmp/linux-amd64/helm):
#
#   install rc=1 bytes=127   upgrade rc=1 bytes=127   -z fires? NO   shas equal? YES
#   -> "ok    render is identical for install and upgrade"
#
# Found by gd-p0-rev-2 and gd-p0-rev-3 independently, from different containers,
# byte-identical. THE REMEDY ARRIVED CARRYING THE DEFECT IT WAS WRITTEN TO
# REMOVE, because it was written against the description of the bug rather than
# against its mechanism. So this now gates on what is actually claimed - that
# helm SUCCEEDED and produced the manifest whose identity is under test - and
# keeps `-z` as well, which costs nothing and is not the load-bearing arm.
executed=$((executed + 1))
_a="$(render)"; _rc_a=$?
_b="$(render --is-upgrade)"; _rc_b=$?
if [ "$_rc_a" -ne 0 ] || [ "$_rc_b" -ne 0 ]; then
  echo "FAIL  install/upgrade comparison: helm EXITED NON-ZERO (install=${_rc_a} upgrade=${_rc_b}), so nothing was compared"
  echo "        got: $(printf '%s' "$_a" | tr '\n' ' ' | cut -c1-160)"
  failed=$((failed + 1))
elif [ -z "$_a" ] || [ -z "$_b" ]; then
  echo "FAIL  install/upgrade comparison: one or both renders were EMPTY, so nothing was compared"
  failed=$((failed + 1))
elif ! printf '%s\n' "$_a" | grep -q '^kind: Deployment$' \
  || ! printf '%s\n' "$_b" | grep -q '^kind: Deployment$'; then
  echo "FAIL  install/upgrade comparison: helm exited 0 but the output carries no Deployment, so the compared strings are not the manifest"
  failed=$((failed + 1))
elif [ "$(printf '%s' "$_a" | sha256sum)" = "$(printf '%s' "$_b" | sha256sum)" ]; then
  echo "ok    render is identical for install and upgrade"
else
  echo "FAIL  render differs between install and upgrade"; failed=$((failed + 1))
fi
for rel in t other-release t2; do
  executed=$((executed + 1))
  got="$("$HELM" template "$rel" "$CHART" "${BASE[@]}" | grep -o 'scion.io/hub-id: .*' | sort -u)"
  if [ "$got" = 'scion.io/hub-id: "ci-minimal"' ]; then
    echo "ok    hub-id verbatim under release name '${rel}'"
  else
    echo "FAIL  hub-id under release '${rel}': ${got}"; failed=$((failed + 1))
  fi
done

echo "== C4: TWO RELEASES, ONE NAMESPACE, ONE SET OF SECRET NAMES. PINNED AS PRESENT. =="
# 🛑 EVERY ASSERTION IN THIS SECTION IS A FENCE, NOT A GUARANTEE. THREE OF THEM
# ASSERT THAT A DEFECT IS STILL HERE. When C4 is fixed they go RED, and red is
# then the correct result: delete them in the same diff that fixes it.
#
# THE DEFECT (C4, filed separately from C1, gd-em's ruling): scion-hub.fullname
# maps release R and release R-scion-hub to ONE fullname, because it appends
# "-scion-hub" only when the release name does not already contain it. Every
# object this chart names is named from that helper, so two independent installs
# of this chart into ONE namespace render the same metadata.name for all of them.
#
# WHY IT IS THIS FILE'S BUSINESS AND NOT SOMEBODY ELSE'S. The helper is not this
# phase's code and this phase is not fixing it. What this phase DID is hang three
# NEW objects off it - Secret NAME-session, Secret NAME-settings, ConfigMap
# NAME-env - and two of those carry secret material. A shared ClusterRole name is
# an authorization defect; a shared SECRET name means two installs resolve one
# session signing key and one OAuth client secret, and the loser is whichever
# applied first. That escalation is this delta's doing, so the fence is this
# delta's to build. Found by gd-p3-rev; the fixture is mine.
#
# 🔴 AND IT IS THIS SUITE'S BUSINESS FOR A SECOND REASON, WHICH IS THAT THE SUITE
# COULD NOT SEE IT. Every other check in this file renders exactly one release
# name - `t`, hardcoded in render(). A collision between two release names is not
# expressible in a corpus with one release name in it, at any assertion count, so
# no number of green assertions above this line was ever evidence about C4. That
# is the alphabet error: AN EXHAUSTIVE SEARCH IS ONLY EXHAUSTIVE OVER ITS
# ALPHABET. Worse, `t` is itself half of a colliding pair - release `t` and
# release `t-scion-hub` both render `t-scion-hub-session` - so the suite's own
# fixture sits inside the defect it cannot express.
#
# 🛑 DO NOT TRY TO CLOSE C4 HERE, AND DO NOT REACH FOR THE TRUNCATION GATE. In
# this cell the longest name is 30 bytes against a 63-byte bound, so NOTHING
# TRUNCATES and a truncation-keyed disambiguator never fires. Hashing the
# untruncated identity fails too, because both releases produce the SAME
# untruncated identity. The only input that separates these two installs is
# .Release.Name, which is precisely what scion-hub.fullname discards.
_F1_NS=f1-one-namespace
# metadata.name of one template, for one release, in ONE namespace.
# NO 2>/dev/null ANYWHERE: a render that fails must put its reason on the
# terminal. An extractor that silently returns empty is the whole hazard here,
# because TWO EMPTY STRINGS ARE EQUAL and this section's headline assertions are
# equality assertions - they would go green on a broken instrument reporting
# nothing, and green is the answer they are looking for.
_f1_name() {  # <release> <template basename>
  "$HELM" template "$1" "$CHART" "${BASE[@]}" --namespace "$_F1_NS" \
    --show-only "templates/$2" | sed -n 's/^  name: //p' | head -1
}
_f1_label() {  # <release>
  "$HELM" template "$1" "$CHART" "${BASE[@]}" --namespace "$_F1_NS" \
    --show-only templates/secret-session.yaml \
    | sed -n 's/^    app.kubernetes.io\/instance: //p' | head -1
}

_f1_a_sess="$(_f1_name prod            secret-session.yaml)"
_f1_b_sess="$(_f1_name prod-scion-hub  secret-session.yaml)"
_f1_a_set="$(_f1_name  prod            secret-settings.yaml)"
_f1_b_set="$(_f1_name  prod-scion-hub  secret-settings.yaml)"
_f1_a_env="$(_f1_name  prod            configmap-env.yaml)"
_f1_b_env="$(_f1_name  prod-scion-hub  configmap-env.yaml)"
_f1_c_sess="$(_f1_name prod-other      secret-session.yaml)"

# 1. CAPABILITY, AND IT RUNS BEFORE ANY COMPARISON. Six non-empty extractions.
#    This is the arm that makes the three equality assertions below mean
#    something: without it, a chart that stopped rendering secrets altogether
#    would satisfy every one of them.
executed=$((executed + 1))
_f1_empty=""
for _f1_v in "$_f1_a_sess" "$_f1_b_sess" "$_f1_a_set" "$_f1_b_set" "$_f1_a_env" "$_f1_b_env" "$_f1_c_sess"; do
  [ -n "$_f1_v" ] || _f1_empty="${_f1_empty}x"
done
if [ -z "$_f1_empty" ]; then
  echo "ok    C4 capability: all 7 name extractions are non-empty, so the equality arms below compare rendered names and not two absences"
else
  echo "FAIL  C4 capability: ${#_f1_empty} of 7 name extractions came back EMPTY, so nothing below this line is a measurement. Two empty strings are equal and the arms that follow assert equality - they would have gone green. Read the helm errors above."
  failed=$((failed + 1))
fi

# 2. NEGATIVE CONTROL, AND IT IS PLACED BEFORE THE FENCES DELIBERATELY. An
#    unrelated release must render a DIFFERENT session Secret name. If this is
#    red, the extractor cannot say "different" and the fences below are vacuous
#    whatever they print.
executed=$((executed + 1))
if [ -n "$_f1_a_sess" ] && [ -n "$_f1_c_sess" ] && [ "$_f1_a_sess" != "$_f1_c_sess" ]; then
  echo "ok    C4 negative control: release 'prod-other' renders a DIFFERENT session Secret name (${_f1_c_sess}), so the instrument can distinguish two releases"
else
  echo "FAIL  C4 negative control: release 'prod' and release 'prod-other' rendered the SAME session Secret name ('${_f1_a_sess}' vs '${_f1_c_sess}'), or one was empty. Either the collision is far wider than C4 or the extractor is broken. Both fences below are vacuous until this is green."
  failed=$((failed + 1))
fi

# 3-5. THE FENCES. Each asserts the collision is STILL PRESENT.
executed=$((executed + 1))
if [ -n "$_f1_a_sess" ] && [ "$_f1_a_sess" = "$_f1_b_sess" ]; then
  echo "ok    C4 PRESENT (fence): releases 'prod' and 'prod-scion-hub' both render session Secret '${_f1_a_sess}' in one namespace - ONE SESSION SIGNING KEY FOR TWO INSTALLS"
else
  echo "FAIL  C4 fence: session Secret names now DIFFER ('${_f1_a_sess}' vs '${_f1_b_sess}'), or an extraction was EMPTY - check that first, an empty pair is an instrument fault and not a fix. If C4 was fixed deliberately, THIS IS THE EXPECTED RED - delete this whole section in the same diff. If you did not fix it, scion-hub.fullname changed under you."
  failed=$((failed + 1))
fi

executed=$((executed + 1))
if [ -n "$_f1_a_set" ] && [ "$_f1_a_set" = "$_f1_b_set" ]; then
  echo "ok    C4 PRESENT (fence): both releases render settings Secret '${_f1_a_set}' - ONE OAUTH CLIENT SECRET FOR TWO INSTALLS"
else
  echo "FAIL  C4 fence: settings Secret names now DIFFER ('${_f1_a_set}' vs '${_f1_b_set}'), or an extraction was EMPTY. Expected red if C4 was fixed; delete this section in that diff."
  failed=$((failed + 1))
fi

executed=$((executed + 1))
if [ -n "$_f1_a_env" ] && [ "$_f1_a_env" = "$_f1_b_env" ]; then
  echo "ok    C4 PRESENT (fence): both releases render env ConfigMap '${_f1_a_env}'"
else
  echo "FAIL  C4 fence: env ConfigMap names now DIFFER ('${_f1_a_env}' vs '${_f1_b_env}'), or an extraction was EMPTY. Expected red if C4 was fixed; delete this section in that diff."
  failed=$((failed + 1))
fi

# 6. THE INSTANCE LABEL DIFFERS, AND THIS ARM EXISTS TO STOP A CORRECT FACT BEING
#    READ AS A REMEDY. It is also a correction, banked in code rather than left
#    in a message: the two renders are NOT byte-identical, as was reported on the
#    thread. They differ on this label at 11 sites and on two Deployment checksum
#    annotations downstream of it. What is identical is every metadata.name, and
#    that is the whole defect, because an apply keys on kind plus namespace plus
#    NAME. A label that discriminates on an object the apply is already
#    overwriting protects nothing.
executed=$((executed + 1))
_f1_la="$(_f1_label prod)"; _f1_lb="$(_f1_label prod-scion-hub)"
if [ -n "$_f1_la" ] && [ -n "$_f1_lb" ] && [ "$_f1_la" != "$_f1_lb" ]; then
  echo "ok    C4: app.kubernetes.io/instance DOES differ across the colliding pair ('${_f1_la}' vs '${_f1_lb}') - the renders are NOT byte-identical, only their names are, and the label is NOT a mitigation"
else
  echo "FAIL  C4: the instance label did not differ across the colliding pair ('${_f1_la}' vs '${_f1_lb}'), or one was empty. If it is genuinely identical now, the two installs have become indistinguishable on every field and C4 is worse than filed."
  failed=$((failed + 1))
fi

# 7. THE SUITE'S OWN FIXTURE IS INSIDE THE DEFECT, AND THAT IS ASSERTED HERE
#    RATHER THAN ONLY DESCRIBED AT THE TOP OF THIS SECTION. Every other check in
#    this file renders release `t`, and release `t` collides with release
#    `t-scion-hub` by exactly the mechanism above. A fact stated in a comment and
#    checked by nothing is the decay this suite exists to prevent - the SCRIPTS
#    annotations in run-all.sh sat stale at 57 and 38 for the same reason.
#
#    🛑 THIS ARM IS NOT A REQUEST TO CHANGE THE FIXTURE NAME. Renaming `t` would
#    make this one arm green and would not move C4 an inch; it would only stop
#    the suite admitting where it stands. The arm is here so that the admission
#    is load-bearing.
executed=$((executed + 1))
_f1_t="$(_f1_name t secret-session.yaml)"
_f1_t2="$(_f1_name t-scion-hub secret-session.yaml)"
if [ -n "$_f1_t" ] && [ "$_f1_t" = "$_f1_t2" ]; then
  echo "ok    C4 PRESENT (fence): this suite's own fixture release 't' shares session Secret '${_f1_t}' with release 't-scion-hub' - the corpus every check above runs in is itself half a colliding pair"
else
  echo "FAIL  C4 fence: release 't' and release 't-scion-hub' no longer share a session Secret name ('${_f1_t}' vs '${_f1_t2}'), or the extraction was empty. Expected red if C4 was fixed; delete this section in that diff."
  failed=$((failed + 1))
fi

echo "---"
echo "executed=${executed} expected=${EXPECTED_TOTAL} failed=${failed}"
# Emitted unconditionally, on every exit path, so run-all.sh can sum what
# actually ran even when this script is reporting a failure. The count check must
# not be silenced by the outcome it is meant to qualify.
echo "ASSERTIONS_EXECUTED=${executed}"

# THE AUDIT EXIT, PLACED BEFORE EVERY OTHER VERDICT SO NOTHING CAN OVERTAKE IT.
# An audit run is an observation of a deliberately broken world. It has no
# passing form, so it does not get one.
if [ "$AUDIT" = "1" ]; then
  echo "AUDIT MODE: ${executed} assertions ran, ${failed} failed, so $((executed - failed)) went GREEN WITHOUT A WORKING TOOLCHAIN."
  echo "Each of those is a candidate fail-open assertion. This is not a pass."
  exit 2
fi

if [ "$executed" -ne "$EXPECTED_TOTAL" ]; then
  # INEQUALITY, NOT A FLOOR. A short run is a failed run; a LONG run means
  # assertions were added without committing the number, which is the same
  # defect facing the other way. Where a check counts anything, the number is
  # committed and both directions fail.
  echo "HARNESS ERROR: executed ${executed} assertions, expected exactly ${EXPECTED_TOTAL}."
  exit 2
fi
[ "$failed" -eq 0 ] || exit 1
echo "PASS ${executed}/${EXPECTED_TOTAL}"
