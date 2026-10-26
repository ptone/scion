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

// Provision credentials (ptone/scion#2129).
//
// The broker uses ProvisionCredentials only as GitHub bearer credentials
// for the agent's gh:// skills (agent.GitHubSkillResolver); they never
// reach the container. The hub sends only the project-scope secrets those
// skills can use, each authorized for delivery to the agent under the
// delivery credential before its value is read.

import (
	"context"
	"errors"
	"sort"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ghauth"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ghCredentialKeysForSkills returns the secret names the broker's GitHub
// skill resolver can look up for refs, sorted and without duplicates. It
// follows the resolver's lookup (GitHubSkillResolver.lookupToken and
// NewGitHubSkillResolverWithCredentials) for every ref the broker routes to
// it (gh:// and github.com URLs, agent.ParseGitHubSkillURI):
//
//   - a ref with ?token=NAME uses only NAME;
//   - any other ref uses its repo key GH_{OWNER}__{REPO}, its owner key
//     GH_{OWNER} and, when the broker has no other default token,
//     GITHUB_TOKEN.
//
// Refs that are not GitHub skill refs add nothing, so an agent with no
// gh:// refs gets no keys.
func ghCredentialKeysForSkills(refs []api.SkillReference) []string {
	set := make(map[string]struct{})
	for _, ref := range refs {
		gh, err := agent.ParseGitHubSkillURI(ref.URI)
		if err != nil || gh == nil {
			continue
		}
		if gh.TokenSecretName != "" {
			set[gh.TokenSecretName] = struct{}{}
			continue
		}
		set[ghauth.RepoKey(gh.Owner, gh.Repo)] = struct{}{}
		set[ghauth.OwnerKey(gh.Owner)] = struct{}{}
		set[ghauth.DefaultKey] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// resolveProvisionCredentials returns the provision credentials for agent:
// its project-scope secrets whose names are in the key set of its merged
// skill references (ghCredentialKeysForSkills), each one authorized as a
// project-association delivery to the agent (Decide(secret.deliver) under
// the delivery credential) before any value is read. Values are then
// fetched by recorded metadata (FetchValues). User-scope secrets are never
// sent. With no gh:// references, nothing is listed, authorized or sent.
//
// It is shared by the create, start and restart paths, so a gh:// skill
// re-provisioned through any of them gets the same credentials. Failures
// do not fail the dispatch: they are logged at Warn and the affected
// credentials are left out, so the skill resolves with the default
// credential or none. callerName prefixes every log message this function
// writes, at any level; no value is ever logged.
func (d *HTTPAgentDispatcher) resolveProvisionCredentials(ctx context.Context, agent *store.Agent, callerName string) map[string]string {
	if agent == nil || agent.ProjectID == "" || d.secretBackend == nil {
		return nil
	}
	var keys []string
	if d.skillRefLister != nil {
		keys = ghCredentialKeysForSkills(d.skillRefLister(ctx, agent))
	}
	if len(keys) == 0 {
		d.log.Info(callerName+": ProvisionCredentials resolved",
			"agent_id", agent.ID, "project_id", agent.ProjectID, "count", 0)
		return nil
	}
	wanted := make(map[string]bool, len(keys))
	for _, k := range keys {
		wanted[k] = true
	}

	projectSecrets, listErr := d.secretBackend.List(ctx, secret.Filter{
		Scope:   secret.ScopeProject,
		ScopeID: agent.ProjectID,
	})
	if listErr != nil {
		// Logged regardless of debug: without these credentials a private
		// gh:// skill silently resolves with the default credential (or none).
		d.log.Warn(callerName+": failed to list project secrets for ProvisionCredentials",
			"agent_id", agent.ID, "project_id", agent.ProjectID, "error", listErr)
		return nil
	}

	sel := newDeliverySelector(d.authzService, agent, PurposeProvisionCredential)
	var kept []secret.SecretMeta
	for _, m := range projectSecrets {
		if m.SecretType == store.SecretTypeInternal || !wanted[m.Name] {
			continue
		}
		allowed, err := sel.authorize(ctx, MaterialKindSecret, materialRecordSecret, m.ID, m.Name, store.ScopeProject, agent.ProjectID)
		if err != nil {
			d.log.Warn(callerName+": ProvisionCredentials not authorized; none sent",
				"agent_id", agent.ID, "project_id", agent.ProjectID, "error", err)
			return nil
		}
		if allowed {
			kept = append(kept, m)
		}
	}
	if d.debug && sel.denied > 0 {
		d.log.Debug("material delivery: items not authorized", "agent_id", agent.ID, "purpose", string(sel.purpose), "count", sel.denied)
	}
	if len(kept) == 0 {
		d.log.Info(callerName+": ProvisionCredentials resolved",
			"agent_id", agent.ID, "project_id", agent.ProjectID, "count", 0)
		return nil
	}

	values, fetchErr := d.secretBackend.FetchValues(ctx, kept)
	if fetchErr != nil {
		d.log.Warn(callerName+": failed to fetch project secrets for ProvisionCredentials",
			"agent_id", agent.ID, "project_id", agent.ProjectID, "error", fetchErr)
		return nil
	}

	creds := make(map[string]string, len(kept))
	// interrupted counts secrets not fetched because ctx was cancelled or
	// timed out. Those are reported once below rather than once per secret.
	interrupted := 0
	for _, m := range kept {
		r, ok := values[m.ID]
		if !ok {
			if ctx.Err() != nil {
				interrupted++
			}
			continue
		}
		if r.Err != nil {
			if ctx.Err() != nil || errors.Is(r.Err, context.Canceled) || errors.Is(r.Err, context.DeadlineExceeded) {
				interrupted++
				continue
			}
			// Secret name and project only; never the value.
			d.log.Warn(callerName+": failed to get project secret for ProvisionCredentials",
				"agent_id", agent.ID, "project_id", agent.ProjectID, "secret", m.Name, "error", r.Err)
			continue
		}
		if r.Value != "" {
			creds[m.Name] = r.Value
		}
	}
	if interrupted > 0 {
		cause := ctx.Err()
		if cause == nil {
			cause = context.Canceled
		}
		d.log.Warn(callerName+": ProvisionCredentials fetch stopped early; some project secrets were not fetched",
			"agent_id", agent.ID, "project_id", agent.ProjectID, "not_fetched", interrupted, "error", cause)
	}
	d.log.Info(callerName+": ProvisionCredentials resolved",
		"agent_id", agent.ID, "project_id", agent.ProjectID, "count", len(creds))
	if len(creds) == 0 {
		return nil
	}
	return creds
}
