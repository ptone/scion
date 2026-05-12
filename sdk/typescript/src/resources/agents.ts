/**
 * Resource class for managing agents via the Scion Hub API.
 *
 * @packageDocumentation
 */

import { BaseResource } from './base.js';
import { Page } from '../pagination.js';
import type {
  Agent,
  CreateAgentRequest,
  CreateAgentResponse,
  ListAgentsOptions,
  SendStructuredMessageOptions,
} from '../types/agents.js';
import type { StructuredMessage } from '../types/common.js';

/** Raw list response from the API. */
interface ListAgentsApiResponse {
  agents: Agent[];
  nextCursor?: string;
  totalCount?: number;
}

/**
 * Provides operations on Scion agents.
 *
 * Access through {@link ScionClient.agents}:
 * ```ts
 * const client = new ScionClient({ hubUrl: 'https://hub.example.com' });
 * const agent = await client.agents.get('agent-id');
 * ```
 */
export class AgentsResource extends BaseResource {
  private readonly projectId?: string;

  constructor(transport: ConstructorParameters<typeof BaseResource>[0], projectId?: string) {
    super(transport);
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
    return '/api/v1/agents';
  }

  /**
   * Create a new agent.
   *
   * @param params - Agent creation parameters.
   * @returns The created agent and any warnings.
   */
  async create(params: CreateAgentRequest): Promise<CreateAgentResponse> {
    return this.transport.request<CreateAgentResponse>('POST', this.agentsPath(), {
      body: params,
    });
  }

  /**
   * Get a single agent by ID.
   *
   * @param agentId - The agent's unique identifier.
   * @returns The agent.
   * @throws {NotFoundError} If the agent does not exist.
   */
  async get(agentId: string): Promise<Agent> {
    return this.transport.request<Agent>('GET', this.agentPath(agentId));
  }

  /**
   * List agents with optional filtering and pagination.
   *
   * Returns a {@link Page} that supports async iteration across all pages:
   * ```ts
   * for await (const agent of client.agents.list({ phase: 'running' })) {
   *   console.log(agent.name);
   * }
   * ```
   *
   * @param params - Optional filter and pagination parameters.
   * @returns A page of agents.
   */
  async list(params?: ListAgentsOptions): Promise<Page<Agent>> {
    return this.fetchPage(params);
  }

  /**
   * Start a stopped agent.
   *
   * @param agentId - The agent's unique identifier.
   */
  async start(agentId: string): Promise<void> {
    await this.transport.request<void>('POST', `${this.agentPath(agentId)}/start`);
  }

  /**
   * Stop a running agent.
   *
   * @param agentId - The agent's unique identifier.
   */
  async stop(agentId: string): Promise<void> {
    await this.transport.request<void>('POST', `${this.agentPath(agentId)}/stop`);
  }

  /**
   * Suspend a running agent, preserving state for later resume.
   *
   * @param agentId - The agent's unique identifier.
   */
  async suspend(agentId: string): Promise<void> {
    await this.transport.request<void>('POST', `${this.agentPath(agentId)}/suspend`);
  }

  /**
   * Restart an agent.
   *
   * @param agentId - The agent's unique identifier.
   */
  async restart(agentId: string): Promise<void> {
    await this.transport.request<void>('POST', `${this.agentPath(agentId)}/restart`);
  }

  /**
   * Delete an agent.
   *
   * @param agentId - The agent's unique identifier.
   */
  async delete(agentId: string): Promise<void> {
    await this.transport.request<void>('DELETE', this.agentPath(agentId));
  }

  /**
   * Restore a soft-deleted agent.
   *
   * @param agentId - The agent's unique identifier.
   * @returns The restored agent.
   */
  async restore(agentId: string): Promise<Agent> {
    return this.transport.request<Agent>('POST', `${this.agentPath(agentId)}/restore`);
  }

  /**
   * Send a plain text message to an agent.
   *
   * @param agentId - The agent's unique identifier.
   * @param message - The message text.
   * @param interrupt - Whether to interrupt the agent's current work.
   */
  async sendMessage(agentId: string, message: string, interrupt?: boolean): Promise<void> {
    await this.transport.request<void>('POST', `${this.agentPath(agentId)}/message`, {
      body: { message, interrupt: interrupt ?? false },
    });
  }

  /**
   * Send a structured message to an agent.
   *
   * @param agentId - The agent's unique identifier.
   * @param msg - The structured message.
   * @param opts - Options for delivery (interrupt, notify).
   */
  async sendStructuredMessage(
    agentId: string,
    msg: StructuredMessage,
    opts?: SendStructuredMessageOptions,
  ): Promise<void> {
    await this.transport.request<void>('POST', `${this.agentPath(agentId)}/message`, {
      body: {
        structured_message: msg,
        interrupt: opts?.interrupt ?? false,
        notify: opts?.notify ?? false,
      },
    });
  }

  /**
   * Broadcast a structured message to all running agents in the project.
   *
   * Requires a project-scoped client (constructed with a project ID).
   *
   * @param msg - The structured message to broadcast.
   * @param interrupt - Whether to interrupt agents' current work.
   * @throws {Error} If the client is not project-scoped.
   */
  async broadcastMessage(msg: StructuredMessage, interrupt?: boolean): Promise<void> {
    if (!this.projectId) {
      throw new Error('broadcastMessage requires a project-scoped client');
    }
    await this.transport.request<void>(
      'POST',
      `/api/v1/projects/${this.projectId}/broadcast`,
      {
        body: {
          structured_message: msg,
          interrupt: interrupt ?? false,
        },
      },
    );
  }

  /** Internal: fetch a single page of agents. */
  private async fetchPage(params?: ListAgentsOptions): Promise<Page<Agent>> {
    const query: Record<string, string> = {};

    if (params) {
      if (params.projectId) query.projectId = params.projectId;
      if (params.phase) query.phase = params.phase;
      if (params.activity) query.activity = params.activity;
      if (params.runtimeBrokerId) query.runtimeBrokerId = params.runtimeBrokerId;
      if (params.includeDeleted) query.includeDeleted = 'true';
      if (params.limit !== undefined) query.limit = String(params.limit);
      if (params.cursor) query.cursor = params.cursor;
      const labelStr = this.serializeLabels(params.labels);
      if (labelStr) query.label = labelStr;
    }

    const result = await this.transport.request<ListAgentsApiResponse>(
      'GET',
      this.agentsPath(),
      { query },
    );

    return new Page<Agent>(
      result.agents ?? [],
      result.nextCursor,
      (cursor) => this.fetchPage({ ...params, cursor }),
      result.totalCount,
    );
  }
}
