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

import { defineConfig } from '@playwright/test';

/**
 * ATTACH-ONLY Wave01 measurement config (contract FROZEN rev 4 §5b).
 *
 * Deliberately has NO globalSetup / globalTeardown / webServer: it never
 * builds, starts, seeds, resets or stops a Hub. testMatch selects only the
 * Wave01 spec. Playwright traces/videos/screenshots are off (they can carry
 * cookies); evidence is written explicitly by the spec.
 */
export default defineConfig({
  testDir: '.',
  testMatch: 'wave01.pw.ts',
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: 'list',
  outputDir: process.env.LAYOUT_SURVEY_PW_OUTPUT_DIR || '../../test-results/layout-survey-wave01',
  use: {
    trace: 'off',
    video: 'off',
    screenshot: 'off',
    browserName: 'chromium',
    launchOptions: {
      ...(process.env.CHROMIUM_EXECUTABLE
        ? { executablePath: process.env.CHROMIUM_EXECUTABLE }
        : {}),
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    },
    navigationTimeout: 30_000,
    actionTimeout: 15_000,
  },
  projects: [{ name: 'chromium-wave01' }],
});
