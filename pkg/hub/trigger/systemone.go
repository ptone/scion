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

package trigger

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/hubfire"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/spec"
	"github.com/GoogleCloudPlatform/scion/pkg/triggerrule"
)

// Trigger System One content environment v1 field names and bounds
// (t2-p2-trigger-binding-amendment.md §2.1).
const (
	SystemOneEnvVersion           = 1
	SystemOneEnvMaxTotalBytes     = 32 * 1024
	SystemOneFieldEventType       = "event.type"
	SystemOneFieldEventSourceKind = "event.source_kind"
	SystemOneFieldEventSubject    = "event.subject"
	SystemOneFieldEventAttributes = "event.attributes"
	SystemOneFieldEventPayload    = "event.payload"
	SystemOneMaxEventTypeBytes    = 128
	SystemOneMaxSourceKindBytes   = 32
	SystemOneMaxEventSubjectBytes = 256
	SystemOneMaxAttributesBytes   = 8192
	SystemOneMaxEventPayloadBytes = 24576
	ErrFireAgentSystemOneNotAllow = "agent not allowed to evaluate system_one rules"
	AuditReasonAgentNotAllowed    = "agent_not_allowed"
)

// SystemOneContentEnv returns the v1 System One ContentEnvironment for the
// "trigger" consumer (t2-p2-trigger-binding-amendment.md §2.1).
//
// Dynamic choice sets are omitted (P2 refuses them). Nothing from the trigger
// definition, endpoint secret, action payload, or authority is exposed.
func SystemOneContentEnv() spec.ContentEnvironment {
	return spec.ContentEnvironment{
		Name:    triggerrule.EnvironmentName,
		Version: SystemOneEnvVersion,
		Fields: []spec.FieldDecl{
			{
				Name:     SystemOneFieldEventType,
				Kind:     spec.FieldKindText,
				MaxBytes: SystemOneMaxEventTypeBytes,
				Trust:    spec.TrustUntrusted,
			},
			{
				Name:     SystemOneFieldEventSourceKind,
				Kind:     spec.FieldKindText,
				MaxBytes: SystemOneMaxSourceKindBytes,
				Trust:    spec.TrustTrusted,
			},
			{
				Name:     SystemOneFieldEventSubject,
				Kind:     spec.FieldKindText,
				MaxBytes: SystemOneMaxEventSubjectBytes,
				Trust:    spec.TrustUntrusted,
			},
			{
				Name:     SystemOneFieldEventAttributes,
				Kind:     spec.FieldKindJSON,
				MaxBytes: SystemOneMaxAttributesBytes,
				Trust:    spec.TrustUntrusted,
			},
			{
				Name:     SystemOneFieldEventPayload,
				Kind:     spec.FieldKindJSON,
				MaxBytes: SystemOneMaxEventPayloadBytes,
				Trust:    spec.TrustUntrusted,
			},
		},
		MaxTotalBytes: SystemOneEnvMaxTotalBytes,
	}
}

// BuildSystemOneInput constructs the bounded, once-per-event systemone.Input
// from ev exposing ONLY the v1 trigger content environment fields. The runtime
// selects only fields declared in each compiled element's inputs (and applies
// any payload_paths narrowing).
func BuildSystemOneInput(ev hubfire.FireEvent) systemone.Input {
	attrsJSON := "{}"
	if len(ev.Attributes) > 0 {
		if b, err := json.Marshal(ev.Attributes); err == nil {
			attrsJSON = string(b)
		}
	}
	fields := []provider.InputField{
		{
			Name:  SystemOneFieldEventType,
			Kind:  spec.FieldKindText,
			Value: ev.Type,
			Trust: spec.TrustUntrusted,
		},
		{
			Name:  SystemOneFieldEventSourceKind,
			Kind:  spec.FieldKindText,
			Value: ev.SourceKind,
			Trust: spec.TrustTrusted,
		},
		{
			Name:  SystemOneFieldEventSubject,
			Kind:  spec.FieldKindText,
			Value: ev.Subject,
			Trust: spec.TrustUntrusted,
		},
		{
			Name:  SystemOneFieldEventAttributes,
			Kind:  spec.FieldKindJSON,
			Value: attrsJSON,
			Trust: spec.TrustUntrusted,
		},
	}
	if trimmed := bytes.TrimSpace(ev.Payload); len(trimmed) > 0 {
		if canonical, ok := canonicalizeJSONBytes(trimmed); ok {
			fields = append(fields, provider.InputField{
				Name:  SystemOneFieldEventPayload,
				Kind:  spec.FieldKindJSON,
				Value: canonical,
				Trust: spec.TrustUntrusted,
			})
		}
	}
	return systemone.Input{Fields: fields}
}

func canonicalizeJSONBytes(raw []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if dec.More() {
		return "", false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// BuildSystemOneRequest wraps in with the hub-owned trigger's authorized
// ProjectID, RuleID, and AuthorizationRevision (t2-p2-trigger-binding-amendment
// §2.2). ProjectID is ALWAYS taken from tr.ProjectID, never from event payload,
// attributes, endpoint input, or caller claims.
func BuildSystemOneRequest(in systemone.Input, tr *store.Trigger) systemone.Request {
	if tr == nil {
		return systemone.Request{Input: in}
	}
	return systemone.Request{
		Input:     in,
		ProjectID: tr.ProjectID,
		RuleID:    tr.ID,
		Revision:  int64(tr.AuthorizationRevision),
	}
}

// SystemOneErrorProvenance recovers the compact content-free provenance
// projection from a System One evaluation error via systemone.EvaluationFrom
// (t2-p2-trigger-binding-amendment §2.7).
func SystemOneErrorProvenance(err error) map[string]any {
	eval, ok := systemone.EvaluationFrom(err)
	if !ok || eval == nil {
		return nil
	}
	return systemone.CompactRuleTraceProvenance(eval)
}

// HasSystemOneMatchRule reports whether rules contains at least one element
// using the "system_one" dialect.
func HasSystemOneMatchRule(rules []triggerrule.Rule) bool {
	for _, r := range rules {
		if strings.TrimSpace(r.Dialect) == systemone.DialectName {
			return true
		}
	}
	return false
}

// IsAgentInitiatedTrigger reports whether tr's recorded authorizing principal
// is an agent (ptone decision D2: no agent-authored System One elements in v1).
func IsAgentInitiatedTrigger(tr *store.Trigger) bool {
	if tr == nil {
		return false
	}
	if tr.InitiatorCredentialKind == store.InitiatorCredentialKindAgent {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(tr.InitiatorPrincipalKind), "agent")
}

func anyMatchingCandidateHasSystemOne(candidates []store.Trigger, eventType string) bool {
	for i := range candidates {
		tr := &candidates[i]
		if !triggerrule.MatchTypes(tr.MatchTypes, eventType) {
			continue
		}
		rules, err := MatchRules(*tr)
		if err != nil {
			continue
		}
		if HasSystemOneMatchRule(rules) {
			return true
		}
	}
	return false
}
