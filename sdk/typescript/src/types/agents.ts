/**
 * Type definitions for the Agent resource.
 *
 * @packageDocumentation
 */

import type { PageParams, PaginatedResponse } from './common.js';

/** Agent configuration applied at creation or update time. */
export interface AgentConfig {
  /** Container image override. */
  image?: string;
  /** Harness configuration name or slug. */
  harnessConfig?: string;
  /** Harness authentication configuration. */
  harnessAuth?: string;
  /** Environment variables injected into the agent container. */
  env?: Record<string, string>;
  /** Model identifier (e.g. "claude-sonnet-4-20250514"). */
  model?: string;
  /** Runtime profile name. */
  profile?: string;
  /** Initial task / prompt for the agent. */
  task?: string;
}

/** Direct-connect (SSH) information for an agent. */
export interface DirectConnect {
  /** Whether direct connect is available. */
  enabled: boolean;
  /** SSH hostname or IP address. */
  sshHost?: string;
  /** SSH port number. */
  sshPort?: number;
  /** SSH username. */
  sshUser?: string;
}

/** Kubernetes-specific metadata for an agent. */
export interface KubernetesInfo {
  cluster?: string;
  namespace?: string;
  podName?: string;
  syncedAt?: string;
}

/** An agent as returned by the Scion Hub API. */
export interface Agent {
  /** Hub UUID (database primary key). */
  id: string;
  /** URL-safe slug identifier (unique per project). */
  slug: string;
  /** Runtime container ID (ephemeral). */
  containerId: string;
  /** Human-readable name. */
  name: string;
  /** Template slug used to create this agent. */
  template?: string;
  /** Harness configuration name. */
  harnessConfig?: string;
  /** Harness authentication configuration. */
  harnessAuth?: string;
  /** ID of the owning project. */
  projectId?: string;
  /** Name of the owning project. */
  project?: string;
  /** User-defined labels. */
  labels?: Record<string, string>;
  /** User-defined annotations. */
  annotations?: Record<string, string>;
  /** Lifecycle phase: created, provisioning, running, stopped, error. */
  phase?: string;
  /** Runtime activity: idle, thinking, executing, waiting_for_input, completed. */
  activity?: string;
  /** Legacy / fallback status field. */
  status: string;
  /** Connection state. */
  connectionState?: string;
  /** Container-level status. */
  containerStatus?: string;
  /** Runtime-level state. */
  runtimeState?: string;
  /** Container image used by this agent. */
  image?: string;
  /** Whether the agent is running in detached mode. */
  detached?: boolean;
  /** Runtime type (docker, apple, kubernetes). */
  runtime?: string;
  /** ID of the runtime broker hosting this agent. */
  runtimeBrokerId?: string;
  /** Name of the runtime broker hosting this agent. */
  runtimeBrokerName?: string;
  /** Type of the runtime broker. */
  runtimeBrokerType?: string;
  /** Whether web PTY is enabled. */
  webPtyEnabled?: boolean;
  /** Short summary of the agent's current task. */
  taskSummary?: string;
  /** Fully-resolved configuration applied to this agent. */
  appliedConfig?: AgentConfig;
  /** Direct-connect information. */
  directConnect?: DirectConnect;
  /** Kubernetes-specific metadata. */
  kubernetes?: KubernetesInfo;
  /** Creation timestamp (ISO 8601). */
  created: string;
  /** Last-updated timestamp (ISO 8601). */
  updated: string;
  /** Last heartbeat timestamp (ISO 8601). */
  lastSeen?: string;
  /** Soft-deletion timestamp (ISO 8601). */
  deletedAt?: string;
  /** ID of the user who created this agent. */
  createdBy?: string;
  /** Owner user ID. */
  ownerId?: string;
  /** Visibility level. */
  visibility?: string;
  /** Optimistic-concurrency version. */
  stateVersion?: number;
}

/** Request body for creating a new agent. */
export interface CreateAgentRequest {
  /** Agent name (must be unique within the project). */
  name: string;
  /** Project ID. */
  projectId?: string;
  /** Template slug to base the agent on. */
  template?: string;
  /** Inline configuration overrides. */
  config?: AgentConfig;
  /** Harness configuration name. */
  harnessConfig?: string;
  /** Harness authentication configuration. */
  harnessAuth?: string;
  /** Target runtime broker ID. */
  runtimeBrokerId?: string;
  /** Profile name. */
  profile?: string;
  /** Initial task / prompt. */
  task?: string;
  /** Git branch. */
  branch?: string;
  /** Workspace path. */
  workspace?: string;
  /** User-defined labels. */
  labels?: Record<string, string>;
  /** User-defined annotations. */
  annotations?: Record<string, string>;
  /** Whether to start in detached mode. */
  detached?: boolean;
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
  /** The newly created agent. */
  agent: Agent;
  /** Warnings generated during creation. */
  warnings?: string[];
}

/** Request body for updating an existing agent. */
export interface UpdateAgentRequest {
  /** New agent name. */
  name?: string;
  /** Updated labels (merged, not replaced). */
  labels?: Record<string, string>;
  /** Updated annotations (merged, not replaced). */
  annotations?: Record<string, string>;
  /** Updated configuration overrides. */
  config?: AgentConfig;
}

/** Options for listing agents. */
export interface ListAgentsOptions extends PageParams {
  /** Filter by project ID. */
  projectId?: string;
  /** Filter by agent phase. */
  phase?: string;
  /** Filter by agent activity. */
  activity?: string;
  /** Filter by runtime broker ID. */
  runtimeBrokerId?: string;
  /** Include soft-deleted agents. */
  includeDeleted?: boolean;
  /** Label selector (key=value pairs). */
  labels?: Record<string, string>;
}

/** Response from listing agents. */
export type ListAgentsResponse = PaginatedResponse<Agent>;

/** Options for sending a structured message to an agent. */
export interface SendStructuredMessageOptions {
  /** Interrupt the agent's current work. */
  interrupt?: boolean;
  /** Subscribe to status notifications for the target agent. */
  notify?: boolean;
}
