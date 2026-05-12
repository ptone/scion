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

/** Configuration applied to an agent. */
export interface AgentConfig {
  image?: string;
  harnessConfig?: string;
  harnessAuth?: string;
  env?: Record<string, string>;
  model?: string;
  profile?: string;
  task?: string;
}

/** Direct SSH connection info. */
export interface DirectConnect {
  enabled: boolean;
  sshHost?: string;
  sshPort?: number;
  sshUser?: string;
}

/** Kubernetes-specific metadata. */
export interface KubernetesInfo {
  cluster?: string;
  namespace?: string;
  podName?: string;
  syncedAt?: string;
}

/** An agent from the Hub API. */
export interface Agent {
  id: string;
  slug: string;
  containerId?: string;
  name: string;
  template?: string;
  harnessConfig?: string;
  harnessAuth?: string;
  projectId?: string;
  project?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  phase?: string;
  activity?: string;
  status: string;
  connectionState?: string;
  containerStatus?: string;
  runtimeState?: string;
  image?: string;
  detached?: boolean;
  runtime?: string;
  runtimeBrokerId?: string;
  runtimeBrokerName?: string;
  runtimeBrokerType?: string;
  webPtyEnabled?: boolean;
  taskSummary?: string;
  appliedConfig?: AgentConfig;
  directConnect?: DirectConnect;
  kubernetes?: KubernetesInfo;
  created: string;
  updated: string;
  lastSeen?: string;
  deletedAt?: string;
  createdBy?: string;
  ownerId?: string;
  visibility?: string;
  stateVersion?: number;
}
