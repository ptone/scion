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

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// repinUserTemplateForStart re-records, before a start, restart or resume is
// dispatched, the current content hash of the agent's user-scoped template
// (ptone/scion#5770; see "Version pinning" in authorize.go). The template is
// the SAME one the hub recorded (AppliedConfig.TemplateID), never another
// template of the same name, and the hash is re-recorded only while the
// agent's creator can still read it. The launch is then pinned to that hash:
// the dispatch-bound read refuses any later version, so a commit that races
// the launch fails it.
//
// Nothing is written when there is nothing to re-pin: no recorded template,
// a template that no longer exists (hydration then fails with a clear
// message), a global or project template (their reads follow the current
// version anyway), an unchanged hash, or a creator who can no longer read
// it (the read gate then refuses, and the broker reports it clearly).
//
// On a write, agent's in-memory TemplateHash and StateVersion are updated to
// the stored row, so a later whole-row write neither conflicts nor reverts
// it. A concurrent change of the recorded template or hash returns an error
// wrapping store.ErrVersionConflict, and the start must not be dispatched.
//
// A reincarnate or move never calls this: its rebuild refuses a changed
// user-scoped template instead (carryUserScopedTemplate).
func (s *Server) repinUserTemplateForStart(ctx context.Context, agent *store.Agent) error {
	if agent == nil || agent.AppliedConfig == nil || agent.AppliedConfig.TemplateID == "" {
		return nil
	}
	tmpl, err := s.store.GetTemplate(ctx, agent.AppliedConfig.TemplateID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up the agent's template %s: %w", agent.AppliedConfig.TemplateID, err)
	}
	if tmpl == nil || tmpl.Scope != store.TemplateScopeUser || tmpl.ContentHash == agent.AppliedConfig.TemplateHash {
		return nil
	}
	creator := s.creatorIdentityForAgent(ctx, agent)
	if creator == nil || s.authzService == nil ||
		!s.authzService.CheckAccess(ctx, creator, templateResource(tmpl), ActionRead).Allowed {
		s.agentLifecycleLog.Warn("start: agent's user-scoped template changed and its creator can no longer read it; not re-pinned",
			"agent_id", agent.ID, "template_id", tmpl.ID)
		return nil
	}
	newVersion, err := s.store.SetAgentTemplatePin(ctx, agent.ID, tmpl.ID, agent.AppliedConfig.TemplateHash, tmpl.ContentHash)
	if errors.Is(err, store.ErrTemplatePinChanged) {
		return fmt.Errorf("agent template changed concurrently; retry: %w", store.ErrVersionConflict)
	}
	if err != nil {
		return err
	}
	agent.AppliedConfig.TemplateHash = tmpl.ContentHash
	agent.StateVersion = newVersion
	return nil
}
