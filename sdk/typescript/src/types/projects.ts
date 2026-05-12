/**
 * Type definitions for the Project resource.
 *
 * @packageDocumentation
 */

import type { PageParams, PaginatedResponse } from './common.js';

/** A broker providing runtime services to a project. */
export interface ProjectProvider {
  brokerId: string;
  brokerName: string;
  status: string;
  lastSeen?: string;
  localPath?: string;
  linkedBy?: string;
  linkedAt?: string;
}

/** A project as returned by the Scion Hub API. */
export interface Project {
  /** Hub UUID. */
  id: string;
  /** Human-readable name. */
  name: string;
  /** URL-safe slug. */
  slug: string;
  /** Git remote URL associated with this project. */
  gitRemote?: string;
  /** Default runtime broker ID. */
  defaultRuntimeBrokerId?: string;
  /** Creation timestamp (ISO 8601). */
  created: string;
  /** Last-updated timestamp (ISO 8601). */
  updated: string;
  /** ID of the user who created this project. */
  createdBy?: string;
  /** Owner user ID. */
  ownerId?: string;
  /** Visibility level. */
  visibility?: string;
  /** User-defined labels. */
  labels?: Record<string, string>;
  /** User-defined annotations. */
  annotations?: Record<string, string>;
  /** Runtime brokers providing services to this project. */
  providers?: ProjectProvider[];
  /** Number of agents in this project. */
  agentCount?: number;
  /** Number of active brokers providing this project. */
  activeBrokerCount?: number;
  /** Project type classification. */
  projectType?: string;
}

/** Request body for creating a new project. */
export interface CreateProjectRequest {
  /** Project name. */
  name: string;
  /** Git remote URL. */
  gitRemote?: string;
  /** User-defined labels. */
  labels?: Record<string, string>;
  /** User-defined annotations. */
  annotations?: Record<string, string>;
}

/** Request body for updating an existing project. */
export interface UpdateProjectRequest {
  /** New project name. */
  name?: string;
  /** Updated git remote URL. */
  gitRemote?: string;
  /** Updated labels. */
  labels?: Record<string, string>;
  /** Updated annotations. */
  annotations?: Record<string, string>;
}

/** Options for listing projects. */
export interface ListProjectsOptions extends PageParams {
  /** Filter by project type. */
  projectType?: string;
}

/** Response from listing projects. */
export type ListProjectsResponse = PaginatedResponse<Project>;
