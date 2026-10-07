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
 * STEWARD-ONLY seed invocation for the layout survey. Separate from the
 * capture config so a capture run can never seed. No browser is launched;
 * the seed calls the real Hub API. No globalSetup/teardown.
 */
export default defineConfig({
  testDir: '.',
  testMatch: 'seed.steward.ts',
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: 'list',
  outputDir: process.env.LAYOUT_SURVEY_PW_OUTPUT_DIR || '../../test-results/layout-survey-seed',
  use: { trace: 'off', video: 'off', screenshot: 'off' },
  projects: [{ name: 'steward-seed' }],
});
