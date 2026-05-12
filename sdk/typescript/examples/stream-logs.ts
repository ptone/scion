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
 * Stream cloud logs from a running agent.
 *
 * This example demonstrates:
 * - Connecting to the cloud log streaming SSE endpoint
 * - Filtering logs by severity via query parameters
 * - Using AbortController for graceful shutdown
 * - Colorized terminal output
 *
 * Usage:
 *   export SCION_API_TOKEN="your-token"
 *   npx tsx stream-logs.ts --hub-url https://hub.example.com --agent agent-id
 *
 *   # Filter to only ERROR logs
 *   npx tsx stream-logs.ts --hub-url https://hub.example.com --agent agent-id --severity ERROR
 */

import { ScionClient, NotFoundError, ScionError, StreamError } from '@scion/sdk';
import type { LogEntry } from '@scion/sdk';

// ---------------------------------------------------------------------------
// ANSI colors
// ---------------------------------------------------------------------------

const RESET = '\x1b[0m';
const SEVERITY_COLORS: Record<string, string> = {
  DEBUG: '\x1b[36m',      // Cyan
  INFO: '\x1b[32m',       // Green
  NOTICE: '\x1b[34m',     // Blue
  WARNING: '\x1b[33m',    // Yellow
  ERROR: '\x1b[31m',      // Red
  CRITICAL: '\x1b[35m',   // Magenta
  ALERT: '\x1b[35;1m',    // Bold Magenta
  EMERGENCY: '\x1b[31;1m', // Bold Red
};

function colorize(severity: string, text: string, enabled: boolean): string {
  if (!enabled) return text;
  const color = SEVERITY_COLORS[severity] ?? RESET;
  return `${color}${text}${RESET}`;
}

function formatTimestamp(ts: string): string {
  try {
    const date = new Date(ts);
    return date.toISOString().substring(11, 19); // HH:MM:SS
  } catch {
    return '??:??:??';
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function parseArgs(): { hubUrl: string; agentId: string; severity?: string; noColor: boolean } {
  const args = process.argv.slice(2);
  let hubUrl = '';
  let agentId = '';
  let severity: string | undefined;
  let noColor = false;

  for (let i = 0; i < args.length; i++) {
    switch (args[i]) {
      case '--hub-url':
        hubUrl = args[++i] ?? '';
        break;
      case '--agent':
        agentId = args[++i] ?? '';
        break;
      case '--severity':
        severity = args[++i]?.toUpperCase();
        break;
      case '--no-color':
        noColor = true;
        break;
    }
  }

  if (!hubUrl || !agentId) {
    console.error('Usage: stream-logs.ts --hub-url <url> --agent <id> [--severity <level>] [--no-color]');
    process.exit(1);
  }

  return { hubUrl, agentId, severity, noColor };
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main(): Promise<void> {
  const { hubUrl, agentId, severity, noColor } = parseArgs();
  const useColor = !noColor && process.stdout.isTTY;

  const client = new ScionClient({ hubUrl });

  // Verify the agent exists
  try {
    const agent = await client.agents.get(agentId);
    console.log(`Streaming logs for agent: ${agent.name} (${agent.id})`);
    console.log(`Agent phase: ${agent.phase}`);
  } catch (err) {
    if (err instanceof NotFoundError) {
      console.error(`Error: Agent '${agentId}' not found`);
      process.exit(1);
    }
    throw err;
  }

  if (severity) {
    console.log(`Filtering: severity >= ${severity}`);
  }
  console.log('-'.repeat(60));

  // Set up graceful shutdown
  const controller = new AbortController();
  process.on('SIGINT', () => {
    console.log('\n\nStopped by user.');
    controller.abort();
  });

  // Build query params for severity filter
  const query: Record<string, string> = {};
  if (severity) {
    query.severity = severity;
  }

  // Open the SSE stream
  try {
    const stream = client.agents.streamCloudLogs(agentId, {
      signal: controller.signal,
      query: Object.keys(query).length > 0 ? query : undefined,
    });

    for await (const entry of stream) {
      const ts = formatTimestamp(entry.timestamp);
      const sev = (entry.severity ?? 'UNKNOWN').padEnd(8);
      const msg = entry.message ?? '';

      const prefix = colorize(entry.severity ?? '', `${ts} [${sev}]`, useColor);
      let line = `${prefix} ${msg}`;

      if (entry.sourceLocation?.function) {
        line += ` (fn: ${entry.sourceLocation.function})`;
      }

      console.log(line);
    }
  } catch (err) {
    if (err instanceof StreamError) {
      console.error(`\nStream error: ${err.message}`);
      process.exit(1);
    }

    // Ignore abort errors (expected on Ctrl+C)
    if (err instanceof Error && err.name === 'AbortError') {
      return;
    }

    throw err;
  }
}

main().catch((err) => {
  if (err instanceof ScionError) {
    console.error(`API error: ${err.message}`);
    if (err.requestId) console.error(`Request ID: ${err.requestId}`);
    process.exit(1);
  }
  console.error(err);
  process.exit(1);
});
