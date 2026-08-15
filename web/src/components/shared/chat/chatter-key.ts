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
 * Identity and visibility of a space's Agent Chatter view.
 *
 * The chatter thread is a client-side entry rather than a topic row, so it
 * needs a conversation key the page can tell apart from a topic UUID, and a
 * per-space flag saying whether to list it at all. Both live here so the rail,
 * the page and the feed component can agree without importing each other.
 */

const CHATTER_PREFIX = 'chatter:';

/** The conversation key of a space's Agent Chatter view. */
export function chatterKey(projectId: string): string {
  return `${CHATTER_PREFIX}${projectId}`;
}

/** The projectId inside a chatter key, or '' if the key is not one. */
export function parseChatterKey(key: string): string {
  return key.startsWith(CHATTER_PREFIX) ? key.slice(CHATTER_PREFIX.length) : '';
}

/** localStorage key holding a space's Agent Chatter preference. */
export function chatterPrefKey(projectId: string): string {
  return `scion-chat-chatter-${projectId}`;
}

/**
 * Whether the Agent Chatter thread is shown for a space. The preference is
 * per-browser: it governs nothing but what this client renders, so it needs
 * no server state.
 */
export function isChatterEnabled(projectId: string): boolean {
  return localStorage.getItem(chatterPrefKey(projectId)) === 'true';
}
