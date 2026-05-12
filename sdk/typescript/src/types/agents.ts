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

/** Agent lifecycle phases. */
export type AgentPhase =
  | "created"
  | "provisioning"
  | "running"
  | "stopped"
  | "error"
  | "suspended"
  | "deleted";

/** Agent runtime activity states. */
export type AgentActivity =
  | "idle"
  | "thinking"
  | "executing"
  | "waiting_for_input"
  | "completed";

/** Applied configuration for an agent. */
export interface AgentConfig {
  image?: string;
  harnessConfig?: string;
  harnessAuth?: string;
  env?: Record<string, string>;
  model?: string;
  profile?: string;
  task?: string;
}

/** Direct connection info for an agent. */
export interface DirectConnect {
  enabled: boolean;
  sshHost?: string;
  sshPort?: number;
  sshUser?: string;
}

/** Kubernetes-specific metadata. */
export interface KubernetesInfo {
  cluster?: string;
  namespace?: string;
  podName?: string;
  syncedAt?: string;
}

/** An agent managed by the Scion Hub. */
export interface Agent {
  /** Hub UUID (database primary key). */
  id: string;
  /** URL-safe slug identifier (unique per project). */
  slug: string;
  /** Runtime container ID (ephemeral). */
  containerId: string;
  /** Human-readable agent name. */
  name: string;
  /** Template used to create the agent. */
  template?: string;
  /** Harness configuration name. */
  harnessConfig?: string;
  /** Harness auth type. */
  harnessAuth?: string;
  /** Project ID this agent belongs to. */
  projectId?: string;
  /** Project name. */
  project?: string;
  /** User-defined labels. */
  labels?: Record<string, string>;
  /** User-defined annotations. */
  annotations?: Record<string, string>;
  /** Lifecycle phase. */
  phase?: AgentPhase;
  /** Runtime activity. */
  activity?: AgentActivity;
  /** Legacy/fallback status. */
  status: string;
  /** Connection state. */
  connectionState?: string;
  /** Container status. */
  containerStatus?: string;
  /** Runtime state. */
  runtimeState?: string;
  /** Container image. */
  image?: string;
  /** Whether the agent is detached. */
  detached?: boolean;
  /** Runtime type (e.g. "docker", "kubernetes"). */
  runtime?: string;
  /** Runtime broker ID. */
  runtimeBrokerId?: string;
  /** Runtime broker name. */
  runtimeBrokerName?: string;
  /** Runtime broker type. */
  runtimeBrokerType?: string;
  /** Whether web PTY is enabled. */
  webPtyEnabled?: boolean;
  /** Summary of the agent's current task. */
  taskSummary?: string;
  /** Applied configuration. */
  appliedConfig?: AgentConfig;
  /** Direct connection info. */
  directConnect?: DirectConnect;
  /** Kubernetes metadata. */
  kubernetes?: KubernetesInfo;
  /** Creation timestamp (ISO 8601). */
  created: string;
  /** Last update timestamp (ISO 8601). */
  updated: string;
  /** Last seen timestamp (ISO 8601). */
  lastSeen?: string;
  /** Deletion timestamp (ISO 8601), if soft-deleted. */
  deletedAt?: string;
  /** User who created the agent. */
  createdBy?: string;
  /** Owner user ID. */
  ownerId?: string;
  /** Visibility level. */
  visibility?: string;
  /** Optimistic locking version. */
  stateVersion?: number;
}

/** Parameters for creating an agent. */
export interface CreateAgentParams {
  /** Agent name. */
  name: string;
  /** Project ID. */
  projectId: string;
  /** Template name. */
  template?: string;
  /** Harness configuration name. */
  harnessConfig?: string;
  /** Harness auth type. */
  harnessAuth?: string;
  /** Target runtime broker ID. */
  runtimeBrokerId?: string;
  /** Profile name. */
  profile?: string;
  /** Initial task/prompt. */
  task?: string;
  /** Git branch. */
  branch?: string;
  /** Workspace path. */
  workspace?: string;
  /** User-defined labels. */
  labels?: Record<string, string>;
  /** User-defined annotations. */
  annotations?: Record<string, string>;
  /** Whether to resume a previously stopped agent. */
  resume?: boolean;
  /** Whether to attach interactively. */
  attach?: boolean;
  /** Provision only — write task to prompt.md without starting. */
  provisionOnly?: boolean;
  /** Subscribe to status notifications for the new agent. */
  notify?: boolean;
}

/** Response from creating an agent. */
export interface CreateAgentResponse {
  /** The created agent. */
  agent: Agent;
  /** Warnings generated during creation. */
  warnings?: string[];
}

/** Parameters for listing agents. */
export interface ListAgentsParams {
  /** Filter by project ID. */
  projectId?: string;
  /** Filter by lifecycle phase. */
  phase?: string;
  /** Filter by runtime broker ID. */
  runtimeBrokerId?: string;
  /** Include soft-deleted agents. */
  includeDeleted?: boolean;
  /** Label selector (key=value pairs). */
  labels?: Record<string, string>;
  /** Maximum number of agents per page. */
  limit?: number;
  /** Cursor for pagination. */
  cursor?: string;
}

/** Options for sending a structured message. */
export interface SendStructuredMessageOptions {
  /** Interrupt the agent's current work. */
  interrupt?: boolean;
  /** Subscribe to status notifications for the target agent. */
  notify?: boolean;
}
