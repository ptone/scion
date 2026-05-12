/**
 * @scion/sdk - TypeScript SDK for the Scion Hub API
 *
 * @packageDocumentation
 */

export const SDK_VERSION = '0.1.0';

// Error types
export {
  ScionError,
  AuthenticationError,
  PermissionError,
  NotFoundError,
  ConflictError,
  ValidationError,
  RateLimitError,
  ServerError,
  ConnectionError,
  StreamError,
  ErrorCode,
  parseErrorResponse,
} from './errors.js';
export type { ErrorCodeValue } from './errors.js';

// Transport
export { Transport } from './transport.js';
export type { TransportOptions, RequestOptions } from './transport.js';

// Client
export { ScionClient } from './client.js';
export type { ScionClientOptions } from './client.js';

// Pagination
export { Page } from './pagination.js';
export type { FetchNextPage } from './pagination.js';

// Streaming
export { ScionStream, createSSEParser, createLineSplitter } from './streaming.js';

// Resources
export {
  BaseResource,
  AgentsResource,
  MessagesResource,
  ProjectsResource,
  SecretsResource,
} from './resources/index.js';
export type { SecretsPage } from './resources/index.js';

// Type models
export type {
  // Common
  HealthResponse,
  PageParams,
  PaginatedResponse,
  StructuredMessage,
  // Agents
  Agent,
  AgentConfig,
  DirectConnect,
  KubernetesInfo,
  CreateAgentRequest,
  CreateAgentResponse,
  UpdateAgentRequest,
  ListAgentsOptions,
  ListAgentsResponse,
  SendStructuredMessageOptions,
  // Projects
  Project,
  ProjectProvider,
  CreateProjectRequest,
  UpdateProjectRequest,
  ListProjectsOptions,
  ListProjectAgentsOptions,
  ListProjectsResponse,
  // Secrets
  Secret,
  SetSecretRequest,
  SetSecretResponse,
  SecretScopeOptions,
  ListSecretsOptions,
  ListSecretResponse,
  // Messages
  Message,
  ListMessagesOptions,
  ListMessagesResponse,
  // Streaming
  StreamEvent,
  AgentEvent,
  AgentDetail,
  LogEntry,
  SourceLocation,
  StreamOptions,
  StreamCallbackOptions,
} from './types/index.js';
