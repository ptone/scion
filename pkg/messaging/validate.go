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

package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// AgentProjectLookup is the minimal interface needed to check which project
// an agent belongs to. Implementations include store.Store.
type AgentProjectLookup interface {
	GetAgent(ctx context.Context, id string) (*store.Agent, error)
}

// ValidateMessage checks that a Message is internally consistent.
// Every rule has a corresponding test that fails when the rule is removed.
func ValidateMessage(msg *Message) error {
	if msg == nil {
		return fmt.Errorf("message must not be nil")
	}
	// 1. ConversationID is required.
	if msg.ConversationID == "" {
		return fmt.Errorf("conversation_id is required")
	}
	// 2. From is required and well-formed.
	if msg.From == "" {
		return fmt.Errorf("from is required")
	}
	if err := ValidatePrincipalRef(msg.From); err != nil {
		return fmt.Errorf("invalid from: %w", err)
	}
	// 3. Kind must be valid.
	if err := ValidateMessageKind(msg.Kind); err != nil {
		return err
	}
	// 4–6. Kind / intent / event mutual-exclusivity.
	switch msg.Kind {
	case KindText:
		if msg.Intent == nil {
			return fmt.Errorf("text message must have intent set")
		}
		if err := ValidateTextIntent(*msg.Intent); err != nil {
			return err
		}
		if msg.Event != nil {
			return fmt.Errorf("text message must not have event body")
		}
	case KindEvent:
		if msg.Event == nil {
			return fmt.Errorf("event message must have event body set")
		}
		if err := msg.Event.Validate(); err != nil {
			return fmt.Errorf("invalid event body: %w", err)
		}
		if msg.Intent != nil {
			return fmt.Errorf("event message must not have intent")
		}
	}
	// 7. Body size limits (reuse constants from messages package).
	if len([]rune(msg.Body)) > messages.MaxMessageLength {
		return fmt.Errorf("body exceeds %d character limit (current: %d chars)",
			messages.MaxMessageLength, len([]rune(msg.Body)))
	}
	if len(msg.Body) > messages.MaxMsgSize {
		return fmt.Errorf("body exceeds maximum size of %d bytes", messages.MaxMsgSize)
	}
	// 8. Attachment count limit.
	if len(msg.Attachments) > messages.MaxAttachments {
		return fmt.Errorf("too many attachments: %d (max %d)",
			len(msg.Attachments), messages.MaxAttachments)
	}
	// 9. Visibility is valid if set.
	if err := ValidateVisibility(msg.Visibility); err != nil {
		return err
	}
	// 10. ReplyToID, if present, must not be empty string.
	if msg.ReplyToID != nil && *msg.ReplyToID == "" {
		return fmt.Errorf("reply_to_id must not be empty when set")
	}
	return nil
}

// ValidateAddressees checks the addressee list for a message.
func ValidateAddressees(addrs []Addressee, msg *Message) error {
	seen := make(map[string]bool, len(addrs))
	for i, a := range addrs {
		// 1. PrincipalKind must be "user" or "agent".
		if a.PrincipalKind != "user" && a.PrincipalKind != "agent" {
			return fmt.Errorf("addressee[%d]: principal_kind must be user or agent, got %q", i, a.PrincipalKind)
		}
		// 2. PrincipalID must not be empty.
		if a.PrincipalID == "" {
			return fmt.Errorf("addressee[%d]: principal_id is required", i)
		}
		// 3. Via must be valid.
		if err := ValidateAddressedVia(a.Via); err != nil {
			return fmt.Errorf("addressee[%d]: %w", i, err)
		}
		// 4. DeliveryState must be valid.
		if err := ValidateDeliveryState(a.DeliveryState); err != nil {
			return fmt.Errorf("addressee[%d]: %w", i, err)
		}
		// 5. No duplicate addressees (same PrincipalKind + PrincipalID).
		key := a.PrincipalKind + ":" + a.PrincipalID
		if seen[key] {
			return fmt.Errorf("addressee[%d]: duplicate addressee %s", i, key)
		}
		seen[key] = true
	}
	return nil
}

// ValidateCrossProjectAddressees checks that all agent addressees belong to
// the same project. This prevents a single message from addressing agents
// across project boundaries, which would violate project isolation (AC-33 / DEF-2).
//
// User addressees are ignored — the rule applies to agent addressees only.
func ValidateCrossProjectAddressees(
	ctx context.Context,
	agentStore AgentProjectLookup,
	addrs []Addressee,
) error {
	var projectID string
	var projectAgents []string // for error reporting

	for _, a := range addrs {
		if a.PrincipalKind != "agent" {
			continue
		}
		agent, err := agentStore.GetAgent(ctx, a.PrincipalID)
		if err != nil {
			return fmt.Errorf("failed to look up agent %q: %w", a.PrincipalID, err)
		}
		if projectID == "" {
			projectID = agent.ProjectID
			projectAgents = append(projectAgents, a.PrincipalID)
			continue
		}
		if agent.ProjectID != projectID {
			projectAgents = append(projectAgents, a.PrincipalID)
			// Collect the conflicting projects.
			projects := []string{projectID, agent.ProjectID}
			return fmt.Errorf(
				"message addresses agents in multiple projects (%s); "+
					"a single message may only target agents within one project",
				strings.Join(projects, ", "),
			)
		}
		projectAgents = append(projectAgents, a.PrincipalID)
	}
	return nil
}
