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
	}{
		{name: "allow", allowed: true, outcome: OutcomeAllow, severity: SeverityInfo},
		{name: "deny", allowed: false, outcome: OutcomeDeny, severity: SeverityWarning},
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
				ReasonCode: ReasonAllowed,
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
				ReasonCode: ReasonAllowed,
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

	for _, operation := range declaredAuthorizationOperations {
		event := validAuthorizationEvent(t)
		event.Action = string(operation)
		assert.NoError(t, Validate(event), operation)
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
	assert.Equal(t, "bcebee1b06b534193d705f66e50a57b5ef850d306aa8bb23d7e8d1a9e34e745a", hex.EncodeToString(digest[:]))
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

func TestAuthorizationReasonCodeClosedSet(t *testing.T) {
	t.Parallel()

	for _, reason := range reasonCodeStrings() {
		event := validAuthorizationEvent(t)
		event.Payload = AuthorizationPayload{Permission: "agent.read", ReasonCode: ReasonCode(reason)}
		assert.NoError(t, Validate(event), reason)
	}
}

func TestAuthorizationCatalogSnapshotIsImmutable(t *testing.T) {
	t.Parallel()

	first := Catalog()
	first[1].AllowedActions[0] = "operation-alias-canary"
	first[1].RequiredPayloadLeaves[0].AllowedValues[0] = "permission-alias-canary"
	second := Catalog()
	assert.NotContains(t, second[1].AllowedActions, "operation-alias-canary")
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
			require.NoError(t, err)
			require.NoError(t, sink.Emit(ctx, event))
		}
	}()
	workers.Wait()
}

func TestAuthorizationAllowAndDenyRenderRawSlogAndOTelJSONEquivalence(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		name := "deny"
		if allowed {
			name = "allow"
		}
		t.Run(name, func(t *testing.T) {
			event := validAuthorizationEvent(t)
			if !allowed {
				event.Outcome = OutcomeDeny
				event.Severity = SeverityWarning
			}
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
