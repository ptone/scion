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
 * CRUD operations on secrets.
 *
 * This example demonstrates:
 * - Listing secrets in different scopes (user, project)
 * - Creating and updating secrets
 * - Reading secret metadata (values are write-only)
 * - Deleting secrets
 *
 * Usage:
 *   export SCION_API_TOKEN="your-token"
 *
 *   # List user-scoped secrets
 *   npx tsx manage-secrets.ts --hub-url https://hub.example.com list
 *
 *   # List project-scoped secrets
 *   npx tsx manage-secrets.ts --hub-url https://hub.example.com list \
 *       --scope project --scope-id proj-123
 *
 *   # Set a secret
 *   npx tsx manage-secrets.ts --hub-url https://hub.example.com set \
 *       MY_API_KEY "sk-secret-value" --description "External API key"
 *
 *   # Get secret metadata
 *   npx tsx manage-secrets.ts --hub-url https://hub.example.com get MY_API_KEY
 *
 *   # Delete a secret
 *   npx tsx manage-secrets.ts --hub-url https://hub.example.com delete MY_API_KEY
 */

import { ScionClient, NotFoundError, ScionError } from '@scion/sdk';

// ---------------------------------------------------------------------------
// Argument parsing
// ---------------------------------------------------------------------------

interface Args {
  hubUrl: string;
  scope?: string;
  scopeId?: string;
  command: string;
  key?: string;
  value?: string;
  description?: string;
}

function parseArgs(): Args {
  const argv = process.argv.slice(2);
  let hubUrl = '';
  let scope: string | undefined;
  let scopeId: string | undefined;
  let command = '';
  let key: string | undefined;
  let value: string | undefined;
  let description: string | undefined;

  // Extract flags first
  const positional: string[] = [];
  for (let i = 0; i < argv.length; i++) {
    switch (argv[i]) {
      case '--hub-url':
        hubUrl = argv[++i] ?? '';
        break;
      case '--scope':
        scope = argv[++i];
        break;
      case '--scope-id':
        scopeId = argv[++i];
        break;
      case '--description':
        description = argv[++i];
        break;
      default:
        positional.push(argv[i]!);
    }
  }

  command = positional[0] ?? '';
  key = positional[1];
  value = positional[2];

  if (!hubUrl || !command) {
    console.error(`Usage: manage-secrets.ts --hub-url <url> [--scope <scope>] [--scope-id <id>] <command> [args...]

Commands:
  list                     List secrets
  get <key>                Get secret metadata
  set <key> <value>        Create or update a secret
  delete <key>             Delete a secret`);
    process.exit(1);
  }

  return { hubUrl, scope, scopeId, command, key, value, description };
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

async function cmdList(client: ScionClient, args: Args): Promise<void> {
  const result = await client.secrets.list({
    scope: args.scope,
    scopeId: args.scopeId,
  });

  console.log(`Scope: ${result.scope} (${result.scopeId || 'default'})`);
  console.log(`Secrets: ${result.data.length}`);
  console.log('-'.repeat(60));

  if (result.data.length === 0) {
    console.log('(no secrets found)');
    return;
  }

  for (const secret of result.data) {
    console.log(`  Key:          ${secret.key}`);
    console.log(`  Type:         ${secret.type ?? 'environment'}`);
    console.log(`  Description:  ${secret.description ?? '(none)'}`);
    console.log(`  Injection:    ${secret.injectionMode ?? 'as_needed'}`);
    console.log(`  Version:      ${secret.version}`);
    if (secret.created) console.log(`  Created:      ${secret.created}`);
    if (secret.updated) console.log(`  Updated:      ${secret.updated}`);
    console.log();
  }
}

async function cmdGet(client: ScionClient, args: Args): Promise<void> {
  if (!args.key) {
    console.error('Usage: get <key>');
    process.exit(1);
  }

  try {
    const secret = await client.secrets.get(args.key, {
      scope: args.scope,
      scopeId: args.scopeId,
    });

    console.log(`Key:            ${secret.key}`);
    console.log(`ID:             ${secret.id}`);
    console.log(`Type:           ${secret.type ?? 'environment'}`);
    console.log(`Scope:          ${secret.scope}`);
    console.log(`Scope ID:       ${secret.scopeId}`);
    console.log(`Description:    ${secret.description ?? '(none)'}`);
    console.log(`Target:         ${secret.target ?? secret.key}`);
    console.log(`Injection Mode: ${secret.injectionMode ?? 'as_needed'}`);
    console.log(`Allow Progeny:  ${secret.allowProgeny}`);
    console.log(`Version:        ${secret.version}`);
    console.log(`Created:        ${secret.created}`);
    console.log(`Updated:        ${secret.updated}`);
    console.log(`Created By:     ${secret.createdBy}`);
    console.log(`Updated By:     ${secret.updatedBy}`);
    console.log();
    console.log('Note: Secret values are write-only and never returned by the API.');
  } catch (err) {
    if (err instanceof NotFoundError) {
      console.error(`Secret '${args.key}' not found`);
      process.exit(1);
    }
    throw err;
  }
}

async function cmdSet(client: ScionClient, args: Args): Promise<void> {
  if (!args.key || !args.value) {
    console.error('Usage: set <key> <value> [--description <desc>]');
    process.exit(1);
  }

  const result = await client.secrets.set(args.key, {
    value: args.value,
    scope: args.scope,
    scopeId: args.scopeId,
    description: args.description,
  });

  const action = result.created ? 'Created' : 'Updated';
  console.log(`${action} secret: ${args.key}`);
  if (result.secret) {
    console.log(`  Scope:   ${result.secret.scope}`);
    console.log(`  Version: ${result.secret.version}`);
  }
}

async function cmdDelete(client: ScionClient, args: Args): Promise<void> {
  if (!args.key) {
    console.error('Usage: delete <key>');
    process.exit(1);
  }

  try {
    await client.secrets.delete(args.key, {
      scope: args.scope,
      scopeId: args.scopeId,
    });
    console.log(`Deleted secret: ${args.key}`);
  } catch (err) {
    if (err instanceof NotFoundError) {
      console.error(`Secret '${args.key}' not found`);
      process.exit(1);
    }
    throw err;
  }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main(): Promise<void> {
  const args = parseArgs();
  const client = new ScionClient({ hubUrl: args.hubUrl });

  const commands: Record<string, (client: ScionClient, args: Args) => Promise<void>> = {
    list: cmdList,
    get: cmdGet,
    set: cmdSet,
    delete: cmdDelete,
  };

  const handler = commands[args.command];
  if (!handler) {
    console.error(`Unknown command: ${args.command}`);
    console.error('Valid commands: list, get, set, delete');
    process.exit(1);
  }

  await handler(client, args);
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
