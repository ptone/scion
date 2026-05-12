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

export { ScionClient, type ScionClientOptions } from "./client.js";
export { AgentsResource } from "./resources/agents.js";
export { Page, type PageParams } from "./pagination.js";
export { Transport, type TransportOptions, type AuthProvider } from "./transport.js";
export {
  ScionError,
  ApiError,
  AuthenticationError,
  AuthorizationError,
  NotFoundError,
  ConflictError,
  ValidationError,
} from "./errors.js";
export type {
  StructuredMessage,
  Agent,
  AgentPhase,
  AgentActivity,
  AgentConfig,
  DirectConnect,
  KubernetesInfo,
  CreateAgentParams,
  CreateAgentResponse,
  ListAgentsParams,
  SendStructuredMessageOptions,
} from "./types/index.js";
