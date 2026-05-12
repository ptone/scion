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

import { BaseResource } from './base';
import type { Page } from '../types/common';
import type { Agent } from '../types/agents';
import type {
  Project,
  ListProjectsParams,
  CreateProjectParams,
  UpdateProjectParams,
  ListProjectAgentsParams,
} from '../types/projects';

/** Raw list projects response from the API. */
interface ListProjectsResponse {
  projects: Project[];
  nextCursor?: string;
  totalCount?: number;
}

/** Raw list agents response from the API. */
interface ListAgentsResponse {
  agents: Agent[];
  nextCursor?: string;
  totalCount?: number;
}

/**
 * Resource for managing Scion projects.
 *
 * Projects are the primary organizational unit in Scion, grouping agents,
 * templates, and runtime broker providers around a shared codebase.
 *
 * @example
 * ```typescript
 * const client = new ScionClient({ baseUrl: 'https://hub.scion.dev', token: 'my-token' });
 *
 * // List all projects
 * const projects = await client.projects.list();
 *
 * // Create a new project
 * const project = await client.projects.create({ name: 'my-project' });
 * ```
 */
export class ProjectsResource extends BaseResource {
  private static readonly BASE_PATH = '/api/v1/projects';

  /**
   * List projects matching the given filter criteria.
   *
   * @param params - Optional filtering and pagination parameters.
   * @returns A paginated list of projects.
   *
   * @example
   * ```typescript
   * // List all projects
   * const page = await client.projects.list();
   * console.log(page.data);
   *
   * // List with filters
   * const filtered = await client.projects.list({
   *   visibility: 'private',
   *   limit: 10,
   * });
   *
   * // Paginate
   * const next = await client.projects.list({ cursor: page.page.nextCursor });
   * ```
   */
  async list(params?: ListProjectsParams): Promise<Page<Project>> {
    const query = this.buildPageQuery(params);

    if (params?.visibility) query['visibility'] = params.visibility;
    if (params?.gitRemote) query['gitRemote'] = params.gitRemote;
    if (params?.brokerId) query['brokerId'] = params.brokerId;
    if (params?.name) query['name'] = params.name;
    if (params?.slug) query['slug'] = params.slug;
    if (params?.labels) {
      // Labels are sent as repeated label=key=value query params.
      // Since our transport builds URLSearchParams, we serialize them
      // as a comma-separated list under a single key for simplicity,
      // but the Hub API expects repeated params. We handle this by
      // building the query string manually for labels.
      const labelEntries = Object.entries(params.labels)
        .map(([k, v]) => `${k}=${v}`)
        .join(',');
      if (labelEntries) query['label'] = labelEntries;
    }

    const response = await this.transport.get<ListProjectsResponse>(
      ProjectsResource.BASE_PATH,
      query,
    );

    return this.buildPage(response.projects ?? [], {
      nextCursor: response.nextCursor,
      totalCount: response.totalCount,
    });
  }

  /**
   * Get a single project by ID.
   *
   * @param projectId - The project ID or slug.
   * @returns The project.
   * @throws {ScionAPIError} If the project is not found (404).
   *
   * @example
   * ```typescript
   * const project = await client.projects.get('proj-abc123');
   * console.log(project.name);
   * ```
   */
  async get(projectId: string): Promise<Project> {
    return this.transport.get<Project>(`${ProjectsResource.BASE_PATH}/${projectId}`);
  }

  /**
   * Create a new project.
   *
   * @param params - Project creation parameters.
   * @returns The newly created project.
   *
   * @example
   * ```typescript
   * const project = await client.projects.create({
   *   name: 'my-project',
   *   gitRemote: 'git@github.com:org/repo.git',
   *   visibility: 'private',
   * });
   * ```
   */
  async create(params: CreateProjectParams): Promise<Project> {
    return this.transport.post<Project>(ProjectsResource.BASE_PATH, params);
  }

  /**
   * Update an existing project's metadata.
   *
   * @param projectId - The project ID or slug.
   * @param params - Fields to update.
   * @returns The updated project.
   * @throws {ScionAPIError} If the project is not found (404).
   *
   * @example
   * ```typescript
   * const updated = await client.projects.update('proj-abc123', {
   *   name: 'renamed-project',
   *   labels: { team: 'platform' },
   * });
   * ```
   */
  async update(projectId: string, params: UpdateProjectParams): Promise<Project> {
    return this.transport.patch<Project>(
      `${ProjectsResource.BASE_PATH}/${projectId}`,
      params,
    );
  }

  /**
   * Delete a project and all its agents.
   *
   * @param projectId - The project ID or slug.
   * @throws {ScionAPIError} If the project is not found (404).
   *
   * @example
   * ```typescript
   * await client.projects.delete('proj-abc123');
   * ```
   */
  async delete(projectId: string): Promise<void> {
    await this.transport.delete(`${ProjectsResource.BASE_PATH}/${projectId}`);
  }

  /**
   * List agents belonging to a project.
   *
   * @param projectId - The project ID or slug.
   * @param params - Optional filtering and pagination parameters.
   * @returns A paginated list of agents within the project.
   *
   * @example
   * ```typescript
   * const agents = await client.projects.listAgents('proj-abc123');
   * console.log(agents.data.map(a => a.name));
   *
   * // Filter by phase
   * const running = await client.projects.listAgents('proj-abc123', {
   *   phase: 'running',
   * });
   * ```
   */
  async listAgents(
    projectId: string,
    params?: ListProjectAgentsParams,
  ): Promise<Page<Agent>> {
    const query = this.buildPageQuery(params);

    if (params?.phase) query['phase'] = params.phase;
    if (params?.runtimeBrokerId) query['runtimeBrokerId'] = params.runtimeBrokerId;
    if (params?.labels) {
      const labelEntries = Object.entries(params.labels)
        .map(([k, v]) => `${k}=${v}`)
        .join(',');
      if (labelEntries) query['label'] = labelEntries;
    }

    const response = await this.transport.get<ListAgentsResponse>(
      `${ProjectsResource.BASE_PATH}/${projectId}/agents`,
      query,
    );

    return this.buildPage(response.agents ?? [], {
      nextCursor: response.nextCursor,
      totalCount: response.totalCount,
    });
  }
}
