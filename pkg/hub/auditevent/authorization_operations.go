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

	"github.com/GoogleCloudPlatform/scion/pkg/hub/authzop"
)

// declaredAuthorizationOperations is intentionally explicit: adding an
// authzop operation does not silently make it an auditable action.
var declaredAuthorizationOperations = []authzop.OperationID{
	"project.membership.add", "project.membership.update", "project.membership.remove", "project.membership.list", "project.membership.transfer",
	"role.definition.create", "role.definition.update", "role.definition.delete", "role.binding.create", "role.binding.delete",
	"group.member.add", "group.member.remove", "group.delete",
	"access.constraint.create", "access.constraint.update", "access.constraint.delete",
	"credential.token.create", "credential.token.revoke",
	"gcp.identity.create", "gcp.identity.delete", "gcp.identity.assign", "gcp.identity.mint",
	"agent.lifecycle.create", "agent.lifecycle.delete", "agent.lifecycle.control", "agent.lifecycle.restore", "agent.lifecycle.exec", "agent.lifecycle.env", "agent.lifecycle.resetauth", "agent.lifecycle.reincarnate", "project.lifecycle.create", "project.lifecycle.delete", "agent.message.send",
	"user.admin.suspend", "secret.read", "secret.write", "user.admin.invite", "user.admin.promote", "user.admin.delete",
	"hub.authreset", "hub.config.read", "hub.config.update", "hub.messaging.update", "hub.experiments.update", "hub.maintenance.execute", "hub.adminmode.update", "hub.allowlist.update", "hub.health.read", "hub.diagnostics.read", "hub.scheduler.read", "hub.projectdefaults.read", "hub.lifecyclehooks.read", "hub.validate.execute", "hub.integrations.read", "hub.teamsmanifest.read", "hub.metrics.read", "hub.githubapp.read", "hub.githubapp.update",
	"agent.read", "agent.list", "agent.update", "agent.attach", "agent.portaccess", "agent.stopall", "agent.setmessagemode",
	"project.read", "project.list", "project.update", "project.register",
	"skill.read", "skill.create", "skill.update", "skill.delete", "skill.register",
	"template.read", "template.create", "template.update", "template.delete",
	"harnessconfig.read", "harnessconfig.create", "harnessconfig.update", "harnessconfig.delete",
	"group.read", "group.create", "group.update", "user.read", "user.update", "broker.read",
	"gcp.identity.read", "gcp.identity.verify", "role.read", "role.binding.read", "access.constraint.read",
	"quota.read", "quota.create", "quota.update", "quota.delete",
	"schedule.event.read", "schedule.event.create", "schedule.event.update", "schedule.event.delete",
	"chat.access", "env.read",
}

func declaredAuthorizationOperationStrings() []string {
	actions := make([]string, len(declaredAuthorizationOperations))
	for i, operation := range declaredAuthorizationOperations {
		actions[i] = string(operation)
	}
	sort.Strings(actions)
	return actions
}

func declaredAuthorizationActionPermissions() []ActionPermissionSchema {
	byID := make(map[authzop.OperationID]authzop.OperationSpec, len(authzop.Catalog))
	for _, operation := range authzop.Catalog {
		byID[operation.ID] = operation
	}
	pairs := make([]ActionPermissionSchema, 0, len(declaredAuthorizationOperations))
	for _, operationID := range declaredAuthorizationOperations {
		operation, ok := byID[operationID]
		if !ok || operation.BasePermission == "" {
			continue
		}
		pairs = append(pairs, ActionPermissionSchema{Action: string(operationID), Permission: operation.BasePermission})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Action < pairs[j].Action })
	return pairs
}
