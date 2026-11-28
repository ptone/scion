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
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// MessageInput carries the in-flight envelope fields needed by FromMessage
// without importing pkg/messages, pkg/messaging, or pkg/store.
type MessageInput struct {
	// Persisted identity (present for post-persist trigger_chat; empty for pre-persist policy scopes).
	ID        string
	CreatedAt time.Time

	// Legacy StructuredMessage envelope fields.
	Type        string
	Sender      string
	SenderID    string
	Recipient   string
	Broadcasted bool
	Status      string
	Body        string
	HasChoices  bool
	Urgent      bool
	Metadata    map[string]string
	EventJSON   []byte

	// Derived counts & source metadata.
	BodySource       BodySource
	AttachmentCount  int
	ArtifactCount    int
	InteractionCount int
	IsBridgeSender   bool

	// Pre-built ends and mentions (no I/O inside pkg/rulefacts).
	SenderEnd    *End
	RecipientEnd *End
	Mentions     MentionsFacts
}

// ConversationInfo carries already-loaded conversation metadata for FromMessage.
type ConversationInfo struct {
	Ref                   string
	ID                    string
	Kind                  string // "direct" | "group"
	Surface               string // "native" | "discord" | "slack" | "telegram" | "gchat" | "teams"
	ProjectID             string
	ThreadID              string
	AgentCount            int
	UserCount             int
	AgentParticipantIDs   []string
	UserParticipantIDs    []string
	UnmentionedAgentSlugs []string
}

// DeliveryInfo carries delivery routing metadata for FromMessage.
type DeliveryInfo struct {
	Surface      string
	Via          string
	Copy         bool
	CrossProject bool
	Delegated    bool
}

// AgentInput carries already-loaded agent row fields for EndFromAgent (no pkg/store import).
type AgentInput struct {
	ID            string
	Slug          string
	Name          string
	ProjectID     string
	CreatedByKind string
	CreatedByID   string
	RootUserID    string
	Labels        map[string]string
}

// AppliedConfigInput carries already-loaded AgentAppliedConfig provenance fields.
type AppliedConfigInput struct {
	TemplateID   string
	TemplateHash string
}

// UserInput carries already-loaded user row fields for EndFromUser (no pkg/store import).
type UserInput struct {
	ID          string
	Slug        string
	DisplayName string
	ProjectID   string
	Labels      map[string]string
}

// EndFromAgent constructs a typed End from already-loaded agent data and an
// optional live template slug lookup callback (v1 §3.3b, §4.1).
//
// When liveTemplateSlug returns ("", false) or is nil, Attributes.Template is
// left unset and Attributes.TemplateLookup is set to TemplateLookupNotFound.
func EndFromAgent(a AgentInput, applied *AppliedConfigInput, liveTemplateSlug func() (string, bool)) End {
	var lookup func() (string, TemplateLookupStatus)
	if liveTemplateSlug != nil {
		lookup = func() (string, TemplateLookupStatus) {
			slug, ok := liveTemplateSlug()
			if !ok || slug == "" {
				return "", TemplateLookupNotFound
			}
			return slug, TemplateLookupOK
		}
	}
	return EndFromAgentWithLookup(a, applied, lookup)
}

// EndFromAgentWithLookup constructs a typed End like EndFromAgent, while
// allowing callers to distinguish TemplateLookupNotFound from
// TemplateLookupError (v1 §3.3b).
func EndFromAgentWithLookup(a AgentInput, applied *AppliedConfigInput, lookup func() (string, TemplateLookupStatus)) End {
	end := End{
		Kind:       EndKindAgent,
		ID:         a.ID,
		Slug:       a.Slug,
		ProjectID:  a.ProjectID,
		RootUserID: a.RootUserID,
		Attributes: EndAttributes{
			Name:   a.Name,
			Labels: cloneStringMap(a.Labels),
		},
	}
	if a.CreatedByKind != "" || a.CreatedByID != "" {
		end.CreatedBy = &PrincipalRef{
			Kind: a.CreatedByKind,
			ID:   a.CreatedByID,
		}
	}
	if applied != nil {
		end.Attributes.TemplateID = applied.TemplateID
		end.Attributes.TemplateVersion = applied.TemplateHash
	}
	if lookup == nil {
		end.Attributes.TemplateLookup = TemplateLookupNotFound
	} else {
		slug, status := lookup()
		switch status {
		case TemplateLookupOK:
			if slug == "" {
				end.Attributes.TemplateLookup = TemplateLookupNotFound
			} else {
				end.Attributes.Template = slug
				end.Attributes.TemplateLookup = TemplateLookupOK
			}
		case TemplateLookupError:
			end.Attributes.TemplateLookup = TemplateLookupError
		default:
			end.Attributes.TemplateLookup = TemplateLookupNotFound
		}
	}
	return end
}

// EndFromUser constructs a typed End from already-loaded user data (v1 §4.1).
func EndFromUser(u UserInput) End {
	return End{
		Kind:      EndKindUser,
		ID:        u.ID,
		Slug:      u.Slug,
		ProjectID: u.ProjectID,
		Attributes: EndAttributes{
			Name:   u.DisplayName,
			Labels: cloneStringMap(u.Labels),
		},
	}
}

// TruncateBodyUTF8 truncates body to at most maxBytes bytes without splitting a
// multi-byte UTF-8 rune, returning the truncated string, original byte length,
// and whether truncation occurred.
func TruncateBodyUTF8(body string, maxBytes int) (string, int, bool) {
	origBytes := len(body)
	if maxBytes < 0 {
		maxBytes = 0
	}
	if origBytes <= maxBytes {
		return body, origBytes, false
	}
	end := maxBytes
	start := end
	for start > 0 && end-start < utf8.UTFMax && !utf8.RuneStart(body[start]) {
		start--
	}
	if start < end {
		r, size := utf8.DecodeRuneInString(body[start:])
		if r != utf8.RuneError && start+size > maxBytes {
			end = start
		}
	}
	return body[:end], origBytes, true
}

// FromMessage builds typed, unprojected Facts from an in-flight message
// envelope and already-loaded conversation/delivery metadata (v1 §3.2, §4.1).
//
// It mirrors pkg/messaging.MapLegacyEnvelope's kind/intent/event mapping and
// hub-minted sender guards so an agent-sent type=system message with an Event
// payload never yields hub-stamped event sub-blocks.
func FromMessage(in MessageInput, conv ConversationInfo, deliv DeliveryInfo) (Facts, error) {
	if strings.TrimSpace(in.Type) == "" {
		return Facts{}, errors.New("rulefacts: MessageInput.Type must not be empty")
	}

	hasAddressee := in.Recipient != "" && !in.Broadcasted
	isHubTrigger := isHubMintedTriggerSender(in)
	isHubScheduler := isHubMintedSchedulerSender(in)

	// Neutralize forged system_category="trigger" from non-trigger senders
	// (mirrors pkg/messaging/envelope_compat.go:212-215).
	systemCategory := ""
	if in.Metadata != nil {
		systemCategory = in.Metadata["system_category"]
	}
	if strings.EqualFold(strings.TrimSpace(systemCategory), "trigger") && !isHubTrigger {
		systemCategory = ""
	}

	kind, intent, ev := mapLegacyType(in.Type, systemCategory, hasAddressee)

	// Human-to-human replies are inform, not request (envelope_compat.go:246-249).
	if in.Type == "reply" && !strings.HasPrefix(in.Recipient, "agent:") {
		intent = IntentInform
	}

	// Honor EventJSON ONLY for hub-minted trigger or scheduler senders
	// (envelope_compat.go:225-241). Client/agent/user messages never override
	// EventFacts or inject Trigger/Schedule/External/Source/TemplatedVars.
	if kind == KindEvent && len(in.EventJSON) > 0 {
		if isHubTrigger {
			if parsed, ok := parseHubEventJSON(in.EventJSON, false); ok {
				ev = parsed
			}
		} else if isHubScheduler {
			if parsed, ok := parseHubEventJSON(in.EventJSON, true); ok {
				ev = parsed
			}
		}
	}

	// Enrich event body from envelope fields (envelope_compat.go:252-260).
	if ev != nil {
		if in.Status != "" && ev.Status == "" {
			ev.Status = in.Status
		}
		if ev.Type == "agent.state-changed" && in.Sender != "" && ev.Subject == "" {
			ev.Subject = in.Sender
		}
	}

	origin := deriveOrigin(in, conv.Surface, isHubTrigger, isHubScheduler)

	// Body truncation & body_source.
	truncBody, origBytes, wasTruncated := TruncateBodyUTF8(in.Body, MaxBodyBytes)
	bodySource := in.BodySource
	if bodySource == "" {
		switch {
		case in.ID != "":
			bodySource = BodySourcePersisted
		case deliv.Copy:
			bodySource = BodySourceCopyBody
		default:
			bodySource = BodySourceEvaluatedInput
		}
	}

	var interaction *InteractionFacts
	if ik, ok := mapInteractionKind(in.Type); ok {
		interaction = &InteractionFacts{
			Kind:  ik,
			Count: int64(in.InteractionCount),
		}
	}

	var sysFacts *SystemFacts
	if in.Type == "system" && systemCategory != "" {
		sysFacts = &SystemFacts{Category: systemCategory}
	}

	convRef := conv.Ref
	if convRef == "" && conv.Kind == "group" {
		convRef = conv.ID
	}
	convSurface := conv.Surface
	if convSurface == "" {
		convSurface = "native"
	}

	agentCount := int64(conv.AgentCount)
	if agentCount == 0 && len(conv.AgentParticipantIDs) > 0 {
		agentCount = int64(len(conv.AgentParticipantIDs))
	}
	userCount := int64(conv.UserCount)
	if userCount == 0 && len(conv.UserParticipantIDs) > 0 {
		userCount = int64(len(conv.UserParticipantIDs))
	}

	mentionAgentCount := in.Mentions.AgentCount
	if mentionAgentCount == 0 && len(in.Mentions.AgentIDs) > 0 {
		mentionAgentCount = int64(len(in.Mentions.AgentIDs))
	}
	mentionUserCount := in.Mentions.UserCount
	if mentionUserCount == 0 && len(in.Mentions.UserIDs) > 0 {
		mentionUserCount = int64(len(in.Mentions.UserIDs))
	}

	crossProject := deliv.CrossProject
	if !crossProject && in.SenderEnd != nil && in.RecipientEnd != nil &&
		in.SenderEnd.ProjectID != "" && in.RecipientEnd.ProjectID != "" &&
		in.SenderEnd.ProjectID != in.RecipientEnd.ProjectID {
		crossProject = true
	}

	return Facts{
		Message: MessageFacts{
			ID:              in.ID,
			CreatedAt:       in.CreatedAt,
			Kind:            kind,
			Intent:          intent,
			Type:            in.Type,
			Origin:          origin,
			Body:            truncBody,
			Bytes:           int64(origBytes),
			BodyTruncated:   wasTruncated,
			BodySource:      bodySource,
			HasChoices:      in.HasChoices,
			Urgent:          in.Urgent,
			AttachmentCount: int64(in.AttachmentCount),
			ArtifactCount:   int64(in.ArtifactCount),
			Event:           ev,
			Interaction:     interaction,
			System:          sysFacts,
		},
		Sender:    cloneEnd(in.SenderEnd),
		Recipient: cloneEnd(in.RecipientEnd),
		Conversation: ConversationFacts{
			Ref:                   convRef,
			ID:                    conv.ID,
			Kind:                  conv.Kind,
			Surface:               convSurface,
			ProjectID:             conv.ProjectID,
			ThreadID:              conv.ThreadID,
			AgentCount:            agentCount,
			UserCount:             userCount,
			AgentParticipantIDs:   slices.Clone(conv.AgentParticipantIDs),
			UserParticipantIDs:    slices.Clone(conv.UserParticipantIDs),
			UnmentionedAgentSlugs: slices.Clone(conv.UnmentionedAgentSlugs),
		},
		Mentions: MentionsFacts{
			AgentCount: mentionAgentCount,
			UserCount:  mentionUserCount,
			AgentIDs:   slices.Clone(in.Mentions.AgentIDs),
			UserIDs:    slices.Clone(in.Mentions.UserIDs),
		},
		Delivery: DeliveryFacts{
			Surface:      deliv.Surface,
			Via:          deliv.Via,
			Copy:         deliv.Copy,
			CrossProject: crossProject,
			Delegated:    deliv.Delegated,
		},
	}, nil
}

func isHubMintedTriggerSender(in MessageInput) bool {
	return in.Type == "system" &&
		in.Sender == "trigger" &&
		in.SenderID == "TRIGGER" &&
		in.Metadata != nil &&
		in.Metadata["system_category"] == "trigger"
}

func isHubMintedSchedulerSender(in MessageInput) bool {
	return in.Type == "system" &&
		in.Sender == "scheduler" &&
		in.SenderID == "SCHEDULER" &&
		in.Metadata != nil &&
		in.Metadata["system_category"] == "scheduler"
}

func deriveOrigin(in MessageInput, convSurface string, isHubTrigger, isHubScheduler bool) MessageOrigin {
	if isHubTrigger {
		return OriginTrigger
	}
	if isHubScheduler {
		return OriginScheduler
	}
	if in.IsBridgeSender && convSurface != "" && convSurface != "native" {
		return OriginBridge
	}
	switch {
	case strings.HasPrefix(in.Sender, "user:"):
		return OriginUser
	case strings.HasPrefix(in.Sender, "agent:"):
		return OriginAgent
	case strings.HasPrefix(in.Sender, "system:"):
		return OriginSystem
	}
	if in.SenderEnd != nil {
		switch in.SenderEnd.Kind {
		case EndKindUser:
			return OriginUser
		case EndKindAgent:
			return OriginAgent
		}
	}
	return OriginSystem
}

func mapLegacyType(oldType, systemCategory string, hasAddressee bool) (MessageKind, TextIntent, *EventFacts) {
	switch oldType {
	case "instruction", "reply", "mention", "group-set":
		return KindText, IntentRequest, nil
	case "chat", "assistant-reply", "decision-response", "secret-response", "command-response":
		return KindText, IntentInform, nil
	case "decision-prompt", "secret-request", "command-request":
		return KindText, IntentQuestion, nil
	case "input-needed":
		if hasAddressee {
			return KindText, IntentQuestion, nil
		}
		return KindEvent, "", &EventFacts{Type: "agent.input-needed"}
	case "state-change":
		return KindEvent, "", &EventFacts{Type: "agent.state-changed"}
	case "system":
		return KindEvent, "", mapSystemCategory(systemCategory)
	default:
		return KindText, IntentInform, nil
	}
}

func mapSystemCategory(category string) *EventFacts {
	switch category {
	case "scheduler":
		return &EventFacts{Type: "schedule.fired"}
	case "trigger":
		return &EventFacts{Type: "trigger.fired"}
	case "port-forward":
		return &EventFacts{Type: "port.exposed"}
	case "artifact-review":
		return &EventFacts{Type: "artifact.review"}
	case "delivery-failed":
		return &EventFacts{Type: "delivery.failed"}
	case "delivery-deferred":
		return &EventFacts{Type: "delivery.failed", Status: "DELIVERY_DEFERRED"}
	case "delivery-unconfirmed":
		return &EventFacts{Type: "delivery.failed", Status: "DELIVERY_UNCONFIRMED"}
	default:
		return &EventFacts{Type: "agent.state-changed"}
	}
}

func mapInteractionKind(msgType string) (InteractionKind, bool) {
	switch msgType {
	case "decision-prompt":
		return InteractionDecisionPrompt, true
	case "decision-response":
		return InteractionDecisionResponse, true
	case "secret-request":
		return InteractionSecretRequest, true
	case "secret-response":
		return InteractionSecretResponse, true
	case "command-request":
		return InteractionCommandRequest, true
	case "command-response":
		return InteractionCommandResponse, true
	default:
		return "", false
	}
}

var validEventTypes = map[string]bool{
	"agent.state-changed": true,
	"agent.input-needed":  true,
	"delivery.failed":     true,
	"schedule.fired":      true,
	"port.exposed":        true,
	"artifact.review":     true,
	"webhook.received":    true,
	"trigger.fired":       true,
}

// wireEventBody mirrors pkg/messaging.EventBody JSON tags for deserialization
// without importing pkg/messaging. Attributes, Lists, URL, and External.JSON /
// External.Text are intentionally never copied into EventFacts (A1 §B.1, v1 §3.2).
type wireEventBody struct {
	Type          string              `json:"type"`
	Subject       string              `json:"subject,omitempty"`
	Status        string              `json:"status,omitempty"`
	Reason        string              `json:"reason,omitempty"`
	EventID       string              `json:"event_id,omitempty"`
	FireID        string              `json:"fire_id,omitempty"`
	EventType     string              `json:"event_type,omitempty"`
	Source        *EventSource        `json:"source,omitempty"`
	Trigger       *EventTrigger       `json:"trigger,omitempty"`
	Schedule      *EventSchedule      `json:"schedule,omitempty"`
	TemplatedVars []string            `json:"templated_vars,omitempty"`
	External      *wireExternalHeader `json:"external_payload,omitempty"`
}

type wireExternalHeader struct {
	Verified  string `json:"verified"`
	Truncated bool   `json:"truncated,omitempty"`
	Bytes     int64  `json:"bytes"`
}

func parseHubEventJSON(raw []byte, schedulerOnly bool) (*EventFacts, bool) {
	var wire wireEventBody
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, false
	}
	if !validEventTypes[wire.Type] {
		return nil, false
	}
	if schedulerOnly {
		if wire.Type != "schedule.fired" || wire.External != nil || wire.Trigger != nil {
			return nil, false
		}
	}
	ev := &EventFacts{
		Type:          wire.Type,
		EventType:     wire.EventType,
		EventID:       wire.EventID,
		FireID:        wire.FireID,
		Status:        wire.Status,
		Reason:        wire.Reason,
		Subject:       wire.Subject,
		Source:        cloneEventSource(wire.Source),
		Trigger:       cloneEventTrigger(wire.Trigger),
		Schedule:      cloneEventSchedule(wire.Schedule),
		TemplatedVars: slices.Clone(wire.TemplatedVars),
	}
	if wire.External != nil {
		ev.External = &EventExternal{
			Present:   true,
			Verified:  wire.External.Verified,
			Bytes:     wire.External.Bytes,
			Truncated: wire.External.Truncated,
		}
	}
	return ev, true
}

func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}

func cloneEnd(e *End) *End {
	if e == nil {
		return nil
	}
	cp := *e
	if e.CreatedBy != nil {
		cb := *e.CreatedBy
		cp.CreatedBy = &cb
	}
	cp.Attributes.Labels = cloneStringMap(e.Attributes.Labels)
	return &cp
}

func cloneEventSource(s *EventSource) *EventSource {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

func cloneEventTrigger(t *EventTrigger) *EventTrigger {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func cloneEventSchedule(s *EventSchedule) *EventSchedule {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}
