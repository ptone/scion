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

package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AgentCredential holds the schema definition for the AgentCredential entity.
// Tracks issued agent JWT tokens for revocation and validation.
type AgentCredential struct {
	ent.Schema
}

// Fields of the AgentCredential.
func (AgentCredential) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.String("agent_id").
			NotEmpty(),
		field.String("project_id").
			NotEmpty(),
		field.String("token_jti_hash").
			NotEmpty(),
		field.Time("issued_at").
			Default(time.Now).
			Immutable(),
		field.Time("expires_at"),
		field.Time("revoked_at").
			Optional().
			Nillable(),
		field.String("revoked_by").
			Optional().
			Nillable(),
		field.String("revoke_reason").
			Optional().
			Nillable(),
		field.Time("last_seen_at").
			Optional().
			Nillable(),
		// EntitledSecretKeys records the set of secret key names this
		// session is entitled to fetch via POST /api/v1/agent/secrets.
		// Computed at start time by the dispatcher's secret resolution
		// and stored here so the endpoint can enforce entitlement without
		// re-deriving it. The field is session-scoped: each lifecycle
		// event (start/restart) mints a fresh token and records a fresh
		// entitled set on the new credential.
		//
		// NULL means no entitlement was ever recorded (pre-migration
		// credential, or a start that failed between token generation
		// and secret resolution). The endpoint must fail closed on NULL
		// with a loud log — it is a server-side bookkeeping failure.
		//
		// [] (empty JSON array) means the agent is entitled to zero
		// secrets — a valid state, distinct from NULL.
		//
		// On token refresh, the entitled keys are copied from the old
		// credential to the new one (the entitlement was computed at
		// start time and does not change within a session).
		field.JSON("entitled_secret_keys", []string{}).
			Optional(),
	}
}

// Indexes of the AgentCredential.
func (AgentCredential) Indexes() []ent.Index {
	return []ent.Index{
		// Unique lookup by JTI hash
		index.Fields("token_jti_hash").Unique(),
		// Find all credentials for an agent in a project
		index.Fields("agent_id", "project_id"),
		// Bulk revocation queries by agent
		index.Fields("agent_id"),
		// Cleanup queries by expiry
		index.Fields("expires_at"),
	}
}

// Edges of the AgentCredential.
func (AgentCredential) Edges() []ent.Edge {
	return nil
}
