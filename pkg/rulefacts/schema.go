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

// Package rulefacts defines the shared structured message-facts contract
// consumed by both message policies (pkg/messagepolicy) and conversation
// triggers (pkg/triggerrule).
//
// It is a leaf package: it imports only the Go standard library and
// pkg/celengine. It performs no I/O and never imports pkg/hub, pkg/store,
// pkg/messages, pkg/messaging, or any consumer package.
package rulefacts

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/celengine"
)

// SchemaVersion is the current version of the message-facts contract registry.
const SchemaVersion = 1

// MaxBodyBytes is the UTF-8 safe cap applied to message.body in projected
// activations (16 KiB, matching messagepolicy.MaxBodyBytes).
const MaxBodyBytes = 16 * 1024

// Grade classifies the trust level of a fact path (contract v1 §3.3).
type Grade string

const (
	// GradeAuthority marks hub-written facts that are immutable or changed only
	// by an authorized hub operation; safe to gate authority-bearing actions on.
	GradeAuthority Grade = "authority"

	// GradeDescriptor marks hub-resolved facts that are agent- or user-influenced;
	// suitable for selection and display, not security decisions.
	GradeDescriptor Grade = "descriptor"

	// GradeAsserted marks structured fields chosen by the sender and carried as
	// sent; typed and bounded, but not vouched for by the hub.
	GradeAsserted Grade = "asserted"

	// GradeAssertedUnlessSystemOrigin marks hub-derived fields whose input is
	// sender-chosen unless message.origin is trigger, scheduler, or system.
	GradeAssertedUnlessSystemOrigin Grade = "asserted_unless_system_origin"

	// GradeContent marks free text or attachments; untrusted, bounded, and framed
	// as data downstream.
	GradeContent Grade = "content"

	// GradeExternal marks integrity-verified descriptors passed through from an
	// outside system (never external payload bytes).
	GradeExternal Grade = "external"
)

// Valid reports whether g is a recognized trust grade.
func (g Grade) Valid() bool {
	switch g {
	case GradeAuthority, GradeDescriptor, GradeAsserted,
		GradeAssertedUnlessSystemOrigin, GradeContent, GradeExternal:
		return true
	default:
		return false
	}
}

// IdentityRef classifies whom a fact path can identify (Amendment A1 §B.1).
// Every registry field MUST declare an explicit IdentityRef; there is no default.
type IdentityRef string

const (
	// IdentityRefNone indicates the path carries no principal identity.
	IdentityRefNone IdentityRef = "none"

	// IdentityRefSelfEnd indicates the path belongs to the visible end in the
	// current scope (sender in policy_out/a2a, recipient in policy_in/a2a).
	IdentityRefSelfEnd IdentityRef = "self_end"

	// IdentityRefOppositeEnd indicates the path belongs to the opposite end and
	// must be dropped from policy_out / policy_in activations.
	IdentityRefOppositeEnd IdentityRef = "opposite_end"

	// IdentityRefThirdParty indicates the path names third-party principals
	// (mentions lists, participant lists, unmentioned slugs).
	IdentityRefThirdParty IdentityRef = "third_party"

	// IdentityRefValueDependent indicates the path may embed an opposite-end or
	// third-party identity depending on its runtime value; Project drops it in
	// policy_out and policy_in unless provably equal to the visible end.
	IdentityRefValueDependent IdentityRef = "value_dependent"
)

// Valid reports whether r is a recognized IdentityRef classification.
func (r IdentityRef) Valid() bool {
	switch r {
	case IdentityRefNone, IdentityRefSelfEnd, IdentityRefOppositeEnd,
		IdentityRefThirdParty, IdentityRefValueDependent:
		return true
	default:
		return false
	}
}

// Scope identifies a consumer visibility scope (contract v1 §3.4, §4.1).
type Scope string

const (
	// ScopePolicyOut is the outbound message-policy scope (sender visible, recipient absent).
	ScopePolicyOut Scope = "policy_out"

	// ScopePolicyIn is the inbound message-policy scope (recipient visible, sender absent).
	ScopePolicyIn Scope = "policy_in"

	// ScopePolicyA2A is the agent-to-agent message-policy scope (both ends and identity lists visible).
	ScopePolicyA2A Scope = "policy_a2a"

	// ScopeTriggerChat is the post-persist conversation-trigger scope (a2a-equivalent per ptone Q1).
	ScopeTriggerChat Scope = "trigger_chat"

	// ScopeLegacyPolicyV1 is the frozen legacy message_policy v1 mirror scope.
	// It is test/compatibility-only and never widens the frozen v1 surface:
	// production legacy activation remains in pkg/messagepolicy.
	ScopeLegacyPolicyV1 Scope = "legacy_policy_v1"
)

// AllScopes lists the four canonical contract scopes in deterministic order.
var AllScopes = []Scope{
	ScopePolicyOut,
	ScopePolicyIn,
	ScopePolicyA2A,
	ScopeTriggerChat,
}

// Valid reports whether s is a recognized scope.
func (s Scope) Valid() bool {
	switch s {
	case ScopePolicyOut, ScopePolicyIn, ScopePolicyA2A, ScopeTriggerChat, ScopeLegacyPolicyV1:
		return true
	default:
		return false
	}
}

// Bit returns the ScopeSet bit corresponding to s, or 0 if s is invalid or is
// the mirror-only ScopeLegacyPolicyV1 (which is not part of the canonical
// registry's ScopeSet).
func (s Scope) Bit() ScopeSet {
	switch s {
	case ScopePolicyOut:
		return ScopeBitPolicyOut
	case ScopePolicyIn:
		return ScopeBitPolicyIn
	case ScopePolicyA2A:
		return ScopeBitPolicyA2A
	case ScopeTriggerChat:
		return ScopeBitTriggerChat
	default:
		return 0
	}
}

// ScopeSet is a bitset of canonical consumer visibility scopes.
type ScopeSet uint8

const (
	ScopeBitPolicyOut ScopeSet = 1 << iota
	ScopeBitPolicyIn
	ScopeBitPolicyA2A
	ScopeBitTriggerChat

	// ScopesPolicy covers the three directional message-policy scopes.
	ScopesPolicy = ScopeBitPolicyOut | ScopeBitPolicyIn | ScopeBitPolicyA2A

	// ScopesAll covers all four canonical scopes (policy_out, policy_in, policy_a2a, trigger_chat).
	ScopesAll = ScopesPolicy | ScopeBitTriggerChat
)

// Has reports whether s includes scope.
func (s ScopeSet) Has(scope Scope) bool {
	b := scope.Bit()
	return b != 0 && (s&b) != 0
}

// Scopes returns the scopes present in s in canonical order.
func (s ScopeSet) Scopes() []Scope {
	var out []Scope
	for _, sc := range AllScopes {
		if s.Has(sc) {
			out = append(out, sc)
		}
	}
	return out
}

// MarshalJSON encodes ScopeSet as a deterministic JSON array of scope names.
func (s ScopeSet) MarshalJSON() ([]byte, error) {
	names := make([]string, 0, len(AllScopes))
	for _, sc := range s.Scopes() {
		names = append(names, string(sc))
	}
	return json.Marshal(names)
}

// CELTypeKind enumerates the contract's CEL type descriptors (v1 §4.1).
type CELTypeKind string

const (
	CELKindString    CELTypeKind = "string"
	CELKindInt       CELTypeKind = "int"
	CELKindBool      CELTypeKind = "bool"
	CELKindTimestamp CELTypeKind = "timestamp"
	CELKindList      CELTypeKind = "list"
	CELKindMap       CELTypeKind = "map"
	CELKindObject    CELTypeKind = "object"
)

// CELType describes the CEL type of a registry path. In R1, object(<name>)
// maps to map(string, dyn) at the celengine boundary; R3 switches it to a
// declared-object type provider built from the same registry (v1 §4.2).
type CELType struct {
	Kind       CELTypeKind
	Elem       *CELType
	Key        *CELType
	Val        *CELType
	ObjectName string
}

// TypeString returns the string CELType.
func TypeString() CELType { return CELType{Kind: CELKindString} }

// TypeInt returns the int CELType.
func TypeInt() CELType { return CELType{Kind: CELKindInt} }

// TypeBool returns the bool CELType.
func TypeBool() CELType { return CELType{Kind: CELKindBool} }

// TypeTimestamp returns the timestamp CELType.
func TypeTimestamp() CELType { return CELType{Kind: CELKindTimestamp} }

// TypeList returns a homogeneous list CELType.
func TypeList(elem CELType) CELType {
	e := elem
	return CELType{Kind: CELKindList, Elem: &e}
}

// TypeMap returns a map CELType.
func TypeMap(key, val CELType) CELType {
	k, v := key, val
	return CELType{Kind: CELKindMap, Key: &k, Val: &v}
}

// TypeObject returns a named object CELType.
func TypeObject(name string) CELType {
	return CELType{Kind: CELKindObject, ObjectName: name}
}

// Valid reports whether t is a well-formed CELType.
func (t CELType) Valid() bool {
	switch t.Kind {
	case CELKindString, CELKindInt, CELKindBool, CELKindTimestamp:
		return true
	case CELKindList:
		return t.Elem != nil && t.Elem.Valid()
	case CELKindMap:
		return t.Key != nil && t.Val != nil && t.Key.Valid() && t.Val.Valid()
	case CELKindObject:
		return strings.TrimSpace(t.ObjectName) != ""
	default:
		return false
	}
}

// String returns the canonical string spelling of t.
func (t CELType) String() string {
	switch t.Kind {
	case CELKindString, CELKindInt, CELKindBool, CELKindTimestamp:
		return string(t.Kind)
	case CELKindList:
		if t.Elem == nil {
			return "list(<invalid>)"
		}
		return "list(" + t.Elem.String() + ")"
	case CELKindMap:
		if t.Key == nil || t.Val == nil {
			return "map(<invalid>)"
		}
		return "map(" + t.Key.String() + "," + t.Val.String() + ")"
	case CELKindObject:
		return "object(" + t.ObjectName + ")"
	default:
		return "<invalid>"
	}
}

// MarshalJSON encodes CELType as its canonical string representation.
func (t CELType) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// ToCELType converts t to its R1 celengine.Type representation.
// Object types project to map(string, dyn) in R1 (v1 §4.2).
func (t CELType) ToCELType() celengine.Type {
	switch t.Kind {
	case CELKindString:
		return celengine.String()
	case CELKindInt:
		return celengine.Int()
	case CELKindBool:
		return celengine.Bool()
	case CELKindTimestamp:
		return celengine.Timestamp()
	case CELKindList:
		if t.Elem == nil {
			return celengine.Type{}
		}
		return celengine.List(t.Elem.ToCELType())
	case CELKindMap:
		if t.Key == nil || t.Val == nil {
			return celengine.Type{}
		}
		return celengine.Map(t.Key.ToCELType(), t.Val.ToCELType())
	case CELKindObject:
		return celengine.Map(celengine.String(), celengine.Dyn())
	default:
		return celengine.Type{}
	}
}

// ContentInputKind distinguishes System One text vs list input fields (v1 §3.5).
type ContentInputKind string

const (
	ContentInputText ContentInputKind = "text"
	ContentInputList ContentInputKind = "list"
)

// ContentInput records System One content-environment eligibility for a field (v1 §3.5).
type ContentInput struct {
	Eligible    bool             `json:"eligible"`
	MaxBytes    int              `json:"max_bytes"`
	Kind        ContentInputKind `json:"kind"`
	ContentOnly bool             `json:"content_only,omitempty"` // true if NOT emitted in CEL activation maps
}

// Field describes a single declared path in the message-facts contract (v1 §4.1, A1 §B.1).
type Field struct {
	Path        string        `json:"path"`
	Type        CELType       `json:"type"`
	Grade       Grade         `json:"grade"`
	IdentityRef IdentityRef   `json:"identity_ref"`
	Producer    string        `json:"producer"`
	Visible     ScopeSet      `json:"visible"`
	Since       int           `json:"since"`
	Derived     bool          `json:"derived,omitempty"`
	Content     *ContentInput `json:"content,omitempty"`
}

// IdentityRefFor returns the effective IdentityRef of f in the given scope.
// sender.* is opposite_end in policy_in and self_end in policy_out/policy_a2a/trigger_chat.
// recipient.* is opposite_end in policy_out and self_end in policy_in/policy_a2a/trigger_chat.
func (f Field) IdentityRefFor(scope Scope) IdentityRef {
	if f.Path == "sender" || strings.HasPrefix(f.Path, "sender.") {
		if scope == ScopePolicyIn {
			return IdentityRefOppositeEnd
		}
		return IdentityRefSelfEnd
	}
	if f.Path == "recipient" || strings.HasPrefix(f.Path, "recipient.") {
		if scope == ScopePolicyOut {
			return IdentityRefOppositeEnd
		}
		return IdentityRefSelfEnd
	}
	return f.IdentityRef
}

// Registry is the single source of truth for all declared message-fact paths.
type Registry struct {
	Version int
	Fields  []Field

	byPath map[string]Field
}

// Lookup returns the Field registered at path, if any.
func (r *Registry) Lookup(path string) (Field, bool) {
	if r.byPath != nil {
		f, ok := r.byPath[path]
		return f, ok
	}
	for _, f := range r.Fields {
		if f.Path == path {
			return f, true
		}
	}
	return Field{}, false
}

// VisibleIn returns all fields visible in scope, preserving registry order.
func (r *Registry) VisibleIn(scope Scope) []Field {
	var out []Field
	for _, f := range r.Fields {
		if f.Visible.Has(scope) {
			out = append(out, f)
		}
	}
	return out
}

// ValidatePath checks whether path is a declared CEL fact path visible in scope.
// Consumers use this for authoring-time literal-path validation (v1 §4.2, R1).
// Content-only fields (such as conversation.unmentioned_agent_slugs) are
// rejected for CEL path validation. Dynamic label keys under
// sender.attributes.labels.<k> and recipient.attributes.labels.<k> are accepted
// when the parent labels map is visible in scope.
func (r *Registry) ValidatePath(scope Scope, path string) error {
	if !scope.Valid() {
		return fmt.Errorf("rulefacts: unknown scope %q", scope)
	}
	f, ok := r.Lookup(path)
	if !ok {
		// Allow dynamic map keys under *.attributes.labels.<key>.
		for _, prefix := range []string{"sender.attributes.labels.", "recipient.attributes.labels."} {
			if strings.HasPrefix(path, prefix) && len(path) > len(prefix) && !strings.Contains(path[len(prefix):], ".") {
				parent := strings.TrimSuffix(prefix, ".")
				if pf, pok := r.Lookup(parent); pok && pf.Visible.Has(scope) {
					return nil
				}
				return fmt.Errorf("rulefacts: path %q is not visible in scope %q", path, scope)
			}
		}
		return fmt.Errorf("rulefacts: unknown fact path %q", path)
	}
	if f.Content != nil && f.Content.ContentOnly {
		return fmt.Errorf("rulefacts: path %q is content-only and not a CEL fact path", path)
	}
	if !f.Visible.Has(scope) {
		return fmt.Errorf("rulefacts: path %q is not visible in scope %q", path, scope)
	}
	return nil
}

// Validate checks internal invariants of the registry: non-zero version, unique
// paths, valid types, valid grades, mandatory IdentityRef, non-empty producer,
// and non-empty visibility.
func (r *Registry) Validate() error {
	if r.Version < 1 {
		return fmt.Errorf("rulefacts: registry Version must be >= 1 (got %d)", r.Version)
	}
	seen := make(map[string]bool, len(r.Fields))
	for i, f := range r.Fields {
		if strings.TrimSpace(f.Path) == "" {
			return fmt.Errorf("rulefacts: field[%d] has empty Path", i)
		}
		if seen[f.Path] {
			return fmt.Errorf("rulefacts: duplicate field Path %q", f.Path)
		}
		seen[f.Path] = true
		if !f.Type.Valid() {
			return fmt.Errorf("rulefacts: field %q has invalid Type %q", f.Path, f.Type.String())
		}
		if !f.Grade.Valid() {
			return fmt.Errorf("rulefacts: field %q has invalid Grade %q", f.Path, f.Grade)
		}
		if !f.IdentityRef.Valid() {
			return fmt.Errorf("rulefacts: field %q missing or invalid IdentityRef %q", f.Path, f.IdentityRef)
		}
		if strings.TrimSpace(f.Producer) == "" {
			return fmt.Errorf("rulefacts: field %q has empty Producer", f.Path)
		}
		if f.Visible == 0 {
			return fmt.Errorf("rulefacts: field %q has empty Visible scope set", f.Path)
		}
		if f.Since < 1 || f.Since > r.Version {
			return fmt.Errorf("rulefacts: field %q has invalid Since %d (registry version %d)", f.Path, f.Since, r.Version)
		}
		if f.Content != nil {
			if f.Content.MaxBytes <= 0 {
				return fmt.Errorf("rulefacts: field %q Content.MaxBytes must be > 0", f.Path)
			}
			if f.Content.Kind != ContentInputText && f.Content.Kind != ContentInputList {
				return fmt.Errorf("rulefacts: field %q invalid Content.Kind %q", f.Path, f.Content.Kind)
			}
		}
	}
	return nil
}

// ContentInputFields returns all fields eligible as System One content inputs
// in the given scope, in deterministic registry order.
func (r *Registry) ContentInputFields(scope Scope) []Field {
	var out []Field
	for _, f := range r.Fields {
		if f.Content != nil && f.Content.Eligible && f.Visible.Has(scope) {
			out = append(out, f)
		}
	}
	return out
}

func endFields(prefix string, visible ScopeSet, isRecipient bool) []Field {
	var slugContent, kindContent *ContentInput
	slugContent = &ContentInput{Eligible: true, MaxBytes: 128, Kind: ContentInputText}
	if isRecipient {
		kindContent = &ContentInput{Eligible: true, MaxBytes: 16, Kind: ContentInputText}
	}
	return []Field{
		{Path: prefix, Type: TypeObject("End"), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:principal", Visible: visible, Since: 1},
		{Path: prefix + ".kind", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:principal", Visible: visible, Since: 1, Content: kindContent},
		{Path: prefix + ".id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:principal", Visible: visible, Since: 1},
		{Path: prefix + ".slug", Type: TypeString(), Grade: GradeDescriptor, IdentityRef: IdentityRefSelfEnd, Producer: "hub:principal", Visible: visible, Since: 1, Content: slugContent},
		{Path: prefix + ".project_id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:principal", Visible: visible, Since: 1},
		{Path: prefix + ".created_by", Type: TypeObject("PrincipalRef"), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:agent_row", Visible: visible, Since: 1},
		{Path: prefix + ".created_by.kind", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:agent_row", Visible: visible, Since: 1},
		{Path: prefix + ".created_by.id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:agent_row", Visible: visible, Since: 1},
		{Path: prefix + ".root_user_id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:agent_row", Visible: visible, Since: 1},
		{Path: prefix + ".attributes", Type: TypeObject("EndAttributes"), Grade: GradeDescriptor, IdentityRef: IdentityRefSelfEnd, Producer: "hub:agent_row", Visible: visible, Since: 1},
		{Path: prefix + ".attributes.name", Type: TypeString(), Grade: GradeDescriptor, IdentityRef: IdentityRefSelfEnd, Producer: "hub:agent_row", Visible: visible, Since: 1},
		{Path: prefix + ".attributes.template", Type: TypeString(), Grade: GradeDescriptor, IdentityRef: IdentityRefSelfEnd, Producer: "hub:template_lookup", Visible: visible, Since: 1},
		{Path: prefix + ".attributes.template_lookup", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:template_lookup", Visible: visible, Since: 1},
		{Path: prefix + ".attributes.template_id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:agent_row", Visible: visible, Since: 1},
		{Path: prefix + ".attributes.template_version", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefSelfEnd, Producer: "hub:agent_row", Visible: visible, Since: 1},
		{Path: prefix + ".attributes.labels", Type: TypeMap(TypeString(), TypeString()), Grade: GradeDescriptor, IdentityRef: IdentityRefSelfEnd, Producer: "hub:agent_row", Visible: visible, Since: 1},
	}
}

func buildSchema() Registry {
	senderScopes := ScopeBitPolicyOut | ScopeBitPolicyA2A | ScopeBitTriggerChat
	recipientScopes := ScopeBitPolicyIn | ScopeBitPolicyA2A | ScopeBitTriggerChat
	a2aAndTrigger := ScopeBitPolicyA2A | ScopeBitTriggerChat

	fields := []Field{
		// message.* (envelope, persisted-only id/created_at, kind, payloads, content)
		{Path: "message", Type: TypeObject("Message"), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:message_row", Visible: ScopeBitTriggerChat, Since: 1},
		{Path: "message.created_at", Type: TypeTimestamp(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:message_row", Visible: ScopeBitTriggerChat, Since: 1},
		{Path: "message.kind", Type: TypeString(), Grade: GradeAssertedUnlessSystemOrigin, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.intent", Type: TypeString(), Grade: GradeAsserted, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.type", Type: TypeString(), Grade: GradeAsserted, IdentityRef: IdentityRefNone, Producer: "sender", Visible: ScopesAll, Since: 1, Content: &ContentInput{Eligible: true, MaxBytes: 64, Kind: ContentInputText}},
		{Path: "message.origin", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.body", Type: TypeString(), Grade: GradeContent, IdentityRef: IdentityRefNone, Producer: "sender", Visible: ScopesAll, Since: 1, Content: &ContentInput{Eligible: true, MaxBytes: MaxBodyBytes, Kind: ContentInputText}},
		{Path: "message.bytes", Type: TypeInt(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.body_truncated", Type: TypeBool(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.body_source", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.has_choices", Type: TypeBool(), Grade: GradeAsserted, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.urgent", Type: TypeBool(), Grade: GradeAsserted, IdentityRef: IdentityRefNone, Producer: "sender", Visible: ScopesAll, Since: 1},
		{Path: "message.attachment_count", Type: TypeInt(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.artifact_count", Type: TypeInt(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},

		// message.event.* (present only when kind == "event"; EventBody.Attributes/Lists/URL are NOT facts in v1)
		// A1 §B.2 static visibility: subject, reason, and templated_vars are ALWAYS
		// dropped in policy_out and policy_in, so their Visible set excludes out/in.
		{Path: "message.event", Type: TypeObject("Event"), Grade: GradeAssertedUnlessSystemOrigin, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.event.type", Type: TypeString(), Grade: GradeAssertedUnlessSystemOrigin, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.event.event_type", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.event_id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.fire_id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.status", Type: TypeString(), Grade: GradeAssertedUnlessSystemOrigin, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.event.reason", Type: TypeString(), Grade: GradeAssertedUnlessSystemOrigin, IdentityRef: IdentityRefValueDependent, Producer: "hub:derived", Visible: a2aAndTrigger, Since: 1},
		{Path: "message.event.subject", Type: TypeString(), Grade: GradeAssertedUnlessSystemOrigin, IdentityRef: IdentityRefValueDependent, Producer: "hub:derived", Visible: a2aAndTrigger, Since: 1},
		{Path: "message.event.source", Type: TypeObject("EventSource"), Grade: GradeAuthority, IdentityRef: IdentityRefValueDependent, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.source.kind", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.source.id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefValueDependent, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.source.name", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefValueDependent, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.trigger", Type: TypeObject("EventTrigger"), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.trigger.id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.trigger.name", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.schedule", Type: TypeObject("EventSchedule"), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:scheduler", Visible: ScopesAll, Since: 1},
		{Path: "message.event.schedule.id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:scheduler", Visible: ScopesAll, Since: 1},
		{Path: "message.event.schedule.name", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:scheduler", Visible: ScopesAll, Since: 1},
		{Path: "message.event.schedule.event_id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:scheduler", Visible: ScopesAll, Since: 1},
		{Path: "message.event.schedule.fire_at", Type: TypeTimestamp(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:scheduler", Visible: ScopesAll, Since: 1},
		{Path: "message.event.external", Type: TypeObject("EventExternal"), Grade: GradeExternal, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.external.present", Type: TypeBool(), Grade: GradeExternal, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.external.verified", Type: TypeString(), Grade: GradeExternal, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.external.bytes", Type: TypeInt(), Grade: GradeExternal, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.external.truncated", Type: TypeBool(), Grade: GradeExternal, IdentityRef: IdentityRefNone, Producer: "hub:trigger", Visible: ScopesAll, Since: 1},
		{Path: "message.event.templated_vars", Type: TypeList(TypeString()), Grade: GradeAuthority, IdentityRef: IdentityRefValueDependent, Producer: "hub:trigger", Visible: a2aAndTrigger, Since: 1},

		// message.interaction.* (present only for hub-minted interaction types)
		{Path: "message.interaction", Type: TypeObject("Interaction"), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.interaction.kind", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.interaction.count", Type: TypeInt(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},

		// message.system.* (present for type == system with system_category)
		{Path: "message.system", Type: TypeObject("SystemInfo"), Grade: GradeAssertedUnlessSystemOrigin, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "message.system.category", Type: TypeString(), Grade: GradeAssertedUnlessSystemOrigin, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
	}

	// sender.* and recipient.*
	fields = append(fields, endFields("sender", senderScopes, false)...)
	fields = append(fields, endFields("recipient", recipientScopes, true)...)

	// conversation.*, mentions.*, delivery.*
	fields = append(fields, []Field{
		{Path: "conversation", Type: TypeObject("Conversation"), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:conversation", Visible: ScopesAll, Since: 1},
		{Path: "conversation.ref", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefValueDependent, Producer: "hub:conversation", Visible: ScopesAll, Since: 1},
		{Path: "conversation.id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefValueDependent, Producer: "hub:conversation", Visible: ScopesAll, Since: 1},
		{Path: "conversation.kind", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:conversation", Visible: ScopesAll, Since: 1},
		{Path: "conversation.surface", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:conversation", Visible: ScopesAll, Since: 1},
		{Path: "conversation.project_id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:conversation", Visible: ScopesAll, Since: 1},
		{Path: "conversation.thread_id", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:conversation", Visible: ScopesAll, Since: 1},
		{Path: "conversation.agent_count", Type: TypeInt(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:conversation", Visible: ScopesAll, Since: 1},
		{Path: "conversation.user_count", Type: TypeInt(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:conversation", Visible: ScopesAll, Since: 1},
		{Path: "conversation.agent_participant_ids", Type: TypeList(TypeString()), Grade: GradeAuthority, IdentityRef: IdentityRefThirdParty, Producer: "hub:conversation", Visible: a2aAndTrigger, Since: 1},
		{Path: "conversation.user_participant_ids", Type: TypeList(TypeString()), Grade: GradeAuthority, IdentityRef: IdentityRefThirdParty, Producer: "hub:conversation", Visible: a2aAndTrigger, Since: 1},
		{Path: "conversation.unmentioned_agent_slugs", Type: TypeList(TypeString()), Grade: GradeDescriptor, IdentityRef: IdentityRefThirdParty, Producer: "hub:derived", Visible: ScopeBitPolicyA2A, Since: 1, Derived: true, Content: &ContentInput{Eligible: true, MaxBytes: 2048, Kind: ContentInputList, ContentOnly: true}},

		{Path: "mentions", Type: TypeObject("Mentions"), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "mentions.agent_count", Type: TypeInt(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "mentions.user_count", Type: TypeInt(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:derived", Visible: ScopesAll, Since: 1},
		{Path: "mentions.agent_ids", Type: TypeList(TypeString()), Grade: GradeAuthority, IdentityRef: IdentityRefThirdParty, Producer: "hub:derived", Visible: a2aAndTrigger, Since: 1},
		{Path: "mentions.user_ids", Type: TypeList(TypeString()), Grade: GradeAuthority, IdentityRef: IdentityRefThirdParty, Producer: "hub:derived", Visible: a2aAndTrigger, Since: 1},

		{Path: "delivery", Type: TypeObject("Delivery"), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:delivery", Visible: ScopesAll, Since: 1},
		{Path: "delivery.surface", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:delivery", Visible: ScopesAll, Since: 1},
		{Path: "delivery.via", Type: TypeString(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:delivery", Visible: ScopesAll, Since: 1},
		{Path: "delivery.copy", Type: TypeBool(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:delivery", Visible: ScopesAll, Since: 1},
		{Path: "delivery.cross_project", Type: TypeBool(), Grade: GradeAuthority, IdentityRef: IdentityRefOppositeEnd, Producer: "hub:delivery", Visible: a2aAndTrigger, Since: 1},
		{Path: "delivery.delegated", Type: TypeBool(), Grade: GradeAuthority, IdentityRef: IdentityRefNone, Producer: "hub:delivery", Visible: ScopesAll, Since: 1},
	}...)

	byPath := make(map[string]Field, len(fields))
	for _, f := range fields {
		byPath[f.Path] = f
	}
	return Registry{
		Version: SchemaVersion,
		Fields:  slices.Clone(fields),
		byPath:  byPath,
	}
}

// Schema is the single source of truth for the message-facts contract (v1 + A1 + ptone addendum).
var Schema = buildSchema()
