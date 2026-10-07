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

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// RecipeSchema is the only accepted value of Recipe.Schema.
const RecipeSchema = "scion.wave01.fixtures/v1"

// Content limits that keep fixture text realistic and non-pathological.
const (
	maxLabels         = 8
	maxLabelKeyLen    = 63
	maxLabelValueLen  = 128
	maxTaskSummaryLen = 600
	maxMessageLen     = 400
	maxBrokerNameLen  = 253
	maxTemplateLen    = 63
)

// Recipe is the steward-supplied fixture map. It binds pre-existing admin and
// project rows (created through the real API while the slot was running) to
// the agent and broker rows this helper inserts. The decoder rejects unknown
// fields, so live/heartbeat fields (lastSeen, startedAt, runtimeBrokerId,
// status, ...) cannot even be expressed.
type Recipe struct {
	Schema   string         `json:"schema"`
	Admin    RecipeAdmin    `json:"admin"`
	Projects []RecipeProj   `json:"projects"`
	Agents   []RecipeAgent  `json:"agents"`
	Brokers  []RecipeBroker `json:"brokers"`
}

// RecipeAdmin identifies the existing synthetic admin user. Both fields are
// required and must agree with the store (GetUserByEmail and GetUser).
type RecipeAdmin struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

// RecipeProj names an existing project that agents may reference. Slug is
// required and must match the stored project, as a guard against binding the
// recipe to the wrong database.
type RecipeProj struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// RecipeAgent is one fixture agent. Name is always written equal to Slug,
// exactly as the API's create handler does.
type RecipeAgent struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"projectId"`
	Slug        string            `json:"slug"`
	Template    string            `json:"template"`
	AgentRole   string            `json:"agentRole"`
	Phase       string            `json:"phase"`
	Activity    string            `json:"activity,omitempty"`
	ExitCode    *int              `json:"exitCode,omitempty"`
	ExitReason  string            `json:"exitReason,omitempty"`
	Message     string            `json:"message,omitempty"`
	TaskSummary string            `json:"taskSummary,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// RecipeBroker is one offline (B1) broker row. Status, connection state and
// heartbeat are not expressible: the schema defaults (offline/disconnected,
// NULL heartbeat) always apply.
type RecipeBroker struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Slug    string            `json:"slug"`
	Version string            `json:"version,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// agentRoles mirrors pkg/hub.ValidAgentRole (pkg/hub/agentrole.go) without
// linking the hub server package into the helper binary. It is NOT
// cross-checked by a test, to keep pkg/hub out of the test build; re-check it
// by hand if the stock roles change.
var agentRoles = map[string]bool{"none": true, "readonly": true, "baseline": true, "full": true}

// stateKind is one of the five (phase, activity) pairs the frozen acceptance
// (H3.4) allows.
type stateKind string

const (
	kindStopped        stateKind = "stopped"
	kindStoppedCrashed stateKind = "stopped/crashed"
	kindStoppedLimits  stateKind = "stopped/limits_exceeded"
	kindError          stateKind = "error"
	kindCreated        stateKind = "created"
)

// classify returns the allowed state kind for a (phase, activity) pair, or an
// error for every other pair (including stopped/completed, running, and the
// in-flight phases).
func classify(phase, activity string) (stateKind, error) {
	switch {
	case phase == string(state.PhaseStopped) && activity == "":
		return kindStopped, nil
	case phase == string(state.PhaseStopped) && activity == string(state.ActivityCrashed):
		return kindStoppedCrashed, nil
	case phase == string(state.PhaseStopped) && activity == string(state.ActivityLimitsExceeded):
		return kindStoppedLimits, nil
	case phase == string(state.PhaseError) && activity == "":
		return kindError, nil
	case phase == string(state.PhaseCreated) && activity == "":
		return kindCreated, nil
	}
	return "", fmt.Errorf("unsupported (phase, activity) = (%q, %q): allowed pairs are "+
		"(stopped, ∅), (stopped, crashed), (stopped, limits_exceeded), (error, ∅), (created, ∅)", phase, activity)
}

// DecodeRecipe strictly decodes a recipe: unknown fields, trailing data and
// a wrong schema identifier are rejected.
func DecodeRecipe(data []byte) (*Recipe, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Recipe
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("decode recipe: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode recipe: unexpected data after the recipe object")
	}
	return &r, nil
}

// canonicalUUID requires s to be a UUID in canonical lowercase hyphenated form.
func canonicalUUID(field, s string) error {
	u, err := uuid.Parse(s)
	if err != nil || u.String() != s {
		return fmt.Errorf("%s %q is not a canonical lowercase UUID", field, s)
	}
	if u == uuid.Nil {
		return fmt.Errorf("%s must not be the nil UUID", field)
	}
	return nil
}

func validLabels(where string, labels map[string]string) error {
	if len(labels) > maxLabels {
		return fmt.Errorf("%s: %d labels exceeds the limit of %d", where, len(labels), maxLabels)
	}
	for k, v := range labels {
		if k == "" || len(k) > maxLabelKeyLen || strings.TrimSpace(k) != k {
			return fmt.Errorf("%s: label key %q must be 1-%d characters without surrounding space", where, k, maxLabelKeyLen)
		}
		if v == "" || utf8.RuneCountInString(v) > maxLabelValueLen || strings.TrimSpace(v) != v {
			return fmt.Errorf("%s: label %q value must be 1-%d characters without surrounding space", where, k, maxLabelValueLen)
		}
	}
	return nil
}

func validText(where, field, s string, limit int) error {
	if strings.TrimSpace(s) != s {
		return fmt.Errorf("%s: %s must not have leading or trailing whitespace", where, field)
	}
	if utf8.RuneCountInString(s) > limit {
		return fmt.Errorf("%s: %s exceeds %d characters", where, field, limit)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s: %s is not valid UTF-8", where, field)
	}
	return nil
}

// Validate checks the whole recipe without touching any store. Every
// violation is collected so the steward sees the full list at once.
func (r *Recipe) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if r.Schema != RecipeSchema {
		add("schema must be %q, got %q", RecipeSchema, r.Schema)
	}
	if err := canonicalUUID("admin.userId", r.Admin.UserID); err != nil {
		add("%v", err)
	}
	if r.Admin.Email == "" || !strings.Contains(r.Admin.Email, "@") {
		add("admin.email is required")
	}
	if len(r.Agents) == 0 && len(r.Brokers) == 0 {
		add("recipe has no agents and no brokers")
	}

	ids := map[string]string{} // every recipe-introduced or bound ID -> where
	claim := func(id, where string) {
		if prev, ok := ids[id]; ok && id != "" {
			add("%s: ID %s is already used by %s", where, id, prev)
			return
		}
		ids[id] = where
	}
	claim(r.Admin.UserID, "admin")

	projects := map[string]bool{}
	for i, p := range r.Projects {
		where := fmt.Sprintf("projects[%d]", i)
		if err := canonicalUUID(where+".id", p.ID); err != nil {
			add("%v", err)
		}
		if p.Slug == "" {
			add("%s.slug is required", where)
		}
		claim(p.ID, where)
		projects[p.ID] = true
	}

	slugsPerProject := map[string]map[string]string{}
	for i, a := range r.Agents {
		where := fmt.Sprintf("agents[%d] (%s)", i, a.Slug)
		if err := canonicalUUID(where+".id", a.ID); err != nil {
			add("%v", err)
		}
		claim(a.ID, where)
		if !projects[a.ProjectID] {
			add("%s: projectId %q is not listed in projects", where, a.ProjectID)
		}
		key, err := api.ValidateDisplayName(a.Slug)
		switch {
		case err != nil:
			add("%s: slug: %v", where, err)
		case key != a.Slug:
			add("%s: slug %q is not canonical (ValidateDisplayName key is %q)", where, a.Slug, key)
		}
		if slugsPerProject[a.ProjectID] == nil {
			slugsPerProject[a.ProjectID] = map[string]string{}
		}
		if prev, ok := slugsPerProject[a.ProjectID][a.Slug]; ok {
			add("%s: slug duplicates %s in the same project", where, prev)
		} else {
			slugsPerProject[a.ProjectID][a.Slug] = where
		}
		if a.Template == "" || len(a.Template) > maxTemplateLen || api.Slugify(a.Template) != a.Template {
			add("%s: template %q must be a non-empty slug", where, a.Template)
		}
		if !agentRoles[a.AgentRole] {
			add("%s: agentRole %q must be one of none/readonly/baseline/full", where, a.AgentRole)
		}
		if err := validText(where, "taskSummary", a.TaskSummary, maxTaskSummaryLen); err != nil {
			add("%v", err)
		}
		if err := validText(where, "message", a.Message, maxMessageLen); err != nil {
			add("%v", err)
		}
		if err := validLabels(where, a.Labels); err != nil {
			add("%v", err)
		}
		if a.ExitCode != nil && (*a.ExitCode < 0 || *a.ExitCode > 255) {
			add("%s: exitCode %d must be 0-255", where, *a.ExitCode)
		}
		if a.ExitReason != "" && !state.ExitReason(a.ExitReason).IsValid() {
			add("%s: exitReason %q is not a recognised exit reason", where, a.ExitReason)
		}

		kind, err := classify(a.Phase, a.Activity)
		if err != nil {
			add("%s: %v", where, err)
			continue
		}
		switch kind {
		case kindStopped:
			if a.ExitReason != "" {
				add("%s: plain stopped must not carry an exitReason (use activity crashed/limits_exceeded)", where)
			}
		case kindStoppedCrashed, kindStoppedLimits:
			if a.ExitReason != a.Activity {
				add("%s: activity %q requires exitReason %q", where, a.Activity, a.Activity)
			}
			if a.ExitCode == nil {
				add("%s: activity %q requires an exitCode", where, a.Activity)
			}
		case kindError:
			if a.ExitReason == "" || a.Message == "" {
				add("%s: phase error requires both exitReason and message", where)
			}
		case kindCreated:
			if a.ExitReason != "" || a.ExitCode != nil {
				add("%s: phase created must not carry exitCode/exitReason", where)
			}
		}
	}

	brokerSlugs := map[string]string{}
	brokerNames := map[string]string{}
	for i, b := range r.Brokers {
		where := fmt.Sprintf("brokers[%d] (%s)", i, b.Slug)
		if err := canonicalUUID(where+".id", b.ID); err != nil {
			add("%v", err)
		}
		claim(b.ID, where)
		if b.Name == "" || len(b.Name) > maxBrokerNameLen {
			add("%s: name must be 1-%d characters", where, maxBrokerNameLen)
		} else if err := validText(where, "name", b.Name, maxBrokerNameLen); err != nil {
			add("%v", err)
		}
		if b.Slug == "" || api.Slugify(b.Slug) != b.Slug {
			add("%s: slug %q must be a non-empty canonical slug", where, b.Slug)
		}
		if prev, ok := brokerSlugs[b.Slug]; ok {
			add("%s: slug duplicates %s", where, prev)
		}
		brokerSlugs[b.Slug] = where
		lname := strings.ToLower(b.Name)
		if prev, ok := brokerNames[lname]; ok {
			add("%s: name duplicates %s (names are matched case-insensitively)", where, prev)
		}
		brokerNames[lname] = where
		if err := validLabels(where, b.Labels); err != nil {
			add("%v", err)
		}
	}

	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("recipe invalid (%d problems):\n  - %s", len(errs), strings.Join(errs, "\n  - "))
	}
	return nil
}

// StateMix counts agents per allowed state kind (for the manifest and for
// checking a recipe against the frozen Wave01 mix).
func (r *Recipe) StateMix() map[string]int {
	mix := map[string]int{}
	for _, a := range r.Agents {
		if k, err := classify(a.Phase, a.Activity); err == nil {
			mix[string(k)]++
		}
	}
	return mix
}
