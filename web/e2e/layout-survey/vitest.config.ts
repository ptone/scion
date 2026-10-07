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

import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';

/**
 * Hub-free unit tests for the layout-survey helpers and the parameterized
 * harness auth helpers: `npx vitest run -c e2e/layout-survey/vitest.config.ts`.
 */
export default defineConfig({
  root: fileURLToPath(new URL('.', import.meta.url)),
  // keep the cache in web/node_modules (gitignored), not under this suite
  cacheDir: fileURLToPath(new URL('../../node_modules/.vite-layout-survey', import.meta.url)),
  test: {
    environment: 'node',
    include: ['unit/**/*.test.ts'],
    env: { TZ: 'UTC' },
  },
});
