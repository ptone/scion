# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Public type models for the Scion Python SDK."""

from scion.types.agents import (
    Agent,
    AgentConfig,
    CreateAgentRequest,
    CreateAgentResponse,
    DirectConnect,
    ListAgentsResponse,
    UpdateAgentRequest,
)
from scion.types.common import HealthResponse, ListMeta, PaginationParams
from scion.types.messages import AgentMessage, Message, StructuredMessage
from scion.types.projects import (
    CreateProjectRequest,
    ListProjectsResponse,
    Project,
    ProjectProvider,
    UpdateProjectRequest,
)
from scion.types.secrets import (
    ListSecretResponse,
    Secret,
    SetSecretRequest,
    SetSecretResponse,
)

__all__ = [
    # Agents
    "Agent",
    "AgentConfig",
    "CreateAgentRequest",
    "CreateAgentResponse",
    "DirectConnect",
    "ListAgentsResponse",
    "UpdateAgentRequest",
    # Common
    "HealthResponse",
    "ListMeta",
    "PaginationParams",
    # Messages
    "AgentMessage",
    "Message",
    "StructuredMessage",
    # Projects
    "CreateProjectRequest",
    "ListProjectsResponse",
    "Project",
    "ProjectProvider",
    "UpdateProjectRequest",
    # Secrets
    "ListSecretResponse",
    "Secret",
    "SetSecretRequest",
    "SetSecretResponse",
]
