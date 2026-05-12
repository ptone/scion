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

import type { PageOptions } from './common';

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

/** A project from the Hub API. */
export interface Project {
  id: string;
  name: string;
  slug: string;
  gitRemote?: string;
  defaultRuntimeBrokerId?: string;
  created: string;
  updated: string;
  createdBy?: string;
  ownerId?: string;
  visibility?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  providers?: ProjectProvider[];
  agentCount?: number;
  activeBrokerCount?: number;
  projectType?: string;
}

/** Parameters for listing projects. */
export interface ListProjectsParams extends PageOptions {
  /** Filter by visibility (e.g. "private", "public"). */
  visibility?: string;
  /** Filter by git remote URL (exact or prefix match). */
  gitRemote?: string;
  /** Filter by contributing broker ID. */
  brokerId?: string;
  /** Filter by exact name (case-insensitive). */
  name?: string;
  /** Filter by exact slug (case-insensitive). */
  slug?: string;
  /** Filter by labels (key=value pairs). */
  labels?: Record<string, string>;
}

/** Parameters for creating a new project. */
export interface CreateProjectParams {
  /** Project name. */
  name: string;
  /** Optional client-provided ID. */
  id?: string;
  /** Optional URL-safe slug. */
  slug?: string;
  /** Optional git remote URL. */
  gitRemote?: string;
  /** Optional visibility setting. */
  visibility?: string;
  /** Optional labels. */
  labels?: Record<string, string>;
}

/** Parameters for updating an existing project. */
export interface UpdateProjectParams {
  /** Updated project name. */
  name?: string;
  /** Updated labels. */
  labels?: Record<string, string>;
  /** Updated annotations. */
  annotations?: Record<string, string>;
  /** Updated visibility. */
  visibility?: string;
  /** Default runtime broker ID. */
  defaultRuntimeBrokerId?: string;
}

/** Parameters for listing agents within a project. */
export interface ListProjectAgentsParams extends PageOptions {
  /** Filter by lifecycle phase (e.g. "running", "stopped"). */
  phase?: string;
  /** Filter by runtime broker ID. */
  runtimeBrokerId?: string;
  /** Filter by labels (key=value pairs). */
  labels?: Record<string, string>;
}
