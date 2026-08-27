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

package messages

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDMConversationKey_Roundtrip(t *testing.T) {
	idA := uuid.NewString()
	idB := uuid.NewString()

	key, err := DMConversationKey("user", idA, "agent", idB)
	require.NoError(t, err)

	kindA, gotIDA, kindB, gotIDB, err := ParseDMKey(key)
	require.NoError(t, err)

	// Tokens are sorted: "agent" < "user".
	assert.Equal(t, "agent", kindA)
	assert.Equal(t, idB, gotIDA) // agent's ID comes first
	assert.Equal(t, "user", kindB)
	assert.Equal(t, idA, gotIDB) // user's ID comes second
}

func TestDMConversationKey_Ordering_AgentUser(t *testing.T) {
	userID := uuid.NewString()
	agentID := uuid.NewString()

	// Regardless of argument order, key is the same.
	key1, err := DMConversationKey("user", userID, "agent", agentID)
	require.NoError(t, err)
	key2, err := DMConversationKey("agent", agentID, "user", userID)
	require.NoError(t, err)

	assert.Equal(t, key1, key2, "key must be order-independent")
	assert.True(t, strings.HasPrefix(key1, "dm:agent:"), "mixed pair must start with dm:agent:")
}

func TestDMConversationKey_Ordering_SameKind(t *testing.T) {
	id1 := "00000000-0000-0000-0000-000000000001"
	id2 := "00000000-0000-0000-0000-000000000002"

	key1, err := DMConversationKey("user", id1, "user", id2)
	require.NoError(t, err)
	key2, err := DMConversationKey("user", id2, "user", id1)
	require.NoError(t, err)

	assert.Equal(t, key1, key2, "same-kind key must be order-independent")
	// Smaller UUID sorts first.
	assert.Equal(t, "dm:user:"+id1+":user:"+id2, key1)
}

func TestDMConversationKey_CaseNormalisation(t *testing.T) {
	id := "AABBCCDD-0011-2233-4455-667788990011"
	lower := strings.ToLower(id)

	key, err := DMConversationKey("USER", id, "AGENT", lower)
	require.NoError(t, err)

	// Kind normalised to lowercase, UUID normalised to lowercase.
	assert.Contains(t, key, "agent:")
	assert.Contains(t, key, "user:")
	assert.NotContains(t, key, "AABBCCDD")
	assert.NotContains(t, key, "USER")
	assert.NotContains(t, key, "AGENT")
}

func TestDMConversationKey_RejectBadUUID(t *testing.T) {
	_, err := DMConversationKey("user", "not-a-uuid", "agent", uuid.NewString())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid UUID")

	_, err = DMConversationKey("user", uuid.NewString(), "agent", "also-bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid UUID")
}

func TestDMConversationKey_RejectUnknownKind(t *testing.T) {
	_, err := DMConversationKey("bot", uuid.NewString(), "user", uuid.NewString())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown kind")

	_, err = DMConversationKey("user", uuid.NewString(), "system", uuid.NewString())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown kind")
}

func TestParseDMKey_MissingPrefix(t *testing.T) {
	_, _, _, _, err := ParseDMKey("notdm:user:" + uuid.NewString() + ":agent:" + uuid.NewString())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing dm: prefix")
}

func TestParseDMKey_WrongSegmentCount(t *testing.T) {
	_, _, _, _, err := ParseDMKey("dm:user:" + uuid.NewString())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "4 colon-separated segments")
}

func TestParseDMKey_InvalidKindInKey(t *testing.T) {
	id := uuid.NewString()
	_, _, _, _, err := ParseDMKey("dm:bot:" + id + ":user:" + id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown kind")
}

func TestParseDMKey_InvalidUUIDInKey(t *testing.T) {
	id := uuid.NewString()
	_, _, _, _, err := ParseDMKey("dm:user:not-a-uuid:agent:" + id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid UUID")
}

func TestDMConversationKey_AgentAgent(t *testing.T) {
	id1 := uuid.NewString()
	id2 := uuid.NewString()

	key1, err := DMConversationKey("agent", id1, "agent", id2)
	require.NoError(t, err)
	key2, err := DMConversationKey("agent", id2, "agent", id1)
	require.NoError(t, err)

	assert.Equal(t, key1, key2, "agent-agent key must be order-independent")
}
