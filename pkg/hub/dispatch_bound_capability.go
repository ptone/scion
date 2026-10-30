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

package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// errBrokerLacksDispatchBoundRead is matched by the refusal of a user-scoped
// template on a Runtime Broker known to lack the dispatchBoundCatalogRead
// capability (ptone/scion#5770): such a broker never names the agent on its
// template reads, so its hydration would fail. Handlers map it to 412.
var errBrokerLacksDispatchBoundRead = errors.New("runtime broker does not support user-scoped templates")

// brokerLacksDispatchBoundReadError is errBrokerLacksDispatchBoundRead with
// the template and broker names.
type brokerLacksDispatchBoundReadError struct{ template, broker string }

func (e *brokerLacksDispatchBoundReadError) Error() string {
	return fmt.Sprintf("user-scoped template %s cannot be used on Runtime Broker %s: the broker does not support user-scoped templates; upgrade it or use a project or global template", e.template, e.broker)
}

func (e *brokerLacksDispatchBoundReadError) Unwrap() error { return errBrokerLacksDispatchBoundRead }

// checkDispatchBoundReadCapability refuses a user-scoped template on a
// broker known to lack dispatchBoundCatalogRead: capabilities recorded and
// the flag false. A broker with no recorded capabilities is tried, as for
// AsyncStart; global and project templates are never refused.
func checkDispatchBoundReadCapability(tmpl *store.Template, broker *store.RuntimeBroker) error {
	if tmpl == nil || tmpl.Scope != store.TemplateScopeUser || broker == nil || broker.Capabilities == nil {
		return nil
	}
	if broker.Capabilities.DispatchBoundCatalogRead {
		return nil
	}
	name := tmpl.Slug
	if name == "" {
		name = tmpl.Name
	}
	bname := broker.Name
	if bname == "" {
		bname = broker.ID
	}
	return &brokerLacksDispatchBoundReadError{template: name, broker: bname}
}

// writeDispatchBoundReadCapabilityError writes the 412 for err if it is an
// errBrokerLacksDispatchBoundRead error and reports whether it did.
func writeDispatchBoundReadCapabilityError(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, errBrokerLacksDispatchBoundRead) {
		return false
	}
	writeError(w, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability, err.Error(), nil)
	return true
}

// requireDispatchBoundReadCapability is the handler-side gate for a create:
// it loads brokerID and writes a 412 when tmpl is user-scoped and the broker
// is known to lack the capability. Returns false when a response was
// written.
func (s *Server) requireDispatchBoundReadCapability(ctx context.Context, w http.ResponseWriter, tmpl *store.Template, brokerID string) bool {
	if tmpl == nil || tmpl.Scope != store.TemplateScopeUser || brokerID == "" {
		return true
	}
	broker, err := s.store.GetRuntimeBroker(ctx, brokerID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return false
	}
	return !writeDispatchBoundReadCapabilityError(w, checkDispatchBoundReadCapability(tmpl, broker))
}

// requireDispatchBoundReadCapabilityForAgent is the gate for a start,
// restart or resume of an existing agent: it checks the template the hub
// recorded for the agent (AppliedConfig.TemplateID) against the agent's
// broker. A template that no longer exists has nothing to gate.
func (s *Server) requireDispatchBoundReadCapabilityForAgent(ctx context.Context, w http.ResponseWriter, agent *store.Agent) bool {
	if agent == nil || agent.AppliedConfig == nil || agent.AppliedConfig.TemplateID == "" || agent.RuntimeBrokerID == "" {
		return true
	}
	tmpl, err := s.store.GetTemplate(ctx, agent.AppliedConfig.TemplateID)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	if err != nil {
		writeErrorFromErr(w, err, "")
		return false
	}
	return s.requireDispatchBoundReadCapability(ctx, w, tmpl, agent.RuntimeBrokerID)
}
