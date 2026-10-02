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

// Package auditevent defines the versioned security-audit event contract.
package auditevent

import (
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/credentialmeta"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/authzop"
)

const (
	// EventName is the stable structured-log event name.
	EventName = "scion.audit"
	// SchemaVersion is the current serialized envelope version.
	SchemaVersion = 1
)

// Phase identifies which part of an operation an event represents.
type Phase string

const (
	PhaseAttempt     Phase = "attempt"
	PhaseDecision    Phase = "decision"
	PhaseObservation Phase = "observation"
	PhaseCommit      Phase = "commit"
	PhaseFailure     Phase = "failure"
	PhaseDelivery    Phase = "delivery"
)

// Outcome is the bounded result associated with a phase. It is empty only
// for an attempt event.
type Outcome string

const (
	OutcomeAllow     Outcome = "allow"
	OutcomeDeny      Outcome = "deny"
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeSkipped   Outcome = "skipped"
	OutcomeDeferred  Outcome = "deferred"
)

// Severity is the bounded operator severity of an audit event.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
)

// IdentityKind identifies an actor without adding incidental PII.
type IdentityKind string

const (
	IdentityUser    IdentityKind = "user"
	IdentityAgent   IdentityKind = "agent"
	IdentityBroker  IdentityKind = "broker"
	IdentitySystem  IdentityKind = "system"
	IdentityProject IdentityKind = "project"
)

// IdentityRef identifies one participant in an audited operation.
type IdentityRef struct {
	Kind IdentityKind `json:"kind"`
	ID   string       `json:"id"`
}

// CredentialRef is immutable, validated descriptive credential metadata.
// Construct one with NewCredentialRef; arbitrary public refs cannot enter an
// audit builder.
type CredentialRef = credentialmeta.Ref

type CredentialRefInput = credentialmeta.RefInput
type CredentialKind = credentialmeta.Kind
type CredentialBoundaryKind = credentialmeta.BoundaryKind
type CredentialValidationError = credentialmeta.ValidationError

const (
	CredentialInteractive = credentialmeta.KindInteractive
	CredentialUAT         = credentialmeta.KindUAT
	CredentialAgentJWT    = credentialmeta.KindAgentJWT
	CredentialFederation  = credentialmeta.KindFederation
	CredentialBroker      = credentialmeta.KindBroker
	CredentialDev         = credentialmeta.KindDev

	CredentialBoundaryProject = credentialmeta.BoundaryProject
	CredentialBoundaryHub     = credentialmeta.BoundaryHub
)

func NewCredentialRef(input CredentialRefInput) (CredentialRef, error) {
	return credentialmeta.NewRef(input)
}

// RequestRef records bounded, trusted request metadata.
type RequestRef struct {
	ID      string `json:"id,omitempty"`
	Method  string `json:"method,omitempty"`
	Route   string `json:"route,omitempty"`
	Surface string `json:"surface,omitempty"`
}

// ResourceScope identifies whether an audited resource belongs to the system
// or to one project. It is validation metadata and is not serialized as a
// second resource schema.
type ResourceScope string

const (
	ResourceScopeSystem  ResourceScope = "system"
	ResourceScopeProject ResourceScope = "project"
)

// ResourceRef identifies the object affected by an operation.
type ResourceRef struct {
	Kind      string        `json:"kind"`
	ID        string        `json:"id"`
	Scope     ResourceScope `json:"-"`
	ProjectID string        `json:"project_id,omitempty"`
}

// EnvelopeV1 is the common in-memory audit envelope. Payload construction is
// intentionally package-owned so callers cannot submit arbitrary maps.
type EnvelopeV1 struct {
	SchemaVersion int
	EventID       string
	OccurredAt    time.Time
	Family        string
	Action        string
	Phase         Phase
	Outcome       Outcome
	Severity      Severity
	CorrelationID string
	CausationID   string
	Request       *RequestRef
	Initiator     *IdentityRef
	Principal     *IdentityRef
	Executor      *IdentityRef
	Credential    *CredentialRef
	Resource      *ResourceRef
	Payload       Payload
}

// ImpactCounts is the bounded impact summary for an access-boundary change.
type ImpactCounts struct {
	Agents   uint32 `json:"agents"`
	Users    uint32 `json:"users"`
	Projects uint32 `json:"projects"`
}

// Payload is a sealed typed audit payload. Each family owns its concrete
// payload type; callers cannot provide an arbitrary map at the audit boundary.
type Payload interface {
	auditPayloadLeaves() map[string]any
}

// PermissionName is a typed canonical permission ID. The closed value set
// remains owned by permissions.Registry rather than duplicated here.
type PermissionName string

// AuthorizationProducerRequestContract pins the field #2379 must add to
// hub.AuthzRequest before emitter cutover. It is documentation-as-code only;
// A1 does not modify the producer.
type AuthorizationProducerRequestContract struct {
	OperationID authzop.OperationID
}

// AuthorizationProducerDecisionContract pins the structured field #2379 must
// assign on every decision exit without parsing Decision.Reason prose.
type AuthorizationProducerDecisionContract struct {
	AuditReason ReasonCode
}

// PurposeLabel is bounded server-derived authorization purpose metadata.
type PurposeLabel string

// ReasonCode is the closed, value-free explanation vocabulary shared by v1
// audit family payloads.
type ReasonCode string

const (
	ReasonAllowed               ReasonCode = "allowed"
	ReasonPolicyDenied          ReasonCode = "policy_denied"
	ReasonPermissionMissing     ReasonCode = "permission_missing"
	ReasonInvalidRequest        ReasonCode = "invalid_request"
	ReasonNotAuthenticated      ReasonCode = "not_authenticated"
	ReasonNotAuthorized         ReasonCode = "not_authorized"
	ReasonNotFound              ReasonCode = "not_found"
	ReasonConflict              ReasonCode = "conflict"
	ReasonRateLimited           ReasonCode = "rate_limited"
	ReasonDependencyUnavailable ReasonCode = "dependency_unavailable"
	ReasonAttachmentRejected    ReasonCode = "attachment_rejected"
	ReasonCheckDisabled         ReasonCode = "check_disabled"
	ReasonCheckUnavailable      ReasonCode = "check_unavailable"
	ReasonInherited             ReasonCode = "inherited"
	ReasonUnspecified           ReasonCode = "unspecified"
)

func reasonCodeStrings() []string {
	return []string{
		string(ReasonAllowed),
		string(ReasonPolicyDenied),
		string(ReasonPermissionMissing),
		string(ReasonInvalidRequest),
		string(ReasonNotAuthenticated),
		string(ReasonNotAuthorized),
		string(ReasonNotFound),
		string(ReasonConflict),
		string(ReasonRateLimited),
		string(ReasonDependencyUnavailable),
		string(ReasonAttachmentRejected),
		string(ReasonCheckDisabled),
		string(ReasonCheckUnavailable),
		string(ReasonInherited),
		string(ReasonUnspecified),
	}
}

const (
	ProducerReasonAllow                 ProducerReasonCategory = "allow"
	ProducerReasonInherited             ProducerReasonCategory = "inherited"
	ProducerReasonCacheHit              ProducerReasonCategory = "cache_hit"
	ProducerReasonPermissionDenied      ProducerReasonCategory = "permission_denied"
	ProducerReasonPolicyDenied          ProducerReasonCategory = "policy_denied"
	ProducerReasonNotAuthenticated      ProducerReasonCategory = "not_authenticated"
	ProducerReasonNotAuthorized         ProducerReasonCategory = "not_authorized"
	ProducerReasonInvalidRequest        ProducerReasonCategory = "invalid_request"
	ProducerReasonDependencyUnavailable ProducerReasonCategory = "dependency_unavailable"
	ProducerReasonCheckUnavailable      ProducerReasonCategory = "check_unavailable"
	ProducerReasonClosedFallback        ProducerReasonCategory = "closed_fallback"
	ProducerReasonNotFound              ProducerReasonCategory = "not_found"
	ProducerReasonConflict              ProducerReasonCategory = "conflict"
	ProducerReasonRateLimited           ProducerReasonCategory = "rate_limited"
	ProducerReasonAttachmentRejected    ProducerReasonCategory = "attachment_rejected"
	ProducerReasonCheckDisabled         ProducerReasonCategory = "check_disabled"
)

func authorizationProducerReasonMappings() []ProducerReasonMapping {
	return []ProducerReasonMapping{
		{Category: ProducerReasonAllow, Outcome: OutcomeAllow, Reason: ReasonAllowed, Available: true},
		{Category: ProducerReasonInherited, Outcome: OutcomeAllow, Reason: ReasonInherited, Available: true},
		{Category: ProducerReasonCacheHit, Outcome: OutcomeAllow, Reason: ReasonAllowed, Available: true},
		{Category: ProducerReasonPermissionDenied, Outcome: OutcomeDeny, Reason: ReasonPermissionMissing, Available: true},
		{Category: ProducerReasonPolicyDenied, Outcome: OutcomeDeny, Reason: ReasonPolicyDenied, Available: true},
		{Category: ProducerReasonNotAuthenticated, Outcome: OutcomeDeny, Reason: ReasonNotAuthenticated, Available: true},
		{Category: ProducerReasonNotAuthorized, Outcome: OutcomeDeny, Reason: ReasonNotAuthorized, Available: true},
		{Category: ProducerReasonInvalidRequest, Outcome: OutcomeDeny, Reason: ReasonInvalidRequest, Available: true},
		{Category: ProducerReasonDependencyUnavailable, Outcome: OutcomeDeny, Reason: ReasonDependencyUnavailable, Available: true},
		{Category: ProducerReasonCheckUnavailable, Outcome: OutcomeDeny, Reason: ReasonCheckUnavailable, Available: true},
		{Category: ProducerReasonClosedFallback, Outcome: OutcomeDeny, Reason: ReasonUnspecified, Available: true},
		{Category: ProducerReasonNotFound, Outcome: OutcomeDeny, Reason: ReasonNotFound, Available: false},
		{Category: ProducerReasonConflict, Outcome: OutcomeDeny, Reason: ReasonConflict, Available: false},
		{Category: ProducerReasonRateLimited, Outcome: OutcomeDeny, Reason: ReasonRateLimited, Available: false},
		{Category: ProducerReasonAttachmentRejected, Outcome: OutcomeDeny, Reason: ReasonAttachmentRejected, Available: false},
		{Category: ProducerReasonCheckDisabled, Outcome: OutcomeAllow, Reason: ReasonCheckDisabled, Available: false},
	}
}

// AuthorizationPayload is the exact v1 authorization decision payload.
type AuthorizationPayload struct {
	Permission PermissionName
	ReasonCode ReasonCode
	Purpose    PurposeLabel
	CacheHit   *bool
}

func (p AuthorizationPayload) auditPayloadLeaves() map[string]any {
	leaves := map[string]any{
		"permission":  string(p.Permission),
		"reason_code": string(p.ReasonCode),
	}
	if p.Purpose != "" {
		leaves["purpose"] = string(p.Purpose)
	}
	if p.CacheHit != nil {
		leaves["cache_hit"] = *p.CacheHit
	}
	return leaves
}

// AccessBoundaryPayload is the exact v1 payload for access-boundary create,
// update, and recovery events.
type AccessBoundaryPayload struct {
	BeforeRevision *int64
	AfterRevision  *int64
	Classification BoundaryClassification
	PreviewID      string
	DraftHash      string
	ImpactCounts   *ImpactCounts
	ChangedFields  []string
}

func (p AccessBoundaryPayload) auditPayloadLeaves() map[string]any {
	leaves := map[string]any{"classification": string(p.Classification)}
	if p.BeforeRevision != nil {
		leaves["before_revision"] = *p.BeforeRevision
	}
	if p.AfterRevision != nil {
		leaves["after_revision"] = *p.AfterRevision
	}
	if p.PreviewID != "" {
		leaves["preview_id"] = p.PreviewID
	}
	if p.DraftHash != "" {
		leaves["draft_hash"] = p.DraftHash
	}
	if p.ImpactCounts != nil {
		leaves["impact_counts"] = *p.ImpactCounts
	}
	if p.ChangedFields != nil {
		leaves["changed_fields"] = append([]string(nil), p.ChangedFields...)
	}
	return leaves
}

// BoundaryClassification describes the authorization direction of a boundary
// mutation.
type BoundaryClassification string

const (
	BoundaryTighten  BoundaryClassification = "tighten"
	BoundaryRelax    BoundaryClassification = "relax"
	BoundaryMixed    BoundaryClassification = "mixed"
	BoundaryNoEffect BoundaryClassification = "no_effect"
)
