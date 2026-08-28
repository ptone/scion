package messaging

import (
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
)

func TestProbe_DMDivergenceCanNeverMatch(t *testing.T) {
	sender := "11111111-1111-4111-8111-111111111111"
	recip := "22222222-2222-4222-8222-222222222222"

	// Exactly what production pairs: handlers_agent_messaging.go builds the new
	// side with DMConversationKey, and the old side with OldRoutingFromMessage.
	newRef, err := messages.DMConversationKey("agent", sender, "user", recip)
	if err != nil { t.Fatal(err) }
	oldRouting := OldRoutingFromMessage(sender, recip, "")

	t.Logf("old side = %s", oldRouting)
	t.Logf("new side = %s", newRef)

	match, reason := ComputeDivergenceMatch(oldRouting, newRef, "some-conv-id")
	t.Logf("match=%v reason=%q", match, reason)
	if !match {
		t.Errorf("DM DIVERGENCE ALWAYS FIRES: a correctly-routed DM reports mismatch (%s)", reason)
	}
}
