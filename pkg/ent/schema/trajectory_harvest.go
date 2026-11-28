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
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TrajectoryHarvest records the outcome of a broker post-exit / pre-delete /
// pre-reset harvest attempt (Tokenomics P6.1 contract §3).
// Unique on (agent_id, harvest_id) for idempotent replay.
type TrajectoryHarvest struct {
	ent.Schema
}

// Fields of the TrajectoryHarvest.
func (TrajectoryHarvest) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			NotEmpty().
			Immutable(),
		field.String("harvest_id").
			NotEmpty().
			Immutable(),
		field.String("hub_id").
			NotEmpty().
			Immutable(),
		field.String("project_id").
			NotEmpty().
			Immutable(),
		field.String("agent_id").
			NotEmpty().
			Immutable(),
		field.String("broker_id").
			NotEmpty().
			Immutable(),
		field.String("run_id").
			Optional().
			Default(""),
		field.String("trigger").
			NotEmpty(),
		field.String("outcome").
			NotEmpty(),
		field.String("trajectory").
			NotEmpty(),
		field.String("usage").
			NotEmpty(),
		field.Int("segments_committed").
			NonNegative().
			Default(0),
		field.Int64("bytes_committed").
			NonNegative().
			Default(0),
		field.Int64("pending_bytes").
			NonNegative().
			Default(0),
		field.Time("oldest_pending_at").
			Optional().
			Nillable(),
		field.Int("usage_events_drained").
			NonNegative().
			Default(0),
		field.Int("usage_events_pending").
			NonNegative().
			Default(0),
		field.String("error_code").
			Optional().
			Default(""),
		field.String("error").
			Optional().
			Default(""),
		field.Time("started_at"),
		field.Time("finished_at"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Indexes of the TrajectoryHarvest.
func (TrajectoryHarvest) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("agent_id", "harvest_id").
			Unique(),
		index.Fields("agent_id", "created_at"),
	}
}

// Annotations of the TrajectoryHarvest.
func (TrajectoryHarvest) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "trajectory_harvests"},
	}
}
