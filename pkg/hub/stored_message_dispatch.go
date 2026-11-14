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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// errStoredMessageUnrecoverable reports that a stored message row lacks an
// input the dispatch envelope needs. A re-drive must not fall back to
// sending the raw body without attribution (ptone/scion#7949).
var errStoredMessageUnrecoverable = errors.New("stored message envelope is not recoverable")

// storedMessageDispatch rebuilds the dispatch of an already stored message
// row for a re-drive (ptone/scion#7949): the returned context carries the
// hub message id (withDispatchMessageID), so the broker reports buffered
// failures against the row and Conduit binds and dedups by it
// (ptone/scion#7353), and the returned envelope is rebuilt from the row's
// stored fields, so the sender stays attributed. Every re-drive of a stored
// row uses it: reconcile's deliverMessage and the hold-until-session
// re-drive (ptone/scion#7881).
//
// It is pure: no store reads, no rendering. What the row does not store is
// not reproduced: Plain, metadata (artifact references) and attachments
// stay unset, and DeliveryText is not rendered. The broker formats a
// re-dispatched row from the structured fields, so a message originally
// sent plain arrives in the standard envelope; attribution is kept, which
// errs on the safe side. Attachments over a re-drive are ptone/scion#7939.
// Provenance and delegation stay on the row; the envelope has no fields
// for them, as on the live send path.
//
// A row missing an envelope input (id, sender, recipient, body, creation
// time) or carrying a type outside the closed enum returns an error
// wrapping errStoredMessageUnrecoverable and dispatches nothing.
func storedMessageDispatch(ctx context.Context, row *store.Message) (context.Context, *messages.StructuredMessage, error) {
	if row == nil {
		return ctx, nil, fmt.Errorf("%w: no message", errStoredMessageUnrecoverable)
	}
	var missing string
	switch {
	case row.ID == "":
		missing = "id"
	case row.Sender == "":
		missing = "sender"
	case row.Recipient == "":
		missing = "recipient"
	case row.Msg == "":
		missing = "body"
	case row.CreatedAt.IsZero():
		missing = "creation time"
	}
	if missing != "" {
		return ctx, nil, fmt.Errorf("%w: message %q has no %s", errStoredMessageUnrecoverable, row.ID, missing)
	}
	if err := messages.ValidateType(row.Type); err != nil {
		return ctx, nil, fmt.Errorf("%w: message %q: %v", errStoredMessageUnrecoverable, row.ID, err)
	}
	sm := &messages.StructuredMessage{
		Version:         messages.Version,
		Timestamp:       row.CreatedAt.UTC().Format(time.RFC3339),
		Sender:          row.Sender,
		SenderID:        row.SenderID,
		Recipient:       row.Recipient,
		RecipientID:     row.RecipientID,
		Msg:             row.Msg,
		Type:            row.Type,
		Urgent:          row.Urgent,
		Broadcasted:     row.Broadcasted,
		Channel:         row.Channel,
		ThreadID:        row.ThreadID,
		ConversationID:  row.ConversationID,
		StoredMessageID: row.ID,
	}
	return withDispatchMessageID(ctx, row.ID), sm, nil
}
