/**
 * Resource class for managing projects via the Scion Hub API.
 *
 * @packageDocumentation
 */

import { BaseResource } from './base.js';
import { Page } from '../pagination.js';
import type { Agent } from '../types/agents.js';
import type {
  Project,
  CreateProjectRequest,
  UpdateProjectRequest,
  ListProjectsOptions,
  ListProjectAgentsOptions,
} from '../types/projects.js';

/** Raw list projects response from the API. */
interface ListProjectsApiResponse {
  projects: Project[];
  nextCursor?: string;
  totalCount?: number;
}

/** Raw list agents response from the API. */
interface ListAgentsApiResponse {
  agents: Agent[];
  nextCursor?: string;
  totalCount?: number;
}

/**
 * Provides operations on Scion projects.
 *
 * Projects are the primary organizational unit in Scion, grouping agents,
 * templates, and runtime broker providers around a shared codebase.
 *
 * @example
 * ```ts
 * const client = new ScionClient({ hubUrl: 'https://hub.example.com' });
 *
 * // List all projects
 * const page = await client.projects.list();
 * for await (const project of page) {
 *   console.log(project.name);
 * }
 * ```
 */
export class ProjectsResource extends BaseResource {
  private static readonly BASE_PATH = '/api/v1/projects';

  /**
   * List projects matching the given filter criteria.
   *
   * @param params - Optional filtering and pagination parameters.
   * @returns A paginated list of projects.
   */
  async list(params?: ListProjectsOptions): Promise<Page<Project>> {
    return this.fetchPage(params);
  }

  /**
   * Get a single project by ID.
   *
   * @param projectId - The project ID or slug.
   * @returns The project.
   * @throws {NotFoundError} If the project is not found.
   */
  async get(projectId: string): Promise<Project> {
    return this.transport.request<Project>(
      'GET',
      `${ProjectsResource.BASE_PATH}/${projectId}`,
    );
  }

  /**
   * Create a new project.
   *
   * @param params - Project creation parameters.
   * @returns The newly created project.
   */
  async create(params: CreateProjectRequest): Promise<Project> {
    return this.transport.request<Project>('POST', ProjectsResource.BASE_PATH, {
      body: params,
    });
  }

  /**
   * Update an existing project's metadata.
   *
   * @param projectId - The project ID or slug.
   * @param params - Fields to update.
   * @returns The updated project.
   */
  async update(projectId: string, params: UpdateProjectRequest): Promise<Project> {
    return this.transport.request<Project>(
      'PATCH',
      `${ProjectsResource.BASE_PATH}/${projectId}`,
      { body: params },
    );
  }

  /**
   * Delete a project and all its agents.
   *
   * @param projectId - The project ID or slug.
   */
  async delete(projectId: string): Promise<void> {
    await this.transport.request<void>(
      'DELETE',
      `${ProjectsResource.BASE_PATH}/${projectId}`,
    );
  }

  /**
   * List agents belonging to a project.
   *
   * @param projectId - The project ID or slug.
   * @param params - Optional filtering and pagination parameters.
   * @returns A paginated list of agents within the project.
   */
  async listAgents(
    projectId: string,
    params?: ListProjectAgentsOptions,
  ): Promise<Page<Agent>> {
    return this.fetchAgentPage(projectId, params);
  }

  /** Internal: fetch a single page of projects. */
  private async fetchPage(params?: ListProjectsOptions): Promise<Page<Project>> {
    const query: Record<string, string> = {};

    if (params) {
      if (params.limit !== undefined) query.limit = String(params.limit);
      if (params.cursor) query.cursor = params.cursor;
      if (params.projectType) query.projectType = params.projectType;
      if (params.visibility) query.visibility = params.visibility;
      if (params.gitRemote) query.gitRemote = params.gitRemote;
      if (params.brokerId) query.brokerId = params.brokerId;
      if (params.name) query.name = params.name;
      if (params.slug) query.slug = params.slug;
      const labelStr = this.serializeLabels(params.labels);
      if (labelStr) query.label = labelStr;
    }

    const result = await this.transport.request<ListProjectsApiResponse>(
      'GET',
      ProjectsResource.BASE_PATH,
      { query },
    );

    return new Page<Project>(
      result.projects ?? [],
      result.nextCursor,
      (cursor) => this.fetchPage({ ...params, cursor }),
      result.totalCount,
    );
  }

  /** Internal: fetch a single page of project agents. */
  private async fetchAgentPage(
    projectId: string,
    params?: ListProjectAgentsOptions,
  ): Promise<Page<Agent>> {
    const query: Record<string, string> = {};

    if (params) {
      if (params.limit !== undefined) query.limit = String(params.limit);
      if (params.cursor) query.cursor = params.cursor;
      if (params.phase) query.phase = params.phase;
      if (params.runtimeBrokerId) query.runtimeBrokerId = params.runtimeBrokerId;
      const labelStr = this.serializeLabels(params.labels);
      if (labelStr) query.label = labelStr;
    }

    const result = await this.transport.request<ListAgentsApiResponse>(
      'GET',
      `${ProjectsResource.BASE_PATH}/${projectId}/agents`,
      { query },
    );

    return new Page<Agent>(
      result.agents ?? [],
      result.nextCursor,
      (cursor) => this.fetchAgentPage(projectId, { ...params, cursor }),
      result.totalCount,
    );
  }
}
