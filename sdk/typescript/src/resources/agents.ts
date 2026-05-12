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

import { Page } from "../pagination.js";
import type { Transport } from "../transport.js";
import type {
  Agent,
  CreateAgentParams,
  CreateAgentResponse,
  ListAgentsParams,
  SendStructuredMessageOptions,
} from "../types/agents.js";
import type { StructuredMessage } from "../types/common.js";

/**
 * Resource class for managing agents via the Scion Hub API.
 *
 * Access through {@link ScionClient.agents}:
 * ```ts
 * const client = new ScionClient({ baseUrl: "https://hub.example.com" });
 * const agent = await client.agents.get("agent-id");
 * ```
 */
export class AgentsResource {
  private readonly transport: Transport;
  private readonly projectId?: string;

  constructor(transport: Transport, projectId?: string) {
    this.transport = transport;
    this.projectId = projectId;
  }

  /** Build the base path for a single agent. */
  private agentPath(agentId: string): string {
    if (this.projectId) {
      return `/api/v1/projects/${this.projectId}/agents/${agentId}`;
    }
    return `/api/v1/agents/${agentId}`;
  }

  /** Build the base path for the agents collection. */
  private agentsPath(): string {
    if (this.projectId) {
      return `/api/v1/projects/${this.projectId}/agents`;
    }
    return "/api/v1/agents";
  }

  /**
   * Create a new agent.
   *
   * @param params - Agent creation parameters.
   * @returns The created agent and any warnings.
   *
   * @example
   * ```ts
   * const response = await client.agents.create({
   *   name: "code-reviewer",
   *   projectId: "proj-123",
   *   template: "claude",
   *   task: "Review the latest PR",
   * });
   * console.log(response.agent.id);
   * ```
   */
  async create(params: CreateAgentParams): Promise<CreateAgentResponse> {
    return this.transport.post<CreateAgentResponse>(this.agentsPath(), params);
  }

  /**
   * Get a single agent by ID.
   *
   * @param agentId - The agent's unique identifier.
   * @returns The agent.
   * @throws {NotFoundError} If the agent does not exist.
   *
   * @example
   * ```ts
   * const agent = await client.agents.get("agent-uuid");
   * console.log(agent.name, agent.phase);
   * ```
   */
  async get(agentId: string): Promise<Agent> {
    return this.transport.get<Agent>(this.agentPath(agentId));
  }

  /**
   * List agents with optional filtering and pagination.
   *
   * Returns a {@link Page} that supports async iteration across all pages:
   * ```ts
   * for await (const agent of client.agents.list({ phase: "running" })) {
   *   console.log(agent.name);
   * }
   * ```
   *
   * @param params - Optional filter and pagination parameters.
   * @returns A page of agents.
   */
  async list(params?: ListAgentsParams): Promise<Page<Agent>> {
    return this.fetchPage(params);
  }

  /**
   * Start a stopped agent.
   *
   * @param agentId - The agent's unique identifier.
   * @throws {NotFoundError} If the agent does not exist.
   */
  async start(agentId: string): Promise<void> {
    await this.transport.post<void>(this.agentPath(agentId) + "/start");
  }

  /**
   * Stop a running agent.
   *
   * @param agentId - The agent's unique identifier.
   * @throws {NotFoundError} If the agent does not exist.
   */
  async stop(agentId: string): Promise<void> {
    await this.transport.post<void>(this.agentPath(agentId) + "/stop");
  }

  /**
   * Suspend a running agent, preserving state for later resume.
   *
   * @param agentId - The agent's unique identifier.
   * @throws {NotFoundError} If the agent does not exist.
   */
  async suspend(agentId: string): Promise<void> {
    await this.transport.post<void>(this.agentPath(agentId) + "/suspend");
  }

  /**
   * Restart an agent.
   *
   * @param agentId - The agent's unique identifier.
   * @throws {NotFoundError} If the agent does not exist.
   */
  async restart(agentId: string): Promise<void> {
    await this.transport.post<void>(this.agentPath(agentId) + "/restart");
  }

  /**
   * Delete an agent.
   *
   * @param agentId - The agent's unique identifier.
   * @throws {NotFoundError} If the agent does not exist.
   */
  async delete(agentId: string): Promise<void> {
    await this.transport.delete<void>(this.agentPath(agentId));
  }

  /**
   * Restore a soft-deleted agent.
   *
   * @param agentId - The agent's unique identifier.
   * @returns The restored agent.
   * @throws {NotFoundError} If the agent does not exist.
   */
  async restore(agentId: string): Promise<Agent> {
    return this.transport.post<Agent>(this.agentPath(agentId) + "/restore");
  }

  /**
   * Send a plain text message to an agent.
   *
   * @param agentId - The agent's unique identifier.
   * @param message - The message text.
   * @param interrupt - Whether to interrupt the agent's current work.
   *
   * @example
   * ```ts
   * await client.agents.sendMessage("agent-uuid", "Please check the logs");
   * ```
   */
  async sendMessage(
    agentId: string,
    message: string,
    interrupt?: boolean,
  ): Promise<void> {
    await this.transport.post<void>(this.agentPath(agentId) + "/message", {
      message,
      interrupt: interrupt ?? false,
    });
  }

  /**
   * Send a structured message to an agent.
   *
   * @param agentId - The agent's unique identifier.
   * @param msg - The structured message.
   * @param opts - Options for delivery (interrupt, notify).
   *
   * @example
   * ```ts
   * await client.agents.sendStructuredMessage("agent-uuid", {
   *   version: 1,
   *   timestamp: new Date().toISOString(),
   *   sender: "user:alice",
   *   recipient: "agent:code-reviewer",
   *   msg: "Please review PR #42",
   *   type: "instruction",
   * });
   * ```
   */
  async sendStructuredMessage(
    agentId: string,
    msg: StructuredMessage,
    opts?: SendStructuredMessageOptions,
  ): Promise<void> {
    await this.transport.post<void>(this.agentPath(agentId) + "/message", {
      structured_message: msg,
      interrupt: opts?.interrupt ?? false,
      notify: opts?.notify ?? false,
    });
  }

  /**
   * Broadcast a structured message to all running agents in the project.
   *
   * Requires a project-scoped client (constructed with a project ID).
   *
   * @param msg - The structured message to broadcast.
   * @param interrupt - Whether to interrupt agents' current work.
   * @throws {ScionError} If the client is not project-scoped.
   *
   * @example
   * ```ts
   * const projectClient = new ScionClient({
   *   baseUrl: "https://hub.example.com",
   *   projectId: "proj-123",
   * });
   * await projectClient.agents.broadcastMessage({
   *   version: 1,
   *   timestamp: new Date().toISOString(),
   *   sender: "user:alice",
   *   recipient: "all",
   *   msg: "Wrap up your current tasks",
   *   type: "instruction",
   * });
   * ```
   */
  async broadcastMessage(
    msg: StructuredMessage,
    interrupt?: boolean,
  ): Promise<void> {
    if (!this.projectId) {
      throw new Error("broadcastMessage requires a project-scoped client");
    }
    await this.transport.post<void>(
      `/api/v1/projects/${this.projectId}/broadcast`,
      {
        structured_message: msg,
        interrupt: interrupt ?? false,
      },
    );
  }

  /** Internal: fetch a single page of agents. */
  private async fetchPage(params?: ListAgentsParams): Promise<Page<Agent>> {
    const query: Record<string, string | string[] | undefined> = {};

    if (params) {
      if (params.projectId) query.projectId = params.projectId;
      if (params.phase) query.phase = params.phase;
      if (params.runtimeBrokerId)
        query.runtimeBrokerId = params.runtimeBrokerId;
      if (params.includeDeleted) query.includeDeleted = "true";
      if (params.limit !== undefined) query.limit = String(params.limit);
      if (params.cursor) query.cursor = params.cursor;

      if (params.labels) {
        query.label = Object.entries(params.labels).map(
          ([k, v]) => `${k}=${v}`,
        );
      }
    }

    interface ListResponse {
      agents: Agent[];
      nextCursor?: string;
      totalCount?: number;
    }

    const result = await this.transport.get<ListResponse>(
      this.agentsPath(),
      query,
    );

    return new Page<Agent>(
      result.agents,
      result.nextCursor,
      result.totalCount,
      (cursor) => this.fetchPage({ ...params, cursor }),
    );
  }
}
