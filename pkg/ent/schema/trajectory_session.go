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

// TrajectorySession holds the latest normalized ATIF session state per
// (run_id, native_relative_path) in trajectory_sessions (P7 contract §6).
// Upserts apply conditionally only when (parser_version_num, parsed_offset)
// is greater than or equal to the stored pair.
type TrajectorySession struct {
	ent.Schema
}

// Fields of the TrajectorySession.
func (TrajectorySession) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
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
		field.String("run_id").
			NotEmpty().
			Immutable(),
		field.String("harness").
			NotEmpty().
			Immutable(),
		field.String("session_id").
			Optional().
			Default(""),
		field.String("native_relative_path").
			NotEmpty().
			Immutable(),
		field.Bool("is_subagent").
			Default(false),
		field.String("parent_session_id").
			Optional().
			Default(""),
		field.String("parser_version").
			NotEmpty(),
		field.Int64("parser_version_num").
			NonNegative().
			Default(0),
		field.Int64("parsed_offset").
			NonNegative().
			Default(0),
		field.String("atif_artifact_id").
			Optional().
			Default(""),
		field.String("atif_object_path").
			Optional().
			Default(""),
		field.String("atif_schema_version").
			Optional().
			Default(""),
		field.String("file_format").
			Optional().
			Default(""),
		field.Int("step_count").
			NonNegative().
			Default(0),
		field.String("normalize_status").
			NotEmpty().
			Default("ok"),
		field.Int("parse_error_count").
			NonNegative().
			Default(0),
		field.String("first_error").
			Optional().
			Default(""),
		field.Time("updated_at").
			Default(time.Now),
	}
}

// Indexes of the TrajectorySession.
func (TrajectorySession) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("run_id", "native_relative_path").
			Unique(),
		index.Fields("project_id", "agent_id", "run_id"),
		index.Fields("agent_id", "updated_at"),
	}
}

// Annotations of the TrajectorySession.
func (TrajectorySession) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "trajectory_sessions"},
	}
}
