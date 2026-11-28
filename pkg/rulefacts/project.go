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

package rulefacts

import (
	"maps"
	"slices"
)

// Activation holds a scope-projected CEL activation variable map (v1 §4.1).
// Invisible paths are ABSENT from the map (not set to empty strings or empty
// lists) so CEL has() returns false and direct reads yield missing_input.
type Activation struct {
	scope Scope
	vars  map[string]any
}

// Scope returns the scope this Activation was projected for.
func (a Activation) Scope() Scope {
	return a.scope
}

// Map returns the underlying CEL variable binding map.
func (a Activation) Map() map[string]any {
	if a.vars == nil {
		return map[string]any{}
	}
	return a.vars
}

// Project projects typed Facts into a scope-bounded CEL Activation (v1 §3.4,
// §3.2a, D1, Amendment A1 §B.2, ptone Q1).
//
// Rules enforced:
//   - Opposite-end facts are ABSENT in policy_out (recipient absent) and
//     policy_in (sender absent).
//   - Third-party identity lists (mentions.agent_ids/user_ids,
//     conversation.*_participant_ids, conversation.unmentioned_agent_slugs) are
//     ABSENT in policy_out and policy_in, and present in policy_a2a and
//     trigger_chat (Q1: a2a_equiv; unmentioned_agent_slugs is a2a/legacy only).
//   - D1: conversation.ref and conversation.id are ABSENT in policy_out and
//     policy_in when conversation.kind == "direct".
//   - A1 §B.2: in policy_out and policy_in, value_dependent event fields
//     (event.subject, event.reason, event.templated_vars) are DROPPED;
//     event.source keeps id/name ONLY when source.kind is "agent" or "user" and
//     source.id equals the visible end's id, otherwise only source.kind remains.
//   - message.id and message.created_at are ABSENT in policy scopes (pre-persist)
//     and present in trigger_chat (post-persist).
//   - message.body is truncated UTF-8 safely to MaxBodyBytes (16 KiB).
func Project(f Facts, scope Scope) Activation {
	switch scope {
	case ScopePolicyOut:
		return Activation{
			scope: scope,
			vars:  projectPolicy(f, scope, f.Sender),
		}
	case ScopePolicyIn:
		return Activation{
			scope: scope,
			vars:  projectPolicy(f, scope, f.Recipient),
		}
	case ScopePolicyA2A:
		return Activation{
			scope: scope,
			vars:  projectPolicy(f, scope, nil),
		}
	case ScopeTriggerChat:
		return Activation{
			scope: scope,
			vars:  projectTriggerChat(f),
		}
	case ScopeLegacyPolicyV1:
		return Activation{
			scope: scope,
			vars:  projectLegacyPolicyV1(f),
		}
	default:
		return Activation{scope: scope, vars: map[string]any{}}
	}
}

func projectPolicy(f Facts, scope Scope, visibleEnd *End) map[string]any {
	vars := map[string]any{
		"message":      projectMessage(f.Message, scope, visibleEnd),
		"conversation": projectConversation(f.Conversation, scope),
		"mentions":     projectMentions(f.Mentions, scope),
		"delivery":     projectDelivery(f.Delivery),
	}
	if (scope == ScopePolicyOut || scope == ScopePolicyA2A) && f.Sender != nil {
		vars["sender"] = projectEnd(*f.Sender)
	}
	if (scope == ScopePolicyIn || scope == ScopePolicyA2A) && f.Recipient != nil {
		vars["recipient"] = projectEnd(*f.Recipient)
	}
	return vars
}

func projectTriggerChat(f Facts) map[string]any {
	msgMap := projectMessage(f.Message, ScopeTriggerChat, nil)
	msgMap["context"] = map[string]any{
		"conversation": projectConversation(f.Conversation, ScopeTriggerChat),
		"mentions":     projectMentions(f.Mentions, ScopeTriggerChat),
		"delivery":     projectDelivery(f.Delivery),
	}
	eventMap := map[string]any{
		"message": msgMap,
	}
	if f.Sender != nil {
		eventMap["actor"] = projectEnd(*f.Sender)
	}
	if f.Recipient != nil {
		eventMap["target"] = projectEnd(*f.Recipient)
	}
	return eventMap
}

func projectLegacyPolicyV1(f Facts) map[string]any {
	vars := map[string]any{
		"surface":             f.Delivery.Surface,
		"message":             projectMessage(f.Message, ScopeLegacyPolicyV1, nil),
		"mentioned_agent_ids": cloneStringsNonNil(f.Mentions.AgentIDs),
		"mentioned_user_ids":  cloneStringsNonNil(f.Mentions.UserIDs),
		"conversation":        projectConversation(f.Conversation, ScopeLegacyPolicyV1),
	}
	if f.Sender != nil {
		vars["sender"] = projectEnd(*f.Sender)
	}
	if f.Recipient != nil {
		vars["recipient"] = projectEnd(*f.Recipient)
	}
	return vars
}

func projectMessage(m MessageFacts, scope Scope, visibleEnd *End) map[string]any {
	// Re-enforce MaxBodyBytes cap even if caller constructed Facts directly.
	body := m.Body
	origBytes := m.Bytes
	truncated := m.BodyTruncated
	if len(body) > MaxBodyBytes {
		var calcBytes int
		var calcTrunc bool
		body, calcBytes, calcTrunc = TruncateBodyUTF8(body, MaxBodyBytes)
		if int64(calcBytes) > origBytes {
			origBytes = int64(calcBytes)
		}
		if calcTrunc {
			truncated = true
		}
	}
	if origBytes == 0 && len(body) > 0 {
		origBytes = int64(len(body))
	}

	out := map[string]any{
		"kind":             string(m.Kind),
		"type":             m.Type,
		"origin":           string(m.Origin),
		"body":             body,
		"bytes":            origBytes,
		"body_truncated":   truncated,
		"body_source":      string(m.BodySource),
		"has_choices":      m.HasChoices,
		"urgent":           m.Urgent,
		"attachment_count": m.AttachmentCount,
		"artifact_count":   m.ArtifactCount,
	}

	// Persisted-only fields: present ONLY in trigger_chat (v1 §3.2a, §3.4).
	if scope == ScopeTriggerChat {
		if m.ID != "" {
			out["id"] = m.ID
		}
		if !m.CreatedAt.IsZero() {
			out["created_at"] = m.CreatedAt.UTC()
		}
	}

	if m.Kind == KindText && m.Intent != "" {
		out["intent"] = string(m.Intent)
	}
	if m.Kind == KindEvent && m.Event != nil {
		out["event"] = projectEvent(*m.Event, scope, visibleEnd)
	}
	if m.Interaction != nil {
		out["interaction"] = map[string]any{
			"kind":  string(m.Interaction.Kind),
			"count": m.Interaction.Count,
		}
	}
	if m.System != nil && m.System.Category != "" {
		out["system"] = map[string]any{
			"category": m.System.Category,
		}
	}
	return out
}

func projectEvent(e EventFacts, scope Scope, visibleEnd *End) map[string]any {
	restrictValueDep := scope == ScopePolicyOut || scope == ScopePolicyIn

	out := map[string]any{
		"type": e.Type,
	}
	if e.EventType != "" {
		out["event_type"] = e.EventType
	}
	if e.EventID != "" {
		out["event_id"] = e.EventID
	}
	if e.FireID != "" {
		out["fire_id"] = e.FireID
	}
	if e.Status != "" {
		out["status"] = e.Status
	}

	// Amendment A1 §B.2: subject, reason, templated_vars are value_dependent and
	// not provable, so they are DROPPED in policy_out and policy_in.
	if !restrictValueDep {
		if e.Reason != "" {
			out["reason"] = e.Reason
		}
		if e.Subject != "" {
			out["subject"] = e.Subject
		}
		if len(e.TemplatedVars) > 0 {
			out["templated_vars"] = slices.Clone(e.TemplatedVars)
		}
	}

	// Amendment A1 §B.2: in policy_out and policy_in, event.source is kept in
	// full ONLY when source.kind in {agent, user} AND source.id == visibleEnd.ID;
	// otherwise only source.kind remains.
	if e.Source != nil {
		src := map[string]any{
			"kind": e.Source.Kind,
		}
		keepSourceIdentity := !restrictValueDep ||
			((e.Source.Kind == "agent" || e.Source.Kind == "user") &&
				visibleEnd != nil && visibleEnd.ID != "" && e.Source.ID == visibleEnd.ID)
		if keepSourceIdentity {
			if e.Source.ID != "" {
				src["id"] = e.Source.ID
			}
			if e.Source.Name != "" {
				src["name"] = e.Source.Name
			}
		}
		out["source"] = src
	}

	if e.Trigger != nil {
		trig := map[string]any{
			"id": e.Trigger.ID,
		}
		if e.Trigger.Name != "" {
			trig["name"] = e.Trigger.Name
		}
		out["trigger"] = trig
	}

	if e.Schedule != nil {
		sched := map[string]any{}
		if e.Schedule.ID != "" {
			sched["id"] = e.Schedule.ID
		}
		if e.Schedule.Name != "" {
			sched["name"] = e.Schedule.Name
		}
		if e.Schedule.EventID != "" {
			sched["event_id"] = e.Schedule.EventID
		}
		if e.Schedule.FireAt != "" {
			sched["fire_at"] = e.Schedule.FireAt
		}
		out["schedule"] = sched
	}

	if e.External != nil {
		out["external"] = map[string]any{
			"present":   e.External.Present,
			"verified":  e.External.Verified,
			"bytes":     e.External.Bytes,
			"truncated": e.External.Truncated,
		}
	}

	return out
}

func projectEnd(e End) map[string]any {
	attrs := map[string]any{
		"name":   e.Attributes.Name,
		"labels": cloneStringMapNonNil(e.Attributes.Labels),
	}
	// v1 §3.3b: attributes.template is present ONLY when template_lookup == ok
	// and template is non-empty; absent on not_found or error.
	if e.Attributes.TemplateLookup != "" {
		attrs["template_lookup"] = string(e.Attributes.TemplateLookup)
	}
	if e.Attributes.TemplateLookup == TemplateLookupOK && e.Attributes.Template != "" {
		attrs["template"] = e.Attributes.Template
	}
	if e.Attributes.TemplateID != "" {
		attrs["template_id"] = e.Attributes.TemplateID
	}
	if e.Attributes.TemplateVersion != "" {
		attrs["template_version"] = e.Attributes.TemplateVersion
	}

	out := map[string]any{
		"kind":       string(e.Kind),
		"id":         e.ID,
		"slug":       e.Slug,
		"project_id": e.ProjectID,
		"attributes": attrs,
	}
	if e.CreatedBy != nil && (e.CreatedBy.Kind != "" || e.CreatedBy.ID != "") {
		out["created_by"] = map[string]any{
			"kind": e.CreatedBy.Kind,
			"id":   e.CreatedBy.ID,
		}
	}
	if e.RootUserID != "" {
		out["root_user_id"] = e.RootUserID
	}
	return out
}

func projectConversation(c ConversationFacts, scope Scope) map[string]any {
	out := map[string]any{
		"kind":        c.Kind,
		"surface":     c.Surface,
		"project_id":  c.ProjectID,
		"thread_id":   c.ThreadID,
		"agent_count": c.AgentCount,
		"user_count":  c.UserCount,
	}

	// D1 (v1 §3.2a, A1 §B.2): in policy_out and policy_in, when
	// conversation.kind == "direct", both conversation.ref and conversation.id
	// are ABSENT because either identifies the opposite end.
	dropDirectIdentity := (scope == ScopePolicyOut || scope == ScopePolicyIn) && c.Kind == "direct"
	if !dropDirectIdentity {
		if c.Ref != "" {
			out["ref"] = c.Ref
		}
		if c.ID != "" {
			out["id"] = c.ID
		}
	}

	// Third-party participant lists: visible in policy_a2a, trigger_chat (Q1),
	// and legacy_policy_v1; ABSENT in policy_out and policy_in.
	if scope == ScopePolicyA2A || scope == ScopeTriggerChat || scope == ScopeLegacyPolicyV1 {
		out["agent_participant_ids"] = cloneStringsNonNil(c.AgentParticipantIDs)
		out["user_participant_ids"] = cloneStringsNonNil(c.UserParticipantIDs)
	}

	// Derived unmentioned_agent_slugs: a2a and legacy_policy_v1 only (v1 §3.5).
	if (scope == ScopePolicyA2A || scope == ScopeLegacyPolicyV1) && c.UnmentionedAgentSlugs != nil {
		out["unmentioned_agent_slugs"] = cloneStringsNonNil(c.UnmentionedAgentSlugs)
	}

	return out
}

func projectMentions(m MentionsFacts, scope Scope) map[string]any {
	out := map[string]any{
		"agent_count": m.AgentCount,
		"user_count":  m.UserCount,
	}
	// Third-party mention lists: visible in policy_a2a and trigger_chat (Q1);
	// ABSENT in policy_out and policy_in.
	if scope == ScopePolicyA2A || scope == ScopeTriggerChat {
		out["agent_ids"] = cloneStringsNonNil(m.AgentIDs)
		out["user_ids"] = cloneStringsNonNil(m.UserIDs)
	}
	return out
}

func projectDelivery(d DeliveryFacts) map[string]any {
	return map[string]any{
		"surface":       d.Surface,
		"via":           d.Via,
		"copy":          d.Copy,
		"cross_project": d.CrossProject,
		"delegated":     d.Delegated,
	}
}

func cloneStringsNonNil(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return slices.Clone(in)
}

func cloneStringMapNonNil(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}
