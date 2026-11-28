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

package rulefacts

import "github.com/GoogleCloudPlatform/scion/pkg/celengine"

// Naming selects the outer CEL variable naming convention for a consumer (v1 §3.1, §4.1).
type Naming string

const (
	// NamingPolicy declares top-level policy roots (message, sender/recipient per
	// scope, conversation, mentions, delivery).
	NamingPolicy Naming = "policy"

	// NamingTrigger declares the top-level trigger event root (event, carrying
	// event.actor, event.target, and event.message with event.message.context).
	NamingTrigger Naming = "trigger"

	// NamingLegacyPolicyV1 declares the legacy message_policy v1 top-level fact
	// roots (surface, sender, recipient, message, mentioned_agent_ids,
	// mentioned_user_ids, conversation) for compatibility verification.
	NamingLegacyPolicyV1 Naming = "legacy_policy_v1"
)

// Declarations returns the CEL top-level variable declarations for the given
// scope and consumer naming convention, derived directly from Schema (v1 §4.1).
//
// In R1, structured objects are declared as map(string, dyn) and paired with
// Schema.ValidatePath for authoring-time path checking; R3 upgrades them to
// declared object types from the same Schema (v1 §4.2).
//
// Consumer-owned variables (such as chain.*, judgment_flags/labels/scores, or
// trigger.*) are not part of this contract and are appended by each consumer.
func Declarations(scope Scope, outer Naming) []celengine.Variable {
	if !scope.Valid() {
		return nil
	}
	switch outer {
	case NamingTrigger:
		if scope != ScopeTriggerChat {
			return nil
		}
		return []celengine.Variable{
			{Name: "event", Type: celengine.Map(celengine.String(), celengine.Dyn())},
		}

	case NamingLegacyPolicyV1:
		if scope != ScopeLegacyPolicyV1 {
			return nil
		}
		// Exact mirror of today's frozen message_policy v1 mechanical variables
		// (excluding consumer-owned judgment_* maps).
		return []celengine.Variable{
			{Name: "surface", Type: celengine.String()},
			{Name: "sender", Type: celengine.Map(celengine.String(), celengine.String())},
			{Name: "recipient", Type: celengine.Map(celengine.String(), celengine.String())},
			{Name: "message", Type: celengine.Map(celengine.String(), celengine.Dyn())},
			{Name: "mentioned_agent_ids", Type: celengine.List(celengine.String())},
			{Name: "mentioned_user_ids", Type: celengine.List(celengine.String())},
			{Name: "conversation", Type: celengine.Map(celengine.String(), celengine.Dyn())},
		}

	case NamingPolicy:
		// Derive top-level roots strictly from Schema visibility for scope.
		rootOrder := []string{"message", "sender", "recipient", "conversation", "mentions", "delivery"}
		vars := make([]celengine.Variable, 0, len(rootOrder))
		for _, root := range rootOrder {
			f, ok := Schema.Lookup(root)
			if !ok || !f.Visible.Has(scope) {
				continue
			}
			vars = append(vars, celengine.Variable{
				Name: root,
				Type: f.Type.ToCELType(),
			})
		}
		return vars

	default:
		return nil
	}
}
