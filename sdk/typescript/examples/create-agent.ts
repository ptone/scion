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

/**
 * Create an agent, start it, and watch events until completion.
 *
 * This example demonstrates:
 * - Creating a Scion client with authentication
 * - Creating a new agent in a project
 * - Streaming agent events via SSE for real-time updates
 * - Falling back to polling when streaming is not needed
 *
 * Usage:
 *   export SCION_API_TOKEN="your-token"
 *   npx tsx create-agent.ts --hub-url https://hub.example.com --project proj-123
 *
 *   # Use streaming instead of polling
 *   npx tsx create-agent.ts --hub-url https://hub.example.com --project proj-123 --stream
 */

import { ScionClient, NotFoundError, ScionError } from '@scion/sdk';
import type { Agent } from '@scion/sdk';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function parseArgs(): { hubUrl: string; projectId: string; name: string; task: string; stream: boolean } {
  const args = process.argv.slice(2);
  let hubUrl = '';
  let projectId = '';
  let name = 'example-agent';
  let task = 'Hello from the TypeScript SDK!';
  let stream = false;

  for (let i = 0; i < args.length; i++) {
    switch (args[i]) {
      case '--hub-url':
        hubUrl = args[++i] ?? '';
        break;
      case '--project':
        projectId = args[++i] ?? '';
        break;
      case '--name':
        name = args[++i] ?? name;
        break;
      case '--task':
        task = args[++i] ?? task;
        break;
      case '--stream':
        stream = true;
        break;
    }
  }

  if (!hubUrl || !projectId) {
    console.error('Usage: create-agent.ts --hub-url <url> --project <id> [--name <name>] [--task <task>] [--stream]');
    process.exit(1);
  }

  return { hubUrl, projectId, name, task, stream };
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// ---------------------------------------------------------------------------
// Polling approach
// ---------------------------------------------------------------------------

async function createAndPoll(client: ScionClient, projectId: string, name: string, task: string): Promise<void> {
  // Create the agent
  console.log(`Creating agent '${name}' in project '${projectId}'...`);
  const response = await client.agents.create({
    name,
    projectId,
    task,
  });

  if (response.warnings?.length) {
    for (const warning of response.warnings) {
      console.log(`  Warning: ${warning}`);
    }
  }

  const agentId = response.agent?.id;
  if (!agentId) {
    console.error('Error: No agent returned in response');
    process.exit(1);
  }

  console.log(`Agent created: ${agentId} (phase: ${response.agent?.phase})`);

  // Poll for completion
  console.log('\nWaiting for agent to complete...');
  const terminalPhases = new Set(['stopped', 'completed', 'failed', 'error']);

  while (true) {
    let agent: Agent;
    try {
      agent = await client.agents.get(agentId);
    } catch (err) {
      if (err instanceof NotFoundError) {
        console.log('Agent was deleted');
        break;
      }
      throw err;
    }

    const phase = agent.phase ?? 'unknown';
    const activity = agent.activity ?? '';
    let statusLine = `  Phase: ${phase}`;
    if (activity) statusLine += ` | Activity: ${activity}`;
    console.log(statusLine);

    if (terminalPhases.has(phase)) {
      console.log(`\nAgent reached terminal phase: ${phase}`);
      break;
    }

    await sleep(5000);
  }

  // Print final state
  try {
    const agent = await client.agents.get(agentId);
    console.log('\nFinal state:');
    console.log(`  Name:    ${agent.name}`);
    console.log(`  Phase:   ${agent.phase}`);
    console.log(`  Status:  ${agent.status}`);
    if (agent.taskSummary) {
      console.log(`  Summary: ${agent.taskSummary}`);
    }
  } catch (err) {
    if (err instanceof NotFoundError) {
      console.log('Agent no longer exists');
    }
  }
}

// ---------------------------------------------------------------------------
// Streaming approach
// ---------------------------------------------------------------------------

async function createAndStream(client: ScionClient, projectId: string, name: string, task: string): Promise<void> {
  // Create the agent
  console.log(`Creating agent '${name}'...`);
  const response = await client.agents.create({
    name,
    projectId,
    task,
  });

  const agentId = response.agent?.id;
  if (!agentId) {
    console.error('Error: No agent returned');
    process.exit(1);
  }

  console.log(`Agent created: ${agentId}`);

  // Stream events
  console.log('\nStreaming events (Ctrl+C to stop)...');
  const controller = new AbortController();

  // Handle Ctrl+C gracefully
  process.on('SIGINT', () => {
    console.log('\nStopping...');
    controller.abort();
  });

  const stream = client.agents.streamEvents(agentId, {
    signal: controller.signal,
  });

  for await (const event of stream) {
    console.log(`[${event.type}] phase=${event.data.phase ?? 'unknown'}`);
    if (event.data.detail?.message) {
      console.log(`  ${event.data.detail.message}`);
    }

    // Stop when the agent reaches a terminal state
    if (event.data.phase === 'stopped' || event.data.phase === 'completed') {
      console.log(`\nAgent finished: ${event.data.phase}`);
      break;
    }
  }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main(): Promise<void> {
  const { hubUrl, projectId, name, task, stream } = parseArgs();

  const client = new ScionClient({ hubUrl });

  try {
    // Verify connectivity
    const health = await client.health();
    console.log(`Connected to hub (status: ${health.status})\n`);

    if (stream) {
      await createAndStream(client, projectId, name, task);
    } else {
      await createAndPoll(client, projectId, name, task);
    }
  } catch (err) {
    if (err instanceof ScionError) {
      console.error(`API error: ${err.message}`);
      if (err.requestId) console.error(`Request ID: ${err.requestId}`);
      process.exit(1);
    }
    throw err;
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
