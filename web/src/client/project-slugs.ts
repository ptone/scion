/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * A page-lifetime project ID to slug index for "Jump to agent" palettes on
 * surfaces that do not load project slugs themselves (agent rows carry the
 * project ID and display name only).
 *
 * A surface seeds it with any projects it already holds and asks it to
 * {@link ProjectSlugIndex.ensure} the projects its agents belong to. Only
 * when one of those slugs is unknown does it walk `GET /api/v1/projects`,
 * at most once per page lifetime after a walk succeeds; a failed walk is
 * tried again on the next `ensure`. Subscribers hear every change, so an
 * open palette can update its rows when a slug becomes known.
 */

import type { ProjectSlugLookup } from './agent-palette-candidate.js';
import { paginateAll } from './paginate-all.js';

/** The project fields the index reads. */
export interface ProjectSlugSource {
  id: string;
  slug?: string;
}

/** Fetches every project the viewer can list. */
export type ProjectSlugFetcher = () => Promise<ProjectSlugSource[]>;

const PROJECT_PAGE_SIZE = 200;

/** Walks every page of `GET /api/v1/projects`. */
export function fetchAllProjectSlugs(): Promise<ProjectSlugSource[]> {
  return paginateAll<ProjectSlugSource>({
    path: '/api/v1/projects',
    pageSize: PROJECT_PAGE_SIZE,
    label: 'projects',
    parsePage: (body) => {
      const page = (body ?? {}) as { projects?: ProjectSlugSource[]; nextCursor?: string };
      const items = page.projects ?? [];
      return page.nextCursor ? { items, nextCursor: page.nextCursor } : { items };
    },
  });
}

export class ProjectSlugIndex {
  private readonly slugs = new Map<string, string>();
  private readonly listeners = new Set<() => void>();
  private walk: Promise<void> | null = null;
  private walked = false;
  private changes = 0;

  constructor(private readonly fetchProjects: ProjectSlugFetcher = fetchAllProjectSlugs) {}

  /** Bumped on every change to the index, for memoising on it. */
  get version(): number {
    return this.changes;
  }

  /** The slug of `projectId`, or `undefined` while it is not known. */
  readonly lookup: ProjectSlugLookup = (projectId) => this.slugs.get(projectId);

  /** Records the slugs of `projects`; subscribers hear it if any changed. */
  seed(projects: Iterable<ProjectSlugSource>): void {
    let changed = false;
    for (const project of projects) {
      if (!project.id || !project.slug || this.slugs.get(project.id) === project.slug) continue;
      this.slugs.set(project.id, project.slug);
      changed = true;
    }
    if (!changed) return;
    this.changes++;
    for (const listener of [...this.listeners]) listener();
  }

  /** Hears every change to the index. Returns the unsubscribe function. */
  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  /**
   * Makes the slugs of `projectIds` known if they can be: resolves at once
   * when they all are, or when a walk has already succeeded; otherwise
   * joins or starts the project walk. Never rejects: a project the viewer
   * cannot list keeps its name on the row.
   */
  ensure(projectIds: Iterable<string>): Promise<void> {
    if (this.walked) return Promise.resolve();
    if (this.walk) return this.walk;
    let unknown = false;
    for (const id of projectIds) {
      if (id && !this.slugs.has(id)) {
        unknown = true;
        break;
      }
    }
    if (!unknown) return Promise.resolve();
    const walk = this.fetchProjects()
      .then(
        (projects) => {
          this.walked = true;
          this.seed(projects);
        },
        (err: unknown) => {
          console.warn('[palette] project slugs unavailable:', err);
        }
      )
      .finally(() => {
        if (this.walk === walk) this.walk = null;
      });
    this.walk = walk;
    return walk;
  }
}

/** The index shared by every surface in this page. */
export const projectSlugs = new ProjectSlugIndex();
