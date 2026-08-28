package messaging

import (
	"context"
	"errors"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
)

// failingRestamp wraps the mock and makes message re-stamping always fail.
type failingRestamp struct{ *mockMigrationStore }

func (f *failingRestamp) SetMessageConversationID(_ context.Context, _, _ string) error {
	return errors.New("simulated DB failure during re-stamp")
}

// PROBE A: does a merge that fails to move messages still delete the old row?
func TestProbeA_MergeDeletesOldRowDespiteRestampFailure(t *testing.T) {
	ms := newMockMigrationStore()
	ctx := context.Background()
	userID, agentID := uuid.NewString(), uuid.NewString()
	oldConvID, newConvID := uuid.NewString(), uuid.NewString()

	ms.users[userID] = &store.User{ID: userID, Email: "t@e.com"}
	ms.agents[agentID] = &store.Agent{ID: agentID, Slug: "a"}
	ms.addConv(&store.Conversation{ID: oldConvID, Kind: "direct", Surface: "native", ExternalRef: ""},
		store.ConversationParticipant{ConversationID: oldConvID, PrincipalKind: "user", PrincipalID: userID},
		store.ConversationParticipant{ConversationID: oldConvID, PrincipalKind: "agent", PrincipalID: agentID})
	ms.addConv(&store.Conversation{ID: newConvID, Kind: "direct", Surface: "native",
		ExternalRef: mustDMKey("user", userID, "agent", agentID)},
		store.ConversationParticipant{ConversationID: newConvID, PrincipalKind: "user", PrincipalID: userID},
		store.ConversationParticipant{ConversationID: newConvID, PrincipalKind: "agent", PrincipalID: agentID})
	msgID := uuid.NewString()
	ms.addMessage(&store.Message{ID: msgID, ConversationID: oldConvID, Msg: "important history"})

	svc := NewDMMigrationService(&failingRestamp{ms})
	result, err := svc.Run(ctx, DMMigrationConfig{})

	t.Logf("Run() error            = %v", err)
	t.Logf("EmptyRefMerged counter = %d", result.EmptyRefMerged)
	t.Logf("result.Errors          = %v", result.Errors)
	t.Logf("old conv DeletedAt     = %v", ms.conversations[oldConvID].DeletedAt)
	t.Logf("message still points to= %s (old=%s new=%s)", ms.messages[msgID].ConversationID, oldConvID, newConvID)

	if ms.conversations[oldConvID].DeletedAt != nil && ms.messages[msgID].ConversationID == oldConvID {
		t.Errorf("DATA LOSS: old conversation soft-deleted while its message was left behind on it")
	}
	if err == nil && len(result.Errors) > 0 {
		t.Errorf("SILENT FAILURE: Run() returned nil error with %d recorded errors", len(result.Errors))
	}
}

// PROBE B: does merge copy a participant the target DM key does not name?
func TestProbeB_MergeInjectsForeignParticipant(t *testing.T) {
	ms := newMockMigrationStore()
	ctx := context.Background()
	userID, agentID := uuid.NewString(), uuid.NewString()
	strangerID := uuid.NewString() // NOT named by either key
	oldConvID, newConvID := uuid.NewString(), uuid.NewString()

	ms.users[userID] = &store.User{ID: userID, Email: "t@e.com"}
	ms.users[strangerID] = &store.User{ID: strangerID, Email: "stranger@e.com"}
	ms.agents[agentID] = &store.Agent{ID: agentID, Slug: "a"}

	// Old-format row (step 3b) whose participant table has drifted to include a stranger.
	ms.addConv(&store.Conversation{ID: oldConvID, Kind: "direct", Surface: "native",
		ExternalRef: "dm:" + userID + ":" + agentID},
		store.ConversationParticipant{ConversationID: oldConvID, PrincipalKind: "user", PrincipalID: userID},
		store.ConversationParticipant{ConversationID: oldConvID, PrincipalKind: "agent", PrincipalID: agentID},
		store.ConversationParticipant{ConversationID: oldConvID, PrincipalKind: "user", PrincipalID: strangerID})

	ms.addConv(&store.Conversation{ID: newConvID, Kind: "direct", Surface: "native",
		ExternalRef: mustDMKey("user", userID, "agent", agentID)},
		store.ConversationParticipant{ConversationID: newConvID, PrincipalKind: "user", PrincipalID: userID},
		store.ConversationParticipant{ConversationID: newConvID, PrincipalKind: "agent", PrincipalID: agentID})

	svc := NewDMMigrationService(ms)
	_, _ = svc.Run(ctx, DMMigrationConfig{})

	for _, p := range ms.participants[newConvID] {
		t.Logf("target DM participant: %s:%s", p.PrincipalKind, p.PrincipalID)
		if p.PrincipalID == strangerID {
			t.Errorf("OVER-GRANT: stranger %s injected into DM keyed %s",
				strangerID, ms.conversations[newConvID].ExternalRef)
		}
	}
}
