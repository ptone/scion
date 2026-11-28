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

// Package mcpserver implements the Scion MCP server protocol facade over the
// Hub REST API. It deliberately imports no store, authorization, or identity
// packages: every authorization decision is delegated to the Hub.
package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

const (
	// CredentialClassBearer identifies an Authorization: Bearer credential.
	CredentialClassBearer = "bearer"
	// CredentialClassFederation identifies an X-Scion-Federation-Token credential.
	CredentialClassFederation = "federation"

	// HeaderFederationToken is the inbound and outbound federation OIDC header.
	HeaderFederationToken = "X-Scion-Federation-Token"
	// HeaderScionClient tags outbound Hub requests originating from MCP.
	HeaderScionClient = "X-Scion-Client"
	// ClientHeaderValue is the value sent in X-Scion-Client.
	ClientHeaderValue = "mcp"
)

// Credential holds the caller's Hub credential extracted by the front door.
// Exactly one field is non-empty for an authenticated request.
type Credential struct {
	Bearer     string // forwarded as Authorization: Bearer
	Federation string // forwarded as X-Scion-Federation-Token
}

// Class returns the credential class label for logging and audit records.
func (c Credential) Class() string {
	if c.Bearer != "" {
		return CredentialClassBearer
	}
	if c.Federation != "" {
		return CredentialClassFederation
	}
	return ""
}

// Hash returns a stable SHA-256 hex digest suitable as a cache key.
func (c Credential) Hash() string {
	h := sha256.New()
	_, _ = h.Write([]byte(c.Class()))
	_, _ = h.Write([]byte{0})
	if c.Bearer != "" {
		_, _ = h.Write([]byte(c.Bearer))
	} else {
		_, _ = h.Write([]byte(c.Federation))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Principal describes the Hub caller resolved via GET /api/v1/auth/me.
type Principal struct {
	Kind    string
	ID      string
	Email   string
	Display string
}

// AgentRef is the minimal MCP projection of a project agent (§4.4).
type AgentRef struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
	Phase     string `json:"phase,omitempty"`
	Display   string `json:"display,omitempty"`
}

// ListConvOpts configures HubBackend.ListConversations.
type ListConvOpts struct {
	Project   string
	Limit     int
	PageToken string
}

// ConversationParticipantDTO is a participant summary in a conversation listing
// or metadata resource.
type ConversationParticipantDTO struct {
	Kind    string `json:"kind"` // "user" | "agent"
	ID      string `json:"id"`
	Display string `json:"display,omitempty"`
}

// ConversationSummary is the stable MCP projection of a conversation (§4.4).
type ConversationSummary struct {
	ConversationID string                       `json:"conversation_id"`
	Kind           string                       `json:"kind"`
	Title          string                       `json:"title,omitempty"`
	Participants   []ConversationParticipantDTO `json:"participants"`
	LastMessageAt  *time.Time                   `json:"last_message_at,omitempty"`
	ProjectID      string                       `json:"project_id,omitempty"`
}

// ConvPage is returned by HubBackend.ListConversations.
type ConvPage struct {
	Conversations []ConversationSummary `json:"conversations"`
	NextPageToken string                `json:"next_page_token,omitempty"`
	NotAvailable  bool                  `json:"not_available,omitempty"`
	Reason        string                `json:"reason,omitempty"`
}

// SendResult is returned by HubBackend.SendToAgent, HubBackend.SendToConversation,
// and the send_message tool.
type SendResult struct {
	MessageID      string `json:"message_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Status         string `json:"status"`
	Deferred       string `json:"deferred,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
	Warning        string `json:"warning,omitempty"`
}

// MessageSenderDTO identifies the sender of a message in MCP tool outputs.
type MessageSenderDTO struct {
	Kind    string `json:"kind"` // "user" | "agent"
	ID      string `json:"id"`
	Display string `json:"display,omitempty"`
}

// MessageAttachmentDTO is the minimal attachment projection in MessageDTO.
type MessageAttachmentDTO struct {
	Name string `json:"name"`
	URI  string `json:"uri,omitempty"`
}

// MessageDTO is the stable MCP projection of a conversation message.
// Raw store.Message rows are never returned directly on the MCP wire.
type MessageDTO struct {
	ID             string                 `json:"id"`
	ConversationID string                 `json:"conversation_id"`
	Sender         MessageSenderDTO       `json:"sender"`
	Recipients     []string               `json:"recipients,omitempty"`
	Text           string                 `json:"text"`
	CreatedAt      time.Time              `json:"created_at"`
	ResourceURI    string                 `json:"resource_uri,omitempty"`
	Attachments    []MessageAttachmentDTO `json:"attachments,omitempty"`
}

// projectMessageDTO converts a Hub WatchEvent into a stable MessageDTO.
func projectMessageDTO(ev hubclient.WatchEvent) MessageDTO {
	msg := ev.Data
	if msg == nil {
		return MessageDTO{
			CreatedAt:   ev.OccurredAt,
			ResourceURI: ev.ResourceURI,
		}
	}
	dto := projectHubMessageDTO(msg, msg.ConversationID)
	if ev.ResourceURI != "" {
		dto.ResourceURI = ev.ResourceURI
	}
	if dto.CreatedAt.IsZero() {
		dto.CreatedAt = ev.OccurredAt
	}
	return dto
}

// projectHubMessageDTO converts a hubclient.Message into a stable MessageDTO,
// never exposing raw internal store fields on the MCP wire.
func projectHubMessageDTO(msg *hubclient.Message, convID string) MessageDTO {
	if msg == nil {
		return MessageDTO{ConversationID: convID}
	}
	cid := msg.ConversationID
	if cid == "" {
		cid = convID
	}
	dto := MessageDTO{
		ID:             msg.ID,
		ConversationID: cid,
		Text:           msg.Msg,
		CreatedAt:      msg.CreatedAt,
	}
	if cid != "" && msg.ID != "" {
		dto.ResourceURI = fmt.Sprintf("scion://conversations/%s/messages/%s", cid, msg.ID)
	}

	senderKind, senderDisplay := parsePrincipalLabel(msg.Sender)
	dto.Sender = MessageSenderDTO{
		Kind:    senderKind,
		ID:      msg.SenderID,
		Display: senderDisplay,
	}
	if msg.Recipient != "" {
		dto.Recipients = []string{msg.Recipient}
	}
	if len(msg.Attachments) > 0 {
		dto.Attachments = make([]MessageAttachmentDTO, 0, len(msg.Attachments))
		for _, a := range msg.Attachments {
			dto.Attachments = append(dto.Attachments, MessageAttachmentDTO{
				Name: a.Name,
				URI:  a.Ref,
			})
		}
	}
	return dto
}

// projectConversationSummary converts a hubclient.ConversationDetail into a
// stable ConversationSummary DTO.
func projectConversationSummary(detail hubclient.ConversationDetail) ConversationSummary {
	title := detail.DisplayName
	if title == "" {
		title = detail.ThreadName
	}
	if title == "" && detail.DMPeer != nil && detail.DMPeer.Name != "" {
		title = detail.DMPeer.Name
	}

	participants := make([]ConversationParticipantDTO, 0, len(detail.Participants))
	for _, p := range detail.Participants {
		display := ""
		if detail.DMPeer != nil && detail.DMPeer.ID == p.PrincipalID && detail.DMPeer.Name != "" {
			display = detail.DMPeer.Name
		}
		participants = append(participants, ConversationParticipantDTO{
			Kind:    normalizeParticipantKind(p.PrincipalKind),
			ID:      p.PrincipalID,
			Display: display,
		})
	}
	if len(participants) == 0 && detail.DMPeer != nil {
		participants = append(participants, ConversationParticipantDTO{
			Kind:    normalizeParticipantKind(detail.DMPeer.Kind),
			ID:      detail.DMPeer.ID,
			Display: detail.DMPeer.Name,
		})
	}

	var lastMsgAt *time.Time
	if !detail.LastActivityAt.IsZero() {
		t := detail.LastActivityAt
		lastMsgAt = &t
	}
	var projectID string
	if detail.ProjectID != nil {
		projectID = *detail.ProjectID
	}

	return ConversationSummary{
		ConversationID: detail.ID,
		Kind:           detail.Kind,
		Title:          title,
		Participants:   participants,
		LastMessageAt:  lastMsgAt,
		ProjectID:      projectID,
	}
}

// parsePrincipalLabel splits a Hub sender string like "user:alice" or
// "agent:echo-t" into its kind ("user" or "agent") and display name.
func parsePrincipalLabel(raw string) (kind, display string) {
	prefix, rest, ok := strings.Cut(raw, ":")
	if !ok {
		return "user", raw
	}
	return normalizeParticipantKind(prefix), rest
}

func normalizeParticipantKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "agent":
		return "agent"
	default:
		return "user"
	}
}
