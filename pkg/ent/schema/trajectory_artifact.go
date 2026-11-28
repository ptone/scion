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

// TrajectoryArtifact holds one Track 2 L1 normalized trajectory artifact row
// (usage_records, run_inventory, or atif) in trajectory_artifacts (P7 contract §6).
// Unique on (run_id, artifact_type, parser_version, identity_key) where
// identity_key encodes chunk_id (usage_records), inventory_seq (run_inventory),
// or native_relative_path + parsed_offset (atif).
type TrajectoryArtifact struct {
	ent.Schema
}

// Fields of the TrajectoryArtifact.
func (TrajectoryArtifact) Fields() []ent.Field {
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
		field.String("artifact_type").
			NotEmpty().
			Immutable(),
		field.String("parser_version").
			NotEmpty().
			Immutable(),
		field.String("identity_key").
			NotEmpty().
			Immutable(),
		field.String("chunk_id").
			Optional().
			Default(""),
		field.Int64("inventory_seq").
			NonNegative().
			Default(0),
		field.Bool("final").
			Default(false),
		field.String("session_id").
			Optional().
			Default(""),
		field.String("native_relative_path").
			Optional().
			Default(""),
		field.Int64("parsed_offset").
			NonNegative().
			Default(0),
		field.Int64("row_count").
			NonNegative().
			Default(0),
		field.String("atif_schema_version").
			Optional().
			Default(""),
		field.String("file_format").
			Optional().
			Default(""),
		field.String("content_encoding").
			NotEmpty().
			Default("gzip"),
		field.Int64("size").
			Positive(),
		field.Int64("charged_bytes").
			NonNegative().
			Default(0),
		field.String("sha256").
			NotEmpty(),
		field.Int64("stamped_size").
			NonNegative().
			Default(0),
		field.String("stamped_sha256").
			Optional().
			Default(""),
		field.String("pending_object_path").
			NotEmpty().
			Immutable(),
		field.String("object_path").
			NotEmpty().
			Immutable(),
		field.String("status").
			NotEmpty().
			Default("pending"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("committed_at").
			Optional().
			Nillable(),
		field.Time("prepare_expires_at"),
	}
}

// Indexes of the TrajectoryArtifact.
func (TrajectoryArtifact) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("run_id", "artifact_type", "parser_version", "identity_key").
			Unique(),
		index.Fields("agent_id", "run_id"),
		index.Fields("run_id", "parser_version", "artifact_type", "chunk_id"),
		index.Fields("project_id", "agent_id", "run_id"),
	}
}

// Annotations of the TrajectoryArtifact.
func (TrajectoryArtifact) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "trajectory_artifacts"},
	}
}
