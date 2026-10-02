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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/authzop"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

func TestBuildAuthorizationDecisionAllowAndDeny(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		allowed  bool
		outcome  Outcome
		severity Severity
		reason   ReasonCode
	}{
		{name: "allow", allowed: true, outcome: OutcomeAllow, severity: SeverityInfo, reason: ReasonAllowed},
		{name: "deny", allowed: false, outcome: OutcomeDeny, severity: SeverityWarning, reason: ReasonPolicyDenied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cacheHit := false
			ctx := ContextWithOperation(context.Background(), AuditOperationContext{CorrelationID: "request-1"})
			event, err := BuildAuthorizationDecision(ctx, AuthorizationDecisionInput{
				Operation:  authzop.OperationID("agent.read"),
				Allowed:    tc.allowed,
				Principal:  IdentityRef{Kind: IdentityUser, ID: "user-1"},
				Resource:   ResourceRef{Kind: "agent", ID: "agent-1", Scope: ResourceScopeProject, ProjectID: "project-1"},
				Permission: PermissionName("agent.read"),
				ReasonCode: tc.reason,
				Purpose:    PurposeLabel("interactive read"),
				CacheHit:   &cacheHit,
			})
			require.NoError(t, err)

			assert.Equal(t, "authorization", event.Family)
			assert.Equal(t, "agent.read", event.Action)
			assert.Equal(t, PhaseDecision, event.Phase)
			assert.Equal(t, tc.outcome, event.Outcome)
			assert.Equal(t, tc.severity, event.Severity)
			assert.Equal(t, "request-1", event.CorrelationID)
			assert.NotEqual(t, uuid.Nil, uuid.MustParse(event.EventID))
			assert.False(t, event.OccurredAt.IsZero())
			assert.Equal(t, time.UTC, event.OccurredAt.Location())
			assert.Equal(t, AuthorizationPayload{
				Permission: PermissionName("agent.read"),
				ReasonCode: tc.reason,
				Purpose:    PurposeLabel("interactive read"),
				CacheHit:   &cacheHit,
			}, event.Payload)
		})
	}
}

func TestAuthorizationCatalogRejectsUndeclaredOperation(t *testing.T) {
	t.Parallel()

	event := validAuthorizationEvent(t)
	event.Action = "undeclared.operation"
	err := Validate(event)
	require.Error(t, err)
	assertValidationErrorDoesNotRetain(t, err, "family_action", "must be declared in the catalog", "undeclared.operation")
}

func TestAuthorizationEveryDeclaredOperationValidates(t *testing.T) {
	t.Parallel()

	for _, pair := range declaredAuthorizationActionPermissions() {
		event := validAuthorizationEvent(t)
		event.Action = pair.Action
		event.Payload = AuthorizationPayload{Permission: PermissionName(pair.Permission), ReasonCode: ReasonAllowed}
		assert.NoError(t, Validate(event), pair.Action)
	}
}

func TestAuthorizationOperationPermissionMatrix(t *testing.T) {
	t.Parallel()
	pairs := declaredAuthorizationActionPermissions()
	require.Len(t, pairs, len(declaredAuthorizationOperations))
	for _, pair := range pairs {
		for _, permission := range permissions.Registry {
			event := validAuthorizationEvent(t)
			event.Action = pair.Action
			event.Payload = AuthorizationPayload{Permission: PermissionName(permission.ID), ReasonCode: ReasonAllowed}
			err := Validate(event)
			if permission.ID == pair.Permission {
				assert.NoError(t, err, "%s/%s", pair.Action, permission.ID)
			} else {
				assert.Error(t, err, "%s/%s", pair.Action, permission.ID)
			}
		}
	}
}

func TestAuthorizationOperationsExactlyMatchCanonicalCatalog(t *testing.T) {
	t.Parallel()

	want := make([]string, 0, len(authzop.Catalog))
	for _, operation := range authzop.Catalog {
		want = append(want, string(operation.ID))
	}
	sort.Strings(want)
	assert.Equal(t, want, declaredAuthorizationOperationStrings())
}

func TestAuthorizationCatalogSnapshot(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(Catalog())
	require.NoError(t, err)
	digest := sha256.Sum256(encoded)
	assert.Equal(t, "ba97bdbcd6c86bb2537e948405a2935560dfb5f3f852fb8071a151c99e9e8105", hex.EncodeToString(digest[:]))
}

func TestAuthorizationRequiredEnvelopeAndPayloadLeaves(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*EnvelopeV1)
		field  string
	}{
		{name: "principal", mutate: func(event *EnvelopeV1) { event.Principal = nil }, field: "principal"},
		{name: "resource", mutate: func(event *EnvelopeV1) { event.Resource = nil }, field: "resource"},
		{name: "permission", mutate: func(event *EnvelopeV1) {
			event.Payload = retainedMapPayload{leaves: map[string]any{"reason_code": "allowed"}}
		}, field: "payload.permission"},
		{name: "reason code", mutate: func(event *EnvelopeV1) {
			event.Payload = retainedMapPayload{leaves: map[string]any{"permission": "agent.read"}}
		}, field: "payload.reason_code"},
		{name: "undeclared leaf", mutate: func(event *EnvelopeV1) {
			event.Payload = retainedMapPayload{leaves: map[string]any{"permission": "agent.read", "reason_code": "allowed", "secret": "canary"}}
		}, field: "payload"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := validAuthorizationEvent(t)
			tc.mutate(&event)
			err := Validate(event)
			require.Error(t, err)
			validationErr := new(ValidationError)
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, tc.field, validationErr.Field)
		})
	}
}

func TestAuthorizationActionAndResourceBoundsAreValueFree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*EnvelopeV1, string)
		field  string
		canary string
	}{
		{name: "empty action", mutate: func(event *EnvelopeV1, _ string) { event.Action = "" }, field: "action"},
		{name: "long action", mutate: func(event *EnvelopeV1, canary string) { event.Action = canary }, field: "action", canary: strings.Repeat("a", 65)},
		{name: "empty resource kind", mutate: func(event *EnvelopeV1, _ string) { event.Resource.Kind = "" }, field: "resource.kind"},
		{name: "long resource kind", mutate: func(event *EnvelopeV1, canary string) { event.Resource.Kind = canary }, field: "resource.kind", canary: strings.Repeat("r", 65)},
		{name: "controlled resource kind", mutate: func(event *EnvelopeV1, canary string) { event.Resource.Kind = canary }, field: "resource.kind", canary: "resource-canary\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := validAuthorizationEvent(t)
			tc.mutate(&event, tc.canary)
			err := Validate(event)
			require.Error(t, err)
			validationErr := new(ValidationError)
			require.ErrorAs(t, err, &validationErr)
			assert.Equal(t, tc.field, validationErr.Field)
			if tc.canary != "" {
				assertValidationErrorDoesNotRetain(t, err, tc.field, validationErr.Rule, tc.canary)
			}
		})
	}
}

func TestAuthorizationCatalogOnlyAllowsDecisionAllowOrDeny(t *testing.T) {
	t.Parallel()

	pairs := []PhaseOutcome{
		{Phase: PhaseAttempt},
		{Phase: PhaseDecision, Outcome: OutcomeAllow},
		{Phase: PhaseDecision, Outcome: OutcomeDeny},
		{Phase: PhaseObservation, Outcome: OutcomeSucceeded},
		{Phase: PhaseObservation, Outcome: OutcomeFailed},
		{Phase: PhaseCommit, Outcome: OutcomeSucceeded},
		{Phase: PhaseFailure, Outcome: OutcomeFailed},
		{Phase: PhaseDelivery, Outcome: OutcomeSucceeded},
		{Phase: PhaseDelivery, Outcome: OutcomeFailed},
		{Phase: PhaseDelivery, Outcome: OutcomeSkipped},
		{Phase: PhaseDelivery, Outcome: OutcomeDeferred},
	}
	for _, pair := range pairs {
		event := validAuthorizationEvent(t)
		event.Phase = pair.Phase
		event.Outcome = pair.Outcome
		event.Severity = SeverityInfo
		if pair.Outcome == OutcomeDeny || pair.Outcome == OutcomeFailed {
			event.Severity = SeverityWarning
		}
		if pair.Outcome == OutcomeDeny {
			event.Payload = AuthorizationPayload{Permission: "agent.read", ReasonCode: ReasonPolicyDenied}
		}
		err := Validate(event)
		if pair.Phase == PhaseDecision && (pair.Outcome == OutcomeAllow || pair.Outcome == OutcomeDeny) {
			assert.NoError(t, err, pair)
		} else {
			assert.Error(t, err, pair)
		}
	}
}

func TestAuthorizationPayloadClosedValuesAndBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*EnvelopeV1)
		field  string
		canary string
	}{
		{
			name: "permission",
			mutate: func(event *EnvelopeV1) {
				event.Payload = AuthorizationPayload{Permission: "permission-canary", ReasonCode: ReasonAllowed}
			},
			field: "payload.permission", canary: "permission-canary",
		},
		{
			name: "reason code",
			mutate: func(event *EnvelopeV1) {
				event.Payload = AuthorizationPayload{Permission: "agent.read", ReasonCode: "reason-canary"}
			},
			field: "payload.reason_code", canary: "reason-canary",
		},
		{
			name: "purpose",
			mutate: func(event *EnvelopeV1) {
				event.Payload = AuthorizationPayload{Permission: "agent.read", ReasonCode: ReasonAllowed, Purpose: PurposeLabel("purpose-canary\n")}
			},
			field: "payload.purpose", canary: "purpose-canary",
		},
		{
			name: "purpose length",
			mutate: func(event *EnvelopeV1) {
				event.Payload = AuthorizationPayload{Permission: "agent.read", ReasonCode: ReasonAllowed, Purpose: PurposeLabel(strings.Repeat("p", 129))}
			},
			field: "payload.purpose", canary: strings.Repeat("p", 129),
		},
		{
			name: "cache hit type",
			mutate: func(event *EnvelopeV1) {
				event.Payload = retainedMapPayload{leaves: map[string]any{"permission": "agent.read", "reason_code": "allowed", "cache_hit": "cache-canary"}}
			},
			field: "payload.cache_hit", canary: "cache-canary",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := validAuthorizationEvent(t)
			tc.mutate(&event)
			err := Validate(event)
			require.Error(t, err)
			validationErr := new(ValidationError)
			require.ErrorAs(t, err, &validationErr)
			assertValidationErrorDoesNotRetain(t, err, tc.field, validationErr.Rule, tc.canary)
		})
	}
}

func TestAuthorizationOpenStringsCanonicalSafety(t *testing.T) {
	t.Parallel()
	invalidUTF8 := string([]byte{0xff})
	values := []string{invalidUTF8, strings.Repeat("x", 129), "line\nbreak", "zero\u200bwidth", "Bearer secret-canary", "scion_pat_secret-canary"}
	for _, value := range values {
		value := value
		t.Run("purpose", func(t *testing.T) {
			event := validAuthorizationEvent(t)
			event.Payload = AuthorizationPayload{Permission: "agent.read", ReasonCode: ReasonAllowed, Purpose: PurposeLabel(value)}
			err := Validate(event)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), value)
		})
		t.Run("resource_kind", func(t *testing.T) {
			event := validAuthorizationEvent(t)
			event.Resource.Kind = value
			err := Validate(event)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), value)
		})
	}

	event := validAuthorizationEvent(t)
	event.Payload = AuthorizationPayload{Permission: "agent.read", ReasonCode: ReasonAllowed, Purpose: "  interactive  "}
	assert.Error(t, Validate(event))
	built, err := buildAuthorizationDecision(AuditOperationContext{CorrelationID: "request-1"}, AuthorizationDecisionInput{
		Operation: "agent.read", Allowed: true, Principal: IdentityRef{Kind: IdentityUser, ID: "user-1"},
		Resource: ResourceRef{Kind: "agent", ID: "agent-1", Scope: ResourceScopeProject, ProjectID: "project-1"}, Permission: "agent.read", ReasonCode: ReasonAllowed, Purpose: "  interactive  ",
	}, "22222222-2222-4222-8222-222222222222", time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, PurposeLabel("interactive"), built.Payload.(AuthorizationPayload).Purpose)
}

func TestAuthorizationProducerMappingContract(t *testing.T) {
	t.Parallel()
	request := AuthorizationProducerRequestContract{OperationID: authzop.OperationID("agent.read")}
	decision := AuthorizationProducerDecisionContract{AuditReason: ReasonAllowed}
	assert.Equal(t, authzop.OperationID("agent.read"), request.OperationID)
	assert.Equal(t, ReasonAllowed, decision.AuditReason)

	entry := Catalog()[1]
	require.Len(t, entry.ProducerReasonMappings, 16)
	seen := map[ProducerReasonCategory]bool{}
	for _, mapping := range entry.ProducerReasonMappings {
		assert.False(t, seen[mapping.Category])
		seen[mapping.Category] = true
		if mapping.Available {
			allowed := false
			for _, schema := range entry.OutcomeReasons {
				if schema.Outcome == mapping.Outcome && containsString(schema.AllowedReasons, string(mapping.Reason)) {
					allowed = true
				}
			}
			assert.True(t, allowed, mapping.Category)
		}
	}
}

func TestAuthorizationOutcomeReasonMatrix(t *testing.T) {
	t.Parallel()

	entry := Catalog()[1]
	admitted, rejected := 0, 0
	for _, outcome := range []Outcome{OutcomeAllow, OutcomeDeny} {
		for _, reason := range reasonCodeStrings() {
			event := validAuthorizationEvent(t)
			event.Outcome = outcome
			event.Severity = SeverityInfo
			if outcome == OutcomeDeny {
				event.Severity = SeverityWarning
			}
			event.Payload = AuthorizationPayload{Permission: "agent.read", ReasonCode: ReasonCode(reason)}
			wantValid := false
			for _, schema := range entry.OutcomeReasons {
				wantValid = wantValid || schema.Outcome == outcome && containsString(schema.AllowedReasons, reason)
			}
			err := Validate(event)
			if wantValid {
				admitted++
				assert.NoError(t, err, "%s/%s", outcome, reason)
			} else {
				rejected++
				assert.Error(t, err, "%s/%s", outcome, reason)
			}
		}
	}
	assert.Equal(t, 17, admitted)
	assert.Equal(t, 13, rejected)
}

func TestAuthorizationRejectsOutcomeIncompatibleReasons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		outcome Outcome
		reason  ReasonCode
	}{{OutcomeAllow, ReasonPolicyDenied}, {OutcomeDeny, ReasonAllowed}, {OutcomeDeny, ReasonInherited}} {
		event := validAuthorizationEvent(t)
		event.Outcome = tc.outcome
		if tc.outcome == OutcomeDeny {
			event.Severity = SeverityWarning
		}
		event.Payload = AuthorizationPayload{Permission: "agent.read", ReasonCode: tc.reason}
		assert.Error(t, Validate(event))
	}
}

func TestAuthorizationCatalogSnapshotIsImmutable(t *testing.T) {
	t.Parallel()

	first := Catalog()
	first[1].AllowedActions[0] = "operation-alias-canary"
	first[1].ActionPermissions[0].Permission = "action-permission-alias-canary"
	first[1].OutcomeReasons[0].AllowedReasons[0] = "outcome-reason-alias-canary"
	first[1].ProducerReasonMappings[0].Reason = "producer-reason-alias-canary"
	first[1].RequiredPayloadLeaves[0].AllowedValues[0] = "permission-alias-canary"
	second := Catalog()
	assert.NotContains(t, second[1].AllowedActions, "operation-alias-canary")
	assert.NotEqual(t, "action-permission-alias-canary", second[1].ActionPermissions[0].Permission)
	assert.NotContains(t, second[1].OutcomeReasons[0].AllowedReasons, "outcome-reason-alias-canary")
	assert.NotEqual(t, ReasonCode("producer-reason-alias-canary"), second[1].ProducerReasonMappings[0].Reason)
	assert.NotContains(t, second[1].RequiredPayloadLeaves[0].AllowedValues, "permission-alias-canary")
}

func TestAuthorizationBuilderDoesNotRetainInputAliases(t *testing.T) {
	t.Parallel()

	request := &RequestRef{ID: "request-1", Method: "GET", Route: "/api/v1/agents/{id}", Surface: "api"}
	initiator := &IdentityRef{Kind: IdentityUser, ID: "initiator-1"}
	executor := &IdentityRef{Kind: IdentitySystem, ID: "executor-1"}
	cacheHit := true
	event, err := buildAuthorizationDecision(
		AuditOperationContext{CorrelationID: "request-1"},
		AuthorizationDecisionInput{
			Operation:  "agent.read",
			Allowed:    true,
			Request:    request,
			Initiator:  initiator,
			Principal:  IdentityRef{Kind: IdentityUser, ID: "user-1"},
			Executor:   executor,
			Resource:   ResourceRef{Kind: "agent", ID: "agent-1", Scope: ResourceScopeProject, ProjectID: "project-1"},
			Permission: "agent.read",
			ReasonCode: ReasonAllowed,
			Purpose:    "interactive",
			CacheHit:   &cacheHit,
		},
		"22222222-2222-4222-8222-222222222222",
		time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)

	request.Route = "request-alias-canary"
	initiator.ID = "initiator-alias-canary"
	executor.ID = "executor-alias-canary"
	cacheHit = false
	rendered, err := Render(event)
	require.NoError(t, err)
	assert.NotContains(t, string(rendered), "alias-canary")
	assert.Contains(t, string(rendered), `"cache_hit":true`)
}

func TestAuthorizationBuilderRenderAndSinksRaceClosure(t *testing.T) {
	request := &RequestRef{ID: "request-1", Method: "GET"}
	cacheHit := true
	input := AuthorizationDecisionInput{
		Operation: "agent.read", Allowed: true, Request: request,
		Principal:  IdentityRef{Kind: IdentityUser, ID: "user-1"},
		Resource:   ResourceRef{Kind: "agent", ID: "agent-1", Scope: ResourceScopeProject, ProjectID: "project-1"},
		Permission: "agent.read", ReasonCode: ReasonAllowed, CacheHit: &cacheHit,
	}
	ctx := ContextWithOperation(context.Background(), AuditOperationContext{CorrelationID: "request-1"})
	event, err := BuildAuthorizationDecision(ctx, input)
	require.NoError(t, err)
	sink := NewCaptureSink()
	errors := make(chan error, 200)

	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for i := range 100 {
			request.Route = strings.Repeat("r", i%64+1)
			cacheHit = i%2 == 0
		}
	}()
	go func() {
		defer workers.Done()
		for range 100 {
			_, err := Render(event)
			errors <- err
			errors <- sink.Emit(ctx, event)
		}
	}()
	workers.Wait()
	close(errors)
	for err := range errors {
		assert.NoError(t, err)
	}
}

func TestAuthorizationAllowAndDenyRenderRawSlogAndOTelJSONEquivalence(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		name := "deny"
		if allowed {
			name = "allow"
		}
		t.Run(name, func(t *testing.T) {
			cacheHit := allowed
			credential := mustCredentialRef(t, CredentialRefInput{Kind: CredentialUAT, ID: "credential-1", Name: "deploy", Labels: map[string]string{"purpose": "automation"}})
			reason := ReasonPolicyDenied
			if allowed {
				reason = ReasonAllowed
			}
			event, err := buildAuthorizationDecision(AuditOperationContext{CorrelationID: "request-1"}, AuthorizationDecisionInput{
				Operation: "agent.read", Allowed: allowed,
				Request:   &RequestRef{ID: "request-1", Method: "GET", Route: "/api/v1/agents/{id}", Surface: "api"},
				Initiator: &IdentityRef{Kind: IdentityUser, ID: "initiator-1"}, Principal: IdentityRef{Kind: IdentityUser, ID: "user-1"}, Executor: &IdentityRef{Kind: IdentitySystem, ID: "executor-1"}, Credential: &credential,
				Resource: ResourceRef{Kind: "agent", ID: "agent-1", Scope: ResourceScopeProject, ProjectID: "project-1"}, Permission: "agent.read", ReasonCode: reason, Purpose: "interactive", CacheHit: &cacheHit,
			}, "22222222-2222-4222-8222-222222222222", time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			rendered, err := Render(event)
			require.NoError(t, err)

			rawHandler := &captureSlogHandler{}
			rawSink, err := NewSlogSink(slog.New(rawHandler))
			require.NoError(t, err)
			require.NoError(t, rawSink.Emit(context.Background(), event))
			assert.JSONEq(t, string(rendered), string(recordAttrsJSON(t, rawHandler.Records()[0])))

			exporter := &captureOTelExporter{}
			provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
			otelSink, err := NewSlogSink(slog.New(logging.NewOTelHandler("authorization-test", provider)))
			require.NoError(t, err)
			require.NoError(t, otelSink.Emit(context.Background(), event))
			otelJSON, err := json.Marshal(otelRecordMap(exporter.Records()[0]))
			require.NoError(t, err)
			assert.JSONEq(t, string(rendered), string(otelJSON))
			var object map[string]any
			require.NoError(t, json.Unmarshal(rendered, &object))
			payload := object["payload"].(map[string]any)
			assert.IsType(t, false, payload["cache_hit"])
			resource := object["resource"].(map[string]any)
			assert.NotContains(t, resource, "scope")
		})
	}
}

func TestAuthorizationBuilderRequiresOperationContext(t *testing.T) {
	t.Parallel()

	_, err := BuildAuthorizationDecision(context.Background(), AuthorizationDecisionInput{})
	assert.Error(t, err)
}

func validAuthorizationEvent(t *testing.T) EnvelopeV1 {
	t.Helper()
	event, err := buildAuthorizationDecision(
		AuditOperationContext{CorrelationID: "request-1"},
		AuthorizationDecisionInput{
			Operation:  authzop.OperationID("agent.read"),
			Allowed:    true,
			Principal:  IdentityRef{Kind: IdentityUser, ID: "user-1"},
			Resource:   ResourceRef{Kind: "agent", ID: "agent-1", Scope: ResourceScopeProject, ProjectID: "project-1"},
			Permission: PermissionName("agent.read"),
			ReasonCode: ReasonAllowed,
		},
		uuid.MustParse("22222222-2222-4222-8222-222222222222").String(),
		time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)
	return event
}
