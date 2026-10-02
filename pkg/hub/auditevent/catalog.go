// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auditevent

import (
	"sort"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
)

// PhaseOutcome is one exact phase/result pair declared by the catalog.
type PhaseOutcome struct {
	Phase   Phase
	Outcome Outcome
}

// Destination is a declared output for an event family/action.
type Destination string

const (
	DestinationStructuredLog Destination = "structured_log"
	DestinationHistory       Destination = "history"
)

// PayloadValueType identifies the concrete Go value shape admitted for a
// payload leaf. Validation is driven exclusively by the leaf declarations in
// the action's catalog entry.
type PayloadValueType string

const (
	PayloadString       PayloadValueType = "string"
	PayloadInt64        PayloadValueType = "int64"
	PayloadHexString    PayloadValueType = "hex_string"
	PayloadImpactCounts PayloadValueType = "impact_counts"
	PayloadStringArray  PayloadValueType = "string_array"
	PayloadBool         PayloadValueType = "bool"
)

// PayloadLeafSchema is the complete machine-readable contract for one
// payload leaf. Zero bounds mean that the corresponding bound is not used.
type PayloadLeafSchema struct {
	Name          string
	Type          PayloadValueType
	MaxBytes      int
	ExactLength   int
	MaxItems      int
	ItemMaxBytes  int
	AllowedValues []string
}

// ResourceProjectIDRule declares how project_id relates to a resource scope.
type ResourceProjectIDRule string

const (
	ResourceProjectIDOmitted  ResourceProjectIDRule = "omitted"
	ResourceProjectIDRequired ResourceProjectIDRule = "required"
)

// ResourceScopeSchema declares the project-ID contract for one resource scope.
type ResourceScopeSchema struct {
	Scope     ResourceScope
	ProjectID ResourceProjectIDRule
}

// ResourceKindRule declares whether an entry requires one literal kind or any
// bounded canonical code supplied by the authorization resource resolver.
type ResourceKindRule string

const (
	ResourceKindExact ResourceKindRule = "exact"
	ResourceKindCode  ResourceKindRule = "code"
)

// CatalogEntry is the machine-readable schema for one action.
type CatalogEntry struct {
	Family                 string
	Action                 string
	AllowedActions         []string
	ActionPermissions      []ActionPermissionSchema
	OutcomeReasons         []OutcomeReasonSchema
	ProducerReasonMappings []ProducerReasonMapping
	AllowedPairs           []PhaseOutcome
	ResourceKind           string
	ResourceKindRule       ResourceKindRule
	RequiredEnvelopeLeaves []string
	ResourceScopes         []ResourceScopeSchema
	RequiredPayloadLeaves  []PayloadLeafSchema
	OptionalPayloadLeaves  []PayloadLeafSchema
	Destinations           []Destination
}

type ActionPermissionSchema struct {
	Action     string
	Permission string
}

type OutcomeReasonSchema struct {
	Outcome        Outcome
	AllowedReasons []string
}

type ProducerReasonCategory string

type ProducerReasonMapping struct {
	Category  ProducerReasonCategory
	Outcome   Outcome
	Reason    ReasonCode
	Available bool
}

var catalog = []CatalogEntry{{
	Family:                 "access_boundary",
	Action:                 "create",
	AllowedPairs:           []PhaseOutcome{{Phase: PhaseCommit, Outcome: OutcomeSucceeded}},
	ResourceKind:           "access_constraint",
	ResourceKindRule:       ResourceKindExact,
	RequiredEnvelopeLeaves: []string{"schema_version", "event_id", "occurred_at", "family", "action", "phase", "outcome", "severity", "correlation_id", "principal", "resource"},
	ResourceScopes: []ResourceScopeSchema{
		{Scope: ResourceScopeSystem, ProjectID: ResourceProjectIDOmitted},
		{Scope: ResourceScopeProject, ProjectID: ResourceProjectIDRequired},
	},
	RequiredPayloadLeaves: []PayloadLeafSchema{{
		Name:          "classification",
		Type:          PayloadString,
		MaxBytes:      9,
		AllowedValues: []string{"tighten", "relax", "mixed", "no_effect"},
	}},
	OptionalPayloadLeaves: []PayloadLeafSchema{
		{Name: "before_revision", Type: PayloadInt64},
		{Name: "after_revision", Type: PayloadInt64},
		{Name: "preview_id", Type: PayloadString, MaxBytes: 128},
		{Name: "draft_hash", Type: PayloadHexString, ExactLength: 64},
		{Name: "impact_counts", Type: PayloadImpactCounts},
		{Name: "changed_fields", Type: PayloadStringArray, MaxItems: 32, ItemMaxBytes: 256},
	},
	Destinations: []Destination{DestinationStructuredLog, DestinationHistory},
}, authorizationCatalogEntry()}

func authorizationCatalogEntry() CatalogEntry {
	return CatalogEntry{
		Family:            "authorization",
		AllowedActions:    declaredAuthorizationOperationStrings(),
		ActionPermissions: declaredAuthorizationActionPermissions(),
		OutcomeReasons: []OutcomeReasonSchema{
			{Outcome: OutcomeAllow, AllowedReasons: []string{string(ReasonAllowed), string(ReasonInherited), string(ReasonCheckDisabled), string(ReasonCheckUnavailable), string(ReasonUnspecified)}},
			{Outcome: OutcomeDeny, AllowedReasons: []string{string(ReasonPolicyDenied), string(ReasonPermissionMissing), string(ReasonInvalidRequest), string(ReasonNotAuthenticated), string(ReasonNotAuthorized), string(ReasonNotFound), string(ReasonConflict), string(ReasonRateLimited), string(ReasonDependencyUnavailable), string(ReasonAttachmentRejected), string(ReasonCheckUnavailable), string(ReasonUnspecified)}},
		},
		ProducerReasonMappings: authorizationProducerReasonMappings(),
		AllowedPairs:           []PhaseOutcome{{Phase: PhaseDecision, Outcome: OutcomeAllow}, {Phase: PhaseDecision, Outcome: OutcomeDeny}},
		ResourceKindRule:       ResourceKindCode,
		RequiredEnvelopeLeaves: []string{"schema_version", "event_id", "occurred_at", "family", "action", "phase", "outcome", "severity", "correlation_id", "principal", "resource"},
		ResourceScopes: []ResourceScopeSchema{
			{Scope: ResourceScopeSystem, ProjectID: ResourceProjectIDOmitted},
			{Scope: ResourceScopeProject, ProjectID: ResourceProjectIDRequired},
		},
		RequiredPayloadLeaves: []PayloadLeafSchema{
			{Name: "permission", Type: PayloadString, MaxBytes: 64, AllowedValues: canonicalPermissionNames()},
			{Name: "reason_code", Type: PayloadString, MaxBytes: 22, AllowedValues: reasonCodeStrings()},
		},
		OptionalPayloadLeaves: []PayloadLeafSchema{
			{Name: "purpose", Type: PayloadString, MaxBytes: 128},
			{Name: "cache_hit", Type: PayloadBool},
		},
		Destinations: []Destination{DestinationStructuredLog},
	}
}

func canonicalPermissionNames() []string {
	names := make([]string, 0, len(permissions.Registry))
	for _, permission := range permissions.Registry {
		names = append(names, permission.ID)
	}
	sort.Strings(names)
	return names
}

// Catalog returns a defensive snapshot of the schemas implemented in this
// milestone. Later family adapters add literal entries here.
func Catalog() []CatalogEntry {
	result := make([]CatalogEntry, len(catalog))
	for i, entry := range catalog {
		result[i] = entry
		result[i].AllowedActions = append([]string(nil), entry.AllowedActions...)
		result[i].ActionPermissions = append([]ActionPermissionSchema(nil), entry.ActionPermissions...)
		result[i].OutcomeReasons = cloneOutcomeReasons(entry.OutcomeReasons)
		result[i].ProducerReasonMappings = append([]ProducerReasonMapping(nil), entry.ProducerReasonMappings...)
		result[i].AllowedPairs = append([]PhaseOutcome(nil), entry.AllowedPairs...)
		result[i].RequiredEnvelopeLeaves = append([]string(nil), entry.RequiredEnvelopeLeaves...)
		result[i].ResourceScopes = append([]ResourceScopeSchema(nil), entry.ResourceScopes...)
		result[i].RequiredPayloadLeaves = clonePayloadLeafSchemas(entry.RequiredPayloadLeaves)
		result[i].OptionalPayloadLeaves = clonePayloadLeafSchemas(entry.OptionalPayloadLeaves)
		result[i].Destinations = append([]Destination(nil), entry.Destinations...)
	}
	return result
}

func cloneOutcomeReasons(schemas []OutcomeReasonSchema) []OutcomeReasonSchema {
	if schemas == nil {
		return nil
	}
	clones := make([]OutcomeReasonSchema, len(schemas))
	for i, schema := range schemas {
		clones[i] = schema
		clones[i].AllowedReasons = append([]string(nil), schema.AllowedReasons...)
	}
	return clones
}

func clonePayloadLeafSchemas(schemas []PayloadLeafSchema) []PayloadLeafSchema {
	clones := make([]PayloadLeafSchema, len(schemas))
	for i, schema := range schemas {
		clones[i] = schema
		clones[i].AllowedValues = append([]string(nil), schema.AllowedValues...)
	}
	return clones
}

func catalogEntry(family, action string) (CatalogEntry, bool) {
	for _, entry := range catalog {
		if entry.Family == family && (entry.Action == action || containsString(entry.AllowedActions, action)) {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
