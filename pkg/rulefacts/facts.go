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

import "time"

// MessageKind mirrors messaging.MessageKind (contract v1 §3.2).
type MessageKind string

const (
	KindText  MessageKind = "text"
	KindEvent MessageKind = "event"
)

// TextIntent mirrors messaging.TextIntent (contract v1 §3.2).
type TextIntent string

const (
	IntentInform   TextIntent = "inform"
	IntentRequest  TextIntent = "request"
	IntentQuestion TextIntent = "question"
)

// MessageOrigin identifies the authenticated or hub-minted producer of a message (v1 §3.2, §3.3a).
type MessageOrigin string

const (
	OriginUser      MessageOrigin = "user"
	OriginAgent     MessageOrigin = "agent"
	OriginTrigger   MessageOrigin = "trigger"
	OriginScheduler MessageOrigin = "scheduler"
	OriginSystem    MessageOrigin = "system"
	OriginBridge    MessageOrigin = "bridge"
)

// BodySource identifies which text surface message.body represents (v1 §3.2a).
type BodySource string

const (
	BodySourceEvaluatedInput BodySource = "evaluated_input"
	BodySourceCopyBody       BodySource = "copy_body"
	BodySourcePersisted      BodySource = "persisted"
)

// TemplateLookupStatus reports the outcome of a live template slug lookup (v1 §3.3b).
type TemplateLookupStatus string

const (
	TemplateLookupOK       TemplateLookupStatus = "ok"
	TemplateLookupNotFound TemplateLookupStatus = "not_found"
	TemplateLookupError    TemplateLookupStatus = "error"
)

// EndKind identifies the kind of principal at a message end.
type EndKind string

const (
	EndKindAgent        EndKind = "agent"
	EndKindUser         EndKind = "user"
	EndKindSystem       EndKind = "system"
	EndKindConversation EndKind = "conversation"
)

// InteractionKind identifies hub-minted interaction message types (v1 §3.2).
type InteractionKind string

const (
	InteractionDecisionPrompt   InteractionKind = "decision_prompt"
	InteractionDecisionResponse InteractionKind = "decision_response"
	InteractionSecretRequest    InteractionKind = "secret_request"
	InteractionSecretResponse   InteractionKind = "secret_response"
	InteractionCommandRequest   InteractionKind = "command_request"
	InteractionCommandResponse  InteractionKind = "command_response"
)

// PrincipalRef identifies a principal by kind and id.
type PrincipalRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// EndAttributes holds agent/user descriptive and provenance attributes (v1 §3.1).
type EndAttributes struct {
	Name            string               `json:"name,omitempty"`
	Template        string               `json:"template,omitempty"`
	TemplateLookup  TemplateLookupStatus `json:"template_lookup,omitempty"`
	TemplateID      string               `json:"template_id,omitempty"`
	TemplateVersion string               `json:"template_version,omitempty"`
	Labels          map[string]string    `json:"labels,omitempty"`
}

// End represents a message sender or recipient principal (v1 §3.1).
type End struct {
	Kind       EndKind       `json:"kind"`
	ID         string        `json:"id"`
	Slug       string        `json:"slug,omitempty"`
	ProjectID  string        `json:"project_id,omitempty"`
	CreatedBy  *PrincipalRef `json:"created_by,omitempty"`
	RootUserID string        `json:"root_user_id,omitempty"`
	Attributes EndAttributes `json:"attributes"`
}

// EventSource carries hub-stamped source identity on an event message.
type EventSource struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// EventTrigger carries hub-stamped trigger metadata on a trigger.fired event.
type EventTrigger struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// EventSchedule carries hub-stamped schedule metadata on a schedule.fired event.
type EventSchedule struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	EventID string `json:"event_id,omitempty"`
	FireAt  string `json:"fire_at,omitempty"`
}

// EventExternal carries integrity-verified metadata about an external payload
// (never the payload bytes themselves; v1 §3.2).
type EventExternal struct {
	Present   bool   `json:"present"`
	Verified  string `json:"verified"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

// EventFacts carries the structured payload for KindEvent messages (v1 §3.2).
// EventBody.Attributes, Lists, and URL are intentionally excluded in v1 (A1 §B.1).
type EventFacts struct {
	Type          string         `json:"type"`
	EventType     string         `json:"event_type,omitempty"`
	EventID       string         `json:"event_id,omitempty"`
	FireID        string         `json:"fire_id,omitempty"`
	Status        string         `json:"status,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Subject       string         `json:"subject,omitempty"`
	Source        *EventSource   `json:"source,omitempty"`
	Trigger       *EventTrigger  `json:"trigger,omitempty"`
	Schedule      *EventSchedule `json:"schedule,omitempty"`
	External      *EventExternal `json:"external,omitempty"`
	TemplatedVars []string       `json:"templated_vars,omitempty"`
}

// InteractionFacts carries the structured payload for hub-minted interaction
// message types (v1 §3.2). Decision/secret IDs and command text are excluded.
type InteractionFacts struct {
	Kind  InteractionKind `json:"kind"`
	Count int64           `json:"count"`
}

// SystemFacts carries the system_category descriptor for system messages (v1 §3.2).
type SystemFacts struct {
	Category string `json:"category"`
}

// MessageFacts holds all message-level facts (v1 §3.1, §3.2, §3.2a).
type MessageFacts struct {
	ID              string            `json:"id,omitempty"`
	CreatedAt       time.Time         `json:"created_at,omitempty"`
	Kind            MessageKind       `json:"kind"`
	Intent          TextIntent        `json:"intent,omitempty"`
	Type            string            `json:"type"`
	Origin          MessageOrigin     `json:"origin"`
	Body            string            `json:"body"`
	Bytes           int64             `json:"bytes"`
	BodyTruncated   bool              `json:"body_truncated"`
	BodySource      BodySource        `json:"body_source"`
	HasChoices      bool              `json:"has_choices"`
	Urgent          bool              `json:"urgent"`
	AttachmentCount int64             `json:"attachment_count"`
	ArtifactCount   int64             `json:"artifact_count"`
	Event           *EventFacts       `json:"event,omitempty"`
	Interaction     *InteractionFacts `json:"interaction,omitempty"`
	System          *SystemFacts      `json:"system,omitempty"`
}

// ConversationFacts holds conversation metadata and participant lists (v1 §3.1).
type ConversationFacts struct {
	Ref                   string   `json:"ref,omitempty"`
	ID                    string   `json:"id,omitempty"`
	Kind                  string   `json:"kind"`
	Surface               string   `json:"surface,omitempty"`
	ProjectID             string   `json:"project_id,omitempty"`
	ThreadID              string   `json:"thread_id,omitempty"`
	AgentCount            int64    `json:"agent_count"`
	UserCount             int64    `json:"user_count"`
	AgentParticipantIDs   []string `json:"agent_participant_ids,omitempty"`
	UserParticipantIDs    []string `json:"user_participant_ids,omitempty"`
	UnmentionedAgentSlugs []string `json:"unmentioned_agent_slugs,omitempty"`
}

// MentionsFacts holds mention counts and resolved identity lists (v1 §3.1).
type MentionsFacts struct {
	AgentCount int64    `json:"agent_count"`
	UserCount  int64    `json:"user_count"`
	AgentIDs   []string `json:"agent_ids,omitempty"`
	UserIDs    []string `json:"user_ids,omitempty"`
}

// DeliveryFacts holds delivery routing and provenance metadata (v1 §3.1).
type DeliveryFacts struct {
	Surface      string `json:"surface"`
	Via          string `json:"via,omitempty"`
	Copy         bool   `json:"copy"`
	CrossProject bool   `json:"cross_project"`
	Delegated    bool   `json:"delegated"`
}

// Facts is the complete, unprojected, typed message-facts record built from
// hub-loaded state before scope projection (v1 §4.1).
type Facts struct {
	Message      MessageFacts      `json:"message"`
	Sender       *End              `json:"sender,omitempty"`
	Recipient    *End              `json:"recipient,omitempty"`
	Conversation ConversationFacts `json:"conversation"`
	Mentions     MentionsFacts     `json:"mentions"`
	Delivery     DeliveryFacts     `json:"delivery"`
}
