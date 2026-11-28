// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GoogleCloudPlatform/scion/pkg/celengine"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/trigger"
	"github.com/GoogleCloudPlatform/scion/pkg/ruleengine"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/triggerrule"
)

var (
	errTriggerRuleEngineNotInitialized = errors.New("trigger rule engine is not initialized")
	ruleDialectNameRE                  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

const (
	maxTriggerMatchFormBytes                   = 16 * 1024
	triggerDialectSystemOne                    = "system_one"
	triggerReasonAgentNotAllowed               = "agent_not_allowed"
	triggerCategoryAgentSystemOnePrefixChanged = "agent_system_one_prefix_changed"
	triggerCategorySystemOneAuthority          = "system_one_authority"
	triggerMsgSystemOneReattributionRequires   = "re-attributing this trigger requires trigger.create (System One elements present)"
)

type triggerMatchRuleKey struct {
	dialect string
	expr    string
}

type triggerMatchValidationOpts struct {
	defaultTypes          bool
	explicitScalarDialect bool
	// allowStoredAdmissionSkip is true on PATCH only (never create or resume;
	// mixed-rule-arrays-design.md §11.3, ptone/scion#9916). When true, elements
	// matching storedRules under the caller's identity rule carry through
	// without re-admission (still running shape, size, and control-char checks).
	allowStoredAdmissionSkip bool
	storedRules              []triggerrule.Rule
}

// validateTriggerMatchForm validates and compacts an opaque Match.Form JSON
// object (Stage 3 §9.2). Empty or JSON null clears the form and returns ("", nil).
func validateTriggerMatchForm(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if len(trimmed) > maxTriggerMatchFormBytes {
		return "", fmt.Errorf("match.form exceeds maximum of %d bytes", maxTriggerMatchFormBytes)
	}
	if !utf8.Valid(trimmed) {
		return "", errors.New("match.form must be valid UTF-8 JSON")
	}
	if trimmed[0] != '{' {
		return "", errors.New("match.form must be a JSON object")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "", fmt.Errorf("invalid match.form JSON: %v", err)
	}
	if compact.Len() > maxTriggerMatchFormBytes {
		return "", fmt.Errorf("match.form exceeds maximum of %d bytes", maxTriggerMatchFormBytes)
	}
	return compact.String(), nil
}

// Trigger rule registration, authoring validation, and engine seam
// (Stage 1 §7.0, §7.0a, §7.1, §7.2; ptone/scion#8030).
//
// Security L1 seam note (ptone/scion#8030): ruleengine.Evaluate derives a context
// deadline for the dialect call and budget tracker, but does not itself discard a
// Decision returned after that deadline if a dialect ignores context cancellation.
// Consumer "trigger" is admitted exclusively to the pure, no-I/O "cel" dialect,
// which is safe because celengine.Program.Eval enforces a post-return wall-clock
// backstop (Elapsed > EvalTimeout returns EvalError{Reason: "timeout"}, never a
// decision). Stage 1 3b-2 dispatch relies on CEL's backstop; admitting any second
// dialect to "trigger" requires either a dialect-level post-return deadline
// backstop or an engine-level post-return check in ruleengine.Evaluate.

// registerTriggerRules builds the Stage 1 trigger CEL environment over Stage 1
// limits and default cache config, registers it on s.celDialect, grants consumer
// "trigger" pure CEL admission on s.rules, and attaches the shared
// *triggerrule.Rules wrapper to s.triggerRules.
//
// Registration performs no network I/O and reads neither the database nor the
// operational settings store.
func (s *Server) registerTriggerRules() error {
	if s.rules == nil || s.celDialect == nil {
		return fmt.Errorf("triggers: cannot register rules before rule engine and CEL dialect are initialized")
	}
	celEng, err := celengine.New(
		triggerrule.Env(),
		triggerrule.Stage1Limits(),
		triggerrule.Stage1CacheConfig(),
	)
	if err != nil {
		return fmt.Errorf("create trigger CEL engine: %w", err)
	}
	if err := s.celDialect.RegisterEnvironment(celEng); err != nil {
		return fmt.Errorf("register trigger CEL environment: %w", err)
	}
	if err := s.rules.AllowConsumerDialects(
		triggerrule.ConsumerName,
		ruleengine.Grant(ruleengine.DialectCEL),
	); err != nil {
		return fmt.Errorf("grant trigger CEL dialect: %w", err)
	}
	trRules, err := triggerrule.New(s.rules)
	if err != nil {
		return fmt.Errorf("create trigger rules wrapper: %w", err)
	}
	s.triggerRules = trRules
	return nil
}

// TriggerRules returns the Hub Server's shared *triggerrule.Rules wrapper.
func (s *Server) TriggerRules() *triggerrule.Rules {
	if s == nil {
		return nil
	}
	return s.triggerRules
}

// invalidateTriggerRuleCache removes cached compiled programs bound to
// (projectID, triggerID, revision) across registered dialects (3b-1 F4).
// Passing revision <= 0 invalidates every revision of triggerID.
func (s *Server) invalidateTriggerRuleCache(projectID, triggerID string, revision int64) {
	if s == nil || s.triggerRules == nil || triggerID == "" {
		return
	}
	scope := celengine.CacheScope{
		Consumer:  triggerrule.ConsumerName,
		ProjectID: projectID,
		ID:        triggerID,
		Revision:  revision,
	}
	s.invalidateTriggerScopeAllDialects(scope)
}

// invalidateTriggerRuleCacheForProject removes all cached trigger programs
// scoped to projectID (3b-1 F4 / project cascade deletion).
func (s *Server) invalidateTriggerRuleCacheForProject(projectID string) {
	if s == nil || s.triggerRules == nil || projectID == "" {
		return
	}
	s.invalidateTriggerScopeAllDialects(celengine.CacheScope{
		Consumer:  triggerrule.ConsumerName,
		ProjectID: projectID,
	})
}

func (s *Server) invalidateTriggerScopeAllDialects(scope celengine.CacheScope) {
	if s.rules != nil {
		for _, d := range s.rules.Dialects() {
			s.triggerRules.Invalidate(d, scope)
		}
		return
	}
	s.triggerRules.Invalidate(ruleengine.DialectCEL, scope)
}

func (s *Server) compileTriggerMatchRule(ctx context.Context, dialect, expr string) error {
	if s != nil && s.triggerRules != nil {
		return s.triggerRules.CompileMatch(ctx, triggerrule.Rule{
			Dialect: dialect,
			Expr:    expr,
		})
	}
	if s != nil && s.rules != nil {
		d := strings.TrimSpace(dialect)
		if d == "" {
			d = ruleengine.DialectCEL
		}
		if _, err := s.rules.DialectFor(triggerrule.ConsumerName, d); err != nil {
			return err
		}
	}
	return errTriggerRuleEngineNotInitialized
}

func (s *Server) compileTriggerSubjectRule(ctx context.Context, dialect, expr string) error {
	if s != nil && s.triggerRules != nil {
		return s.triggerRules.CompileSubject(ctx, triggerrule.Rule{
			Dialect: dialect,
			Expr:    expr,
		})
	}
	if s != nil && s.rules != nil {
		d := strings.TrimSpace(dialect)
		if d == "" {
			d = ruleengine.DialectCEL
		}
		if _, err := s.rules.DialectFor(triggerrule.ConsumerName, d); err != nil {
			return err
		}
	}
	return errTriggerRuleEngineNotInitialized
}

func isDisallowedTriggerRuleExprRune(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

func validateTriggerRuleExpr(field, expr string) error {
	if !utf8.ValidString(expr) || strings.ContainsFunc(expr, isDisallowedTriggerRuleExprRune) {
		return fmt.Errorf("%s must not contain control or format characters", field)
	}
	return nil
}

func validateTriggerDialectName(dialect string) error {
	if len(dialect) > triggerrule.MaxRuleDialectBytes || !ruleDialectNameRE.MatchString(dialect) {
		return fmt.Errorf("invalid dialect name (must match ^[a-z][a-z0-9_]*$ and be at most %d bytes)", triggerrule.MaxRuleDialectBytes)
	}
	return nil
}

// triggerMatchRules normalizes a stored Trigger's match rules on read
// (mixed-rule-arrays §2.1). Normalization fails closed: if MatchRulesCorrupt is
// set, it returns errCorruptTriggerMatchRules so a corrupt column never
// degrades to a Types-only ("no rule") match. Otherwise non-empty MatchRules
// wins; else a defensive non-empty scalar MatchRule normalizes to a one-element
// slice with MatchDialect (defaulting to "cel"); else (nil, nil).
func triggerMatchRules(tr store.Trigger) ([]triggerrule.Rule, error) {
	return trigger.MatchRules(tr)
}

func hasAmbiguousScalarWithRules(dialect, expr string) bool {
	return strings.TrimSpace(dialect) != "" || strings.TrimSpace(expr) != ""
}

func writeCorruptTriggerMatchRulesError(w http.ResponseWriter) {
	const msg = "stored match_rules data is corrupt; replace match.rules before updating dispatch fields"
	writeTriggerMatchElementErrors(w, []triggerElementFailure{{
		entry: map[string]any{
			"element":  1,
			"category": "corrupt_storage",
			"message":  msg,
			"line":     1,
			"column":   1,
		},
		summary: "invalid match rule [element 1]: " + msg,
		kind:    ruleengine.KindFilter,
	}})
}

func triggerRulesToSpecs(rules []triggerrule.Rule) []TriggerMatchRuleSpec {
	out := make([]TriggerMatchRuleSpec, len(rules))
	for i, r := range rules {
		out[i] = TriggerMatchRuleSpec{
			Dialect: r.Dialect,
			Expr:    r.Expr,
		}
	}
	return out
}

func writeAmbiguousTriggerMatchError(w http.ResponseWriter) {
	writeError(
		w,
		http.StatusBadRequest,
		ErrCodeInvalidRule,
		"match cannot specify both rules and scalar dialect/expr",
		map[string]any{
			"field":    "match",
			"category": "ambiguous_match",
		},
	)
}

type triggerElementFailure struct {
	entry    map[string]any
	summary  string
	kind     ruleengine.Kind
	consumer string
}

func writeTriggerMatchElementErrors(w http.ResponseWriter, failures []triggerElementFailure) {
	elements := make([]map[string]any, len(failures))
	summaries := make([]string, len(failures))
	for i, f := range failures {
		elements[i] = f.entry
		summaries[i] = f.summary
	}
	first := failures[0]
	details := map[string]any{
		"field":    "match",
		"elements": elements,
	}
	if elem, hasElem := first.entry["element"]; hasElem {
		details["element"] = elem
	}
	if cat, hasCat := first.entry["category"]; hasCat {
		if first.kind != "" {
			details["kind"] = first.kind
		}
		details["category"] = cat
		if msg, ok := first.entry["message"]; ok {
			details["message"] = msg
		}
		if line, ok := first.entry["line"]; ok {
			details["line"] = line
		}
		if col, ok := first.entry["column"]; ok {
			details["column"] = col
		}
	}
	if dialect, hasDialect := first.entry["dialect"]; hasDialect {
		details["dialect"] = dialect
		if first.consumer != "" {
			details["consumer"] = first.consumer
		}
		if reason, ok := first.entry["reason"]; ok && reason != "" {
			details["reason"] = reason
		}
	}
	writeError(w, http.StatusBadRequest, ErrCodeInvalidRule, strings.Join(summaries, "; "), details)
}

func makeUnsupportedDialectFailure(elemNum int, unsup *ruleengine.UnsupportedDialectError) triggerElementFailure {
	msg := fmt.Sprintf("unsupported match dialect %q [element %d]", unsup.Dialect, elemNum)
	if unsup.Reason != "" {
		msg += ": " + unsup.Reason
	}
	entry := map[string]any{
		"element": elemNum,
		"dialect": unsup.Dialect,
		"reason":  unsup.Reason,
	}
	if unsup.Reason != "" {
		entry["category"] = unsup.Reason
	}
	return triggerElementFailure{
		entry:    entry,
		summary:  msg,
		consumer: unsup.Consumer,
	}
}

func makeAgentSystemOneNotAllowedFailure(elemNum int) triggerElementFailure {
	return triggerElementFailure{
		entry: map[string]any{
			"element":  elemNum,
			"dialect":  triggerDialectSystemOne,
			"reason":   triggerReasonAgentNotAllowed,
			"category": triggerReasonAgentNotAllowed,
		},
		summary:  fmt.Sprintf("unsupported match dialect %q [element %d]: %s", triggerDialectSystemOne, elemNum, triggerReasonAgentNotAllowed),
		consumer: triggerrule.ConsumerName,
	}
}

func makeAgentSystemOnePrefixChangedFailure(elemNum int, dialect string) triggerElementFailure {
	return triggerElementFailure{
		entry: map[string]any{
			"element":  elemNum,
			"dialect":  dialect,
			"reason":   triggerReasonAgentNotAllowed,
			"category": triggerCategoryAgentSystemOnePrefixChanged,
		},
		summary:  fmt.Sprintf("invalid match rule [element %d]: agents cannot modify rules in the prefix through the last system_one element", elemNum),
		consumer: triggerrule.ConsumerName,
	}
}

// isTriggerAgentCaller reports whether ctx carries an agent identity.
func isTriggerAgentCaller(ctx context.Context) bool {
	identity := GetIdentityFromContext(ctx)
	return identity != nil && identity.Type() == "agent"
}

// normalizedTriggerRuleDialect canonicalizes a rule dialect name for authoring
// and comparison: trim whitespace, defaulting empty to "cel".
func normalizedTriggerRuleDialect(dialect string) string {
	d := strings.TrimSpace(dialect)
	if d == "" {
		return ruleengine.DialectCEL
	}
	return d
}

// lastStoredSystemOneIndex returns the largest 0-based index of a system_one
// element in stored, or -1 if stored contains no system_one element.
func lastStoredSystemOneIndex(stored []triggerrule.Rule) int {
	last := -1
	for i, s := range stored {
		if normalizedTriggerRuleDialect(s.Dialect) == triggerDialectSystemOne {
			last = i
		}
	}
	return last
}

// unchangedMatchElements identifies which elements of normalized carry over
// unchanged from stored on PATCH (mixed-rule-arrays-design.md §11.3,
// ptone/scion#9916).
//
// Normalization on both sides: Dialect is trimmed with "" defaulting to "cel";
// Expr is compared byte-for-byte without trimming.
//
// Identity rules:
//   - Users (non-agent callers): bounded multiset content identity. Each stored
//     {Dialect, Expr} may be consumed at most once regardless of index, so
//     reordering or removing elements is allowed while duplicating, editing, or
//     adding a non-admitted element is refused.
//   - Agent callers (D2 carve-out): the ordered rule prefix from index 0
//     through and including the last stored system_one element must be
//     byte-identical (count, order, dialect, expr) to stored; only elements in
//     that identical prefix carry through without re-admission, and CEL rows
//     strictly after the last system_one remain agent-editable.
func unchangedMatchElements(normalized []store.TriggerMatchRule, stored []triggerrule.Rule, isAgent bool) []bool {
	unchanged := make([]bool, len(normalized))
	if len(stored) == 0 {
		return unchanged
	}
	if isAgent {
		lastS1 := lastStoredSystemOneIndex(stored)
		if lastS1 < 0 {
			return unchanged
		}
		for i := 0; i <= lastS1 && i < len(normalized); i++ {
			if normalized[i].Dialect == normalizedTriggerRuleDialect(stored[i].Dialect) && normalized[i].Expr == stored[i].Expr {
				unchanged[i] = true
			}
		}
		return unchanged
	}
	remaining := make(map[triggerMatchRuleKey]int, len(stored))
	for _, s := range stored {
		remaining[triggerMatchRuleKey{
			dialect: normalizedTriggerRuleDialect(s.Dialect),
			expr:    s.Expr,
		}]++
	}
	for i, elem := range normalized {
		key := triggerMatchRuleKey{dialect: elem.Dialect, expr: elem.Expr}
		if remaining[key] > 0 {
			remaining[key]--
			unchanged[i] = true
		}
	}
	return unchanged
}

// validateAgentSystemOnePrefix enforces the D2 agent PATCH invariant
// (mixed-rule-arrays-design.md §11.3): for an agent caller, the ordered rule
// prefix from index 0 through and including the last stored system_one element
// must be byte-identical to stored (count, order, dialect, expr), and no
// system_one element may be added after that prefix. CEL rows strictly after
// the last system_one remain agent-editable.
func validateAgentSystemOnePrefix(normalized []store.TriggerMatchRule, stored []triggerrule.Rule, alreadyFailed map[int]bool) []triggerElementFailure {
	var failures []triggerElementFailure
	lastS1 := lastStoredSystemOneIndex(stored)
	for i := 0; i <= lastS1; i++ {
		elemNum := i + 1
		if alreadyFailed[elemNum] {
			continue
		}
		sd := normalizedTriggerRuleDialect(stored[i].Dialect)
		if i < len(normalized) && normalized[i].Dialect == sd && normalized[i].Expr == stored[i].Expr {
			continue
		}
		if sd == triggerDialectSystemOne {
			failures = append(failures, makeAgentSystemOneNotAllowedFailure(elemNum))
			continue
		}
		actualDialect := sd
		if i < len(normalized) && normalized[i].Dialect != "" {
			actualDialect = normalized[i].Dialect
		}
		failures = append(failures, makeAgentSystemOnePrefixChangedFailure(elemNum, actualDialect))
	}
	return failures
}

// authorizeTriggerSystemOneReattribution enforces mixed-rule-arrays-design.md
// §11.6 on any PATCH that would re-attribute a trigger's revision
// (changesFutureDispatch == true) when the resulting match rules contain any
// system_one element:
//   - Agent callers are refused with 400 invalid_rule (agent_not_allowed, D2).
//   - Non-agent callers whose effective ceiling lacks trigger.create in
//     projectID (e.g., a trigger:update-only UAT) are refused with 400
//     invalid_rule {category: "system_one_authority"}.
//   - CEL-only / Types-only triggers (no system_one elements) are unaffected.
//
// Called in updateTrigger before WithTx (no I/O inside a transaction).
func (s *Server) authorizeTriggerSystemOneReattribution(
	w http.ResponseWriter,
	ctx context.Context,
	projectID string,
	resultingRules []triggerrule.Rule,
) bool {
	firstS1Elem := 0
	for i, r := range resultingRules {
		if normalizedTriggerRuleDialect(r.Dialect) == triggerDialectSystemOne {
			firstS1Elem = i + 1
			break
		}
	}
	if firstS1Elem == 0 {
		return true
	}
	if isTriggerAgentCaller(ctx) {
		writeTriggerMatchElementErrors(w, []triggerElementFailure{makeAgentSystemOneNotAllowedFailure(firstS1Elem)})
		return false
	}
	if !s.triggerAccessDecision(ctx, projectID, ActionCreate, nil).allowed {
		writeError(w, http.StatusBadRequest, ErrCodeInvalidRule, triggerMsgSystemOneReattributionRequires, map[string]any{
			"field":    "match",
			"category": triggerCategorySystemOneAuthority,
			"message":  triggerMsgSystemOneReattributionRequires,
			"element":  firstS1Elem,
			"dialect":  triggerDialectSystemOne,
		})
		return false
	}
	return true
}

// authorizeNewOrChangedTriggerMatchElement is the authoring authorization hook
// for NEW or CHANGED match elements only (never called for unchanged stored
// elements carried through a PATCH). Runs on create, PATCH (new/changed), and
// resume (full re-admission).
//
// T2 hook (ptone/scion#8051 / #8045): when System One trigger authoring lands,
// T2's D1 per-project instance allowlist check and D2 user-only authoring check
// plug in here so they automatically apply only to new or changed elements and
// never re-check unchanged byte-identical elements on PATCH.
func (*Server) authorizeNewOrChangedTriggerMatchElement(_ context.Context, elemNum int, elem store.TriggerMatchRule, isAgent bool) *triggerElementFailure {
	if isAgent && elem.Dialect == triggerDialectSystemOne {
		f := makeAgentSystemOneNotAllowedFailure(elemNum)
		return &f
	}
	return nil
}

// writeAndValidateTriggerMatch normalizes and compiles a trigger's match block
// at authoring time (create, PATCH, or resume; mixed-rule-arrays §2, §4, §11.3).
// Scalar dialect/expr is accepted as sugar for a one-element list; mixing both
// rules and scalar dialect/expr returns 400 invalid_rule (ambiguous_match).
// Every element is validated and compiled, and ALL failing elements are
// reported together in details.elements[].
//
// Authoring-admission invariant inventory (ptone/scion#9916):
//   - POST /triggers (createTrigger) & default webhook trigger creation:
//     allowStoredAdmissionSkip=false -> full admission on every element.
//   - PATCH /triggers/{id} (updateTrigger, with or without req.Match):
//     allowStoredAdmissionSkip=true -> unchanged elements matched against
//     storedRules skip dialect admission and T2's D1/D2 hook while still running
//     shape, size, control-character, and CEL compile checks. Users match by
//     bounded multiset content identity (each stored element consumed at most
//     once); agents must keep the ordered rule prefix from index 0 through the
//     last stored system_one element byte-identical (D2 carve-out; CEL rows
//     strictly after the last system_one remain agent-editable).
//   - POST /triggers/{id}/resume (resumeTrigger):
//     allowStoredAdmissionSkip=false -> full re-admission on all stored elements,
//     returning 400 invalid_rule {element, category: dialect_not_allowed, ...}.
//   - Fire-time evaluation (triggerrule.MatchAllWithTrace): unchanged; engine
//     deny-by-default refuses any non-granted dialect with 0 provider calls.
func (s *Server) writeAndValidateTriggerMatch(
	w http.ResponseWriter,
	ctx context.Context,
	spec *TriggerMatchSpec,
	opts triggerMatchValidationOpts,
) ([]string, []store.TriggerMatchRule, bool) {
	defaultTypes := opts.defaultTypes
	explicitScalarDialect := opts.explicitScalarDialect
	isAgent := isTriggerAgentCaller(ctx)

	var types []string
	if spec != nil {
		types = spec.Types
	}
	if len(types) == 0 {
		if !defaultTypes {
			ValidationError(w, "match.types must contain at least 1 entry", nil)
			return nil, nil, false
		}
		types = []string{defaultWebhookMatchType}
	}
	if err := triggerrule.ValidateMatchTypes(types); err != nil {
		ValidationError(w, err.Error(), nil)
		return nil, nil, false
	}

	if spec != nil && spec.Rules != nil && hasAmbiguousScalarWithRules(spec.Dialect, spec.Expr) {
		writeAmbiguousTriggerMatchError(w)
		return nil, nil, false
	}

	var rawRules []TriggerMatchRuleSpec
	switch {
	case spec == nil:
		rawRules = nil
	case spec.Rules != nil:
		rawRules = spec.Rules
	default:
		scalarDialect := strings.TrimSpace(spec.Dialect)
		if spec.Expr == "" {
			if explicitScalarDialect {
				if err := validateTriggerDialectName(scalarDialect); err != nil {
					writeTriggerMatchElementErrors(w, []triggerElementFailure{{
						entry: map[string]any{
							"element":  1,
							"category": "dialect",
							"message":  err.Error(),
							"line":     1,
							"column":   1,
						},
						summary: fmt.Sprintf("invalid match rule [element 1]: %s", err.Error()),
						kind:    ruleengine.KindFilter,
					}})
					return nil, nil, false
				}
				if err := s.compileTriggerMatchRule(ctx, scalarDialect, ""); err != nil {
					var unsup *ruleengine.UnsupportedDialectError
					if errors.As(err, &unsup) {
						writeTriggerMatchElementErrors(w, []triggerElementFailure{makeUnsupportedDialectFailure(1, unsup)})
						return nil, nil, false
					}
					writeTriggerRuleError(w, "match", err)
					return nil, nil, false
				}
				ValidationError(w, "match.dialect requires a non-empty match.expr", nil)
				return nil, nil, false
			}
			rawRules = nil
		} else {
			rawRules = []TriggerMatchRuleSpec{{
				Dialect: spec.Dialect,
				Expr:    spec.Expr,
			}}
		}
	}

	copiedTypes := make([]string, len(types))
	copy(copiedTypes, types)

	if len(rawRules) == 0 {
		if isAgent && opts.allowStoredAdmissionSkip {
			if removedFailures := validateAgentSystemOnePrefix(nil, opts.storedRules, nil); len(removedFailures) > 0 {
				writeTriggerMatchElementErrors(w, removedFailures)
				return nil, nil, false
			}
		}
		// Verify the trigger rule engine is initialized and consumer "trigger" is admitted.
		if err := s.compileTriggerMatchRule(ctx, ruleengine.DialectCEL, ""); err != nil {
			writeTriggerRuleError(w, "match", err)
			return nil, nil, false
		}
		return copiedTypes, nil, true
	}

	if len(rawRules) > triggerrule.MaxMatchRules {
		writeError(
			w,
			http.StatusBadRequest,
			ErrCodeInvalidRule,
			fmt.Sprintf("match.rules exceeds maximum of %d elements (got %d)", triggerrule.MaxMatchRules, len(rawRules)),
			map[string]any{
				"field":        "match",
				"category":     "size",
				"max_elements": triggerrule.MaxMatchRules,
				"count":        len(rawRules),
			},
		)
		return nil, nil, false
	}

	normalized := make([]store.TriggerMatchRule, len(rawRules))
	for i, elem := range rawRules {
		d := strings.TrimSpace(elem.Dialect)
		if d == "" {
			d = ruleengine.DialectCEL
		}
		normalized[i] = store.TriggerMatchRule{
			Dialect: d,
			Expr:    elem.Expr,
		}
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return nil, nil, false
	}
	if len(encoded) > triggerrule.MaxMatchRulesJSONBytes {
		writeError(
			w,
			http.StatusBadRequest,
			ErrCodeInvalidRule,
			fmt.Sprintf("encoded match.rules length %d exceeds maximum of %d bytes", len(encoded), triggerrule.MaxMatchRulesJSONBytes),
			map[string]any{
				"field":     "match",
				"category":  "size",
				"max_bytes": triggerrule.MaxMatchRulesJSONBytes,
				"bytes":     len(encoded),
			},
		)
		return nil, nil, false
	}

	var unchanged []bool
	if opts.allowStoredAdmissionSkip {
		unchanged = unchangedMatchElements(normalized, opts.storedRules, isAgent)
	}

	var failures []triggerElementFailure
	agentFailedElems := make(map[int]bool)

	for i, elem := range normalized {
		elemNum := i + 1
		d := elem.Dialect
		isUnchanged := len(unchanged) > i && unchanged[i]
		if err := validateTriggerDialectName(d); err != nil {
			failures = append(failures, triggerElementFailure{
				entry: map[string]any{
					"element":  elemNum,
					"category": "dialect",
					"message":  err.Error(),
					"line":     1,
					"column":   1,
				},
				summary: fmt.Sprintf("invalid match rule [element %d]: %s", elemNum, err.Error()),
				kind:    ruleengine.KindFilter,
			})
			continue
		}
		if err := validateTriggerRuleExpr("match.expr", elem.Expr); err != nil {
			failures = append(failures, triggerElementFailure{
				entry: map[string]any{
					"element":  elemNum,
					"category": "control_characters",
					"message":  err.Error(),
					"line":     1,
					"column":   1,
				},
				summary: fmt.Sprintf("invalid match rule [element %d]: %s", elemNum, err.Error()),
				kind:    ruleengine.KindFilter,
			})
			continue
		}

		// For new or changed elements (including all elements on create and
		// resume, where unchanged is nil), enforce T2 authoring hooks (D2 agent
		// carve-out on system_one, and D1 allowlist once T2 lands).
		if !isUnchanged {
			if authzFailure := s.authorizeNewOrChangedTriggerMatchElement(ctx, elemNum, elem, isAgent); authzFailure != nil {
				failures = append(failures, *authzFailure)
				agentFailedElems[elemNum] = true
				continue
			}
		}

		// Check dialect admission and engine initialization first so ungranted
		// dialects surface dialect_not_allowed even when expr is empty or oversize,
		// and an uninitialized engine returns HTTP 500 without leaking details.
		// Unchanged stored elements on PATCH skip UnsupportedDialectError admission
		// failures while still running empty/size checks below.
		admissionSkipped := false
		if err := s.compileTriggerMatchRule(ctx, d, ""); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				writeErrorFromErr(w, err, "")
				return nil, nil, false
			}
			var unsup *ruleengine.UnsupportedDialectError
			if errors.As(err, &unsup) {
				if isUnchanged {
					admissionSkipped = true
				} else {
					failures = append(failures, makeUnsupportedDialectFailure(elemNum, unsup))
					continue
				}
			} else {
				writeErrorFromErr(w, err, "")
				return nil, nil, false
			}
		}

		if elem.Expr == "" {
			msg := "match rule expression must not be empty"
			failures = append(failures, triggerElementFailure{
				entry: map[string]any{
					"element":  elemNum,
					"category": "empty",
					"message":  msg,
					"line":     1,
					"column":   1,
				},
				summary: fmt.Sprintf("invalid match rule [element %d]: %s", elemNum, msg),
				kind:    ruleengine.KindFilter,
			})
			continue
		}
		if len(elem.Expr) > triggerrule.MaxMatchRuleBytes {
			msg := fmt.Sprintf("rule length %d exceeds maximum of %d bytes", len(elem.Expr), triggerrule.MaxMatchRuleBytes)
			failures = append(failures, triggerElementFailure{
				entry: map[string]any{
					"element":  elemNum,
					"category": "size",
					"message":  msg,
					"line":     1,
					"column":   1,
				},
				summary: fmt.Sprintf("invalid match rule [element %d] at line 1, column 1 (size): %s", elemNum, msg),
				kind:    ruleengine.KindFilter,
			})
			continue
		}

		// Unchanged non-CEL elements (e.g. system_one) carry through without
		// re-admission or dialect compilation (0 provider calls; D1 not re-checked).
		// CEL elements always compile so broken stored CEL rules are still caught.
		if admissionSkipped || (isUnchanged && d != ruleengine.DialectCEL) {
			continue
		}

		if err := s.compileTriggerMatchRule(ctx, d, elem.Expr); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				writeErrorFromErr(w, err, "")
				return nil, nil, false
			}
			var ce *celengine.CompileError
			if errors.As(err, &ce) {
				failures = append(failures, triggerElementFailure{
					entry: map[string]any{
						"element":  elemNum,
						"category": ce.Category,
						"message":  ce.Message,
						"line":     ce.Line,
						"column":   ce.Column,
					},
					summary: fmt.Sprintf("invalid match rule [element %d] at line %d, column %d (%s): %s",
						elemNum, ce.Line, ce.Column, ce.Category, ce.Message),
					kind: ruleengine.Kind(ce.Kind),
				})
				continue
			}
			var unsup *ruleengine.UnsupportedDialectError
			if errors.As(err, &unsup) {
				failures = append(failures, makeUnsupportedDialectFailure(elemNum, unsup))
				continue
			}
			writeErrorFromErr(w, err, "")
			return nil, nil, false
		}
	}

	if isAgent && opts.allowStoredAdmissionSkip {
		failures = append(failures, validateAgentSystemOnePrefix(normalized, opts.storedRules, agentFailedElems)...)
	}

	if len(failures) > 0 {
		writeTriggerMatchElementErrors(w, failures)
		return nil, nil, false
	}

	return copiedTypes, normalized, true
}

func formatTriggerCoalesceWindow(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	return (time.Duration(seconds) * time.Second).String()
}

func parseTriggerCoalesceWindow(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "0" || s == "0s" {
		return 0, nil
	}
	maxDur := time.Duration(store.MaxTriggerCoalesceWindowSeconds) * time.Second
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 || d > maxDur || d%time.Second != 0 {
		return 0, errors.New("subject.coalesce_window must be a whole-second duration between 0s and 10m")
	}
	return int(d / time.Second), nil
}

// writeAndValidateTriggerSubject normalizes and compiles a trigger's subject
// block at authoring time (create, PATCH, or resume). When explicitDialect is
// true, an empty expr is refused as a malformed request.
func (s *Server) writeAndValidateTriggerSubject(w http.ResponseWriter, ctx context.Context, spec *TriggerSubjectSpec, explicitDialect bool) (string, string, string, int, bool) {
	dialect := ruleengine.DialectCEL
	expr := ""
	mode := store.TriggerSubjectModeNew
	rawCoalesce := ""
	if spec != nil {
		if d := strings.TrimSpace(spec.Dialect); d != "" {
			dialect = d
		}
		expr = spec.Expr
		if m := strings.TrimSpace(spec.Mode); m != "" {
			mode = m
		}
		rawCoalesce = spec.CoalesceWindow
	}
	switch mode {
	case store.TriggerSubjectModeNew, store.TriggerSubjectModeSkip, store.TriggerSubjectModeReuse:
	default:
		ValidationError(w, fmt.Sprintf("unsupported subject mode %q (supported: %s, %s, %s)",
			mode, store.TriggerSubjectModeNew, store.TriggerSubjectModeSkip, store.TriggerSubjectModeReuse), nil)
		return "", "", "", 0, false
	}
	coalesceSeconds, err := parseTriggerCoalesceWindow(rawCoalesce)
	if err != nil {
		ValidationError(w, err.Error(), nil)
		return "", "", "", 0, false
	}
	if err := validateTriggerDialectName(dialect); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeInvalidRule, "invalid subject dialect: "+err.Error(), map[string]any{
			"field":    "subject",
			"category": "dialect",
			"message":  err.Error(),
			"line":     1,
			"column":   1,
		})
		return "", "", "", 0, false
	}
	if err := validateTriggerRuleExpr("subject.expr", expr); err != nil {
		ValidationError(w, err.Error(), nil)
		return "", "", "", 0, false
	}
	if err := s.compileTriggerSubjectRule(ctx, dialect, expr); err != nil {
		writeTriggerRuleError(w, "subject", err)
		return "", "", "", 0, false
	}
	if explicitDialect && expr == "" {
		ValidationError(w, "subject.dialect requires a non-empty subject.expr", nil)
		return "", "", "", 0, false
	}
	return dialect, expr, mode, coalesceSeconds, true
}

// writeTriggerRuleError maps authoring-time rule compilation and dialect-admission
// errors to HTTP 400 ErrCodeInvalidRule responses carrying structured CompileError
// or UnsupportedDialectError details. Unexpected internal errors (such as an
// uninitialized rule engine) fall through to writeErrorFromErr, which logs the
// cause and returns a generic HTTP 500 without echoing internal error text.
func writeTriggerRuleError(w http.ResponseWriter, slot string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeErrorFromErr(w, err, "")
		return
	}

	var ce *celengine.CompileError
	if errors.As(err, &ce) {
		msg := fmt.Sprintf("invalid %s rule at line %d, column %d (%s): %s", slot, ce.Line, ce.Column, ce.Category, ce.Message)
		details := map[string]any{
			"field":    slot,
			"kind":     ce.Kind,
			"category": ce.Category,
			"message":  ce.Message,
			"line":     ce.Line,
			"column":   ce.Column,
		}
		writeError(w, http.StatusBadRequest, ErrCodeInvalidRule, msg, details)
		return
	}

	var unsup *ruleengine.UnsupportedDialectError
	if errors.As(err, &unsup) {
		details := map[string]any{
			"field":   slot,
			"dialect": unsup.Dialect,
		}
		if unsup.Consumer != "" {
			details["consumer"] = unsup.Consumer
		}
		if unsup.Reason != "" {
			details["reason"] = unsup.Reason
		}
		msg := fmt.Sprintf("unsupported %s dialect %q", slot, unsup.Dialect)
		if unsup.Reason != "" {
			msg += ": " + unsup.Reason
		}
		writeError(w, http.StatusBadRequest, ErrCodeInvalidRule, msg, details)
		return
	}

	writeErrorFromErr(w, err, "")
}
