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

// Type models
export type {
  // Common
  HealthResponse,
  PageParams,
  PaginatedResponse,
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
  // Projects
  Project,
  ProjectProvider,
  CreateProjectRequest,
  UpdateProjectRequest,
  ListProjectsOptions,
  ListProjectsResponse,
  // Secrets
  Secret,
  SetSecretRequest,
  SetSecretResponse,
  SecretScopeOptions,
  ListSecretResponse,
  // Messages
  Message,
  StructuredMessage,
  ListMessagesOptions,
  ListMessagesResponse,
} from './types/index.js';
