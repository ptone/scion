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

import { describe, it, expect, beforeEach } from 'vitest';
import { StateManager } from './state.js';
import type { Agent } from '../shared/types.js';

/** Minimal agent stub for testing */
function stubAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: 'agent-1',
    name: 'test-agent',
    projectId: 'proj-1',
    template: 'default',
    phase: 'running' as Agent['phase'],
    ...overrides,
  };
}

describe('StateManager – SSE capability derivation', () => {
  let sm: StateManager;

  beforeEach(() => {
    sm = new StateManager();
  });

  it('derives _capabilities from an existing agent for SSE-created agents', () => {
    const caps = { actions: ['stop', 'delete', 'attach', 'start', 'message'] };
    // Seed an existing agent with capabilities
    sm.seedAgents([stubAgent({ id: 'existing-1', _capabilities: caps })]);

    // Simulate an SSE agent.created event without _capabilities
    (sm as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }).handleUpdate({
      subject: 'project.proj-1.agent.created',
      data: { agentId: 'new-agent', name: 'new', projectId: 'proj-1', template: 'tpl', phase: 'creating' },
    });

    const newAgent = sm.getAgent('new-agent');
    expect(newAgent).toBeDefined();
    expect(newAgent!._capabilities).toEqual(caps);
  });

  it('falls back to scope capabilities when no existing agent has _capabilities', () => {
    const scopeCaps = { actions: ['stop', 'delete'] };
    sm.seedScopeCapabilities(scopeCaps);

    (sm as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }).handleUpdate({
      subject: 'project.proj-1.agent.created',
      data: { agentId: 'new-agent', name: 'new', projectId: 'proj-1', template: 'tpl', phase: 'creating' },
    });

    const newAgent = sm.getAgent('new-agent');
    expect(newAgent).toBeDefined();
    expect(newAgent!._capabilities).toEqual(scopeCaps);
  });

  it('does not override existing _capabilities on the SSE event', () => {
    const existingCaps = { actions: ['stop', 'delete', 'attach'] };
    const eventCaps = { actions: ['stop'] };
    sm.seedAgents([stubAgent({ id: 'existing-1', _capabilities: existingCaps })]);

    (sm as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }).handleUpdate({
      subject: 'project.proj-1.agent.created',
      data: {
        agentId: 'new-agent', name: 'new', projectId: 'proj-1',
        template: 'tpl', phase: 'creating', _capabilities: eventCaps,
      },
    });

    const newAgent = sm.getAgent('new-agent');
    expect(newAgent!._capabilities).toEqual(eventCaps);
  });

  it('gracefully handles no donor capabilities', () => {
    // No existing agents and no scope capabilities
    (sm as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }).handleUpdate({
      subject: 'project.proj-1.agent.created',
      data: { agentId: 'new-agent', name: 'new', projectId: 'proj-1', template: 'tpl', phase: 'creating' },
    });

    const newAgent = sm.getAgent('new-agent');
    expect(newAgent).toBeDefined();
    expect(newAgent!._capabilities).toBeUndefined();
  });

  it('preserves existing _capabilities on status updates', () => {
    const caps = { actions: ['stop', 'delete'] };
    sm.seedAgents([stubAgent({ id: 'agent-1', _capabilities: caps })]);

    // Simulate a status update (not created) that lacks _capabilities
    (sm as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }).handleUpdate({
      subject: 'agent.agent-1.status',
      data: { phase: 'stopped' },
    });

    const agent = sm.getAgent('agent-1');
    expect(agent!._capabilities).toEqual(caps);
    expect(agent!.phase).toBe('stopped');
  });
});
