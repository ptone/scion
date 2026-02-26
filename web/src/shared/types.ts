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
 * Shared types for server and client
 */

/**
 * User role enumeration
 */
export type UserRole = 'admin' | 'member' | 'viewer';

/**
 * User information
 */
export interface User {
  id: string;
  email: string;
  name: string;
  avatar?: string | undefined;
  role?: UserRole | undefined;
}

/**
 * Admin user information from the Hub API (GET /api/v1/users)
 */
export interface AdminUser {
  id: string;
  email: string;
  displayName: string;
  avatarUrl?: string;
  role: UserRole;
  status: 'active' | 'suspended';
  created: string;
  lastLogin?: string;
  _capabilities?: Capabilities;
}

/**
 * Group type enumeration
 */
export type GroupType = 'explicit' | 'grove_agents';

/**
 * Group information from the Hub API (GET /api/v1/groups)
 */
export interface AdminGroup {
  id: string;
  name: string;
  slug: string;
  description?: string;
  groupType: GroupType;
  groveId?: string;
  parentId?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  ownerId?: string;
  createdBy?: string;
  created: string;
  updated: string;
  _capabilities?: Capabilities;
}

/**
 * Group member information
 */
export interface GroupMember {
  groupId: string;
  memberType: 'user' | 'group' | 'agent';
  memberId: string;
  role: 'member' | 'admin' | 'owner';
  addedAt: string;
  addedBy?: string;
}

/**
 * Initial page data passed from SSR to client
 */
export interface PageData {
  /** Current URL path */
  path: string;
  /** Page title */
  title: string;
  /** Current user (if authenticated) */
  user?: User | undefined;
  /** Additional page-specific data */
  data?: Record<string, unknown> | undefined;
}

/**
 * Route definition for client-side routing
 */
export interface RouteConfig {
  path: string;
  component: string;
  action?: () => Promise<void>;
}

/**
 * Grove status enumeration
 */
export type GroveStatus = 'active' | 'inactive' | 'error';

/**
 * Grove information from the Hub API
 */
export interface Grove {
  id: string;
  name: string;
  slug?: string;
  path: string;
  gitRemote?: string;
  status: GroveStatus;
  visibility?: string;
  labels?: Record<string, string>;
  agentCount: number;
  createdAt: string;
  updatedAt: string;
  _capabilities?: Capabilities;
}

/**
 * Agent status enumeration
 */
export type AgentStatus =
  | 'running'
  | 'stopped'
  | 'provisioning'
  | 'cloning'
  | 'error'
  | 'idle'
  | 'busy'
  | 'waiting_for_input'
  | 'completed';

/**
 * Agent information from the Hub API
 */
export interface Agent {
  id: string;
  name: string;
  groveId: string;
  grove?: string;
  template: string;
  status: AgentStatus;
  taskSummary?: string;
  message?: string;
  lastSeen?: string;
  createdAt: string;
  updatedAt: string;
  _capabilities?: Capabilities;
}

/**
 * Template information from the Hub API
 */
export interface Template {
  id: string;
  name: string;
  slug: string;
  displayName?: string;
  description?: string;
  harness: string;
  status: string;
  scope: string;
  createdAt: string;
  updatedAt: string;
  _capabilities?: Capabilities;
}

/**
 * Runtime Broker status enumeration
 */
/**
 * Scope for environment variables and secrets
 */
export type ResourceScope = 'user' | 'grove' | 'runtime_broker';

/**
 * Injection mode for environment variables
 */
export type InjectionMode = 'always' | 'as_needed';

/**
 * Environment variable from the Hub API (GET /api/v1/env)
 */
export interface EnvVar {
  id: string;
  key: string;
  value: string;
  scope: ResourceScope;
  scopeId: string;
  description?: string;
  sensitive: boolean;
  injectionMode: InjectionMode;
  secret: boolean;
  created: string;
  updated: string;
  createdBy?: string;
}

/**
 * Secret type enumeration
 */
export type SecretType = 'environment' | 'variable' | 'file';

/**
 * Secret metadata from the Hub API (GET /api/v1/secrets)
 * Note: secret values are never returned from the API
 */
export interface Secret {
  id: string;
  key: string;
  secretType: SecretType;
  target?: string;
  scope: ResourceScope;
  scopeId: string;
  description?: string;
  version: number;
  created: string;
  updated: string;
  createdBy?: string;
  updatedBy?: string;
}

export type BrokerStatus = 'online' | 'offline' | 'degraded';

/**
 * Capabilities advertised by a Runtime Broker
 */
export interface BrokerCapabilities {
  webPTY: boolean;
  sync: boolean;
  attach: boolean;
}

/**
 * Runtime profile available on a broker
 */
export interface BrokerProfile {
  name: string;
  type: string;
  available: boolean;
}

/**
 * Runtime Broker information from the Hub API
 */
export interface RuntimeBroker {
  id: string;
  name: string;
  slug: string;
  version: string;
  status: BrokerStatus;
  connectionState: string;
  lastHeartbeat: string;
  capabilities?: BrokerCapabilities;
  profiles?: BrokerProfile[];
  autoProvide: boolean;
  endpoint?: string;
  createdAt: string;
  updatedAt: string;
}

// ---------------------------------------------------------------------------
// Access control capabilities
// ---------------------------------------------------------------------------

/**
 * Capabilities attached to API resource responses.
 * Each resource includes `_capabilities: { actions: [...] }` describing
 * what the current user is allowed to do with that resource.
 */
export interface Capabilities {
  actions: string[];
}

/**
 * Check whether a capability set permits a specific action.
 * Returns false (fail-closed) when capabilities are undefined.
 */
export function can(capabilities: Capabilities | undefined, action: string): boolean {
  if (!capabilities) return false;
  return capabilities.actions.includes(action);
}

/**
 * Check whether a capability set permits any of the given actions.
 * Returns false (fail-closed) when capabilities are undefined.
 */
export function canAny(capabilities: Capabilities | undefined, ...actions: string[]): boolean {
  if (!capabilities) return false;
  return actions.some((a) => capabilities.actions.includes(a));
}

/**
 * Generic wrapper for paginated list responses from the Hub API.
 *
 * Note: The Hub API returns list responses with named keys (e.g., `agents`,
 * `groves`) rather than a generic `items` key. This type is provided as a
 * convenience for new code. Existing components that parse `data.agents` etc.
 * continue to work — the important part is that each item now carries
 * `_capabilities` and the response includes scope-level capabilities.
 */
export interface ListResponse<T> {
  items: T[];
  _capabilities?: Capabilities;
  nextCursor?: string;
  totalCount?: number;
}

// ---------------------------------------------------------------------------
// Policy types (mirrors Go store.Policy)
// ---------------------------------------------------------------------------

/**
 * Condition matching agents delegated from a specific principal.
 */
export interface DelegatedFromCondition {
  principalType: string;
  principalId: string;
}

/**
 * Optional conditional logic for policies.
 */
export interface PolicyConditions {
  labels?: Record<string, string>;
  validFrom?: string;
  validUntil?: string;
  sourceIps?: string[];
  delegatedFrom?: DelegatedFromCondition;
  delegatedFromGroup?: string;
}

/**
 * Policy effect: allow or deny.
 */
export type PolicyEffect = 'allow' | 'deny';

/**
 * Access control policy from the Hub API.
 * Mirrors the Go `store.Policy` struct.
 */
export interface Policy {
  id: string;
  name: string;
  description?: string;
  scopeType: string;
  scopeId: string;
  resourceType: string;
  resourceId?: string;
  actions: string[];
  effect: PolicyEffect;
  conditions?: PolicyConditions;
  priority: number;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  created: string;
  updated: string;
  createdBy?: string;
}
